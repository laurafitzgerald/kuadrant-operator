# Kuadrant Auth

A Kuadrant AuthPolicy custom resource:

1. Targets Gateway API networking resources such as [HTTPRoutes](https://gateway-api.sigs.k8s.io/references/spec/#gateway.networking.k8s.io/v1beta1.HTTPRoute) and [Gateways](https://gateway-api.sigs.k8s.io/references/spec/#gateway.networking.k8s.io/v1beta1.Gateway), using these resources to obtain additional context, i.e., which traffic workload (HTTP attributes, hostnames, user attributes, etc) to enforce auth.
2. Supports targeting subsets (sections) of a network resource to apply the auth rules to.
3. Abstracts the details of the underlying external authorization protocol and configuration resources, that have a much broader remit and surface area.
4. Enables cluster operators to set defaults that govern behavior at the lower levels of the network, until a more specific policy is applied.

## How it works

### Envoy's External Authorization Protocol

Kuadrant's Auth implementation relies on the Envoy's [External Authorization](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/ext_authz_filter) protocol. The workflow per request goes:
1. On incoming request, the gateway checks the matching rules for enforcing the auth rules, as stated in the AuthPolicy custom resources and targeted Gateway API networking objects
2. If the request matches, the gateway sends one [CheckRequest](https://www.envoyproxy.io/docs/envoy/latest/api-v3/service/auth/v3/external_auth.proto#envoy-v3-api-msg-service-auth-v3-checkrequest) to the external auth service ("Authorino").
3. The external auth service responds with a [CheckResponse](https://www.envoyproxy.io/docs/envoy/latest/api-v3/service/auth/v3/external_auth.proto#service-auth-v3-checkresponse) back to the gateway with either an `OK` or `DENIED` response code.

An AuthPolicy and its targeted Gateway API networking resource contain all the statements to configure both the ingress gateway and the external auth service.

### The AuthPolicy custom resource

#### Overview

The `AuthPolicy` spec includes the following parts:

* A reference to an existing Gateway API resource (`spec.targetRef`)
* Authentication/authorization scheme (`spec.rules`)
* Top-level route selectors (`spec.routeSelectors`)
* Top-level additional conditions (`spec.when`)
* List of named patterns (`spec.patterns`)

The auth scheme specify rules for:
* Authentication (`spec.rules.authentication`)
* External auth metadata fetching (`spec.rules.metadata`)
* Authorization (`spec.rules.authorization`)
* Custom response items (`spec.rules.response`)
* Callbacks (`spec.rules.callbacks`)

Each auth rule can declare specific `routeSelectors` and `when` conditions for the rule to apply.

#### High-level example and field definition

```yaml
apiVersion: kuadrant.io/v1beta2
kind: AuthPolicy
metadata:
  name: my-auth-policy
spec:
  # Reference to an existing networking resource to attach the policy to.
  # It can be a Gateway API HTTPRoute or Gateway resource.
  # It can only refer to objects in the same namespace as the AuthPolicy.
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute / Gateway
    name: myroute / mygateway

  # Selectors of HTTPRouteRules within the targeted HTTPRoute that activate the AuthPolicy.
  # Each element contains a HTTPRouteMatch object that will be used to select HTTPRouteRules that include at least
  # one identical HTTPRouteMatch.
  # The HTTPRouteMatch part does not have to be fully identical, but the what's stated in the selector must be
  # identically stated in the HTTPRouteRule.
  # Do not use it on AuthPolicies that target a Gateway.
  routeSelectors:
  - matches:
    - path:
        type: PathPrefix
        value: "/admin"

  # Additional dynamic conditions to trigger the AuthPolicy.
  # Use it for filtering attributes not supported by HTTPRouteRule or with AuthPolicies that target a Gateway.
  # Check out https://github.com/Kuadrant/architecture/blob/main/rfcs/0002-well-known-attributes.md to learn more
  # about the Well-known Attributes that can be used in this field.
  when: […]

  # The auth rules to apply to the network traffic routed through the targeted resource
  rules:
    # Authentication rules to enforce.
    # At least one config must evaluate to a valid identity object for the auth request to be successful.
    # If omitted or empty, anonymous access is assumed.
    authentication:
      "my-authn-rule":
        # The authentication method of this rule.
        # One-of: apiKey, jwt, oauth2Introspection, kubernetesTokenReview, x509, plain, anonymous.
        apiKey: {…}

        # Where credentials are required to be passed in the request for authentication based on this rule.
        # One-of: authorizationHeader, customHeader, queryString, cookie.
        credentials:
          authorizationHeader:
            prefix: APIKEY

        # Rule-level route selectors.
        routeSelectors: […]

        # Rule-level additional conditions.
        when: […]

        # Configs for caching the resolved object returned out of evaluating this auth rule.
        cache: {…}

    # Rules for fetching auth metadata from external sources.
    metadata:
      "my-external-source":
        # The method for fetching metadata from the external source.
        # One-of: http: userInfo, uma.
        http: {…}

    # Authorization rules to enforce.
    # All policies must allow access for the auth request be successful.
    authorization:
      "my-authz-rule":
        # The authorization method of this rule.
        # One-of: patternMatching, opa, kubernetesSubjectAccessReview, spicedb.
        opa: {…}

    # Customizations to the authorization response.
    response:
      # Custom denial status and other HTTP attributes for unauthenticated requests.
      unauthenticated: {…}

      # Custom denial status and other HTTP attributes for unauhtorized requests.
      unauthorized: {…}

      # Custom response items when access is granted.
      success:
        # Custom response items wrapped as HTTP headers to be injected in the request
        headers:
          "my-custom-header":
            # One-of: plain, json, wristband.
            plain: {…}

        # Custom response items wrapped as envoy dynamic metadata.
        dynamicMetadata:
          # One-of: plain, json, wristband.
          "my-custom-dyn-metadata":
            json: {…}

    # Rules for post-authorization callback requests to external services.
    # Triggered regardless of the result of the authorization request.
    callbacks:
      "my-webhook":
        http: {…}
```

Check out the [API reference](reference/authpolicy.md) for a full specification of the AuthPolicy CRD.

## Using the AuthPolicy

### Targeting a HTTPRoute networking resource

When an AuthPolicy targets a HTTPRoute, the policy is enforced to all traffic routed according to the rules and hostnames specified in the HTTPRoute, across all Gateways referenced in the `spec.parentRefs` field of the HTTPRoute.

The targeted HTTPRoute's rules and/or hostnames to which the policy must be enforced can be filtered to specific subsets, by specifying the [`routeSelectors`](reference/route-selectors.md#the-routeselectors-field) field of the AuthPolicy spec.

Target a HTTPRoute by setting the `spec.targetRef` field of the AuthPolicy as follows:

```yaml
apiVersion: kuadrant.io/v1beta2
kind: AuthPolicy
metadata:
  name: my-route-auth
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: <HTTPRoute Name>
  rules: {…}
```

```
┌───────────────────┐             ┌────────────────────┐
│ (Infra namespace) │             │   (App namespace)  │
│                   │             │                    │
│  ┌─────────┐      │  parentRefs │  ┌───────────┐     │
│  │ Gateway │◄─────┼─────────────┼──┤ HTTPRoute │     │
│  └─────────┘      │             │  └───────────┘     │
│                   │             │        ▲           │
│                   │             │        │           │
│                   │             │        │           │
│                   │             │        │ targetRef │
│                   │             │        │           │
│                   │             │  ┌─────┴──────┐    │
│                   │             │  │ AuthPolicy │    │
│                   │             │  └────────────┘    │
│                   │             │                    │
└───────────────────┘             └────────────────────┘
```

#### Multiple HTTPRoutes with the same hostname

Kuadrant currently does not support concurrent AuthPolicies targeting the exact same hostname.

In case 2 or more AuthPolicies target resources that specify the same exact hostnames, the first AuthPolicy created claims the hostname; all the other AuthPolicies will not be enforced for the conflicting hostname and their status will be reported as not ready.

This limitation only applies to identical hostnames. Different AuthPolicies can still be declared for different hostnames, as well as sets and strict subsets of hostnames.

#### Hostnames and wildcards

If an AuthPolicy targets a route defined for `*.com` and another AuthPolicy targets another route for `api.com`, the Kuadrant control plane will not merge these two AuthPolicies. Rather, it will mimic the behavior of gateway implementation by which the "most specific hostname wins", thus enforcing only the corresponding applicable policies and auth rules.

E.g., a request coming for `api.com` will be protected according to the rules from the AuthPolicy that targets the route for `api.com`; while a request for `other.com` will be protected with the rules from the AuthPolicy targeting the route for `*.com`.

Example with 3 AuthPolicies and 3 HTTPRoutes:
- AuthPolicy A → HTTPRoute A (`a.toystore.com`)
- AuthPolicy B → HTTPRoute B (`b.toystore.com`)
- AuthPolicy W → HTTPRoute W (`*.toystore.com`)

Expected behavior:
- Request to `a.toystore.com` → AuthPolicy A will be enforced
- Request to `b.toystore.com` → AuthPolicy B will be enforced
- Request to `other.toystore.com` → AuthPolicy W will be enforced

### Targeting a Gateway networking resource

When an AuthPolicy targets a Gateway, the policy will be enforced to all HTTP traffic hitting the gateway, unless a more specific AuthPolicy targeting a matching HTTPRoute exists.

Any new HTTPRoute referrencing the gateway as parent will be automatically covered by the AuthPolicy that targets the Gateway, as well as changes in the existing HTTPRoutes.

This effectively provides cluster operators with the ability to set _defaults_ to protect the infrastructure against unplanned and malicious network traffic attempt, such as by setting preemptive "deny-all" policies for hostnames and hostname wildcards.

Target a Gateway HTTPRoute by setting the `spec.targetRef` field of the AuthPolicy as follows:

```yaml
apiVersion: kuadrant.io/v1beta2
kind: AuthPolicy
metadata:
  name: my-gw-auth
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: <Gateway Name>
  rules: {…}
```

```
┌───────────────────┐             ┌────────────────────┐
│ (Infra namespace) │             │   (App namespace)  │
│                   │             │                    │
│  ┌─────────┐      │  parentRefs │  ┌───────────┐     │
│  │ Gateway │◄─────┼─────────────┼──┤ HTTPRoute │     │
│  └─────────┘      │             │  └───────────┘     │
│       ▲           │             │        ▲           │
│       │           │             │        │           │
│       │           │             │        │           │
│       │ targetRef │             │        │ targetRef │
│       │           │             │        │           │
│ ┌─────┴──────┐    │             │  ┌─────┴──────┐    │
│ │ AuthPolicy │    │             │  │ AuthPolicy │    │
│ └────────────┘    │             │  └────────────┘    │
│                   │             │                    │
└───────────────────┘             └────────────────────┘
```

#### Overlapping Gateway and HTTPRoute AuthPolicies

Gateway-targeted AuthPolicies will serve as a default to protect all traffic routed through the gateway until a more specific HTTPRoute-targeted AuthPolicy exists, in which case the HTTPRoute AuthPolicy prevails.

Example with 4 AuthPolicies, 3 HTTPRoutes and 1 Gateway (plus 2 HTTPRoute and 2 Gateways without AuthPolicies attached):
- AuthPolicy A → HTTPRoute A (`a.toystore.com`) → Gateway G (`*.com`)
- AuthPolicy B → HTTPRoute B (`b.toystore.com`) → Gateway G (`*.com`)
- AuthPolicy W → HTTPRoute W (`*.toystore.com`) → Gateway G (`*.com`)
- AuthPolicy G → Gateway G (`*.com`)

Expected behavior:
- Request to `a.toystore.com` → AuthPolicy A will be enforced
- Request to `b.toystore.com` → AuthPolicy B will be enforced
- Request to `other.toystore.com` → AuthPolicy W will be enforced
- Request to `other.com` (suppose a route exists) → AuthPolicy G will be enforced
- Request to `yet-another.net` (suppose a route and gateway exist) → No AuthPolicy will be enforced

### Route selectors

Route selectors allow targeting sections of a HTTPRoute, by specifying sets of HTTPRouteMatches and/or hostnames that make the policy controller look up within the HTTPRoute spec for compatible declarations, and select the corresponding HTTPRouteRules and hostnames, to then build conditions that activate the policy or policy rule.

Check out [Route selectors](reference/route-selectors.md) for a full description, semantics and API reference.

#### `when` conditions

`when` conditions can be used to scope an AuthPolicy or auth rule within an AuthPolicy (i.e. to filter the traffic to which a policy or policy rule applies) without any coupling to the underlying network topology, i.e. without making direct references to HTTPRouteRules via [`routeSelectors`](reference/route-selectors.md#the-routeselectors-field).

Use `when` conditions to conditionally activate policies and policy rules based on attributes that cannot be expressed in the HTTPRoutes' `spec.hostnames` and `spec.rules.matches` fields, or in general in AuthPolicies that target a Gateway.

`when` conditions in an AuthPolicy are compatible with Authorino [conditions](https://docs.kuadrant.io/authorino/docs/features/#common-feature-conditions-when), thus supporting complex boolean expressions with AND and OR operators, as well as grouping.

The selectors within the `when` conditions of an AuthPolicy are a subset of Kuadrant's Well-known Attributes ([RFC 0002](https://github.com/Kuadrant/architecture/blob/main/rfcs/0002-well-known-attributes.md)). Check out the reference for the full list of supported selectors.

Authorino [JSON path string modifiers](https://docs.kuadrant.io/authorino/docs/features/#string-modifiers) can also be applied to the selectors within the `when` conditions of an AuthPolicy.

### Examples

Check out the following user guides for examples of protecting services with Kuadrant:
* [Enforcing authentication & authorization with Kuadrant AuthPolicy, for app developers and platform engineers](user-guides/auth-for-app-devs-and-platform-engineers.md)
* [Authenticated Rate Limiting for Application Developers](user-guides/authenticated-rl-for-app-developers.md)
* [Authenticated Rate Limiting with JWTs and Kubernetes RBAC](user-guides/authenticated-rl-with-jwt-and-k8s-authnz.md)

### Known limitations

* One HTTPRoute can only be targeted by one AuthPolicy.
* One Gateway can only be targeted by one AuthPolicy.
* AuthPolicies can only target HTTPRoutes/Gateways defined within the same namespace of the AuthPolicy.
* 2+ AuthPolicies cannot target network resources that define/inherit the same exact hotname.

## Implementation details

Under the hood, for each AuthPolicy, Kuadrant creates an Istio [`AuthorizationPolicy`](https://istio.io/latest/docs/reference/config/security/authorization-policy) and an Authorino [`AuthConfig`](https://docs.kuadrant.io/authorino/docs/architecture/#the-authorino-authconfig-custom-resource-definition-crd) custom resources.

Only requests that matches the rules in the Istio `AuthorizationPolicy` cause an authorization request to be sent to the external authorization service ("Authorino"), i.e., only requests directed to the HTTPRouteRules targeted by the AuthPolicy (directly or indirectly), according to the declared top-level route selectors (if present), or all requests for which a matching HTTPRouteRule exists (otherwise).

Authorino looks up for the auth scheme (`AuthConfig` custom resource) to enforce using the provided hostname of the original request as key. It then checks again if the request matches at least one of the selected HTTPRouteRules, in which case it enforces the auth scheme.

<details>
  <summary>Exception to the rule</summary>

  Due to limitations imposed by the Istio `AuthorizationPolicy`, there are a few patterns of HTTPRouteRules that cannot be translated to filters for the external authorization request. Therefore, the following patterns used in HTTPRouteMatches of top-level route selectors of an AuthPolicy will not be included in the Istio AuthorizationPolicy rules that trigger the check request with Authorino: `PathMatchRegularExpression`, `HeaderMatchRegularExpression`, and `HTTPQueryParamMatch`.

  As a consequence to the above, requests that do not match these rules and otherwise would not be checked with Authorino will result in a request to the external authorization service. Authorino nonetheless will still verify those patterns and ensure the auth scheme is enforced only when it matches a selected HTTPRouteRule. Users of Kuadrant may observe an unnecessary call to the authorization service in those cases where the request is out of the scope of the AuthPolicy and therefore always authorized.
</details>

### Internal custom resources and namespaces

While the Istio `AuthorizationPolicy` needs to be created in the same namespace as the gateway workload, the Authorino `AuthConfig` is created in the namespace of the `AuthPolicy` itself. This allows to simplify references such as to Kubernetes Secrets referred in the AuthPolicy, as well as the RBAC to support the architecture.

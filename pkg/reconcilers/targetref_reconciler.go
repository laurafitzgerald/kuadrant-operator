/*
Copyright 2021 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package reconcilers

import (
	"context"
	"fmt"
	"sort"

	"github.com/go-logr/logr"
	"github.com/laurafitzgerald/kuadrant-operator/pkg/common"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type ParentRefReconciler struct {
	*BaseReconciler
}

// blank assignment to verify that BaseReconciler implements reconcile.Reconciler
var _ reconcile.Reconciler = &ParentRefReconciler{}

func (r *ParentRefReconciler) Reconcile(context.Context, ctrl.Request) (ctrl.Result, error) {
	return reconcile.Result{}, nil
}

func (r *ParentRefReconciler) FetchValidGateway(ctx context.Context, key client.ObjectKey) (*gatewayapiv1.Gateway, error) {
	logger, _ := logr.FromContext(ctx)

	gw := &gatewayapiv1.Gateway{}
	err := r.Client().Get(ctx, key, gw)
	logger.V(1).Info("FetchValidGateway", "gateway", key, "err", err)
	if err != nil {
		return nil, err
	}

	if meta.IsStatusConditionFalse(gw.Status.Conditions, common.GatewayProgrammedConditionType) {
		return nil, fmt.Errorf("FetchValidGateway: gateway (%v) not ready", key)
	}

	return gw, nil
}

func (r *ParentRefReconciler) FetchValidHTTPRoute(ctx context.Context, key client.ObjectKey) (*gatewayapiv1.HTTPRoute, error) {
	logger, _ := logr.FromContext(ctx)

	httpRoute := &gatewayapiv1.HTTPRoute{}
	err := r.Client().Get(ctx, key, httpRoute)
	logger.V(1).Info("FetchValidHTTPRoute", "httpRoute", key, "err", err)
	if err != nil {
		return nil, err
	}

	if !common.IsHTTPRouteAccepted(httpRoute) {
		return nil, fmt.Errorf("FetchValidHTTPRoute: httproute (%v) not accepted", key)
	}

	return httpRoute, nil
}

// FetchValidParentRef fetches the parent reference object and checks the status is valid
func (r *ParentRefReconciler) FetchValidParentRef(ctx context.Context, parentRef gatewayapiv1.ParentReference, defaultNs string) (client.Object, error) {
	tmpNS := defaultNs
	if parentRef.Namespace != nil {
		tmpNS = string(*parentRef.Namespace)
	}

	objKey := client.ObjectKey{Name: string(parentRef.Name), Namespace: tmpNS}

	if common.IsParentRefHTTPRoute(parentRef) {
		return r.FetchValidHTTPRoute(ctx, objKey)
	} else if common.IsParentRefGateway(parentRef) {
		return r.FetchValidGateway(ctx, objKey)
	}

	return nil, fmt.Errorf("FetchValidParentRef: parentRef (%v) to unknown network resource", parentRef)
}

// FetchAcceptedGatewayHTTPRoutes returns the list of HTTPRoutes that have been accepted as children of a gateway.
func (r *ParentRefReconciler) FetchAcceptedGatewayHTTPRoutes(ctx context.Context, gwKey client.ObjectKey) (routes []gatewayapiv1.HTTPRoute) {
	logger, _ := logr.FromContext(ctx)
	logger = logger.WithName("FetchAcceptedGatewayHTTPRoutes").WithValues("gateway", gwKey)

	routeList := &gatewayapiv1.HTTPRouteList{}
	err := r.Client().List(ctx, routeList)
	if err != nil {
		logger.V(1).Info("failed to list httproutes", "err", err)
		return
	}

	for idx := range routeList.Items {
		route := routeList.Items[idx]
		routeParentStatus, found := common.Find(route.Status.RouteStatus.Parents, func(p gatewayapiv1.RouteParentStatus) bool {
			return *p.ParentRef.Kind == ("Gateway") &&
				((p.ParentRef.Namespace == nil && route.GetNamespace() == gwKey.Namespace) || string(*p.ParentRef.Namespace) == gwKey.Namespace) &&
				string(p.ParentRef.Name) == gwKey.Name
		})
		if found && meta.IsStatusConditionTrue(routeParentStatus.Conditions, "Accepted") {
			logger.V(1).Info("found route attached to gateway", "httproute", client.ObjectKeyFromObject(&route))
			routes = append(routes, route)
			continue
		}
		logger.V(1).Info("skipping route, not attached to gateway", "httproute", client.ObjectKeyFromObject(&route))
	}

	return
}

// ParentGatewayKeys returns the list of gateways that are being referenced from the parent.
func (r *ParentRefReconciler) ParentGatewayKeys(_ context.Context, parentNetworkObject client.Object) []client.ObjectKey {
	switch obj := parentNetworkObject.(type) {
	case *gatewayapiv1.HTTPRoute:
		gwKeys := make([]client.ObjectKey, 0)
		for _, parentRef := range obj.Spec.CommonRouteSpec.ParentRefs {
			gwKey := client.ObjectKey{Name: string(parentRef.Name), Namespace: obj.Namespace}
			if parentRef.Namespace != nil {
				gwKey.Namespace = string(*parentRef.Namespace)
			}
			gwKeys = append(gwKeys, gwKey)
		}
		return gwKeys

	case *gatewayapiv1.Gateway:
		return []client.ObjectKey{client.ObjectKeyFromObject(parentNetworkObject)}

	// If the parentNetworkObject is nil, we don't fail; instead, we return an empty slice of gateway keys.
	// This is for supporting a smooth cleanup in cases where the network object has been deleted already
	default:
		return []client.ObjectKey{}
	}
}

// ReconcileParentReference adds policy key in annotations of the parent object
func (r *ParentRefReconciler) ReconcileParentReference(ctx context.Context, policyKey client.ObjectKey, parentNetworkObject client.Object, annotationName string) error {
	logger, _ := logr.FromContext(ctx)

	parentNetworkObjectKey := client.ObjectKeyFromObject(parentNetworkObject)
	parentNetworkObjectKind := parentNetworkObject.GetObjectKind().GroupVersionKind()

	// Reconcile the back reference:
	objAnnotations := common.ReadAnnotationsFromObject(parentNetworkObject)

	if val, ok := objAnnotations[annotationName]; ok {
		if val != policyKey.String() {
			return fmt.Errorf("the %s parent %s is already referenced by policy %s", parentNetworkObjectKind, parentNetworkObjectKey, policyKey.String())
		}
	} else {
		objAnnotations[annotationName] = policyKey.String()
		parentNetworkObject.SetAnnotations(objAnnotations)
		err := r.UpdateResource(ctx, parentNetworkObject)
		logger.V(1).Info("ReconcileParentReference: update parent object", "kind", parentNetworkObjectKind, "name", parentNetworkObjectKey, "err", err)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *ParentRefReconciler) DeleteParentReference(ctx context.Context, parentNetworkObject client.Object, annotationName string) error {
	logger, _ := logr.FromContext(ctx)

	parentNetworkObjectKey := client.ObjectKeyFromObject(parentNetworkObject)
	parentNetworkObjectKind := parentNetworkObject.GetObjectKind().GroupVersionKind()

	// Reconcile the back reference:
	objAnnotations := common.ReadAnnotationsFromObject(parentNetworkObject)

	if _, ok := objAnnotations[annotationName]; ok {
		delete(objAnnotations, annotationName)
		parentNetworkObject.SetAnnotations(objAnnotations)
		err := r.UpdateResource(ctx, parentNetworkObject)
		logger.V(1).Info("DeleteParentReference: update network resource", "kind", parentNetworkObjectKind, "name", parentNetworkObjectKey, "err", err)
		if err != nil {
			return err
		}
	}

	return nil
}

// GetAllGatewayPolicyRefs returns the policy refs of a given policy kind from all gateways managed by kuadrant.
// The gateway objects are handled in order of creation to mitigate the risk of non-idenpotent reconciliations based on
// this list of policy refs; nevertheless, the actual order of returned policy refs depends on the order the policy refs
// appear in the annotations of the gateways.
// Only gateways with status programmed are considered.
func (r *ParentRefReconciler) GetAllGatewayPolicyRefs(ctx context.Context, policyRefsConfig common.PolicyRefsConfig) ([]client.ObjectKey, error) {
	var uniquePolicyRefs map[string]struct{}
	var policyRefs []client.ObjectKey

	gwList := &gatewayapiv1.GatewayList{}
	if err := r.Client().List(ctx, gwList); err != nil {
		return nil, err
	}

	// sort the gateways by creation timestamp to mitigate the risk of non-idenpotent reconciliations
	var gateways common.GatewayWrapperList
	for i := range gwList.Items {
		gateway := gwList.Items[i]
		// skip gateways that are not managed by kuadrant or that are not ready
		if !common.IsKuadrantManaged(&gateway) || meta.IsStatusConditionFalse(gateway.Status.Conditions, common.GatewayProgrammedConditionType) {
			continue
		}
		gateways = append(gateways, common.GatewayWrapper{Gateway: &gateway, PolicyRefsConfig: policyRefsConfig})
	}
	sort.Sort(gateways)

	for _, gw := range gateways {
		for _, policyRef := range gw.PolicyRefs() {
			if _, ok := uniquePolicyRefs[policyRef.String()]; ok {
				continue
			}
			policyRefs = append(policyRefs, policyRef)
		}
	}

	return policyRefs, nil
}

// Returns:
// * list of gateways to which the policy applies for the first time
// * list of gateways to which the policy no longer applies
// * list of gateways to which the policy still applies
func (r *ParentRefReconciler) ComputeGatewayDiffs(ctx context.Context, policy common.KuadrantPolicy, parentNetworkObject client.Object, policyRefsConfig common.PolicyRefsConfig) (*GatewayDiff, error) {
	logger, _ := logr.FromContext(ctx)

	var gwKeys []client.ObjectKey
	if policy.GetDeletionTimestamp() == nil {
		gwKeys = r.ParentGatewayKeys(ctx, parentNetworkObject)
	}

	// TODO(rahulanand16nov): maybe think about optimizing it with a label later
	allGwList := &gatewayapiv1.GatewayList{}
	err := r.Client().List(ctx, allGwList)
	if err != nil {
		return nil, err
	}

	gwDiff := &GatewayDiff{
		GatewaysMissingPolicyRef:     common.GatewaysMissingPolicyRef(allGwList, client.ObjectKeyFromObject(policy), gwKeys, policyRefsConfig),
		GatewaysWithValidPolicyRef:   common.GatewaysWithValidPolicyRef(allGwList, client.ObjectKeyFromObject(policy), gwKeys, policyRefsConfig),
		GatewaysWithInvalidPolicyRef: common.GatewaysWithInvalidPolicyRef(allGwList, client.ObjectKeyFromObject(policy), gwKeys, policyRefsConfig),
	}

	logger.V(1).Info("ComputeGatewayDiffs",
		"#missing-policy-ref", len(gwDiff.GatewaysMissingPolicyRef),
		"#valid-policy-ref", len(gwDiff.GatewaysWithValidPolicyRef),
		"#invalid-policy-ref", len(gwDiff.GatewaysWithInvalidPolicyRef),
	)

	return gwDiff, nil
}

// ReconcileGatewayPolicyReferences updates the annotations in the Gateway resources that list to all the policies
// that directly or indirectly target the gateway, based upon a pre-computed gateway diff object
func (r *ParentRefReconciler) ReconcileGatewayPolicyReferences(ctx context.Context, policy client.Object, gwDiffObj *GatewayDiff) error {
	logger, _ := logr.FromContext(ctx)

	// delete the policy from the annotations of the gateways no longer target by the policy
	for _, gw := range gwDiffObj.GatewaysWithInvalidPolicyRef {
		if gw.DeletePolicy(client.ObjectKeyFromObject(policy)) {
			err := r.UpdateResource(ctx, gw.Gateway)
			logger.V(1).Info("ReconcileGatewayPolicyReferences: update gateway", "gateway with invalid policy ref", gw.Key(), "err", err)
			if err != nil {
				return err
			}
		}
	}

	// add the policy to the annotations of the gateways target by the policy
	for _, gw := range gwDiffObj.GatewaysMissingPolicyRef {
		if gw.AddPolicy(client.ObjectKeyFromObject(policy)) {
			err := r.UpdateResource(ctx, gw.Gateway)
			logger.V(1).Info("ReconcileGatewayPolicyReferences: update gateway", "gateway missinf policy ref", gw.Key(), "err", err)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

type GatewayDiff struct {
	GatewaysMissingPolicyRef     []common.GatewayWrapper
	GatewaysWithValidPolicyRef   []common.GatewayWrapper
	GatewaysWithInvalidPolicyRef []common.GatewayWrapper
}

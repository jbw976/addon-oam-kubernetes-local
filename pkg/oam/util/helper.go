package util

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	cpv1alpha1 "github.com/crossplane/crossplane-runtime/apis/core/v1alpha1"
	"github.com/crossplane/oam-kubernetes-runtime/pkg/oam"
	plur "github.com/gertd/go-pluralize"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/crossplane/oam-kubernetes-runtime/apis/core/v1alpha2"
)

var (
	KindDeployment      = reflect.TypeOf(appsv1.Deployment{}).Name()
	KindService         = reflect.TypeOf(corev1.Service{}).Name()
	ReconcileWaitResult = reconcile.Result{RequeueAfter: 30 * time.Second}
)

const (
	ErrUpdateStatus = "cannot apply status"
)

// A ConditionedObject is an Object type with condition field
type ConditionedObject interface {
	oam.Object

	oam.Conditioned
}

// FetchWorkloadDefinition fetch corresponding workloadDefinition given a workload
func FetchWorkloadDefinition(ctx context.Context, mLog logr.Logger, r client.Reader,
	workload *unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	// The name of the workloadDefinition CR is the CRD name of the component which is <purals>.<group>
	gvr := getGVResource(workload.Object)
	wldName := gvr.Resource + "." + gvr.Group
	nn := types.NamespacedName{Name: wldName}
	// Fetch the corresponding workloadDefinition CR
	workloadDefinition := &v1alpha2.WorkloadDefinition{}
	if err := r.Get(ctx, nn, workloadDefinition); err != nil {
		return nil, err
	}
	return fetchChildResources(ctx, mLog, r, workload, workloadDefinition.Spec.ChildResourceKinds)
}

func fetchChildResources(ctx context.Context, mLog logr.Logger, r client.Reader, workload *unstructured.Unstructured,
	wcrl []v1alpha2.ChildResourceKind) ([]*unstructured.Unstructured, error) {
	var childResources []*unstructured.Unstructured
	// list by each child resource type with namespace and possible label selector
	for _, wcr := range wcrl {
		crs := unstructured.UnstructuredList{}
		crs.SetAPIVersion(wcr.APIVersion)
		crs.SetKind(wcr.Kind)
		mLog.Info("List child resource kind", "APIVersion", wcr.APIVersion, "Kind", wcr.Kind, "owner UID",
			workload.GetUID())
		if err := r.List(ctx, &crs, client.InNamespace(workload.GetNamespace()),
			client.MatchingLabels(wcr.Selector)); err != nil {
			return nil, errors.Wrap(err, fmt.Sprintf("failed to list object %s.%s", crs.GetAPIVersion(), crs.GetKind()))
		}
		// pick the ones that is owned by the workload
		for _, cr := range crs.Items {
			for _, owner := range cr.GetOwnerReferences() {
				if owner.UID == workload.GetUID() {
					mLog.Info("Find a child resource we are looking for",
						"APIVersion", cr.GetAPIVersion(), "Kind", cr.GetKind(),
						"Name", cr.GetName(), "owner", owner.UID)
					or := cr // have to do a copy as the range variable is a reference and will change
					childResources = append(childResources, &or)
				}
			}
		}
	}
	return childResources, nil
}

// Patch condition for a conditioned object
func PatchCondition(ctx context.Context, r client.StatusClient, workload ConditionedObject,
	condition ...cpv1alpha1.Condition) error {
	workloadPatch := client.MergeFrom(workload.DeepCopyObject())
	workload.SetConditions(condition...)
	return errors.Wrap(
		r.Status().Patch(ctx, workload, workloadPatch, client.FieldOwner(workload.GetUID())),
		ErrUpdateStatus)
}

func getGVResource(ob map[string]interface{}) metav1.GroupVersionResource {
	apiVersion, _, _ := unstructured.NestedString(ob, "apiVersion")
	kind, _, _ := unstructured.NestedString(ob, "kind")
	g, v := ApiVersion2GroupVersion(apiVersion)
	return metav1.GroupVersionResource{
		Group:    g,
		Version:  v,
		Resource: Kind2Resource(kind),
	}
}

// ApiVersion2GroupVersion turn an apiVersion string into group and version
func ApiVersion2GroupVersion(str string) (string, string) {
	strs := strings.Split(str, "/")
	if len(strs) == 2 {
		return strs[0], strs[1]
	}
	// core type
	return "", strs[0]
}

// Kind2Resource convert Kind to Resources
func Kind2Resource(str string) string {
	return plur.NewClient().Plural(strings.ToLower(str))
}

// Object2Unstructured convert an object to an unstructured struct
func Object2Unstructured(obj interface{}) (*unstructured.Unstructured, error) {
	objMap, err := Object2Map(obj)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{
		Object: objMap,
	}, nil
}

// Object2Map turn the Object to a map
func Object2Map(obj interface{}) (map[string]interface{}, error) {
	var res map[string]interface{}
	bts, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(bts, &res)
	return res, err
}

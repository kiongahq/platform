package operator

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

const testNamespace = "team-a"

// testScheme registers the built-in kinds, the Kionga kinds and, when asked,
// the optional KEDA HTTP and KServe kinds as unstructured objects, which is
// how the operator handles them.
func testScheme(t *testing.T, optional ...schema.GroupVersionKind) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, autoscalingv2.AddToScheme, batchv1.AddToScheme, corev1.AddToScheme, networkingv1.AddToScheme, policyv1.AddToScheme, kiongav1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	for _, gvk := range optional {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	return scheme
}

func testClient(scheme *runtime.Scheme, objects ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kiongav1.KiongaApplication{}, &kiongav1.KiongaFunction{}, &kiongav1.KiongaAgent{}, &kiongav1.KiongaModelDeployment{}, &kiongav1.KiongaModelPromotion{}, &appsv1.Deployment{}, &batchv1.Job{}, &corev1.Pod{}).
		WithObjects(objects...).Build()
}

func reconcileOnce(t *testing.T, reconciler interface {
	Reconcile(context.Context, ctrl.Request) (ctrl.Result, error)
}, name string) ctrl.Result {
	t.Helper()
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: testNamespace}})
	if err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
	return result
}

func get(t *testing.T, c client.Client, name string, object client.Object) {
	t.Helper()
	if err := c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: testNamespace}, object); err != nil {
		t.Fatalf("get %T %s: %v", object, name, err)
	}
}

func absent(t *testing.T, c client.Client, name string, object client.Object) {
	t.Helper()
	if err := c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: testNamespace}, object); err == nil {
		t.Fatalf("%T %s should not exist", object, name)
	} else if client.IgnoreNotFound(err) != nil {
		t.Fatal(err)
	}
}

func unstructuredOf(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gvk)
	return object
}

// ownedBy fails unless object has a controller reference to owner, which is
// what lets the garbage collector delete it with its owner.
func ownedBy(t *testing.T, object client.Object, owner string) {
	t.Helper()
	reference := metav1.GetControllerOf(object)
	if reference == nil || reference.Name != owner || reference.APIVersion != kiongav1.GroupVersion.String() {
		t.Fatalf("%T %s is not controlled by %s: %#v", object, object.GetName(), owner, object.GetOwnerReferences())
	}
}

func condition(conditions []metav1.Condition, kind string) metav1.Condition {
	for _, item := range conditions {
		if item.Type == kind {
			return item
		}
	}
	return metav1.Condition{}
}

// markAvailable sets a Deployment's status as the Deployment controller
// would after a finished rollout with ready replicas.
func markAvailable(t *testing.T, c client.Client, name string, ready int32) {
	t.Helper()
	var deployment appsv1.Deployment
	get(t, c, name, &deployment)
	deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: deployment.Generation, Replicas: ready, UpdatedReplicas: ready, ReadyReplicas: ready, AvailableReplicas: ready}
	if err := c.Status().Update(context.Background(), &deployment); err != nil {
		t.Fatal(err)
	}
}

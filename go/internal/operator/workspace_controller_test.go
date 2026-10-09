package operator

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mlaiopsv1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

func TestWorkspaceControllerProvisionsBoundedSharedWorkspace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = mlaiopsv1.AddToScheme(scheme)
	workspace := &mlaiopsv1.KiongaWorkspace{
		TypeMeta: metav1.TypeMeta{APIVersion: "mlaiops.io/v1alpha1", Kind: "KiongaWorkspace"}, ObjectMeta: metav1.ObjectMeta{Name: "workspace-user-1", Namespace: "team-a"},
		Spec: mlaiopsv1.KiongaWorkspaceSpec{Subject: "user-1", Services: []string{"workbench", "ide"}, Compute: mlaiopsv1.WorkspaceComputeSpec{VCPUs: 4, MemoryGB: 8}, StorageGB: 100},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(workspace).WithObjects(workspace).Build()
	reconciler := &WorkspaceReconciler{Client: client, WorkbenchImage: "registry/jupyter:1", IDEImage: "registry/ide:1", GatewayURL: "http://gateway:8080"}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: workspace.Name, Namespace: workspace.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := client.Get(context.Background(), request.NamespacedName, &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 || len(deployment.Spec.Template.Spec.Containers) != 2 {
		t.Fatalf("unexpected workspace deployment: %#v", deployment.Spec)
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Resources.Limits.Cpu().String() != "2" || container.Resources.Limits.Memory().String() != "4Gi" {
			t.Fatalf("grant was not split across services: %#v", container.Resources)
		}
	}
	var claim corev1.PersistentVolumeClaim
	if err := client.Get(context.Background(), request.NamespacedName, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.Spec.Resources.Requests.Storage().String() != "100Gi" {
		t.Fatalf("unexpected storage allocation: %s", claim.Spec.Resources.Requests.Storage().String())
	}
	var secret corev1.Secret
	if err := client.Get(context.Background(), types.NamespacedName{Name: workspace.Name + "-auth", Namespace: workspace.Namespace}, &secret); err != nil {
		t.Fatal(err)
	}
	if len(secret.Data["token"]) < 24 {
		t.Fatal("workspace must receive a generated authentication secret")
	}

	if err := client.Get(context.Background(), request.NamespacedName, workspace); err != nil {
		t.Fatal(err)
	}
	workspace.Spec.Disabled = true
	if err := client.Update(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(context.Background(), request.NamespacedName, &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 {
		t.Fatal("suspended workspace must scale to zero")
	}
}

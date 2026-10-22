package operator

import (
	"context"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformapi "github.com/kiongahq/platform/pkg/api"
	mlaiopsv1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

// Private Kionga images (GHCR packages are private) only pull when every pod
// the operator creates references the registry credential.
func TestParseImagePullSecrets(t *testing.T) {
	got := ParseImagePullSecrets(" ghcr-pull, ,mirror ")
	want := []corev1.LocalObjectReference{{Name: "ghcr-pull"}, {Name: "mirror"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if ParseImagePullSecrets("") != nil {
		t.Fatal("unset must leave pods without pull secrets")
	}
}

func pullSecretScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = autoscalingv2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = mlaiopsv1.AddToScheme(scheme)
	return scheme
}

func TestWorkspacePodsReferencePullSecrets(t *testing.T) {
	workspace := &mlaiopsv1.KiongaWorkspace{
		TypeMeta: metav1.TypeMeta{APIVersion: "mlaiops.io/v1alpha1", Kind: "KiongaWorkspace"}, ObjectMeta: metav1.ObjectMeta{Name: "workspace-user-1", Namespace: "team-a"},
		Spec: mlaiopsv1.KiongaWorkspaceSpec{Subject: "user-1", Services: []string{"workbench"}, Compute: mlaiopsv1.WorkspaceComputeSpec{VCPUs: 2, MemoryGB: 4}, StorageGB: 10},
	}
	client := fake.NewClientBuilder().WithScheme(pullSecretScheme()).WithStatusSubresource(workspace).WithObjects(workspace).Build()
	secrets := ParseImagePullSecrets("ghcr-pull")
	reconciler := &WorkspaceReconciler{Client: client, WorkbenchImage: "ghcr.io/kiongahq/jupyter:1", GatewayURL: "http://gateway:8080", ImagePullSecrets: secrets}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: workspace.Name, Namespace: workspace.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := client.Get(context.Background(), request.NamespacedName, &deployment); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deployment.Spec.Template.Spec.ImagePullSecrets, secrets) {
		t.Fatalf("workspace pod pull secrets = %v", deployment.Spec.Template.Spec.ImagePullSecrets)
	}
}

func TestAgentPodsReferencePullSecrets(t *testing.T) {
	resourceID := "agt-pull-secret"
	name := platformapi.AgentDNSName(resourceID)
	agent := &mlaiopsv1.KiongaAgent{
		TypeMeta:   metav1.TypeMeta{APIVersion: "mlaiops.io/v1alpha1", Kind: "KiongaAgent"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a", Labels: map[string]string{"mlaiops.io/resource-id": resourceID}},
		Spec:       mlaiopsv1.KiongaAgentSpec{Version: "1", Image: "ghcr.io/kiongahq/agent-runtime:1", GraphModule: "agents.support:graph", Replicas: mlaiopsv1.ReplicaSpec{Min: 1, Max: 1}},
	}
	client := fake.NewClientBuilder().WithScheme(pullSecretScheme()).WithStatusSubresource(agent, &appsv1.Deployment{}).WithObjects(agent).Build()
	secrets := ParseImagePullSecrets("ghcr-pull")
	reconciler := &AgentReconciler{Client: client, ImagePullSecrets: secrets}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "team-a"}}); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := client.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "team-a"}, &deployment); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deployment.Spec.Template.Spec.ImagePullSecrets, secrets) {
		t.Fatalf("agent pod pull secrets = %v", deployment.Spec.Template.Spec.ImagePullSecrets)
	}
	if image := deployment.Spec.Template.Spec.Containers[1].Image; image != "ghcr.io/kiongahq/trace-proxy:latest" {
		t.Fatalf("trace proxy default image = %q", image)
	}
}

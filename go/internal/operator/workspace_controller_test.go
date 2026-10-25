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
	if len(secret.Data["api-token"]) < 32 || string(secret.Data["api-token"]) == string(secret.Data["token"]) {
		t.Fatal("workspace API needs an independent generated credential")
	}
	firstAPIToken := string(secret.Data["api-token"])
	firstWorkbenchToken := string(secret.Data["token"])
	for _, container := range deployment.Spec.Template.Spec.Containers {
		env := map[string]corev1.EnvVar{}
		for _, item := range container.Env {
			env[item.Name] = item
		}
		if env["MLAIOPS_URL"].Value != "http://127.0.0.1:8890" || env["KIONGA_GATEWAY_INTERNAL_URL"].Value != "http://gateway:8087" || env["KIONGA_WORKSPACE_API_TOKEN"].ValueFrom.SecretKeyRef.Key != "api-token" {
			t.Fatalf("%s is missing workspace API identity: %#v", container.Name, env)
		}
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
	if err := client.Get(context.Background(), request.NamespacedName, workspace); err != nil {
		t.Fatal(err)
	}
	workspace.Spec.Subject = "user-2"
	workspace.Spec.Disabled = false
	if err := client.Update(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(context.Background(), types.NamespacedName{Name: workspace.Name + "-auth", Namespace: workspace.Namespace}, &secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["api-token"]) == firstAPIToken || string(secret.Data["token"]) == firstWorkbenchToken || secret.Annotations["mlaiops.io/subject"] != "user-2" {
		t.Fatal("all workspace credentials must rotate when its owner changes")
	}
}

func TestLangflowWorkspaceHasIndependentVolumeAndScopedSidecar(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = mlaiopsv1.AddToScheme(scheme)
	workspace := &mlaiopsv1.KiongaWorkspace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "mlaiops.io/v1alpha1", Kind: "KiongaWorkspace"},
		ObjectMeta: metav1.ObjectMeta{Name: "langflow-1234567890abcdef12345678", Namespace: "team-a", Annotations: map[string]string{"mlaiops.io/restart-at": "first"}},
		Spec:       mlaiopsv1.KiongaWorkspaceSpec{Subject: "user-1", ProjectID: "project-a", Services: []string{"langflow"}, Compute: mlaiopsv1.WorkspaceComputeSpec{VCPUs: 2, MemoryGB: 4}, StorageGB: 10},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(workspace).WithObjects(workspace).Build()
	r := &WorkspaceReconciler{Client: kube, LangflowImage: "langflow:1", LangflowSidecarImage: "sidecar:1", GatewayURL: "http://gateway:8080"}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: workspace.Name, Namespace: workspace.Namespace}}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := kube.Get(context.Background(), request.NamespacedName, &deployment); err != nil {
		t.Fatal(err)
	}
	containers := deployment.Spec.Template.Spec.Containers
	if len(containers) != 2 || containers[0].Name != "langflow" || containers[1].Name != "kionga-api" {
		t.Fatalf("unexpected Langflow pod: %#v", containers)
	}
	autoAPI := false
	for _, variable := range containers[0].Env {
		if variable.Name == "LANGFLOW_SKIP_AUTH_AUTO_LOGIN" && variable.Value == "true" {
			autoAPI = true
		}
	}
	if !autoAPI {
		t.Fatal("Kionga's scoped gateway needs the Langflow flow API in auto-login mode")
	}
	if deployment.Spec.Template.Annotations["mlaiops.io/restart-at"] != "first" {
		t.Fatal("restart annotation did not propagate")
	}
	for _, item := range containers[0].Env {
		if item.Name == "KIONGA_WORKSPACE_API_TOKEN" {
			t.Fatal("Langflow can read the workspace API credential")
		}
	}
	if containers[1].Env[2].ValueFrom.SecretKeyRef.Key != "api-token" {
		t.Fatal("sidecar missing scoped credential")
	}
	if containers[0].Resources.Limits.Cpu().MilliValue()+containers[1].Resources.Limits.Cpu().MilliValue() > 2000 {
		t.Fatal("Langflow pod exceeds CPU grant")
	}
	var claim corev1.PersistentVolumeClaim
	if err := kube.Get(context.Background(), request.NamespacedName, &claim); err != nil || claim.Spec.Resources.Requests.Storage().String() != "10Gi" {
		t.Fatalf("Langflow volume: %v %#v", err, claim.Spec)
	}
	if err := kube.Get(context.Background(), request.NamespacedName, workspace); err != nil {
		t.Fatal(err)
	}
	workspace.Annotations["mlaiops.io/restart-at"] = "second"
	if err := kube.Update(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), request.NamespacedName, &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Template.Annotations["mlaiops.io/restart-at"] != "second" {
		t.Fatal("restart did not roll the deployment")
	}
}

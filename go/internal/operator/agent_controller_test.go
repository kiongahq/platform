package operator

import (
	"context"
	"testing"

	platformapi "github.com/kiongahq/platform/pkg/api"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mlaiopsv1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

func TestAgentControllerCreatesDeploymentAndService(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = autoscalingv2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = mlaiopsv1.AddToScheme(scheme)
	resourceID := "agt-controller-test"
	workloadName := platformapi.AgentDNSName(resourceID)
	agent := &mlaiopsv1.KiongaAgent{
		TypeMeta:   metav1.TypeMeta{APIVersion: "mlaiops.io/v1alpha1", Kind: "KiongaAgent"},
		ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: "team-a", Labels: map[string]string{"mlaiops.io/resource-id": resourceID}},
		Spec: mlaiopsv1.KiongaAgentSpec{Version: "1.2", Image: "registry/support:1.2", GraphModule: "agents.support:graph", Replicas: mlaiopsv1.ReplicaSpec{Min: 2, Max: 5}, Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("2Gi"), corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1")},
		}},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(agent, &appsv1.Deployment{}).WithObjects(agent).Build()
	reconciler := &AgentReconciler{Client: client}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := client.Get(context.Background(), types.NamespacedName{Name: workloadName, Namespace: "team-a"}, &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 2 || len(deployment.Spec.Template.Spec.Containers) != 2 {
		t.Fatalf("unexpected deployment: %#v", deployment.Spec)
	}
	if deployment.Spec.Template.Spec.Containers[0].ReadinessProbe == nil || deployment.Spec.Template.Spec.Containers[1].LivenessProbe == nil {
		t.Fatalf("agent and trace proxy must have health probes: %#v", deployment.Spec.Template.Spec.Containers)
	}
	// Simulate an HPA scale. An owned Deployment event must not force the
	// replica count back to the minimum.
	scaled := int32(4)
	deployment.Spec.Replicas = &scaled
	if err := client.Update(context.Background(), &deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(context.Background(), types.NamespacedName{Name: workloadName, Namespace: "team-a"}, &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != scaled {
		t.Fatalf("controller reset HPA-owned replicas: %#v", deployment.Spec.Replicas)
	}
	if deployment.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String() != "1" || deployment.Spec.Template.Spec.Containers[0].Resources.Limits.Memory().String() != "2Gi" {
		t.Fatalf("agent resources were not reconciled: %#v", deployment.Spec.Template.Spec.Containers[0].Resources)
	}
	gpuLimit := deployment.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceName("nvidia.com/gpu")]
	if gpuLimit.String() != "1" {
		t.Fatalf("GPU request must also be enforced as a limit: %#v", deployment.Spec.Template.Spec.Containers[0].Resources)
	}
	var service corev1.Service
	if err := client.Get(context.Background(), types.NamespacedName{Name: workloadName, Namespace: "team-a"}, &service); err != nil {
		t.Fatal(err)
	}
	var hpa autoscalingv2.HorizontalPodAutoscaler
	if err := client.Get(context.Background(), types.NamespacedName{Name: workloadName, Namespace: "team-a"}, &hpa); err != nil {
		t.Fatal(err)
	}
	if hpa.Spec.MinReplicas == nil || *hpa.Spec.MinReplicas != 2 || hpa.Spec.MaxReplicas != 5 {
		t.Fatalf("unexpected agent autoscaler: %#v", hpa.Spec)
	}
	var current mlaiopsv1.KiongaAgent
	if err := client.Get(context.Background(), request.NamespacedName, &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != "Progressing" || apimeta.IsStatusConditionTrue(current.Status.Conditions, "Ready") {
		t.Fatalf("agent must not report Ready before pods are available: %#v", current.Status)
	}
	deployment.Status.ReadyReplicas, deployment.Status.AvailableReplicas = 2, 2
	if err := client.Status().Update(context.Background(), &deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := client.Get(context.Background(), request.NamespacedName, &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != "Ready" || !apimeta.IsStatusConditionTrue(current.Status.Conditions, "Ready") || current.Status.ReadyReplicas != 2 || current.Status.WorkloadRef != workloadName {
		t.Fatalf("available workload was not reflected as Ready: %#v", current.Status)
	}
}

func TestAgentControllerMountsPublishedLangflowSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = autoscalingv2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = mlaiopsv1.AddToScheme(scheme)
	agent := &mlaiopsv1.KiongaAgent{
		TypeMeta:   metav1.TypeMeta{APIVersion: "mlaiops.io/v1alpha1", Kind: "KiongaAgent"},
		ObjectMeta: metav1.ObjectMeta{Name: "published-flow", Namespace: "team-a"},
		Spec:       mlaiopsv1.KiongaAgentSpec{Version: "r2", Image: "registry/langflow:1.13", GraphModule: "agents.langflow_adapter:build_langflow", FlowJSON: `{"data":{"nodes":[{"id":"input"}],"edges":[]}}`, ProjectID: "prj-demo", OwnerSubject: "alice", Replicas: mlaiopsv1.ReplicaSpec{Min: 1, Max: 1}},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(agent, &appsv1.Deployment{}).WithObjects(agent).Build()
	reconciler := &AgentReconciler{Client: client, LangflowSidecarImage: "registry/kionga-api:1", GatewayURL: "http://gateway:8080"}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	name := platformapi.AgentDNSName(agent.Name)
	if err := client.Get(context.Background(), types.NamespacedName{Name: name, Namespace: agent.Namespace}, &deployment); err != nil {
		t.Fatal(err)
	}
	if len(deployment.Spec.Template.Spec.Volumes) != 1 || len(deployment.Spec.Template.Spec.Containers[0].VolumeMounts) != 1 {
		t.Fatalf("published flow is not mounted: %#v", deployment.Spec.Template.Spec)
	}
	var config corev1.ConfigMap
	configName := deployment.Spec.Template.Spec.Volumes[0].ConfigMap.Name
	if err := client.Get(context.Background(), types.NamespacedName{Name: configName, Namespace: agent.Namespace}, &config); err != nil {
		t.Fatal(err)
	}
	if config.Data["flow.json"] != agent.Spec.FlowJSON {
		t.Fatal("mounted flow differs from the published snapshot")
	}
	if len(deployment.Spec.Template.Spec.Containers) != 3 || deployment.Spec.Template.Spec.Containers[2].Name != "kionga-api" {
		t.Fatal("published flow requires an authenticated Kionga resource sidecar")
	}
	var secret corev1.Secret
	if err := client.Get(context.Background(), types.NamespacedName{Name: platformapi.AgentAPISecretName(agent.Name), Namespace: agent.Namespace}, &secret); err != nil {
		t.Fatal(err)
	}
	if len(secret.Data["api-token"]) < 32 {
		t.Fatal("published agent API identity token was not provisioned")
	}
}

func TestAgentControllerMountsNativeVersionAndRejectsOtherModules(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = autoscalingv2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = mlaiopsv1.AddToScheme(scheme)
	for _, tc := range []struct {
		module string
		mount  bool
	}{{"agents.native_graph:build_native", true}, {"evil.module:build", false}} {
		agent := &mlaiopsv1.KiongaAgent{
			TypeMeta:   metav1.TypeMeta{APIVersion: "mlaiops.io/v1alpha1", Kind: "KiongaAgent"},
			ObjectMeta: metav1.ObjectMeta{Name: "native-agent", Namespace: "team-a"},
			Spec:       mlaiopsv1.KiongaAgentSpec{Version: "v1", Image: "registry/agent-runtime:1", GraphModule: tc.module, FlowJSON: `{"artifact_version":1}`, ProjectID: "prj-demo", OwnerSubject: "alice", Replicas: mlaiopsv1.ReplicaSpec{Min: 1, Max: 1}},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(agent, &appsv1.Deployment{}).WithObjects(agent).Build()
		reconciler := &AgentReconciler{Client: client, LangflowSidecarImage: "registry/kionga-api:1", GatewayURL: "http://gateway:8080"}
		_, _ = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}})
		var deployment appsv1.Deployment
		err := client.Get(context.Background(), types.NamespacedName{Name: platformapi.AgentDNSName(agent.Name), Namespace: agent.Namespace}, &deployment)
		if !tc.mount {
			if err == nil {
				t.Fatalf("%s: a mounted graph must only run with a published-graph adapter", tc.module)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		mounts := deployment.Spec.Template.Spec.Containers[0].VolumeMounts
		if len(mounts) != 1 || mounts[0].MountPath != "/app/flows" || !mounts[0].ReadOnly || len(deployment.Spec.Template.Spec.Containers) != 3 {
			t.Fatalf("native version must be mounted read-only with the API sidecar: %#v", deployment.Spec.Template.Spec)
		}
	}
}

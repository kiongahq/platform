package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
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
	// agent-runtime requires the gateway's internal token on /invoke and
	// /stream; the operator must supply it by Secret reference, never inline.
	agentEnv := map[string]corev1.EnvVar{}
	for _, variable := range deployment.Spec.Template.Spec.Containers[0].Env {
		agentEnv[variable.Name] = variable
	}
	if agentEnv["MLAIOPS_RUNTIME_AUTH"].Value != "required" {
		t.Fatalf("agent runtime auth must be required: %#v", agentEnv["MLAIOPS_RUNTIME_AUTH"])
	}
	token := agentEnv[RuntimeTokenKey]
	if token.Value != "" || token.ValueFrom == nil || token.ValueFrom.SecretKeyRef == nil || token.ValueFrom.SecretKeyRef.Name != "kionga-gateway" || token.ValueFrom.SecretKeyRef.Key != RuntimeTokenKey {
		t.Fatalf("internal token must come from the platform Secret: %#v", token)
	}
	// trace-proxy#40: the agent authenticates to its sidecar with a per-agent
	// key from a Secret it owns; the sidecar sees only the key's hash and
	// gets the provider key from the provider Secret.
	var traceSecret corev1.Secret
	if err := client.Get(context.Background(), types.NamespacedName{Name: TraceProxySecretName(workloadName), Namespace: "team-a"}, &traceSecret); err != nil {
		t.Fatal(err)
	}
	if len(traceSecret.OwnerReferences) != 1 || traceSecret.OwnerReferences[0].Name != agent.Name || len(traceSecret.Data["key"]) < 32 {
		t.Fatalf("trace-proxy key Secret must be owned by the agent and hold a generated key: %#v", traceSecret.ObjectMeta)
	}
	var keys []map[string]string
	if err := json.Unmarshal(traceSecret.Data["keys.json"], &keys); err != nil || len(keys) != 1 {
		t.Fatalf("keys.json: %v %s", err, traceSecret.Data["keys.json"])
	}
	sum := sha256.Sum256(traceSecret.Data["key"])
	if keys[0]["key_sha256"] != hex.EncodeToString(sum[:]) || keys[0]["key_id"] != workloadName || keys[0]["project"] != "team-a" || keys[0]["agent"] != agent.Name || strings.Contains(string(traceSecret.Data["keys.json"]), string(traceSecret.Data["key"])) {
		t.Fatalf("keys.json must carry only the key hash with its project and agent: %s", traceSecret.Data["keys.json"])
	}
	if apiKey := agentEnv["OPENAI_API_KEY"]; apiKey.ValueFrom == nil || apiKey.ValueFrom.SecretKeyRef.Name != traceSecret.Name || apiKey.ValueFrom.SecretKeyRef.Key != "key" {
		t.Fatalf("agent must use its trace-proxy key as OPENAI_API_KEY: %#v", apiKey)
	}
	sidecar := deployment.Spec.Template.Spec.Containers[1]
	sidecarEnv := map[string]corev1.EnvVar{}
	for _, variable := range sidecar.Env {
		sidecarEnv[variable.Name] = variable
	}
	provider := sidecarEnv["LLM_UPSTREAM_API_KEY"]
	if sidecarEnv["TRACE_PROXY_KEYS_FILE"].Value != "/run/secrets/trace-proxy/keys.json" || len(sidecar.VolumeMounts) != 1 || sidecar.VolumeMounts[0].MountPath != "/run/secrets/trace-proxy" || provider.ValueFrom == nil || provider.ValueFrom.SecretKeyRef.Name != "kionga-llm-provider" || provider.ValueFrom.SecretKeyRef.Key != "LLM_UPSTREAM_API_KEY" {
		t.Fatalf("trace-proxy sidecar wiring: %#v", sidecar)
	}
	if _, ok := sidecarEnv["OPENAI_API_KEY"]; ok {
		t.Fatal("the sidecar must not receive the agent key")
	}
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Secret != nil && (len(volume.Secret.Items) != 1 || volume.Secret.Items[0].Key != "keys.json") {
			t.Fatalf("only keys.json may be mounted into the sidecar: %#v", volume.Secret)
		}
	}
	firstKey := string(traceSecret.Data["key"])
	if agentEnv["MLAIOPS_RUNTIME_PORT"].Value != "8080" || deployment.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort != 8080 {
		t.Fatalf("runtime port and container port disagree: %#v", deployment.Spec.Template.Spec.Containers[0])
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
	if err := client.Get(context.Background(), types.NamespacedName{Name: TraceProxySecretName(workloadName), Namespace: "team-a"}, &traceSecret); err != nil || string(traceSecret.Data["key"]) != firstKey {
		t.Fatalf("trace-proxy key must survive reconcile (err %v)", err)
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
	if len(deployment.Spec.Template.Spec.Volumes) != 2 || deployment.Spec.Template.Spec.Volumes[0].Name != "flow" || len(deployment.Spec.Template.Spec.Containers[0].VolumeMounts) != 1 {
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

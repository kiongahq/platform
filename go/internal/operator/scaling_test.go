package operator

import (
	"strings"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clienttesting "k8s.io/client-go/testing"

	platformapi "github.com/kiongahq/platform/pkg/api"
	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

func TestPlanScaling(t *testing.T) {
	cases := []struct {
		name       string
		requested  kiongav1.ScalingSpec
		replicas   kiongav1.WorkloadReplicaSpec
		defaultFor string
		keda       bool
		metric     string
		min, max   int32
		target     int32
		reason     string
	}{
		{"auto picks requests with KEDA", kiongav1.ScalingSpec{}, kiongav1.WorkloadReplicaSpec{Min: 0, Max: 4}, "", true, "requests", 0, 4, 100, "RequestsScaling"},
		{"auto picks cpu without KEDA", kiongav1.ScalingSpec{}, kiongav1.WorkloadReplicaSpec{Min: 0, Max: 4}, "", false, "cpu", 1, 4, 70, "CPUScaling"},
		{"requests without KEDA falls back", kiongav1.ScalingSpec{Metric: "requests", Target: 10}, kiongav1.WorkloadReplicaSpec{Min: 0, Max: 0}, "", false, "cpu", 1, 1, 10, "KEDAHTTPAddonMissing"},
		{"explicit cpu with KEDA", kiongav1.ScalingSpec{Metric: "cpu", Target: 50}, kiongav1.WorkloadReplicaSpec{Min: 2, Max: 5}, "", true, "cpu", 2, 5, 50, "CPUScaling"},
		{"fixed replicas", kiongav1.ScalingSpec{Metric: "cpu"}, kiongav1.WorkloadReplicaSpec{Min: 3, Max: 3}, "", true, "cpu", 3, 3, 70, "FixedReplicas"},
		{"kind default requests", kiongav1.ScalingSpec{}, kiongav1.WorkloadReplicaSpec{Min: 0, Max: 5}, "requests", true, "requests", 0, 5, 100, "RequestsScaling"},
	}
	for _, test := range cases {
		plan := PlanScaling(test.requested, test.replicas, test.defaultFor, test.keda)
		if plan.Metric != test.metric || plan.Min != test.min || plan.Max != test.max || plan.Target != test.target || plan.Condition.Reason != test.reason {
			t.Errorf("%s: got %s %d-%d target %d reason %s", test.name, plan.Metric, plan.Min, plan.Max, plan.Target, plan.Condition.Reason)
		}
	}
	if plan := PlanScaling(kiongav1.ScalingSpec{}, kiongav1.WorkloadReplicaSpec{Min: 0, Max: 2}, "", true); plan.InitialReplicas() != 1 || !plan.Autoscaled() {
		t.Fatalf("scale-to-zero workloads start with one replica: %+v", plan)
	}
}

func TestRouteServiceNameFitsDNSLabel(t *testing.T) {
	agent := platformapi.AgentDNSName("agt-" + strings.Repeat("x", 80))
	for _, workload := range []string{"docs", agent} {
		name := RouteServiceName(workload)
		if problems := validation.IsDNS1035Label(name); len(problems) > 0 {
			t.Fatalf("%s: %v", name, problems)
		}
	}
	if RouteServiceName(agent) == RouteServiceName(platformapi.AgentDNSName("agt-"+strings.Repeat("y", 80))) {
		t.Fatal("truncated route names must stay unique")
	}
}

func TestDetectCapabilities(t *testing.T) {
	discovery := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
	if got, err := DetectCapabilities(discovery); err != nil || got.KEDAHTTP || got.KServe {
		t.Fatalf("empty cluster: %+v %v", got, err)
	}
	discovery.Resources = []*metav1.APIResourceList{
		{GroupVersion: "http.keda.sh/v1alpha1", APIResources: []metav1.APIResource{{Name: "httpscaledobjects", Kind: "HTTPScaledObject"}}},
		{GroupVersion: "serving.kserve.io/v1beta1", APIResources: []metav1.APIResource{{Name: "inferenceservices", Kind: "InferenceService"}}},
	}
	if got, err := DetectCapabilities(discovery); err != nil || !got.KEDAHTTP || !got.KServe {
		t.Fatalf("full cluster: %+v %v", got, err)
	}
}

func TestLoadWorkloadConfig(t *testing.T) {
	values := map[string]string{
		"KIONGA_APPLICATIONS_BASE_DOMAIN": "apps.example.net.", "KIONGA_APPLICATIONS_TLS_SECRET": "wildcard",
		"KIONGA_INGRESS_CLASS": "traefik", "KIONGA_SSO_UPSTREAM": "kionga-gateway.kionga-system.svc.cluster.local:8080",
		"KIONGA_BUILD_REGISTRY": "registry.example.net/kionga/", "KIONGA_BUILD_REGISTRY_INSECURE": "true",
	}
	config, err := LoadWorkloadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if config.BaseDomain != "apps.example.net" || config.Build.Registry != "registry.example.net/kionga" || !config.Build.Insecure || config.Build.Image != DefaultBuildKitImage {
		t.Fatalf("config: %+v", config)
	}
	if config.InterceptorHost != "keda-add-ons-http-interceptor-proxy.keda.svc.cluster.local" || config.InterceptorPort != 8080 || config.SSOUpstreamPort != 8080 {
		t.Fatalf("host:port parsing: %+v", config)
	}
	values["KIONGA_SSO_UPSTREAM"] = "no-port"
	if _, err := LoadWorkloadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("a malformed upstream must stop the operator at startup")
	}
}

func agentScheme(t *testing.T, keda bool) *runtime.Scheme {
	if keda {
		return testScheme(t, HTTPScaledObjectGVK)
	}
	return testScheme(t)
}

func TestAgentRequestsScalingThroughKEDA(t *testing.T) {
	for _, keda := range []bool{true, false} {
		name := platformapi.AgentDNSName("agt-scale")
		agent := &kiongav1.KiongaAgent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: map[string]string{"kionga.dev/resource-id": "agt-scale"}},
			Spec: kiongav1.KiongaAgentSpec{Version: "1", Image: "agent:1", GraphModule: "agents.x:graph",
				Replicas: kiongav1.ReplicaSpec{Min: 1, Max: 4}, Resources: corev1.ResourceRequirements{}},
		}
		c := testClient(agentScheme(t, keda), agent)
		reconcileOnce(t, &AgentReconciler{Client: c, Config: testConfig(), Capabilities: Capabilities{KEDAHTTP: keda}}, name)
		var stored kiongav1.KiongaAgent
		get(t, c, name, &stored)
		if keda {
			scaled := unstructuredOf(HTTPScaledObjectGVK)
			get(t, c, name, scaled)
			ownedBy(t, scaled, name)
			minimum, _, _ := unstructured.NestedInt64(scaled.Object, "spec", "replicas", "min")
			if minimum != 1 || stored.Status.URL != "http://"+RouteServiceName(name)+".team-a.svc.cluster.local:8080" {
				t.Fatalf("keda agent: min=%d url=%s", minimum, stored.Status.URL)
			}
			absent(t, c, name, &autoscalingv2.HorizontalPodAutoscaler{})
			continue
		}
		var hpa autoscalingv2.HorizontalPodAutoscaler
		get(t, c, name, &hpa)
		if hpa.Spec.MaxReplicas != 4 || stored.Status.URL != "http://"+name+".team-a.svc.cluster.local" {
			t.Fatalf("cpu agent: hpa=%+v url=%s", hpa.Spec, stored.Status.URL)
		}
		if condition(stored.Status.Conditions, ConditionScaling).Reason != "CPUScaling" {
			t.Fatalf("conditions: %#v", stored.Status.Conditions)
		}
	}
}

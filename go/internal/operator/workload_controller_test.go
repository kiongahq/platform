package operator

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

func testConfig() WorkloadConfig {
	return WorkloadConfig{
		BaseDomain: "kionga.example.net", TLSSecret: "kionga-wildcard-tls", IngressClass: "traefik",
		InterceptorHost: "keda-add-ons-http-interceptor-proxy.keda.svc.cluster.local", InterceptorPort: 8080,
		SSOUpstreamHost: "kionga-gateway.kionga-system.svc.cluster.local", SSOUpstreamPort: 8080,
		Build: BuildConfig{Registry: "registry.example.net/kionga", PushSecret: "registry-push", Image: DefaultBuildKitImage},
	}
}

func application(name string, spec kiongav1.WorkloadSpec) *kiongav1.KiongaApplication {
	return &kiongav1.KiongaApplication{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Generation: 1}, Spec: spec}
}

func applicationReconciler(c client.Client, keda bool) *WorkloadReconciler {
	return &WorkloadReconciler{Client: c, Kind: ApplicationKind, Config: testConfig(), Capabilities: Capabilities{KEDAHTTP: keda}}
}

func TestApplicationCreatesOwnedWorkloadWithProbesIngressAndBudget(t *testing.T) {
	replicas := kiongav1.WorkloadReplicaSpec{Min: 2, Max: 6}
	app := application("billing", kiongav1.WorkloadSpec{
		Image: "registry.example.net/billing:1", Port: 9000, HealthPath: "/healthz", Visibility: kiongav1.VisibilityPublic,
		Replicas: &replicas, Scaling: kiongav1.ScalingSpec{Metric: kiongav1.ScalingMetricCPU, Target: 60},
		Env: []kiongav1.EnvVar{{Name: "MODE", Value: "live"}},
	})
	c := testClient(testScheme(t), app)
	reconciler := applicationReconciler(c, false)
	reconcileOnce(t, reconciler, "billing")

	var deployment appsv1.Deployment
	get(t, c, "billing", &deployment)
	ownedBy(t, &deployment, "billing")
	container := deployment.Spec.Template.Spec.Containers[0]
	if container.Image != "registry.example.net/billing:1" || container.Ports[0].ContainerPort != 9000 || *deployment.Spec.Replicas != 2 {
		t.Fatalf("deployment: image=%s port=%d replicas=%d", container.Image, container.Ports[0].ContainerPort, *deployment.Spec.Replicas)
	}
	for name, probe := range map[string]*corev1.Probe{"startup": container.StartupProbe, "readiness": container.ReadinessProbe, "liveness": container.LivenessProbe} {
		if probe == nil || probe.HTTPGet == nil || probe.HTTPGet.Path != "/healthz" || probe.HTTPGet.Port.StrVal != "http" {
			t.Fatalf("%s probe must GET healthPath on the http port: %#v", name, probe)
		}
	}
	rolling := deployment.Spec.Strategy.RollingUpdate
	if deployment.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType || rolling == nil || rolling.MaxUnavailable.IntValue() != 0 {
		t.Fatalf("rollouts must never drop below the current replica count: %#v", deployment.Spec.Strategy)
	}
	env := map[string]string{}
	for _, variable := range container.Env {
		env[variable.Name] = variable.Value
	}
	if env["PORT"] != "9000" || env["MODE"] != "live" {
		t.Fatalf("env: %#v", env)
	}
	if security := container.SecurityContext; security == nil || *security.AllowPrivilegeEscalation || !*security.RunAsNonRoot {
		t.Fatalf("container must run non-root without privilege escalation: %#v", security)
	}

	var service corev1.Service
	get(t, c, "billing", &service)
	ownedBy(t, &service, "billing")
	var ingress networkingv1.Ingress
	get(t, c, "billing", &ingress)
	ownedBy(t, &ingress, "billing")
	rule := ingress.Spec.Rules[0]
	backend := rule.HTTP.Paths[0].Backend.Service
	if rule.Host != "billing.kionga.example.net" || *ingress.Spec.IngressClassName != "traefik" || ingress.Spec.TLS[0].SecretName != "kionga-wildcard-tls" || ingress.Spec.TLS[0].Hosts[0] != rule.Host {
		t.Fatalf("ingress host/class/tls: %#v", ingress.Spec)
	}
	if backend.Name != "billing" || backend.Port.Number != 80 {
		t.Fatalf("cpu-scaled public ingress must hit the Service directly: %#v", backend)
	}
	var budget policyv1.PodDisruptionBudget
	get(t, c, "billing", &budget)
	ownedBy(t, &budget, "billing")
	if budget.Spec.MaxUnavailable.IntValue() != 1 {
		t.Fatalf("budget: %#v", budget.Spec)
	}
	var hpa autoscalingv2.HorizontalPodAutoscaler
	get(t, c, "billing", &hpa)
	ownedBy(t, &hpa, "billing")
	if *hpa.Spec.MinReplicas != 2 || hpa.Spec.MaxReplicas != 6 || *hpa.Spec.Metrics[0].Resource.Target.AverageUtilization != 60 {
		t.Fatalf("hpa: %#v", hpa.Spec)
	}

	var stored kiongav1.KiongaApplication
	get(t, c, "billing", &stored)
	if stored.Status.Phase != "Progressing" || stored.Status.URL != "https://billing.kionga.example.net" || stored.Status.InternalURL != "http://billing.team-a.svc.cluster.local" || stored.Status.Image != "registry.example.net/billing:1" {
		t.Fatalf("status before rollout: %#v", stored.Status)
	}
	markAvailable(t, c, "billing", 2)
	reconcileOnce(t, reconciler, "billing")
	get(t, c, "billing", &stored)
	if stored.Status.Phase != "Ready" || stored.Status.ReadyReplicas != 2 || condition(stored.Status.Conditions, ConditionReady).Status != metav1.ConditionTrue {
		t.Fatalf("status after rollout: %#v", stored.Status)
	}
}

func TestApplicationImageChangeRollsAndKeepsAutoscaledReplicas(t *testing.T) {
	replicas := kiongav1.WorkloadReplicaSpec{Min: 1, Max: 4}
	app := application("shop", kiongav1.WorkloadSpec{Image: "shop:1", Replicas: &replicas, Scaling: kiongav1.ScalingSpec{Metric: kiongav1.ScalingMetricCPU}})
	c := testClient(testScheme(t), app)
	reconciler := applicationReconciler(c, false)
	reconcileOnce(t, reconciler, "shop")

	var deployment appsv1.Deployment
	get(t, c, "shop", &deployment)
	scaled := int32(3) // the HPA scaled up
	deployment.Spec.Replicas = &scaled
	if err := c.Update(context.Background(), &deployment); err != nil {
		t.Fatal(err)
	}
	var stored kiongav1.KiongaApplication
	get(t, c, "shop", &stored)
	stored.Spec.Image = "shop:2"
	if err := c.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "shop")
	get(t, c, "shop", &deployment)
	if deployment.Spec.Template.Spec.Containers[0].Image != "shop:2" {
		t.Fatalf("new image did not reach the pod template: %s", deployment.Spec.Template.Spec.Containers[0].Image)
	}
	if *deployment.Spec.Replicas != 3 {
		t.Fatalf("the operator fought the autoscaler: replicas=%d", *deployment.Spec.Replicas)
	}
}

func TestApplicationRequestsScalingWithKEDAUsesInterceptor(t *testing.T) {
	replicas := kiongav1.WorkloadReplicaSpec{Min: 0, Max: 3}
	app := application("docs", kiongav1.WorkloadSpec{Image: "docs:1", Visibility: kiongav1.VisibilityPublic, Replicas: &replicas})
	c := testClient(testScheme(t, HTTPScaledObjectGVK), app)
	reconciler := applicationReconciler(c, true)
	reconcileOnce(t, reconciler, "docs")

	scaled := unstructuredOf(HTTPScaledObjectGVK)
	get(t, c, "docs", scaled)
	ownedBy(t, scaled, "docs")
	minimum, _, _ := unstructured.NestedInt64(scaled.Object, "spec", "replicas", "min")
	maximum, _, _ := unstructured.NestedInt64(scaled.Object, "spec", "replicas", "max")
	target, _, _ := unstructured.NestedInt64(scaled.Object, "spec", "scalingMetric", "concurrency", "targetValue")
	service, _, _ := unstructured.NestedString(scaled.Object, "spec", "scaleTargetRef", "service")
	hosts, _, _ := unstructured.NestedStringSlice(scaled.Object, "spec", "hosts")
	if minimum != 0 || maximum != 3 || target != int64(defaultRequestsTarget) || service != "docs" {
		t.Fatalf("HTTPScaledObject spec: %#v", scaled.Object["spec"])
	}
	if !contains(hosts, "docs-route.team-a.svc.cluster.local") || !contains(hosts, "docs.kionga.example.net") {
		t.Fatalf("interceptor must accept the in-cluster and public hosts: %v", hosts)
	}
	var route corev1.Service
	get(t, c, "docs-route", &route)
	ownedBy(t, &route, "docs")
	if route.Spec.Type != corev1.ServiceTypeExternalName || route.Spec.ExternalName != testConfig().InterceptorHost {
		t.Fatalf("route service: %#v", route.Spec)
	}
	var ingress networkingv1.Ingress
	get(t, c, "docs", &ingress)
	if backend := ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service; backend.Name != "docs-route" || backend.Port.Number != 8080 {
		t.Fatalf("requests-scaled ingress must go through the interceptor: %#v", backend)
	}
	absent(t, c, "docs", &autoscalingv2.HorizontalPodAutoscaler{})
	absent(t, c, "docs", &policyv1.PodDisruptionBudget{})
	var deployment appsv1.Deployment
	get(t, c, "docs", &deployment)
	if *deployment.Spec.Replicas != 1 {
		t.Fatalf("a scale-to-zero workload starts with one replica to prove the image: %d", *deployment.Spec.Replicas)
	}
	var stored kiongav1.KiongaApplication
	get(t, c, "docs", &stored)
	if stored.Status.InternalURL != "http://docs-route.team-a.svc.cluster.local:8080" || stored.Status.ScalingMetric != kiongav1.ScalingMetricRequests {
		t.Fatalf("status: %#v", stored.Status)
	}

	// Switching to CPU removes the KEDA objects the operator created.
	stored.Spec.Scaling.Metric = kiongav1.ScalingMetricCPU
	if err := c.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "docs")
	absent(t, c, "docs", unstructuredOf(HTTPScaledObjectGVK))
	absent(t, c, "docs-route", &corev1.Service{})
	var hpa autoscalingv2.HorizontalPodAutoscaler
	get(t, c, "docs", &hpa)
	if *hpa.Spec.MinReplicas != 1 {
		t.Fatalf("an HPA cannot reach zero, min must be raised to 1: %d", *hpa.Spec.MinReplicas)
	}
}

func TestApplicationRequestsScalingWithoutKEDAFallsBackToCPU(t *testing.T) {
	replicas := kiongav1.WorkloadReplicaSpec{Min: 0, Max: 3}
	app := application("api", kiongav1.WorkloadSpec{Image: "api:1", Replicas: &replicas, Scaling: kiongav1.ScalingSpec{Metric: kiongav1.ScalingMetricRequests}})
	c := testClient(testScheme(t), app)
	reconcileOnce(t, applicationReconciler(c, false), "api")

	var hpa autoscalingv2.HorizontalPodAutoscaler
	get(t, c, "api", &hpa)
	if *hpa.Spec.MinReplicas != 1 || hpa.Spec.MaxReplicas != 3 {
		t.Fatalf("fallback hpa: %#v", hpa.Spec)
	}
	var stored kiongav1.KiongaApplication
	get(t, c, "api", &stored)
	scaling := condition(stored.Status.Conditions, ConditionScaling)
	if scaling.Status != metav1.ConditionFalse || scaling.Reason != "KEDAHTTPAddonMissing" || stored.Status.ScalingMetric != kiongav1.ScalingMetricCPU {
		t.Fatalf("missing add-on must show in status, not crash: %#v", stored.Status)
	}
}

func TestApplicationSSOFailsClosedWithoutUpstream(t *testing.T) {
	app := application("admin", kiongav1.WorkloadSpec{Image: "admin:1"}) // applications default to sso
	c := testClient(testScheme(t), app)
	reconciler := applicationReconciler(c, false)
	reconciler.Config.SSOUpstreamHost = ""
	reconcileOnce(t, reconciler, "admin")
	absent(t, c, "admin", &networkingv1.Ingress{})
	var stored kiongav1.KiongaApplication
	get(t, c, "admin", &stored)
	if ingress := condition(stored.Status.Conditions, ConditionIngress); ingress.Reason != "SSOUpstreamNotConfigured" || stored.Status.URL != "" {
		t.Fatalf("an sso app must not be published without the gateway in front: %#v", stored.Status)
	}
}

func TestApplicationSSORoutesThroughGateway(t *testing.T) {
	app := application("admin", kiongav1.WorkloadSpec{Image: "admin:1", Visibility: kiongav1.VisibilitySSO})
	c := testClient(testScheme(t), app)
	reconcileOnce(t, applicationReconciler(c, false), "admin")
	var ingress networkingv1.Ingress
	get(t, c, "admin", &ingress)
	backend := ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service
	if backend.Name != "admin-sso" || backend.Port.Number != 8080 || ingress.Annotations[VisibilityAnnotation] != "sso" || ingress.Annotations[UpstreamAnnotation] != "http://admin.team-a.svc.cluster.local" {
		t.Fatalf("sso ingress: backend=%#v annotations=%v", backend, ingress.Annotations)
	}
	var gateway corev1.Service
	get(t, c, "admin-sso", &gateway)
	ownedBy(t, &gateway, "admin")
	if gateway.Spec.ExternalName != testConfig().SSOUpstreamHost {
		t.Fatalf("sso service: %#v", gateway.Spec)
	}

	// Going internal removes the Ingress and the gateway Service.
	var stored kiongav1.KiongaApplication
	get(t, c, "admin", &stored)
	stored.Spec.Visibility = kiongav1.VisibilityInternal
	if err := c.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, applicationReconciler(c, false), "admin")
	absent(t, c, "admin", &networkingv1.Ingress{})
	absent(t, c, "admin-sso", &corev1.Service{})
}

func TestApplicationRejectsUnlabelledSecret(t *testing.T) {
	app := application("leaky", kiongav1.WorkloadSpec{Image: "leaky:1", SecretRefs: []kiongav1.SecretReference{{Name: "kionga-llm-provider"}}})
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kionga-llm-provider", Namespace: testNamespace}, Data: map[string][]byte{"LLM_UPSTREAM_API_KEY": []byte("x")}}
	c := testClient(testScheme(t), app, secret)
	reconcileOnce(t, applicationReconciler(c, false), "leaky")
	absent(t, c, "leaky", &appsv1.Deployment{})
	var stored kiongav1.KiongaApplication
	get(t, c, "leaky", &stored)
	if stored.Status.Phase != "Failed" || condition(stored.Status.Conditions, ConditionReady).Reason != "SecretNotAllowed" {
		t.Fatalf("an unlabelled platform Secret must not be mounted: %#v", stored.Status)
	}

	secret.Labels = map[string]string{WorkloadSecretLabel: "true"}
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, applicationReconciler(c, false), "leaky")
	var deployment appsv1.Deployment
	get(t, c, "leaky", &deployment)
	if from := deployment.Spec.Template.Spec.Containers[0].EnvFrom; len(from) != 1 || from[0].SecretRef.Name != "kionga-llm-provider" {
		t.Fatalf("labelled Secret must load through envFrom: %#v", from)
	}
}

func TestApplicationNameMustFitDNSLabel(t *testing.T) {
	name := strings.Repeat("a", MaxWorkloadNameLength+1)
	app := application(name, kiongav1.WorkloadSpec{Image: "x:1"})
	c := testClient(testScheme(t), app)
	reconcileOnce(t, applicationReconciler(c, false), name)
	var stored kiongav1.KiongaApplication
	get(t, c, name, &stored)
	if condition(stored.Status.Conditions, ConditionReady).Reason != "InvalidName" {
		t.Fatalf("status: %#v", stored.Status)
	}
}

func TestFunctionDefaultsToInternalScaleToZero(t *testing.T) {
	fn := &kiongav1.KiongaFunction{ObjectMeta: metav1.ObjectMeta{Name: "resize", Namespace: testNamespace}, Spec: kiongav1.WorkloadSpec{Image: "resize:1"}}
	c := testClient(testScheme(t, HTTPScaledObjectGVK), fn)
	reconciler := &WorkloadReconciler{Client: c, Kind: FunctionKind, Config: testConfig(), Capabilities: Capabilities{KEDAHTTP: true}}
	reconcileOnce(t, reconciler, "resize")

	absent(t, c, "resize", &networkingv1.Ingress{})
	scaled := unstructuredOf(HTTPScaledObjectGVK)
	get(t, c, "resize", scaled)
	minimum, _, _ := unstructured.NestedInt64(scaled.Object, "spec", "replicas", "min")
	if minimum != 0 {
		t.Fatalf("functions scale to zero by default: min=%d", minimum)
	}
	var service corev1.Service
	get(t, c, "resize", &service)
	if service.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Fatalf("function service: %#v", service.Spec)
	}
	var stored kiongav1.KiongaFunction
	get(t, c, "resize", &stored)
	if stored.Status.URL != "" || stored.Status.InternalURL != "http://resize-route.team-a.svc.cluster.local:8080" {
		t.Fatalf("function status: %#v", stored.Status)
	}

	// Scaled to zero is Ready: the next request starts a replica.
	var deployment appsv1.Deployment
	get(t, c, "resize", &deployment)
	zero := int32(0)
	deployment.Spec.Replicas = &zero
	if err := c.Update(context.Background(), &deployment); err != nil {
		t.Fatal(err)
	}
	markAvailable(t, c, "resize", 0)
	reconcileOnce(t, reconciler, "resize")
	get(t, c, "resize", &stored)
	if stored.Status.Phase != "Ready" || condition(stored.Status.Conditions, ConditionReady).Reason != "ScaledToZero" {
		t.Fatalf("scaled-to-zero status: %#v", stored.Status)
	}
}

func TestFunctionWithoutKEDAKeepsOneReplica(t *testing.T) {
	fn := &kiongav1.KiongaFunction{ObjectMeta: metav1.ObjectMeta{Name: "resize", Namespace: testNamespace}, Spec: kiongav1.WorkloadSpec{Image: "resize:1"}}
	c := testClient(testScheme(t), fn)
	reconcileOnce(t, &WorkloadReconciler{Client: c, Kind: FunctionKind, Config: testConfig()}, "resize")
	var hpa autoscalingv2.HorizontalPodAutoscaler
	get(t, c, "resize", &hpa)
	if *hpa.Spec.MinReplicas != 1 || hpa.Spec.MaxReplicas != 5 {
		t.Fatalf("function fallback: %#v", hpa.Spec)
	}
	var stored kiongav1.KiongaFunction
	get(t, c, "resize", &stored)
	if stored.Status.InternalURL != "http://resize.team-a.svc.cluster.local" {
		t.Fatalf("without KEDA the gateway calls the Service directly: %s", stored.Status.InternalURL)
	}
}

func TestDeploymentHealth(t *testing.T) {
	two := int32(2)
	cases := []struct {
		name   string
		status appsv1.DeploymentStatus
		phase  string
		reason string
	}{
		{"rolled out", appsv1.DeploymentStatus{UpdatedReplicas: 2, AvailableReplicas: 2}, "Ready", "WorkloadAvailable"},
		{"rolling", appsv1.DeploymentStatus{UpdatedReplicas: 1, AvailableReplicas: 2}, "Progressing", "WorkloadProgressing"},
		{"pod lost", appsv1.DeploymentStatus{UpdatedReplicas: 2, AvailableReplicas: 1}, "Degraded", "WorkloadUnavailable"},
		{"crash looping", appsv1.DeploymentStatus{UpdatedReplicas: 2, AvailableReplicas: 0, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: "ReplicaSet has timed out progressing"}}}, "Failed", "ProgressDeadlineExceeded"},
	}
	for _, test := range cases {
		deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "x"}, Spec: appsv1.DeploymentSpec{Replicas: &two}, Status: test.status}
		phase, ready := DeploymentHealth(deployment, 2)
		if phase != test.phase || ready.Reason != test.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", test.name, phase, ready.Reason, test.phase, test.reason)
		}
	}
}

// A pod that dies after a good rollout must surface as Degraded in status.
func TestApplicationUnavailableDeploymentSurfacesInStatus(t *testing.T) {
	replicas := kiongav1.WorkloadReplicaSpec{Min: 2, Max: 2}
	app := application("ledger", kiongav1.WorkloadSpec{Image: "ledger:1", Replicas: &replicas, Visibility: kiongav1.VisibilityInternal})
	c := testClient(testScheme(t), app)
	reconciler := applicationReconciler(c, false)
	reconcileOnce(t, reconciler, "ledger")
	markAvailable(t, c, "ledger", 2)
	reconcileOnce(t, reconciler, "ledger")
	// One pod dies: the rollout stays complete (both pods are the current
	// revision) but only one is available.
	var deployment appsv1.Deployment
	get(t, c, "ledger", &deployment)
	deployment.Status.AvailableReplicas, deployment.Status.ReadyReplicas = 1, 1
	if err := c.Status().Update(context.Background(), &deployment); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "ledger")
	var stored kiongav1.KiongaApplication
	get(t, c, "ledger", &stored)
	ready := condition(stored.Status.Conditions, ConditionReady)
	if stored.Status.Phase != "Degraded" || ready.Status != metav1.ConditionFalse || ready.Reason != "WorkloadUnavailable" {
		t.Fatalf("status: %#v", stored.Status)
	}
}

func TestSourceBuildRunsJobThenRollsToDigest(t *testing.T) {
	source := &kiongav1.SourceSpec{Git: "https://github.com/acme/billing.git", Ref: "main", ContextDir: "service", Dockerfile: "Dockerfile.prod"}
	app := application("billing", kiongav1.WorkloadSpec{Source: source, Visibility: kiongav1.VisibilityInternal})
	c := testClient(testScheme(t), app)
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	reconciler := applicationReconciler(c, false)
	reconciler.Now = func() time.Time { return now }
	reconcileOnce(t, reconciler, "billing")

	hash := SourceHash(source, "")
	jobName := BuildJobName("billing", hash)
	var job batchv1.Job
	get(t, c, jobName, &job)
	ownedBy(t, &job, "billing")
	command := strings.Join(job.Spec.Template.Spec.Containers[0].Command, " ")
	for _, want := range []string{"context=https://github.com/acme/billing.git#main:service", "filename=Dockerfile.prod", "name=registry.example.net/kionga/team-a-billing:" + hash + ",push=true"} {
		if !strings.Contains(command, want) {
			t.Fatalf("build command lacks %q: %s", want, command)
		}
	}
	if security := job.Spec.Template.Spec.Containers[0].SecurityContext; security.Privileged != nil || *security.RunAsUser != 1000 {
		t.Fatalf("builds must be rootless and unprivileged: %#v", security)
	}
	absent(t, c, "billing", &appsv1.Deployment{})
	var stored kiongav1.KiongaApplication
	get(t, c, "billing", &stored)
	if stored.Status.Phase != "Building" || stored.Status.LastBuild == nil || stored.Status.LastBuild.Phase != "Running" {
		t.Fatalf("status while building: %#v", stored.Status)
	}

	// The build succeeds and writes the digest to its termination message.
	digest := "sha256:" + strings.Repeat("ab", 32)
	completed := metav1.NewTime(now)
	job.Status = batchv1.JobStatus{Succeeded: 1, CompletionTime: &completed, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}
	if err := c.Status().Update(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: jobName + "-x", Namespace: testNamespace, Labels: map[string]string{"job-name": jobName}}}
	if err := c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "buildkit", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: digest}}}}
	if err := c.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "billing")
	want := "registry.example.net/kionga/team-a-billing@" + digest
	var deployment appsv1.Deployment
	get(t, c, "billing", &deployment)
	if deployment.Spec.Template.Spec.Containers[0].Image != want {
		t.Fatalf("deployment must run the built digest: %s", deployment.Spec.Template.Spec.Containers[0].Image)
	}
	get(t, c, "billing", &stored)
	if stored.Status.LastBuild.Phase != "Succeeded" || stored.Status.LastBuild.Image != want || condition(stored.Status.Conditions, ConditionBuild).Status != metav1.ConditionTrue {
		t.Fatalf("status after build: %#v", stored.Status)
	}

	// A redeploy builds again; a failed build keeps the running image.
	stored.Annotations = map[string]string{RedeployAnnotation: "2"}
	if err := c.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "billing")
	second := BuildJobName("billing", SourceHash(source, "2"))
	get(t, c, second, &job)
	job.Status = batchv1.JobStatus{Failed: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "BackoffLimitExceeded"}}}
	if err := c.Status().Update(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "billing")
	get(t, c, "billing", &deployment)
	get(t, c, "billing", &stored)
	if deployment.Spec.Template.Spec.Containers[0].Image != want || stored.Status.LastBuild.Phase != "Failed" || condition(stored.Status.Conditions, ConditionBuild).Reason != "BuildFailed" {
		t.Fatalf("failed rebuild: image=%s status=%#v", deployment.Spec.Template.Spec.Containers[0].Image, stored.Status)
	}
}

func TestSourceBuildNeedsRegistry(t *testing.T) {
	app := application("billing", kiongav1.WorkloadSpec{Source: &kiongav1.SourceSpec{Git: "https://github.com/acme/billing.git"}})
	c := testClient(testScheme(t), app)
	reconciler := applicationReconciler(c, false)
	reconciler.Config.Build.Registry = ""
	reconcileOnce(t, reconciler, "billing")
	var stored kiongav1.KiongaApplication
	get(t, c, "billing", &stored)
	if condition(stored.Status.Conditions, ConditionReady).Reason != "BuildNotConfigured" {
		t.Fatalf("status: %#v", stored.Status)
	}
}

func TestBuildContext(t *testing.T) {
	cases := map[string]kiongav1.SourceSpec{
		"https://g.example/a.git":          {Git: "https://g.example/a.git"},
		"https://g.example/a.git#v1":       {Git: "https://g.example/a.git", Ref: "v1"},
		"https://g.example/a.git#:svc/api": {Git: "https://g.example/a.git", ContextDir: "/svc/api/"},
		"https://g.example/a.git#main:svc": {Git: "https://g.example/a.git", Ref: "main", ContextDir: "svc"},
	}
	for want, source := range cases {
		if got := BuildContext(&source); got != want {
			t.Errorf("BuildContext(%+v) = %s, want %s", source, got, want)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

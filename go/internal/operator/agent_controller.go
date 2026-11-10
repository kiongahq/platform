package operator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	platformapi "github.com/kiongahq/platform/pkg/api"
	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

const agentFinalizer = "kionga.dev/agent-cleanup"

type AgentReconciler struct {
	client.Client
	TraceProxyImage      string
	LangflowSidecarImage string
	GatewayURL           string
	// ImagePullSecrets name registry credentials in the agent namespace for
	// private agent and trace-proxy images (KIONGA_IMAGE_PULL_SECRETS).
	ImagePullSecrets []corev1.LocalObjectReference
	// ProviderKeySecret names an optional Secret in the agent namespace whose
	// LLM_UPSTREAM_API_KEY key the trace-proxy sidecar sends to the model
	// provider (KIONGA_LLM_PROVIDER_SECRET, default kionga-llm-provider).
	// Self-hosted upstreams without a key leave it absent.
	ProviderKeySecret string
}

const (
	// traceProxyKeyFile is the sidecar's TRACE_PROXY_KEYS_FILE: a JSON array
	// of {key_id, key_sha256, project, agent} (kiongahq/trace-proxy#40).
	traceProxyKeyDir  = "/run/secrets/trace-proxy"
	traceProxyKeyFile = "keys.json"
	providerKeyName   = "LLM_UPSTREAM_API_KEY"
)

// TraceProxySecretName is the per-agent Secret holding the agent's trace
// proxy key (key) and the hash-only key list the sidecar loads (keys.json).
func TraceProxySecretName(workload string) string { return workload + "-trace-proxy" }

func (r *AgentReconciler) providerKeySecret() string {
	if r.ProviderKeySecret != "" {
		return r.ProviderKeySecret
	}
	return "kionga-llm-provider"
}

// traceProxyKeys renders the sidecar key list for one agent key. Only the
// SHA-256 hash is written; the raw key reaches the agent container alone.
func traceProxyKeys(keyID, project, agent string, key []byte) ([]byte, error) {
	sum := sha256.Sum256(key)
	return json.Marshal([]map[string]string{{"key_id": keyID, "key_sha256": hex.EncodeToString(sum[:]), "project": project, "agent": agent}})
}

// RuntimeTokenKey retains the runtime's existing environment contract.
const RuntimeTokenKey = "KIONGA_INTERNAL_TOKEN"

func runtimeAuthEnv(secretName string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "KIONGA_RUNTIME_AUTH", Value: "required"},
		{Name: RuntimeTokenKey, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: RuntimeTokenKey}}},
	}
}

func (r *AgentReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var agent kiongav1.KiongaAgent
	if err := r.Get(ctx, request.NamespacedName, &agent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !agent.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&agent, agentFinalizer) {
			controllerutil.RemoveFinalizer(&agent, agentFinalizer)
			return ctrl.Result{}, r.Update(ctx, &agent)
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(&agent, agentFinalizer) {
		controllerutil.AddFinalizer(&agent, agentFinalizer)
		if err := r.Update(ctx, &agent); err != nil {
			return ctrl.Result{}, err
		}
	}
	resourceID := agent.Labels["kionga.dev/resource-id"]
	if resourceID == "" {
		resourceID = agent.Name
	}
	plan, err := ReconcileAgent(AgentSpec{
		ResourceID: resourceID,
		Name:       agent.Name, Namespace: agent.Namespace, Version: agent.Spec.Version,
		Image: agent.Spec.Image, GraphModule: agent.Spec.GraphModule,
		MinReplicas: int(agent.Spec.Replicas.Min), MaxReplicas: int(agent.Spec.Replicas.Max),
		LLMBackend: agent.Spec.LLM.Backend, LangfuseProject: agent.Spec.LangfuseProject,
		CanaryWeight: int(agent.Spec.TrafficPolicy.CanaryWeight), StableRef: agent.Spec.TrafficPolicy.StableRef,
	})
	if err != nil {
		return r.fail(ctx, &agent, "InvalidSpec", err)
	}
	resources, err := resolvedAgentResources(agent.Spec.Resources)
	if err != nil {
		return r.fail(ctx, &agent, "InvalidResources", err)
	}
	// Backfill existing agents on reconcile. Preserve the credential until its
	// key is removed or replaced, just like workspace job tokens.
	runtimeSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: platformapi.AgentRuntimeSecretName(resourceID), Namespace: agent.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, runtimeSecret, func() error {
		if runtimeSecret.Data == nil {
			runtimeSecret.Data = map[string][]byte{}
		}
		if len(runtimeSecret.Data[RuntimeTokenKey]) == 0 {
			token := make([]byte, 32)
			if _, err := rand.Read(token); err != nil {
				return err
			}
			runtimeSecret.Data[RuntimeTokenKey] = []byte(base64.RawURLEncoding.EncodeToString(token))
		}
		return controllerutil.SetControllerReference(&agent, runtimeSecret, r.Scheme())
	}); err != nil {
		return ctrl.Result{}, err
	}
	runtimeTokenHash := fmt.Sprintf("%x", sha256.Sum256(runtimeSecret.Data[RuntimeTokenKey]))
	traceProxyImage := r.TraceProxyImage
	if traceProxyImage == "" {
		traceProxyImage = plan.Workload.Containers[1].Image
	}
	flowNameHash := fmt.Sprintf("%x", sha256.Sum256([]byte(plan.Workload.Name)))
	flowConfigName := "kionga-flow-" + flowNameHash[:24]
	agentTokenHash := ""
	if agent.Spec.FlowJSON != "" {
		// Published graphs (Langflow flows and native Agent Designer versions)
		// are mounted read-only and reach Kionga only through the API sidecar.
		if agent.Spec.GraphModule != "agents.langflow_adapter:build_langflow" && agent.Spec.GraphModule != "agents.native_graph:build_native" {
			return r.fail(ctx, &agent, "InvalidSpec", fmt.Errorf("flowJSON requires the Langflow or native graph adapter"))
		}
		if agent.Spec.ProjectID == "" || agent.Spec.OwnerSubject == "" || r.LangflowSidecarImage == "" || r.GatewayURL == "" {
			return r.fail(ctx, &agent, "InvalidSpec", fmt.Errorf("published graph agents require project, owner, API sidecar and gateway"))
		}
		flowConfig := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: flowConfigName, Namespace: agent.Namespace}}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, flowConfig, func() error {
			flowConfig.Data = map[string]string{"flow.json": agent.Spec.FlowJSON}
			return controllerutil.SetControllerReference(&agent, flowConfig, r.Scheme())
		}); err != nil {
			return ctrl.Result{}, err
		}
		apiSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: platformapi.AgentAPISecretName(agent.Name), Namespace: agent.Namespace}}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, apiSecret, func() error {
			if apiSecret.Annotations == nil {
				apiSecret.Annotations = map[string]string{}
			}
			previous := apiSecret.Annotations["kionga.dev/subject"]
			apiSecret.Annotations["kionga.dev/subject"] = agent.Spec.OwnerSubject
			if apiSecret.Data == nil {
				apiSecret.Data = map[string][]byte{}
			}
			if previous != agent.Spec.OwnerSubject || len(apiSecret.Data["api-token"]) == 0 {
				secret := make([]byte, 24)
				if _, err := rand.Read(secret); err != nil {
					return err
				}
				apiSecret.Data["api-token"] = []byte(base64.RawURLEncoding.EncodeToString(secret))
			}
			return controllerutil.SetControllerReference(&agent, apiSecret, r.Scheme())
		}); err != nil {
			return ctrl.Result{}, err
		}
		agentTokenHash = fmt.Sprintf("%x", sha256.Sum256(apiSecret.Data["api-token"]))
	}
	// Every agent pod authenticates to its trace-proxy sidecar with a key of
	// its own; the provider key stays in the sidecar.
	project := agent.Spec.ProjectID
	if project == "" {
		project = agent.Namespace
	}
	traceSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: TraceProxySecretName(plan.Workload.Name), Namespace: agent.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, traceSecret, func() error {
		if traceSecret.Data == nil {
			traceSecret.Data = map[string][]byte{}
		}
		if len(traceSecret.Data["key"]) == 0 {
			random := make([]byte, 32)
			if _, err := rand.Read(random); err != nil {
				return err
			}
			traceSecret.Data["key"] = []byte(base64.RawURLEncoding.EncodeToString(random))
		}
		keys, err := traceProxyKeys(plan.Workload.Name, project, agent.Name, traceSecret.Data["key"])
		if err != nil {
			return err
		}
		traceSecret.Data[traceProxyKeyFile] = keys
		return controllerutil.SetControllerReference(&agent, traceSecret, r.Scheme())
	}); err != nil {
		return ctrl.Result{}, err
	}
	traceKeyHash := fmt.Sprintf("%x", sha256.Sum256(traceSecret.Data["key"]))
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: plan.Workload.Name, Namespace: agent.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		replicas := int32(plan.Workload.Replicas) // #nosec G115 -- source is an int32 replica count
		deployment.Labels = plan.Workload.Labels
		// HorizontalPodAutoscaler owns spec.replicas after creation. Reapplying
		// the minimum on every owned Deployment event would fight every scale.
		if agent.Spec.Replicas.Max <= agent.Spec.Replicas.Min || deployment.ResourceVersion == "" {
			deployment.Spec.Replicas = &replicas
		}
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"kionga.dev/agent": agent.Name, "kionga.dev/version": agent.Spec.Version}}
		deployment.Spec.Template.ObjectMeta.Labels = deployment.Spec.Selector.MatchLabels
		if agent.Spec.FlowJSON != "" {
			deployment.Spec.Template.ObjectMeta.Annotations = map[string]string{"kionga.dev/flow-sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(agent.Spec.FlowJSON))), "kionga.dev/api-token-hash": agentTokenHash}
			deployment.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "flow", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: flowConfigName}}}}}
		}
		if deployment.Spec.Template.ObjectMeta.Annotations == nil || agent.Spec.FlowJSON == "" {
			deployment.Spec.Template.ObjectMeta.Annotations = map[string]string{}
		}
		deployment.Spec.Template.ObjectMeta.Annotations["kionga.dev/runtime-token-hash"] = runtimeTokenHash
		deployment.Spec.Template.ObjectMeta.Annotations["kionga.dev/trace-proxy-key-hash"] = traceKeyHash
		if agent.Spec.FlowJSON == "" {
			deployment.Spec.Template.Spec.Volumes = nil
		}
		deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, corev1.Volume{Name: "trace-proxy-keys", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: traceSecret.Name, Items: []corev1.KeyToPath{{Key: traceProxyKeyFile, Path: traceProxyKeyFile}},
		}}})
		deployment.Spec.Template.Spec.ImagePullSecrets = r.ImagePullSecrets
		deployment.Spec.Template.Spec.Containers = []corev1.Container{
			{Name: "agent", Image: agent.Spec.Image, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}, Env: append(append(envVars(plan.Workload.Containers[0].Env), corev1.EnvVar{Name: "KIONGA_AGENT_ID", Value: resourceID}, corev1.EnvVar{Name: "KIONGA_RUNTIME_PORT", Value: "8080"}), append(runtimeAuthEnv(runtimeSecret.Name), r.traceProxyClientEnv(traceSecret.Name))...), Resources: resources, ReadinessProbe: httpProbe("/healthz", "http", 2), LivenessProbe: httpProbe("/healthz", "http", 10), SecurityContext: hardenedSecurityContext()},
			{Name: "trace-proxy", Image: traceProxyImage, Ports: []corev1.ContainerPort{{Name: "proxy", ContainerPort: 8081}}, Env: append(envVars(plan.Workload.Containers[1].Env), r.traceProxySidecarEnv()...), VolumeMounts: []corev1.VolumeMount{{Name: "trace-proxy-keys", MountPath: traceProxyKeyDir, ReadOnly: true}}, Resources: traceProxyResources(), ReadinessProbe: httpProbe("/healthz", "proxy", 2), LivenessProbe: httpProbe("/healthz", "proxy", 10), SecurityContext: hardenedSecurityContext()},
		}
		if agent.Spec.FlowJSON != "" {
			deployment.Spec.Template.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "flow", MountPath: "/app/flows", ReadOnly: true}}
			deployment.Spec.Template.Spec.Containers[0].Env = append(deployment.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "KIONGA_URL", Value: "http://127.0.0.1:8890"})
			deployment.Spec.Template.Spec.Containers = append(deployment.Spec.Template.Spec.Containers, corev1.Container{
				Name: "kionga-api", Image: r.LangflowSidecarImage,
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(8890)}}, PeriodSeconds: 5},
				Env: []corev1.EnvVar{
					{Name: "KIONGA_GATEWAY_INTERNAL_URL", Value: strings.Replace(r.GatewayURL, ":8080", ":8087", 1)},
					{Name: "KIONGA_WORKSPACE_NAME", Value: agent.Name},
					{Name: "KIONGA_WORKSPACE_API_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: platformapi.AgentAPISecretName(agent.Name)}, Key: "api-token"}}},
				},
				Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}},
				SecurityContext: hardenedSecurityContext(),
			})
		}
		return controllerutil.SetControllerReference(&agent, deployment, r.Scheme())
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: plan.Workload.Name, Namespace: agent.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		service.Spec.Selector = deployment.Spec.Selector.MatchLabels
		service.Spec.Ports = []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstrFromInt(8080)}}
		return controllerutil.SetControllerReference(&agent, service, r.Scheme())
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: plan.Workload.Name, Namespace: agent.Namespace}}
	if agent.Spec.Replicas.Max > agent.Spec.Replicas.Min {
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, hpa, func() error {
			minimum, utilization := agent.Spec.Replicas.Min, int32(70)
			hpa.Spec = autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name},
				MinReplicas:    &minimum, MaxReplicas: agent.Spec.Replicas.Max,
				Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &utilization}}}},
			}
			return controllerutil.SetControllerReference(&agent, hpa, r.Scheme())
		})
		if err != nil {
			return ctrl.Result{}, err
		}
	} else if err := r.Get(ctx, client.ObjectKey{Name: plan.Workload.Name, Namespace: agent.Namespace}, hpa); err == nil {
		if !metav1.IsControlledBy(hpa, &agent) {
			return r.fail(ctx, &agent, "ResourceCollision", fmt.Errorf("HorizontalPodAutoscaler %s is not owned by agent", hpa.Name))
		}
		if err := r.Delete(ctx, hpa); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	ready := deployment.Status.AvailableReplicas >= agent.Spec.Replicas.Min
	agent.Status.Phase = "Progressing"
	agent.Status.WorkloadRef = plan.Workload.Name
	agent.Status.ReadyReplicas = deployment.Status.ReadyReplicas
	agent.Status.ObservedGeneration = agent.Generation
	condition := metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "WorkloadProgressing", Message: fmt.Sprintf("Deployment %s has %d/%d available replicas", deployment.Name, deployment.Status.AvailableReplicas, agent.Spec.Replicas.Min)}
	if ready {
		agent.Status.Phase = "Ready"
		condition = metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "WorkloadAvailable", Message: fmt.Sprintf("Deployment %s has %d available replicas", deployment.Name, deployment.Status.AvailableReplicas)}
	}
	apimeta.SetStatusCondition(&agent.Status.Conditions, condition)
	if err := r.Status().Update(ctx, &agent); err != nil {
		// Returning conflicts lets controller-runtime retry with the latest
		// object instead of silently dropping a readiness transition.
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func (r *AgentReconciler) fail(ctx context.Context, agent *kiongav1.KiongaAgent, reason string, reconcileErr error) (ctrl.Result, error) {
	agent.Status.Phase = "Failed"
	apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: reconcileErr.Error()})
	_ = r.Status().Update(ctx, agent)
	return ctrl.Result{}, reconcileErr
}

func (r *AgentReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&kiongav1.KiongaAgent{}).Owns(&appsv1.Deployment{}).Owns(&corev1.Service{}).Owns(&corev1.ConfigMap{}).Owns(&corev1.Secret{}).Owns(&autoscalingv2.HorizontalPodAutoscaler{}).Complete(r)
}

func resolvedAgentResources(value corev1.ResourceRequirements) (corev1.ResourceRequirements, error) {
	result := *value.DeepCopy()
	if result.Requests == nil {
		result.Requests = corev1.ResourceList{}
	}
	if result.Limits == nil {
		result.Limits = corev1.ResourceList{}
	}
	defaults := map[corev1.ResourceName]string{corev1.ResourceCPU: "500m", corev1.ResourceMemory: "1Gi"}
	for name, raw := range defaults {
		if _, exists := result.Requests[name]; !exists {
			quantity, err := resource.ParseQuantity(raw)
			if err != nil {
				return result, err
			}
			result.Requests[name] = quantity
		}
		if _, exists := result.Limits[name]; !exists {
			result.Limits[name] = result.Requests[name]
		}
	}
	for name, quantity := range result.Requests {
		if _, exists := result.Limits[name]; !exists {
			result.Limits[name] = quantity
		}
	}
	for name, quantity := range result.Limits {
		if _, exists := result.Requests[name]; !exists {
			result.Requests[name] = quantity
		}
	}
	return result, nil
}

func traceProxyResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
	}
}

func httpProbe(path, port string, initialDelay int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromString(port)}},
		InitialDelaySeconds: initialDelay, PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 3,
	}
}

func envVars(values map[string]string) []corev1.EnvVar {
	result := make([]corev1.EnvVar, 0, len(values))
	for name, value := range values {
		result = append(result, corev1.EnvVar{Name: name, Value: value})
	}
	return result
}

func hardenedSecurityContext() *corev1.SecurityContext {
	allow := false
	readOnly := true
	nonRoot := true
	uid := int64(65532)
	return &corev1.SecurityContext{AllowPrivilegeEscalation: &allow, ReadOnlyRootFilesystem: &readOnly, RunAsNonRoot: &nonRoot, RunAsUser: &uid, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
}

// traceProxyClientEnv gives the agent its trace-proxy key as the OpenAI
// client API key; OPENAI_BASE_URL already points at the sidecar.
func (r *AgentReconciler) traceProxyClientEnv(secret string) corev1.EnvVar {
	return corev1.EnvVar{Name: "OPENAI_API_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: "key"}}}
}

// traceProxySidecarEnv points the sidecar at the hash-only key list and
// the provider key Secret (optional, for keyless self-hosted upstreams).
func (r *AgentReconciler) traceProxySidecarEnv() []corev1.EnvVar {
	optional := true
	return []corev1.EnvVar{
		{Name: "TRACE_PROXY_KEYS_FILE", Value: traceProxyKeyDir + "/" + traceProxyKeyFile},
		{Name: providerKeyName, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: r.providerKeySecret()}, Key: providerKeyName, Optional: &optional}}},
	}
}

func intstrFromInt(value int) intstr.IntOrString { return intstr.FromInt(value) }

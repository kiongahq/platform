package operator

import (
	"context"
	"fmt"
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

	mlaiopsv1 "github.com/ml-ai-ops/platform/pkg/kube/v1alpha1"
)

const agentFinalizer = "mlaiops.io/agent-cleanup"

type AgentReconciler struct {
	client.Client
	TraceProxyImage string
}

func (r *AgentReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var agent mlaiopsv1.KiongaAgent
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
	resourceID := agent.Labels["mlaiops.io/resource-id"]
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
	traceProxyImage := r.TraceProxyImage
	if traceProxyImage == "" {
		traceProxyImage = plan.Workload.Containers[1].Image
	}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: plan.Workload.Name, Namespace: agent.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		replicas := int32(plan.Workload.Replicas)
		deployment.Labels = plan.Workload.Labels
		// HorizontalPodAutoscaler owns spec.replicas after creation. Reapplying
		// the minimum on every owned Deployment event would fight every scale.
		if agent.Spec.Replicas.Max <= agent.Spec.Replicas.Min || deployment.ResourceVersion == "" {
			deployment.Spec.Replicas = &replicas
		}
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"mlaiops.io/agent": agent.Name, "mlaiops.io/version": agent.Spec.Version}}
		deployment.Spec.Template.ObjectMeta.Labels = deployment.Spec.Selector.MatchLabels
		deployment.Spec.Template.Spec.Containers = []corev1.Container{
			{Name: "agent", Image: agent.Spec.Image, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}, Env: append(envVars(plan.Workload.Containers[0].Env), corev1.EnvVar{Name: "MLAIOPS_AGENT_ID", Value: resourceID}, corev1.EnvVar{Name: "MLAIOPS_RUNTIME_PORT", Value: "8080"}), Resources: resources, ReadinessProbe: httpProbe("/healthz", "http", 2), LivenessProbe: httpProbe("/healthz", "http", 10), SecurityContext: hardenedSecurityContext()},
			{Name: "trace-proxy", Image: traceProxyImage, Ports: []corev1.ContainerPort{{Name: "proxy", ContainerPort: 8081}}, Env: envVars(plan.Workload.Containers[1].Env), Resources: traceProxyResources(), ReadinessProbe: httpProbe("/healthz", "proxy", 2), LivenessProbe: httpProbe("/healthz", "proxy", 10), SecurityContext: hardenedSecurityContext()},
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

func (r *AgentReconciler) fail(ctx context.Context, agent *mlaiopsv1.KiongaAgent, reason string, reconcileErr error) (ctrl.Result, error) {
	agent.Status.Phase = "Failed"
	apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: reconcileErr.Error()})
	_ = r.Status().Update(ctx, agent)
	return ctrl.Result{}, reconcileErr
}

func (r *AgentReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&mlaiopsv1.KiongaAgent{}).Owns(&appsv1.Deployment{}).Owns(&corev1.Service{}).Owns(&autoscalingv2.HorizontalPodAutoscaler{}).Complete(r)
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
	return &corev1.SecurityContext{AllowPrivilegeEscalation: &allow, ReadOnlyRootFilesystem: &readOnly, RunAsNonRoot: &nonRoot}
}

func intstrFromInt(value int) intstr.IntOrString { return intstr.FromInt(value) }

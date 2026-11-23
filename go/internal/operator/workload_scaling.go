package operator

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

const (
	defaultRequestsTarget = int32(100)
	defaultCPUTarget      = int32(70)

	// ConditionScaling reports whether the requested scaling is in effect.
	ConditionScaling = "ScalingReady"
)

// ScalingPlan is the autoscaling the operator applies to one workload.
type ScalingPlan struct {
	// Metric is requests or cpu, after fallback.
	Metric string
	Min    int32
	Max    int32
	Target int32
	// Condition is the ScalingReady condition to record.
	Condition metav1.Condition
}

// Autoscaled reports whether an autoscaler, not the operator, owns the
// Deployment replica count.
func (p ScalingPlan) Autoscaled() bool {
	return p.Metric == kiongav1.ScalingMetricRequests || p.Max > p.Min
}

// InitialReplicas is the replica count a new Deployment starts with. An
// autoscaled workload starts with at least one replica so the first rollout
// proves the image runs; KEDA scales it to zero afterwards.
func (p ScalingPlan) InitialReplicas() int32 {
	if p.Autoscaled() && p.Min < 1 {
		return 1
	}
	return p.Min
}

// PlanScaling picks the scaling for a workload. requested is spec.scaling;
// defaultMetric applies when the spec names none ("" means requests when the
// KEDA HTTP add-on is installed, cpu otherwise). Requests scaling without
// the add-on falls back to CPU with at least one replica and a False
// ScalingReady condition, so the workload still runs.
func PlanScaling(requested kiongav1.ScalingSpec, replicas kiongav1.WorkloadReplicaSpec, defaultMetric string, keda bool) ScalingPlan {
	plan := ScalingPlan{Metric: requested.Metric, Min: replicas.Min, Max: replicas.Max, Target: requested.Target}
	if plan.Metric == "" {
		plan.Metric = defaultMetric
	}
	if plan.Metric == "" {
		plan.Metric = kiongav1.ScalingMetricCPU
		if keda {
			plan.Metric = kiongav1.ScalingMetricRequests
		}
	}
	plan.Condition = metav1.Condition{Type: ConditionScaling, Status: metav1.ConditionTrue}
	if plan.Metric == kiongav1.ScalingMetricRequests && !keda {
		plan.Metric = kiongav1.ScalingMetricCPU
		plan.Condition.Status = metav1.ConditionFalse
		plan.Condition.Reason = "KEDAHTTPAddonMissing"
		plan.Condition.Message = "requests scaling needs the KEDA HTTP add-on, which this cluster does not serve; using CPU scaling with at least one replica"
	}
	if plan.Metric == kiongav1.ScalingMetricCPU && plan.Min < 1 {
		// A HorizontalPodAutoscaler cannot scale to zero.
		plan.Min = 1
	}
	if plan.Max < plan.Min {
		plan.Max = plan.Min
	}
	if plan.Target <= 0 {
		plan.Target = defaultCPUTarget
		if plan.Metric == kiongav1.ScalingMetricRequests {
			plan.Target = defaultRequestsTarget
		}
	}
	if plan.Condition.Status == metav1.ConditionTrue {
		switch {
		case plan.Metric == kiongav1.ScalingMetricRequests:
			plan.Condition.Reason = "RequestsScaling"
			plan.Condition.Message = fmt.Sprintf("KEDA HTTP scaling between %d and %d replicas at %d concurrent requests per replica", plan.Min, plan.Max, plan.Target)
		case plan.Max > plan.Min:
			plan.Condition.Reason = "CPUScaling"
			plan.Condition.Message = fmt.Sprintf("CPU scaling between %d and %d replicas at %d%% utilization", plan.Min, plan.Max, plan.Target)
		default:
			plan.Condition.Reason = "FixedReplicas"
			plan.Condition.Message = fmt.Sprintf("%d replicas", plan.Min)
		}
	}
	return plan
}

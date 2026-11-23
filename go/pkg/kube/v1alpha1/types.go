package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "kionga.dev", Version: "v1alpha1"}

// ReplicaSpec bounds an agent's replica count. Agents always keep at least one
// replica; applications and functions use WorkloadReplicaSpec, which allows 0.
//
// +kubebuilder:validation:XValidation:rule="self.max >= self.min",message="max replicas must be greater than or equal to min replicas"
type ReplicaSpec struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Min int32 `json:"min"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Max int32 `json:"max"`
}

type LLMSpec struct {
	// +optional
	Backend string `json:"backend,omitempty"`
	// +optional
	InferenceServiceRef string `json:"inferenceServiceRef,omitempty"`
}

type ToolReference struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type TrafficPolicy struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	CanaryWeight int32 `json:"canaryWeight,omitempty"`
	// +optional
	StableRef string `json:"stableRef,omitempty"`
}

const (
	// ScalingMetricRequests scales on in-flight HTTP requests through the KEDA
	// HTTP add-on, which also allows scale to zero.
	ScalingMetricRequests = "requests"
	// ScalingMetricCPU scales on average CPU utilization with a
	// HorizontalPodAutoscaler.
	ScalingMetricCPU = "cpu"
)

// ScalingSpec selects the autoscaling signal. An empty metric lets the
// operator choose: requests when the KEDA HTTP add-on is installed, cpu
// otherwise.
type ScalingSpec struct {
	// +kubebuilder:validation:Enum=requests;cpu
	// +optional
	Metric string `json:"metric,omitempty"`
	// Target is concurrent requests per replica for requests, or average CPU
	// utilization percent for cpu. 0 means the operator default (100 requests,
	// 70 percent).
	// +kubebuilder:validation:Minimum=0
	// +optional
	Target int32 `json:"target,omitempty"`
}

type KiongaAgentSpec struct {
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
	// +kubebuilder:validation:MinLength=1
	GraphModule string `json:"graphModule"`
	// +kubebuilder:validation:MaxLength=524288
	// +optional
	FlowJSON string `json:"flowJSON,omitempty"`
	// +optional
	ProjectID string `json:"projectID,omitempty"`
	// +optional
	OwnerSubject string      `json:"ownerSubject,omitempty"`
	Replicas     ReplicaSpec `json:"replicas"`
	// +optional
	Scaling ScalingSpec `json:"scaling,omitempty"`
	// +optional
	LLM LLMSpec `json:"llm,omitempty"`
	// +optional
	Tools []ToolReference `json:"tools,omitempty"`
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	LangfuseProject string `json:"langfuseProject,omitempty"`
	// +optional
	TrafficPolicy TrafficPolicy `json:"trafficPolicy,omitempty"`
}

type KiongaAgentStatus struct {
	Phase       string `json:"phase,omitempty"`
	WorkloadRef string `json:"workloadRef,omitempty"`
	// URL is the in-cluster address callers use to invoke the agent. With
	// requests scaling it goes through the KEDA HTTP interceptor, so request
	// traffic drives the replica count.
	URL                string             `json:"url,omitempty"`
	ReadyReplicas      int32              `json:"readyReplicas,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=kagent
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
type KiongaAgent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KiongaAgentSpec   `json:"spec"`
	Status            KiongaAgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KiongaAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KiongaAgent `json:"items"`
}

func AddToScheme(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&KiongaAgent{}, &KiongaAgentList{},
		&KiongaModelPromotion{}, &KiongaModelPromotionList{},
		&KiongaWorkspace{}, &KiongaWorkspaceList{},
		&KiongaApplication{}, &KiongaApplicationList{},
		&KiongaFunction{}, &KiongaFunctionList{},
		&KiongaModelDeployment{}, &KiongaModelDeploymentList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

type WorkspaceComputeSpec struct {
	// +kubebuilder:validation:Minimum=1
	VCPUs int32 `json:"vcpus"`
	// +kubebuilder:validation:Minimum=1
	MemoryGB int32 `json:"memoryGB"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	// +optional
	GPUs int32 `json:"gpus,omitempty"`
	// +optional
	GPUType string `json:"gpuType,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	// +optional
	MaxVMs int32 `json:"maxVMs,omitempty"`
}

type KiongaWorkspaceSpec struct {
	// +kubebuilder:validation:MinLength=1
	Subject string `json:"subject"`
	// +optional
	ProjectID string `json:"projectID,omitempty"`
	// +optional
	Services []string             `json:"services,omitempty"`
	Compute  WorkspaceComputeSpec `json:"compute"`
	// +kubebuilder:validation:Minimum=1
	StorageGB int32 `json:"storageGB"`
	// +kubebuilder:default=false
	// +optional
	Disabled bool `json:"disabled,omitempty"`
}

type KiongaWorkspaceStatus struct {
	Phase              string `json:"phase,omitempty"`
	ReadyReplicas      int32  `json:"readyReplicas,omitempty"`
	WorkbenchURL       string `json:"workbenchURL,omitempty"`
	IDEURL             string `json:"ideURL,omitempty"`
	LangflowURL        string `json:"langflowURL,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=kws
type KiongaWorkspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KiongaWorkspaceSpec   `json:"spec"`
	Status            KiongaWorkspaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KiongaWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KiongaWorkspace `json:"items"`
}

type PromotionGate struct {
	Metric string `json:"metric"`
	// +kubebuilder:validation:Enum=gte;lte
	Operator string  `json:"operator"`
	Value    float64 `json:"value"`
}

// KiongaModelPromotionSpec moves a registered model version to an MLflow
// stage. Serving lives in KiongaModelDeployment.
type KiongaModelPromotionSpec struct {
	ModelName string `json:"modelName"`
	Version   string `json:"version"`
	// +kubebuilder:validation:Enum=staging;production;archived
	TargetStage string `json:"targetStage"`
	// +optional
	Gates []PromotionGate `json:"gates,omitempty"`
}

type KiongaModelPromotionStatus struct {
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=kmp
type KiongaModelPromotion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KiongaModelPromotionSpec   `json:"spec"`
	Status            KiongaModelPromotionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KiongaModelPromotionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KiongaModelPromotion `json:"items"`
}

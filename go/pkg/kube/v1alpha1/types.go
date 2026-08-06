package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "mlaiops.io", Version: "v1alpha1"}

type ReplicaSpec struct {
	Min int32 `json:"min,omitempty"`
	Max int32 `json:"max,omitempty"`
}

type LLMSpec struct {
	Backend             string `json:"backend,omitempty"`
	InferenceServiceRef string `json:"inferenceServiceRef,omitempty"`
}

type ToolReference struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type TrafficPolicy struct {
	CanaryWeight int32  `json:"canaryWeight,omitempty"`
	StableRef    string `json:"stableRef,omitempty"`
}

type KiongaAgentSpec struct {
	Version         string                      `json:"version"`
	Image           string                      `json:"image"`
	GraphModule     string                      `json:"graphModule"`
	Replicas        ReplicaSpec                 `json:"replicas,omitempty"`
	LLM             LLMSpec                     `json:"llm,omitempty"`
	Tools           []ToolReference             `json:"tools,omitempty"`
	Resources       corev1.ResourceRequirements `json:"resources,omitempty"`
	LangfuseProject string                      `json:"langfuseProject,omitempty"`
	TrafficPolicy   TrafficPolicy               `json:"trafficPolicy,omitempty"`
}

type KiongaAgentStatus struct {
	Phase              string             `json:"phase,omitempty"`
	WorkloadRef        string             `json:"workloadRef,omitempty"`
	ReadyReplicas      int32              `json:"readyReplicas,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

type KiongaAgent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KiongaAgentSpec   `json:"spec,omitempty"`
	Status            KiongaAgentStatus `json:"status,omitempty"`
}

type KiongaAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KiongaAgent `json:"items"`
}

func AddToScheme(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &KiongaAgent{}, &KiongaAgentList{}, &KiongaPipelineRun{}, &KiongaPipelineRunList{}, &KiongaModelPromotion{}, &KiongaModelPromotionList{}, &KiongaWorkspace{}, &KiongaWorkspaceList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

type WorkspaceComputeSpec struct {
	VCPUs    int32  `json:"vcpus"`
	MemoryGB int32  `json:"memoryGB"`
	GPUs     int32  `json:"gpus,omitempty"`
	GPUType  string `json:"gpuType,omitempty"`
	MaxVMs   int32  `json:"maxVMs,omitempty"`
}

type KiongaWorkspaceSpec struct {
	Subject   string               `json:"subject"`
	Services  []string             `json:"services,omitempty"`
	Compute   WorkspaceComputeSpec `json:"compute"`
	StorageGB int32                `json:"storageGB"`
	Disabled  bool                 `json:"disabled,omitempty"`
}

type KiongaWorkspaceStatus struct {
	Phase              string             `json:"phase,omitempty"`
	ReadyReplicas      int32              `json:"readyReplicas,omitempty"`
	WorkbenchURL       string             `json:"workbenchURL,omitempty"`
	IDEURL             string             `json:"ideURL,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

type KiongaWorkspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KiongaWorkspaceSpec   `json:"spec,omitempty"`
	Status            KiongaWorkspaceStatus `json:"status,omitempty"`
}

type KiongaWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KiongaWorkspace `json:"items"`
}

type KiongaPipelineRunSpec struct {
	PipelineRef        string            `json:"pipelineRef"`
	Parameters         map[string]string `json:"parameters,omitempty"`
	ServiceAccountName string            `json:"serviceAccountName,omitempty"`
}

type KiongaPipelineRunStatus struct {
	Phase       string       `json:"phase,omitempty"`
	WorkflowRef string       `json:"workflowRef,omitempty"`
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	FinishedAt  *metav1.Time `json:"finishedAt,omitempty"`
	Message     string       `json:"message,omitempty"`
}

type KiongaPipelineRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KiongaPipelineRunSpec   `json:"spec,omitempty"`
	Status            KiongaPipelineRunStatus `json:"status,omitempty"`
}

type KiongaPipelineRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KiongaPipelineRun `json:"items"`
}

type PromotionGate struct {
	Metric   string  `json:"metric"`
	Operator string  `json:"operator"`
	Value    float64 `json:"value"`
}

type KiongaModelPromotionSpec struct {
	ModelName   string          `json:"modelName"`
	Version     string          `json:"version"`
	TargetStage string          `json:"targetStage"`
	Gates       []PromotionGate `json:"gates,omitempty"`
}

type KiongaModelPromotionStatus struct {
	Phase               string `json:"phase,omitempty"`
	Message             string `json:"message,omitempty"`
	InferenceServiceRef string `json:"inferenceServiceRef,omitempty"`
}

type KiongaModelPromotion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KiongaModelPromotionSpec   `json:"spec,omitempty"`
	Status            KiongaModelPromotionStatus `json:"status,omitempty"`
}

type KiongaModelPromotionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KiongaModelPromotion `json:"items"`
}

func (in *KiongaAgent) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(KiongaAgent)
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec.Tools = append([]ToolReference(nil), in.Spec.Tools...)
	out.Spec.Resources = *in.Spec.Resources.DeepCopy()
	out.Status.Conditions = append([]metav1.Condition(nil), in.Status.Conditions...)
	return out
}

func (in *KiongaAgentList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(KiongaAgentList)
	*out = *in
	out.Items = make([]KiongaAgent, len(in.Items))
	for i := range in.Items {
		out.Items[i] = *(in.Items[i].DeepCopyObject().(*KiongaAgent))
	}
	return out
}

func (in *KiongaPipelineRun) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(KiongaPipelineRun)
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec.Parameters = make(map[string]string, len(in.Spec.Parameters))
	for key, value := range in.Spec.Parameters {
		out.Spec.Parameters[key] = value
	}
	if in.Status.StartedAt != nil {
		value := in.Status.StartedAt.DeepCopy()
		out.Status.StartedAt = value
	}
	if in.Status.FinishedAt != nil {
		value := in.Status.FinishedAt.DeepCopy()
		out.Status.FinishedAt = value
	}
	return out
}
func (in *KiongaPipelineRunList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(KiongaPipelineRunList)
	*out = *in
	out.Items = make([]KiongaPipelineRun, len(in.Items))
	for i := range in.Items {
		out.Items[i] = *(in.Items[i].DeepCopyObject().(*KiongaPipelineRun))
	}
	return out
}
func (in *KiongaModelPromotion) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(KiongaModelPromotion)
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec.Gates = append([]PromotionGate(nil), in.Spec.Gates...)
	return out
}
func (in *KiongaModelPromotionList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(KiongaModelPromotionList)
	*out = *in
	out.Items = make([]KiongaModelPromotion, len(in.Items))
	for i := range in.Items {
		out.Items[i] = *(in.Items[i].DeepCopyObject().(*KiongaModelPromotion))
	}
	return out
}

func (in *KiongaWorkspace) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(KiongaWorkspace)
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec.Services = append([]string(nil), in.Spec.Services...)
	out.Status.Conditions = append([]metav1.Condition(nil), in.Status.Conditions...)
	return out
}

func (in *KiongaWorkspaceList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(KiongaWorkspaceList)
	*out = *in
	out.Items = make([]KiongaWorkspace, len(in.Items))
	for i := range in.Items {
		out.Items[i] = *(in.Items[i].DeepCopyObject().(*KiongaWorkspace))
	}
	return out
}

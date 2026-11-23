package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KiongaModelDeploymentSpec serves an MLflow registered model version with
// KServe in RawDeployment mode.
type KiongaModelDeploymentSpec struct {
	// +kubebuilder:validation:MinLength=1
	ModelName string `json:"modelName"`
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
	// ModelFormat is the KServe model format. Default mlflow.
	// +optional
	ModelFormat string `json:"modelFormat,omitempty"`
	// Runtime pins a KServe ServingRuntime. Empty lets KServe choose.
	// +optional
	Runtime string `json:"runtime,omitempty"`
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	Replicas *WorkloadReplicaSpec `json:"replicas,omitempty"`
	// CanaryVersion serves a second registered version next to the stable
	// one. Empty means no canary.
	// +optional
	CanaryVersion string `json:"canaryVersion,omitempty"`
	// CanaryWeight is the percent of requests the gateway sends to the canary.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	CanaryWeight int32 `json:"canaryWeight,omitempty"`
}

type KiongaModelDeploymentStatus struct {
	// Phase is Pending, Progressing, Ready or Failed.
	Phase string `json:"phase,omitempty"`
	// Message repeats the Ready condition message.
	Message string `json:"message,omitempty"`
	// URL is the stable InferenceService address.
	URL string `json:"url,omitempty"`
	// StorageURI is the s3:// location KServe loads the stable version from.
	StorageURI          string `json:"storageURI,omitempty"`
	InferenceServiceRef string `json:"inferenceServiceRef,omitempty"`
	// CanaryURL, CanaryStorageURI and CanaryInferenceServiceRef describe the
	// canary version. KServe raw mode has no traffic split, so the gateway
	// sends canaryWeight percent of requests to canaryURL.
	CanaryURL                 string `json:"canaryURL,omitempty"`
	CanaryStorageURI          string `json:"canaryStorageURI,omitempty"`
	CanaryInferenceServiceRef string `json:"canaryInferenceServiceRef,omitempty"`
	CanaryWeight              int32  `json:"canaryWeight,omitempty"`
	ObservedGeneration        int64  `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=kmd
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.modelName`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Canary",type=integer,JSONPath=`.status.canaryWeight`
type KiongaModelDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KiongaModelDeploymentSpec   `json:"spec"`
	Status            KiongaModelDeploymentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KiongaModelDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KiongaModelDeployment `json:"items"`
}

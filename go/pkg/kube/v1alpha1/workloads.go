package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// VisibilityPublic serves the workload at <name>.<baseDomain> to anyone.
	VisibilityPublic = "public"
	// VisibilitySSO serves the workload at <name>.<baseDomain> only after the
	// Kionga gateway has signed the caller in.
	VisibilitySSO = "sso"
	// VisibilityInternal gives the workload no Ingress; only in-cluster
	// callers such as the gateway reach it.
	VisibilityInternal = "internal"
)

// WorkloadReplicaSpec bounds an application or function replica count. min 0
// allows scale to zero, which needs requests scaling.
//
// +kubebuilder:validation:XValidation:rule="self.max >= self.min",message="max replicas must be greater than or equal to min replicas"
type WorkloadReplicaSpec struct {
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Min int32 `json:"min"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Max int32 `json:"max"`
}

// SourceSpec builds the workload image from a Git repository with BuildKit.
type SourceSpec struct {
	// Git is a public HTTPS repository URL, for example
	// https://github.com/acme/billing.git. Builds carry no Git credentials, so
	// SSH URLs and credentials embedded in the URL are rejected.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^https://[^\s#@]+$`
	Git string `json:"git"`
	// Ref is a branch, tag or commit. Empty means the default branch.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._/-]*$`
	// +optional
	Ref string `json:"ref,omitempty"`
	// ContextDir is the build context inside the repository.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._/-]*$`
	// +optional
	ContextDir string `json:"contextDir,omitempty"`
	// Dockerfile is the path of the Dockerfile relative to contextDir.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._/-]*$`
	// +optional
	Dockerfile string `json:"dockerfile,omitempty"`
}

// EnvVar is a literal environment variable. Secret values come from
// secretRefs, never from arbitrary valueFrom references.
type EnvVar struct {
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	Name string `json:"name"`
	// +optional
	Value string `json:"value,omitempty"`
}

// SecretReference names a Secret in the workload namespace whose keys become
// environment variables. The Secret must carry the label
// kionga.dev/workload-secret=true.
type SecretReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// WorkloadSpec is the shared shape of applications and functions.
//
// +kubebuilder:validation:XValidation:rule="has(self.image) != has(self.source)",message="set exactly one of image or source"
type WorkloadSpec struct {
	// Image runs a prebuilt image. Exactly one of image and source is set.
	// +optional
	Image string `json:"image,omitempty"`
	// Source builds the image from Git.
	// +optional
	Source *SourceSpec `json:"source,omitempty"`
	// Port is the container port that serves HTTP. Default 8080.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`
	// +optional
	Env []EnvVar `json:"env,omitempty"`
	// +optional
	SecretRefs []SecretReference `json:"secretRefs,omitempty"`
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	Replicas *WorkloadReplicaSpec `json:"replicas,omitempty"`
	// +optional
	Scaling ScalingSpec `json:"scaling,omitempty"`
	// HealthPath is the HTTP path for startup, readiness and liveness probes.
	// Empty probes the port over TCP.
	// +kubebuilder:validation:Pattern=`^(/[^\s]*)?$`
	// +optional
	HealthPath string `json:"healthPath,omitempty"`
	// Visibility is public, sso or internal. Applications default to sso,
	// functions to internal.
	// +kubebuilder:validation:Enum=public;sso;internal
	// +optional
	Visibility string `json:"visibility,omitempty"`
	// RestartedAt is set (RFC 3339) by a redeploy. A new value rolls the pods
	// and, for a source workload, builds the source again.
	// +optional
	RestartedAt string `json:"restartedAt,omitempty"`
}

// BuildStatus records the most recent source build.
type BuildStatus struct {
	// Name is the build Job.
	Name string `json:"name,omitempty"`
	// SourceHash identifies the source and redeploy request the build ran for.
	SourceHash string `json:"sourceHash,omitempty"`
	// Phase is Running, Succeeded or Failed.
	Phase string `json:"phase,omitempty"`
	// Image is the pushed image by digest, set once the build succeeds.
	Image       string       `json:"image,omitempty"`
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	Message     string       `json:"message,omitempty"`
}

// WorkloadStatus is the shared status of applications and functions.
type WorkloadStatus struct {
	// Phase is Pending, Building, Progressing, Ready, Degraded or Failed.
	Phase string `json:"phase,omitempty"`
	// URL is the public address, https://<name>.<baseDomain>, when the
	// workload has an Ingress.
	URL string `json:"url,omitempty"`
	// InternalURL is the in-cluster address the gateway calls. With requests
	// scaling it goes through the KEDA HTTP interceptor.
	InternalURL string `json:"internalURL,omitempty"`
	// Host is the Host header that routes a call through the KEDA HTTP
	// interceptor to this workload. Empty when the workload scales on CPU.
	Host string `json:"host,omitempty"`
	// Message repeats the Ready condition message.
	Message string `json:"message,omitempty"`
	// Image is the image the Deployment runs.
	Image              string       `json:"image,omitempty"`
	ReadyReplicas      int32        `json:"readyReplicas,omitempty"`
	ScalingMetric      string       `json:"scalingMetric,omitempty"`
	LastBuild          *BuildStatus `json:"lastBuild,omitempty"`
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// KiongaApplication is a user HTTP service served at <name>.<baseDomain>.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=kapp
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
type KiongaApplication struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WorkloadSpec   `json:"spec"`
	Status            WorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KiongaApplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KiongaApplication `json:"items"`
}

// KiongaFunction is an HTTP function. It scales to zero on requests by
// default and has no public host unless visibility says so.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=kfn
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Internal URL",type=string,JSONPath=`.status.internalURL`
type KiongaFunction struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WorkloadSpec   `json:"spec"`
	Status            WorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KiongaFunctionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KiongaFunction `json:"items"`
}

package operator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

const (
	// WorkloadSecretLabel must be "true" on a Secret before an application or
	// function may load it through secretRefs. It keeps platform Secrets in
	// the same namespace (agent runtime tokens, provider keys) out of reach.
	WorkloadSecretLabel = "kionga.dev/workload-secret"
	// RestartedAtAnnotation on the pod template carries spec.restartedAt, so
	// a redeploy rolls the pods even when nothing else changed.
	RestartedAtAnnotation = "kionga.dev/restarted-at"
	// VisibilityAnnotation is set on Ingresses that the gateway must guard.
	VisibilityAnnotation = "kionga.dev/visibility"
	// UpstreamAnnotation tells the gateway where to proxy an sso host.
	UpstreamAnnotation = "kionga.dev/upstream"

	// MaxWorkloadNameLength leaves room for the -build-<hash>, -route and
	// -sso suffixes inside a 63 character DNS label.
	MaxWorkloadNameLength = 45

	defaultWorkloadPort = int32(8080)

	ConditionReady   = "Ready"
	ConditionIngress = "IngressReady"
	ConditionBuild   = "BuildSucceeded"
)

// WorkloadKind describes how one custom resource kind maps onto the shared
// workload reconciliation.
type WorkloadKind struct {
	// Component is the app.kubernetes.io/component label value.
	Component string
	// PodLabel is set to the resource name on every pod, for logs and
	// events: kionga.dev/application or kionga.dev/function.
	PodLabel string
	// DefaultVisibility applies when spec.visibility is empty.
	DefaultVisibility string
	// DefaultReplicas applies when spec.replicas is absent.
	DefaultReplicas kiongav1.WorkloadReplicaSpec
	// DefaultMetric applies when spec.scaling.metric is empty; "" lets the
	// cluster decide.
	DefaultMetric   string
	DefaultCPU      string
	DefaultMemory   string
	NewObject       func() client.Object
	NewList         func() client.ObjectList
	Items           func(client.ObjectList) []client.Object
	Spec            func(client.Object) *kiongav1.WorkloadSpec
	Status          func(client.Object) *kiongav1.WorkloadStatus
	ContainerName   string
	ScaledToZeroMsg string
}

// ApplicationKind reconciles KiongaApplication: sso by default, served at
// <name>.<baseDomain>, one replica unless the spec says otherwise.
var ApplicationKind = WorkloadKind{
	Component: "application", PodLabel: "kionga.dev/application", DefaultVisibility: kiongav1.VisibilitySSO,
	DefaultReplicas: kiongav1.WorkloadReplicaSpec{Min: 1, Max: 1},
	DefaultCPU:      "250m", DefaultMemory: "512Mi", ContainerName: "app",
	NewObject: func() client.Object { return &kiongav1.KiongaApplication{} },
	NewList:   func() client.ObjectList { return &kiongav1.KiongaApplicationList{} },
	Items: func(list client.ObjectList) []client.Object {
		items := list.(*kiongav1.KiongaApplicationList).Items
		objects := make([]client.Object, len(items))
		for i := range items {
			objects[i] = &items[i]
		}
		return objects
	},
	Spec:   func(o client.Object) *kiongav1.WorkloadSpec { return &o.(*kiongav1.KiongaApplication).Spec },
	Status: func(o client.Object) *kiongav1.WorkloadStatus { return &o.(*kiongav1.KiongaApplication).Status },
}

// FunctionKind reconciles KiongaFunction: internal by default, scale to zero
// on requests, reached by the gateway through status.internalURL.
var FunctionKind = WorkloadKind{
	Component: "function", PodLabel: "kionga.dev/function", DefaultVisibility: kiongav1.VisibilityInternal,
	DefaultReplicas: kiongav1.WorkloadReplicaSpec{Min: 0, Max: 5},
	DefaultMetric:   kiongav1.ScalingMetricRequests,
	DefaultCPU:      "100m", DefaultMemory: "256Mi", ContainerName: "function",
	NewObject: func() client.Object { return &kiongav1.KiongaFunction{} },
	NewList:   func() client.ObjectList { return &kiongav1.KiongaFunctionList{} },
	Items: func(list client.ObjectList) []client.Object {
		items := list.(*kiongav1.KiongaFunctionList).Items
		objects := make([]client.Object, len(items))
		for i := range items {
			objects[i] = &items[i]
		}
		return objects
	},
	Spec:   func(o client.Object) *kiongav1.WorkloadSpec { return &o.(*kiongav1.KiongaFunction).Spec },
	Status: func(o client.Object) *kiongav1.WorkloadStatus { return &o.(*kiongav1.KiongaFunction).Status },
}

// WorkloadReconciler turns a KiongaApplication or KiongaFunction into a
// Deployment, Service, autoscaler, optional Ingress and PodDisruptionBudget,
// all owned by the resource so deleting it removes them.
type WorkloadReconciler struct {
	client.Client
	Kind         WorkloadKind
	Config       WorkloadConfig
	Capabilities Capabilities
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// errInvalid marks a spec problem the user must fix; retrying will not help.
type errInvalid struct {
	reason string
	err    error
}

func (e errInvalid) Error() string { return e.err.Error() }

func invalid(reason, format string, args ...any) error {
	return errInvalid{reason: reason, err: fmt.Errorf(format, args...)}
}

func (r *WorkloadReconciler) now() metav1.Time {
	if r.Now != nil {
		return metav1.NewTime(r.Now())
	}
	return metav1.Now()
}

func (r *WorkloadReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	object := r.Kind.NewObject()
	if err := r.Get(ctx, request.NamespacedName, object); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !object.GetDeletionTimestamp().IsZero() {
		// Owner references let the garbage collector remove every child.
		return ctrl.Result{}, nil
	}
	spec, status := r.Kind.Spec(object), r.Kind.Status(object)
	before := status.DeepCopy()
	result, err := r.reconcile(ctx, object, spec, status)
	var bad errInvalid
	if errors.As(err, &bad) {
		status.Phase = "Failed"
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: bad.reason, Message: bad.Error(), ObservedGeneration: object.GetGeneration()})
		status.ObservedGeneration = object.GetGeneration()
		// No hot loop on a spec error. A spec change triggers a reconcile; the
		// slow requeue picks up a Secret created or labelled later.
		status.Message = readyMessage(status.Conditions)
		return ctrl.Result{RequeueAfter: time.Minute}, updateStatusIfChanged(ctx, r.Client, object, before, status)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	status.ObservedGeneration = object.GetGeneration()
	status.Message = readyMessage(status.Conditions)
	if err := updateStatusIfChanged(ctx, r.Client, object, before, status); err != nil {
		return ctrl.Result{}, err
	}
	return result, nil
}

func (r *WorkloadReconciler) reconcile(ctx context.Context, object client.Object, spec *kiongav1.WorkloadSpec, status *kiongav1.WorkloadStatus) (ctrl.Result, error) {
	name, namespace := object.GetName(), object.GetNamespace()
	if problems := validation.IsDNS1035Label(name); len(problems) > 0 || len(name) > MaxWorkloadNameLength {
		return ctrl.Result{}, invalid("InvalidName", "name %q must be a DNS label of at most %d characters", name, MaxWorkloadNameLength)
	}
	if (spec.Image == "") == (spec.Source == nil) {
		return ctrl.Result{}, invalid("InvalidSpec", "set exactly one of image or source")
	}
	port := spec.Port
	if port == 0 {
		port = defaultWorkloadPort
	}
	visibility := spec.Visibility
	if visibility == "" {
		visibility = r.Kind.DefaultVisibility
	}
	replicas := r.Kind.DefaultReplicas
	if spec.Replicas != nil {
		replicas = *spec.Replicas
	}
	if replicas.Max < replicas.Min {
		return ctrl.Result{}, invalid("InvalidSpec", "max replicas must be greater than or equal to min replicas")
	}
	resources, err := resolvedResources(spec.Resources, r.Kind.DefaultCPU, r.Kind.DefaultMemory)
	if err != nil {
		return ctrl.Result{}, invalid("InvalidResources", "%v", err)
	}
	secretHash, err := r.workloadSecrets(ctx, namespace, spec.SecretRefs)
	if err != nil {
		var bad errInvalid
		if errors.As(err, &bad) {
			// Fail closed: a Secret that is gone or no longer labelled for
			// workloads leaves the running pod template now, not on the next
			// valid spec.
			if revokeErr := r.revokeSecrets(ctx, object, spec.SecretRefs); revokeErr != nil {
				return ctrl.Result{}, revokeErr
			}
		}
		return ctrl.Result{}, err
	}
	image, err := r.resolveImage(ctx, object, spec, status)
	if err != nil {
		return ctrl.Result{}, err
	}
	if image == "" {
		// The first source build is still running; nothing to deploy yet.
		status.Phase = "Building"
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: "Building", Message: "waiting for the first source build", ObservedGeneration: object.GetGeneration()})
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	plan := PlanScaling(spec.Scaling, replicas, r.Kind.DefaultMetric, r.Capabilities.KEDAHTTP)
	selector := map[string]string{"kionga.dev/workload": name, "kionga.dev/component": r.Kind.Component}
	labels := map[string]string{
		"app.kubernetes.io/name": name, "app.kubernetes.io/component": r.Kind.Component,
		"app.kubernetes.io/managed-by": "kionga-operator", "kionga.dev/workload": name, "kionga.dev/component": r.Kind.Component,
	}

	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		deployment.Labels = labels
		// An autoscaler owns spec.replicas once the Deployment exists.
		if !plan.Autoscaled() || deployment.ResourceVersion == "" {
			initial := plan.InitialReplicas()
			deployment.Spec.Replicas = &initial
		}
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
		surge, unavailable := intstr.FromInt32(1), intstr.FromInt32(0)
		deployment.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: &surge, MaxUnavailable: &unavailable}}
		history, deadline := int32(5), int32(600)
		deployment.Spec.RevisionHistoryLimit, deployment.Spec.ProgressDeadlineSeconds = &history, &deadline
		// Template labels may add to the immutable selector, never change it.
		deployment.Spec.Template.Labels = map[string]string{r.Kind.PodLabel: name}
		for key, value := range selector {
			deployment.Spec.Template.Labels[key] = value
		}
		deployment.Spec.Template.Annotations = map[string]string{"kionga.dev/secret-hash": secretHash}
		if spec.RestartedAt != "" {
			deployment.Spec.Template.Annotations[RestartedAtAnnotation] = spec.RestartedAt
		}
		deployment.Spec.Template.Spec = r.podSpec(spec, image, port, resources)
		return controllerutil.SetControllerReference(object, deployment, r.Scheme())
	}); err != nil {
		return ctrl.Result{}, err
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		service.Labels = labels
		service.Spec.Type = corev1.ServiceTypeClusterIP
		service.Spec.Selector = selector
		// The Service listens on the container port, which is where the
		// gateway calls a CPU-scaled function.
		service.Spec.Ports = []corev1.ServicePort{{Name: "http", Port: port, TargetPort: intstr.FromString("http")}}
		return controllerutil.SetControllerReference(object, service, r.Scheme())
	}); err != nil {
		return ctrl.Result{}, err
	}

	host := ""
	ingressCondition := metav1.Condition{Type: ConditionIngress, Status: metav1.ConditionTrue, Reason: "Internal", Message: "no public host; reach the workload through internalURL"}
	if visibility != kiongav1.VisibilityInternal {
		switch {
		case r.Config.BaseDomain == "":
			ingressCondition = metav1.Condition{Type: ConditionIngress, Status: metav1.ConditionFalse, Reason: "BaseDomainNotConfigured", Message: "the operator has no KIONGA_APPLICATIONS_BASE_DOMAIN, so no host is served"}
		case visibility == kiongav1.VisibilitySSO && r.Config.SSOUpstreamHost == "":
			// Fail closed: never publish an sso workload without the gateway in front.
			ingressCondition = metav1.Condition{Type: ConditionIngress, Status: metav1.ConditionFalse, Reason: "SSOUpstreamNotConfigured", Message: "visibility sso needs KIONGA_SSO_UPSTREAM; the host is not served"}
		default:
			host = name + "." + r.Config.BaseDomain
		}
	}
	internalURL, routeHost, err := applyScaling(ctx, r.Client, object, plan, scalingTarget{Deployment: name, Service: name, ServicePort: port, Labels: labels}, r.Config, host)
	if err != nil {
		return ctrl.Result{}, err
	}
	backend := ingressBackend{Service: name, Port: port}
	if plan.Metric == kiongav1.ScalingMetricRequests {
		backend = ingressBackend{Service: RouteServiceName(name), Port: r.Config.InterceptorPort}
	}
	var annotations map[string]string
	sso := host != "" && visibility == kiongav1.VisibilitySSO
	if sso {
		backend = ingressBackend{Service: SSOServiceName(name), Port: r.Config.SSOUpstreamPort}
		annotations = map[string]string{VisibilityAnnotation: kiongav1.VisibilitySSO, UpstreamAnnotation: internalURL}
	}
	if err := applyExternalNameService(ctx, r.Client, object, SSOServiceName(name), r.Config.SSOUpstreamHost, r.Config.SSOUpstreamPort, labels, sso); err != nil {
		return ctrl.Result{}, err
	}
	if err := applyIngress(ctx, r.Client, object, name, host, backend, labels, annotations, r.Config); err != nil {
		return ctrl.Result{}, err
	}
	if host != "" {
		scheme := "http"
		if r.Config.TLSSecret != "" {
			scheme = "https"
		}
		status.URL = scheme + "://" + host
		ingressCondition = metav1.Condition{Type: ConditionIngress, Status: metav1.ConditionTrue, Reason: "Served", Message: fmt.Sprintf("serving %s (%s)", status.URL, visibility)}
	} else {
		status.URL = ""
	}
	if err := applyPodDisruptionBudget(ctx, r.Client, object, name, selector, labels, plan.Min); err != nil {
		return ctrl.Result{}, err
	}

	status.Image = image
	status.InternalURL = internalURL
	status.Host = routeHost
	status.ScalingMetric = plan.Metric
	status.ReadyReplicas = deployment.Status.ReadyReplicas
	ingressCondition.ObservedGeneration = object.GetGeneration()
	plan.Condition.ObservedGeneration = object.GetGeneration()
	apimeta.SetStatusCondition(&status.Conditions, ingressCondition)
	apimeta.SetStatusCondition(&status.Conditions, plan.Condition)
	phase, ready := DeploymentHealth(deployment, plan.Min)
	ready.ObservedGeneration = object.GetGeneration()
	status.Phase = phase
	apimeta.SetStatusCondition(&status.Conditions, ready)
	if status.LastBuild != nil && status.LastBuild.Phase == "Running" {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	if phase != "Ready" {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// DeploymentHealth maps a Deployment's status to a phase and Ready
// condition. minReplicas is the floor the workload promises (0 when it may
// scale to zero).
//
//   - Failed: the rollout exceeded its progress deadline.
//   - Progressing: a rollout is underway.
//   - Degraded: no rollout is underway but fewer replicas are available
//     than required, for example after a pod crashed.
//   - Ready: the current revision has the required replicas available.
func DeploymentHealth(deployment *appsv1.Deployment, minReplicas int32) (string, metav1.Condition) {
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	required := minReplicas
	if required < 1 {
		required = 1
	}
	if required > desired {
		required = desired
	}
	available := deployment.Status.AvailableReplicas
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded" {
			return "Failed", metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: fmt.Sprintf("Deployment %s did not roll out: %s", deployment.Name, condition.Message)}
		}
	}
	rolledOut := deployment.Status.ObservedGeneration >= deployment.Generation && deployment.Status.UpdatedReplicas >= desired
	switch {
	case rolledOut && desired == 0:
		return "Ready", metav1.Condition{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: "ScaledToZero", Message: "no replicas are running; the next request starts one"}
	case rolledOut && available >= required:
		return "Ready", metav1.Condition{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: "WorkloadAvailable", Message: fmt.Sprintf("Deployment %s has %d/%d replicas available", deployment.Name, available, desired)}
	case rolledOut:
		return "Degraded", metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: "WorkloadUnavailable", Message: fmt.Sprintf("Deployment %s has %d/%d replicas available, %d required", deployment.Name, available, desired, required)}
	default:
		return "Progressing", metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: "WorkloadProgressing", Message: fmt.Sprintf("Deployment %s is rolling out: %d/%d updated, %d available", deployment.Name, deployment.Status.UpdatedReplicas, desired, available)}
	}
}

func (r *WorkloadReconciler) podSpec(spec *kiongav1.WorkloadSpec, image string, port int32, resources corev1.ResourceRequirements) corev1.PodSpec {
	env := make([]corev1.EnvVar, 0, len(spec.Env)+1)
	userPort := false
	for _, variable := range spec.Env {
		env = append(env, corev1.EnvVar{Name: variable.Name, Value: variable.Value})
		userPort = userPort || variable.Name == "PORT"
	}
	if !userPort {
		env = append([]corev1.EnvVar{{Name: "PORT", Value: fmt.Sprint(port)}}, env...)
	}
	envFrom := make([]corev1.EnvFromSource, 0, len(spec.SecretRefs))
	for _, ref := range spec.SecretRefs {
		envFrom = append(envFrom, corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name}}})
	}
	handler := corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString("http")}}
	if spec.HealthPath != "" {
		handler = corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: spec.HealthPath, Port: intstr.FromString("http")}}
	}
	allow, nonRoot, automount := false, true, false
	return corev1.PodSpec{
		AutomountServiceAccountToken: &automount,
		ImagePullSecrets:             r.Config.ImagePullSecrets,
		SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: &nonRoot, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Volumes:                      []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		Containers: []corev1.Container{{
			Name: r.Kind.ContainerName, Image: image,
			Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: port}},
			Env:   env, EnvFrom: envFrom, Resources: resources,
			VolumeMounts:   []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
			StartupProbe:   &corev1.Probe{ProbeHandler: handler, PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 60},
			ReadinessProbe: &corev1.Probe{ProbeHandler: handler, PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 3},
			LivenessProbe:  &corev1.Probe{ProbeHandler: handler, PeriodSeconds: 10, TimeoutSeconds: 2, FailureThreshold: 3},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &allow, RunAsNonRoot: &nonRoot,
				Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
		}},
	}
}

// workloadSecrets checks every referenced Secret exists and is labelled for
// workload use, and hashes their data so a changed Secret rolls the pods.
func (r *WorkloadReconciler) workloadSecrets(ctx context.Context, namespace string, refs []kiongav1.SecretReference) (string, error) {
	digest := sha256.New()
	for _, ref := range refs {
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: namespace}, &secret); err != nil {
			if apierrors.IsNotFound(err) {
				return "", invalid("SecretNotFound", "Secret %s does not exist", ref.Name)
			}
			return "", err
		}
		if secret.Labels[WorkloadSecretLabel] != "true" {
			return "", invalid("SecretNotAllowed", "Secret %s is not labelled %s=true", ref.Name, WorkloadSecretLabel)
		}
		keys := make([]string, 0, len(secret.Data))
		for key := range secret.Data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Fprintf(digest, "%s\x00", ref.Name)
		for _, key := range keys {
			fmt.Fprintf(digest, "%s\x00%x\x00", key, sha256.Sum256(secret.Data[key]))
		}
	}
	return fmt.Sprintf("%x", digest.Sum(nil))[:16], nil
}

// resolvedResources fills missing CPU and memory with defaults and mirrors
// requests and limits so every workload gets the Guaranteed QoS class.
func resolvedResources(value corev1.ResourceRequirements, cpu, memory string) (corev1.ResourceRequirements, error) {
	result := *value.DeepCopy()
	if result.Requests == nil {
		result.Requests = corev1.ResourceList{}
	}
	if result.Limits == nil {
		result.Limits = corev1.ResourceList{}
	}
	for name, raw := range map[corev1.ResourceName]string{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory} {
		if _, exists := result.Requests[name]; exists {
			continue
		}
		if limit, exists := result.Limits[name]; exists {
			result.Requests[name] = limit
			continue
		}
		quantity, err := resource.ParseQuantity(raw)
		if err != nil {
			return result, err
		}
		result.Requests[name] = quantity
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

func (r *WorkloadReconciler) SetupWithManager(manager ctrl.Manager) error {
	controller := ctrl.NewControllerManagedBy(manager).Named(r.Kind.Component).For(r.Kind.NewObject(), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.Deployment{}).Owns(&corev1.Service{}).Owns(&networkingv1.Ingress{}).
		Owns(&policyv1.PodDisruptionBudget{}).Owns(&autoscalingv2.HorizontalPodAutoscaler{}).Owns(&batchv1.Job{}).
		// A rotated workload Secret must roll the pods that load it.
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.workloadsUsingSecret),
			builder.WithPredicates(workloadSecretChanged))
	if r.Capabilities.KEDAHTTP {
		scaled := &unstructured.Unstructured{}
		scaled.SetGroupVersionKind(HTTPScaledObjectGVK)
		controller = controller.Owns(scaled)
	}
	return controller.Complete(r)
}

// updateStatusIfChanged writes status only when the reconcile changed it, so
// a healthy resource costs no API writes.
func updateStatusIfChanged(ctx context.Context, c client.Client, object client.Object, before, after any) error {
	if equality.Semantic.DeepEqual(before, after) {
		return nil
	}
	return c.Status().Update(ctx, object)
}

// workloadsUsingSecret maps a workload Secret to the resources of this kind
// in its namespace that load it through secretRefs.
func (r *WorkloadReconciler) workloadsUsingSecret(ctx context.Context, secret client.Object) []reconcile.Request {
	list := r.Kind.NewList()
	if err := r.List(ctx, list, client.InNamespace(secret.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "list workloads for Secret", "secret", secret.GetName())
		return nil
	}
	var requests []reconcile.Request
	for _, object := range r.Kind.Items(list) {
		for _, ref := range r.Kind.Spec(object).SecretRefs {
			if ref.Name == secret.GetName() {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(object)})
				break
			}
		}
	}
	return requests
}

func labelledWorkloadSecret(object client.Object) bool {
	return object != nil && object.GetLabels()[WorkloadSecretLabel] == "true"
}

// workloadSecretChanged passes events for Secrets that are, or were until
// this update, labelled for workloads. Removing the label must reconcile the
// workloads that load the Secret so they stop loading it.
var workloadSecretChanged = predicate.Funcs{
	CreateFunc:  func(e event.CreateEvent) bool { return labelledWorkloadSecret(e.Object) },
	DeleteFunc:  func(e event.DeleteEvent) bool { return labelledWorkloadSecret(e.Object) },
	GenericFunc: func(e event.GenericEvent) bool { return labelledWorkloadSecret(e.Object) },
	UpdateFunc: func(e event.UpdateEvent) bool {
		return labelledWorkloadSecret(e.ObjectOld) || labelledWorkloadSecret(e.ObjectNew)
	},
}

// revokeSecrets removes from the owned Deployment every envFrom Secret that
// is missing or not labelled for workloads, which rolls the pods without it.
func (r *WorkloadReconciler) revokeSecrets(ctx context.Context, object client.Object, refs []kiongav1.SecretReference) error {
	var deployment appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKeyFromObject(object), &deployment); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(&deployment, object) {
		return nil
	}
	allowed := map[string]bool{}
	for _, ref := range refs {
		var secret corev1.Secret
		err := r.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: object.GetNamespace()}, &secret)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		allowed[ref.Name] = err == nil && labelledWorkloadSecret(&secret)
	}
	changed := false
	for i := range deployment.Spec.Template.Spec.Containers {
		container := &deployment.Spec.Template.Spec.Containers[i]
		kept := container.EnvFrom[:0]
		for _, source := range container.EnvFrom {
			if source.SecretRef != nil && !allowed[source.SecretRef.Name] {
				changed = true
				continue
			}
			kept = append(kept, source)
		}
		container.EnvFrom = kept
	}
	if !changed {
		return nil
	}
	return r.Update(ctx, &deployment)
}

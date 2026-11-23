package operator

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

const (
	// KServeDeploymentModeAnnotation selects KServe's deployment mode.
	KServeDeploymentModeAnnotation = "serving.kserve.io/deploymentMode"
	// DefaultKServeDeploymentMode is RawDeployment: plain Deployments and
	// Services, no Knative or Istio. KServe 0.21 calls the same mode
	// "Standard" and still accepts "RawDeployment".
	DefaultKServeDeploymentMode = "RawDeployment"
	defaultModelFormat          = "mlflow"
)

// ModelArtifactSource is the subset of the MLflow REST API the operator uses
// to find where a registered model version's files live.
type ModelArtifactSource interface {
	ModelVersionDownloadURI(ctx context.Context, name, version string) (string, error)
	RunArtifactURI(ctx context.Context, runID string) (string, error)
	LoggedModelArtifactURI(ctx context.Context, modelID string) (string, error)
}

// ModelStorageConfig tells the KServe storage initializer how to reach the
// S3-compatible object store.
type ModelStorageConfig struct {
	// CredentialsSecret holds AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY in
	// the workload namespace (KIONGA_MODEL_STORAGE_SECRET). Empty leaves
	// credentials to the cluster, for example workload identity.
	CredentialsSecret string
	// Endpoint is the S3 endpoint host:port, without scheme
	// (KIONGA_MODEL_S3_ENDPOINT).
	Endpoint string
	// UseHTTPS is "1" or "0" (KIONGA_MODEL_S3_USE_HTTPS, default 0).
	UseHTTPS string
	// Region is the S3 region (KIONGA_MODEL_S3_REGION, default us-east-1).
	Region string
	// ArtifactsDestination maps MLflow proxied artifact URIs
	// (mlflow-artifacts:/...) to the bucket behind them, for example
	// s3://kionga-models (KIONGA_MLFLOW_ARTIFACTS_DESTINATION).
	ArtifactsDestination string
	// DeploymentMode is the KServe deployment mode annotation value
	// (KIONGA_KSERVE_DEPLOYMENT_MODE, default RawDeployment).
	DeploymentMode string
}

// ResolveModelStorageURI turns a registered model version into the s3:// URI
// KServe's storage initializer downloads. MLflow's get-download-uri answers
// with the backing store location; runs:/ and models:/ answers from older
// servers are resolved through the run or logged model, and
// mlflow-artifacts: answers through the configured artifacts destination.
func ResolveModelStorageURI(ctx context.Context, source ModelArtifactSource, artifactsDestination, name, version string) (string, error) {
	if source == nil {
		return "", errors.New("MLflow is not configured on the operator (MLFLOW_URL)")
	}
	uri, err := source.ModelVersionDownloadURI(ctx, name, version)
	if err != nil {
		return "", fmt.Errorf("MLflow could not locate %s version %s: %w", name, version, err)
	}
	for range 3 {
		parsed, err := url.Parse(uri)
		if err != nil || uri == "" {
			return "", fmt.Errorf("MLflow returned an unusable artifact location %q", uri)
		}
		switch parsed.Scheme {
		case "s3":
			if parsed.Host == "" {
				return "", fmt.Errorf("artifact location %q has no bucket", uri)
			}
			return strings.TrimRight(uri, "/"), nil
		case "mlflow-artifacts":
			if artifactsDestination == "" {
				return "", fmt.Errorf("artifact location %q is proxied by MLflow; set KIONGA_MLFLOW_ARTIFACTS_DESTINATION to the bucket behind it", uri)
			}
			return strings.TrimRight(artifactsDestination, "/") + "/" + strings.TrimLeft(parsed.Path, "/"), nil
		case "runs":
			// runs:/<run_id>/<path>
			runID, path, _ := strings.Cut(strings.TrimLeft(parsed.Path, "/"), "/")
			root, err := source.RunArtifactURI(ctx, runID)
			if err != nil {
				return "", fmt.Errorf("MLflow could not locate run %s: %w", runID, err)
			}
			uri = strings.TrimRight(root, "/") + "/" + path
		case "models":
			// models:/m-<id> names an MLflow 3 logged model.
			modelID := strings.Trim(parsed.Opaque+parsed.Path, "/")
			if !strings.HasPrefix(modelID, "m-") {
				return "", fmt.Errorf("artifact location %q is a registry reference MLflow did not resolve", uri)
			}
			root, err := source.LoggedModelArtifactURI(ctx, modelID)
			if err != nil {
				return "", fmt.Errorf("MLflow could not locate logged model %s: %w", modelID, err)
			}
			uri = root
		default:
			return "", fmt.Errorf("artifact location %q uses scheme %q; KServe here reads s3:// only", uri, parsed.Scheme)
		}
	}
	return "", fmt.Errorf("artifact location for %s version %s did not resolve to s3://", name, version)
}

// ModelDeploymentReconciler serves a KiongaModelDeployment as one KServe
// InferenceService in RawDeployment mode, plus a second one for a canary.
// KServe raw mode has no traffic split (canaryTrafficPercent needs Knative),
// so the gateway splits requests by status.canaryWeight.
type ModelDeploymentReconciler struct {
	client.Client
	MLflow       ModelArtifactSource
	Storage      ModelStorageConfig
	Capabilities Capabilities
}

// CanaryName is the canary InferenceService of a model deployment.
func CanaryName(deployment string) string { return deployment + "-canary" }

// StorageAccountName is the ServiceAccount carrying object store credentials.
func StorageAccountName(deployment string) string { return deployment + "-storage" }

func (r *ModelDeploymentReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var deployment kiongav1.KiongaModelDeployment
	if err := r.Get(ctx, request.NamespacedName, &deployment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !deployment.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	before := deployment.Status.DeepCopy()
	result, reason, err := r.reconcile(ctx, &deployment)
	status := &deployment.Status
	status.ObservedGeneration = deployment.Generation
	if reason != "" {
		status.Phase = "Failed"
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: reason, Message: err.Error(), ObservedGeneration: deployment.Generation})
		if updateErr := updateStatusIfChanged(ctx, r.Client, &deployment, before, &deployment.Status); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return result, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return result, updateStatusIfChanged(ctx, r.Client, &deployment, before, &deployment.Status)
}

// reconcile returns a reason when the failure belongs in status rather than
// in a controller retry.
func (r *ModelDeploymentReconciler) reconcile(ctx context.Context, deployment *kiongav1.KiongaModelDeployment) (ctrl.Result, string, error) {
	if !r.Capabilities.KServe {
		return ctrl.Result{}, "KServeNotInstalled", errors.New("this cluster does not serve serving.kserve.io/v1beta1 InferenceService; install KServe in RawDeployment mode")
	}
	if problems := validation.IsDNS1035Label(deployment.Name); len(problems) > 0 || len(deployment.Name) > MaxWorkloadNameLength {
		return ctrl.Result{}, "InvalidName", fmt.Errorf("name %q must be a DNS label of at most %d characters", deployment.Name, MaxWorkloadNameLength)
	}
	// Resolve through MLflow once per spec generation, so an MLflow outage
	// does not disturb a model that is already serving.
	resolved := deployment.Status.ObservedGeneration == deployment.Generation && deployment.Status.Phase != "Failed" && deployment.Status.StorageURI != "" &&
		(deployment.Spec.Canary == nil || deployment.Status.CanaryStorageURI != "")
	stableURI, canaryURI := deployment.Status.StorageURI, deployment.Status.CanaryStorageURI
	if !resolved {
		var err error
		if stableURI, err = ResolveModelStorageURI(ctx, r.MLflow, r.Storage.ArtifactsDestination, deployment.Spec.ModelName, deployment.Spec.Version); err != nil {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, "ModelResolutionFailed", err
		}
		canaryURI = ""
		if canary := deployment.Spec.Canary; canary != nil {
			if canaryURI, err = ResolveModelStorageURI(ctx, r.MLflow, r.Storage.ArtifactsDestination, deployment.Spec.ModelName, canary.Version); err != nil {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, "ModelResolutionFailed", err
			}
		}
	}
	serviceAccount, err := r.storageAccount(ctx, deployment)
	if err != nil {
		var bad errInvalid
		if errors.As(err, &bad) {
			return ctrl.Result{RequeueAfter: time.Minute}, bad.reason, err
		}
		return ctrl.Result{}, "", err
	}
	stable, err := r.applyInferenceService(ctx, deployment, deployment.Name, deployment.Spec.Version, stableURI, serviceAccount, "stable")
	if err != nil {
		return ctrl.Result{}, "", err
	}
	status := &deployment.Status
	status.InferenceServiceRef, status.StorageURI = deployment.Name, stableURI
	stableURL, stableReady, stableMessage := inferenceServiceState(stable)
	status.URL = stableURL
	ready, message := stableReady, stableMessage
	if deployment.Spec.Canary != nil {
		canary, err := r.applyInferenceService(ctx, deployment, CanaryName(deployment.Name), deployment.Spec.Canary.Version, canaryURI, serviceAccount, "canary")
		if err != nil {
			return ctrl.Result{}, "", err
		}
		canaryURL, canaryReady, canaryMessage := inferenceServiceState(canary)
		status.CanaryURL, status.CanaryStorageURI, status.CanaryInferenceServiceRef = canaryURL, canaryURI, CanaryName(deployment.Name)
		status.CanaryWeight = deployment.Spec.Canary.Weight
		ready = ready && canaryReady
		message = "stable: " + stableMessage + "; canary: " + canaryMessage
	} else {
		canary := &unstructured.Unstructured{}
		canary.SetGroupVersionKind(InferenceServiceGVK)
		if err := deleteIfOwned(ctx, r.Client, canary, client.ObjectKey{Name: CanaryName(deployment.Name), Namespace: deployment.Namespace}, deployment); err != nil {
			return ctrl.Result{}, "", err
		}
		status.CanaryURL, status.CanaryStorageURI, status.CanaryInferenceServiceRef, status.CanaryWeight = "", "", "", 0
	}
	condition := metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: "InferenceServiceProgressing", Message: message, ObservedGeneration: deployment.Generation}
	status.Phase = "Progressing"
	if ready {
		status.Phase = "Ready"
		condition.Status, condition.Reason = metav1.ConditionTrue, "InferenceServiceReady"
	}
	apimeta.SetStatusCondition(&status.Conditions, condition)
	if !ready {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, "", nil
	}
	return ctrl.Result{}, "", nil
}

// inferenceServiceState reads the address and Ready condition KServe
// reports. The in-cluster address is preferred: the gateway calls it.
func inferenceServiceState(service *unstructured.Unstructured) (string, bool, string) {
	address, _, _ := unstructured.NestedString(service.Object, "status", "address", "url")
	if address == "" {
		address, _, _ = unstructured.NestedString(service.Object, "status", "url")
	}
	conditions, _, _ := unstructured.NestedSlice(service.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		if condition["type"] != "Ready" {
			continue
		}
		message, _ := condition["message"].(string)
		reason, _ := condition["reason"].(string)
		if condition["status"] == "True" {
			return address, address != "", "InferenceService " + service.GetName() + " is ready"
		}
		return address, false, strings.TrimSpace(fmt.Sprintf("InferenceService %s is not ready: %s %s", service.GetName(), reason, message))
	}
	return address, false, "InferenceService " + service.GetName() + " has not reported readiness"
}

func (r *ModelDeploymentReconciler) applyInferenceService(ctx context.Context, owner *kiongav1.KiongaModelDeployment, name, version, storageURI, serviceAccount, track string) (*unstructured.Unstructured, error) {
	service := &unstructured.Unstructured{}
	service.SetGroupVersionKind(InferenceServiceGVK)
	service.SetName(name)
	service.SetNamespace(owner.Namespace)
	mode := r.Storage.DeploymentMode
	if mode == "" {
		mode = DefaultKServeDeploymentMode
	}
	replicas := kiongav1.WorkloadReplicaSpec{Min: 1, Max: 1}
	if owner.Spec.Replicas != nil {
		replicas = *owner.Spec.Replicas
	}
	if replicas.Min < 1 {
		// Raw mode scales with an HPA, which cannot reach zero.
		replicas.Min = 1
	}
	if replicas.Max < replicas.Min {
		replicas.Max = replicas.Min
	}
	format := owner.Spec.ModelFormat
	if format == "" {
		format = defaultModelFormat
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		annotations := service.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[KServeDeploymentModeAnnotation] = mode
		service.SetAnnotations(annotations)
		service.SetLabels(map[string]string{
			"app.kubernetes.io/managed-by": "kionga-operator", "app.kubernetes.io/component": "model",
			"kionga.dev/model-deployment": owner.Name, "kionga.dev/track": track, "kionga.dev/model-version": dnsLabel(version),
		})
		model := map[string]any{"modelFormat": map[string]any{"name": format}, "storageUri": storageURI}
		if owner.Spec.Runtime != "" {
			model["runtime"] = owner.Spec.Runtime
		}
		if resources := resourceMap(owner.Spec.Resources); resources != nil {
			model["resources"] = resources
		}
		predictor := map[string]any{"minReplicas": int64(replicas.Min), "maxReplicas": int64(replicas.Max), "model": model}
		if serviceAccount != "" {
			predictor["serviceAccountName"] = serviceAccount
		}
		service.Object["spec"] = map[string]any{"predictor": predictor}
		return controllerutil.SetControllerReference(owner, service, r.Scheme())
	})
	return service, err
}

func resourceMap(resources corev1.ResourceRequirements) map[string]any {
	result := map[string]any{}
	for key, list := range map[string]corev1.ResourceList{"requests": resources.Requests, "limits": resources.Limits} {
		if len(list) == 0 {
			continue
		}
		values := map[string]any{}
		for name, quantity := range list {
			values[string(name)] = quantity.String()
		}
		result[key] = values
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// storageAccount gives the predictor a ServiceAccount whose Secret carries
// the object store keys and the serving.kserve.io/s3-* annotations KServe's
// storage initializer reads. Both are owned by the deployment.
func (r *ModelDeploymentReconciler) storageAccount(ctx context.Context, deployment *kiongav1.KiongaModelDeployment) (string, error) {
	name := StorageAccountName(deployment.Name)
	key := client.ObjectKey{Name: name, Namespace: deployment.Namespace}
	if r.Storage.CredentialsSecret == "" {
		if err := deleteIfOwned(ctx, r.Client, &corev1.ServiceAccount{}, key, deployment); err != nil {
			return "", err
		}
		return "", deleteIfOwned(ctx, r.Client, &corev1.Secret{}, key, deployment)
	}
	var source corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Name: r.Storage.CredentialsSecret, Namespace: deployment.Namespace}, &source); err != nil {
		if apierrors.IsNotFound(err) {
			return "", invalid("StorageCredentialsMissing", "object store Secret %s does not exist in %s", r.Storage.CredentialsSecret, deployment.Namespace)
		}
		return "", err
	}
	for _, field := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		if len(source.Data[field]) == 0 {
			return "", invalid("StorageCredentialsMissing", "object store Secret %s has no %s", r.Storage.CredentialsSecret, field)
		}
	}
	useHTTPS, region := r.Storage.UseHTTPS, r.Storage.Region
	if useHTTPS == "" {
		useHTTPS = "0"
	}
	if region == "" {
		region = "us-east-1"
	}
	labels := map[string]string{"app.kubernetes.io/managed-by": "kionga-operator", "kionga.dev/model-deployment": deployment.Name}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: deployment.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = labels
		secret.Annotations = map[string]string{
			"serving.kserve.io/s3-usehttps": useHTTPS, "serving.kserve.io/s3-region": region,
			"serving.kserve.io/s3-useanoncredential": "false",
		}
		if r.Storage.Endpoint != "" {
			secret.Annotations["serving.kserve.io/s3-endpoint"] = r.Storage.Endpoint
		}
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{"AWS_ACCESS_KEY_ID": source.Data["AWS_ACCESS_KEY_ID"], "AWS_SECRET_ACCESS_KEY": source.Data["AWS_SECRET_ACCESS_KEY"]}
		return controllerutil.SetControllerReference(deployment, secret, r.Scheme())
	}); err != nil {
		return "", err
	}
	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: deployment.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, account, func() error {
		account.Labels = labels
		account.Secrets = []corev1.ObjectReference{{Name: name}}
		automount := false
		account.AutomountServiceAccountToken = &automount
		return controllerutil.SetControllerReference(deployment, account, r.Scheme())
	}); err != nil {
		return "", err
	}
	return name, nil
}

func (r *ModelDeploymentReconciler) SetupWithManager(manager ctrl.Manager) error {
	controller := ctrl.NewControllerManagedBy(manager).For(&kiongav1.KiongaModelDeployment{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&corev1.ServiceAccount{}).Owns(&corev1.Secret{})
	if r.Storage.CredentialsSecret != "" {
		// Rotating the object store keys refreshes every derived storage
		// Secret, which new predictor pods load.
		controller = controller.Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.deploymentsUsingStorageSecret),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(object client.Object) bool {
				return object.GetName() == r.Storage.CredentialsSecret
			})))
	}
	if r.Capabilities.KServe {
		service := &unstructured.Unstructured{}
		service.SetGroupVersionKind(InferenceServiceGVK)
		controller = controller.Owns(service)
	}
	return controller.Complete(r)
}

// deploymentsUsingStorageSecret maps the configured object store Secret to
// every model deployment in its namespace.
func (r *ModelDeploymentReconciler) deploymentsUsingStorageSecret(ctx context.Context, secret client.Object) []reconcile.Request {
	var deployments kiongav1.KiongaModelDeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(secret.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "list model deployments for storage Secret", "secret", secret.GetName())
		return nil
	}
	requests := make([]reconcile.Request, 0, len(deployments.Items))
	for i := range deployments.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&deployments.Items[i])})
	}
	return requests
}

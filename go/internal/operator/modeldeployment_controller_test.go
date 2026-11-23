package operator

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

// fakeMLflow answers the three artifact-location calls from maps.
type fakeMLflow struct {
	versions map[string]string // "name/version" -> download URI
	runs     map[string]string
	models   map[string]string
	err      error
	calls    int
}

func (f *fakeMLflow) ModelVersionDownloadURI(_ context.Context, name, version string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	uri, ok := f.versions[name+"/"+version]
	if !ok {
		return "", errors.New("integration returned 404: RESOURCE_DOES_NOT_EXIST")
	}
	return uri, nil
}

func (f *fakeMLflow) RunArtifactURI(_ context.Context, runID string) (string, error) {
	if uri, ok := f.runs[runID]; ok {
		return uri, nil
	}
	return "", errors.New("run not found")
}

func (f *fakeMLflow) LoggedModelArtifactURI(_ context.Context, modelID string) (string, error) {
	if uri, ok := f.models[modelID]; ok {
		return uri, nil
	}
	return "", errors.New("logged model not found")
}

func TestResolveModelStorageURI(t *testing.T) {
	source := &fakeMLflow{
		versions: map[string]string{
			"churn/1": "s3://kionga-models/1/abc/artifacts/model/",
			"churn/2": "runs:/abc/model",
			"churn/3": "models:/m-123",
			"churn/4": "mlflow-artifacts:/1/def/artifacts/model",
			"churn/5": "file:///mlruns/1/x/artifacts/model",
			"churn/6": "models:/churn/6",
		},
		runs:   map[string]string{"abc": "s3://kionga-models/1/abc/artifacts"},
		models: map[string]string{"m-123": "s3://kionga-models/1/models/m-123/artifacts"},
	}
	cases := []struct{ version, want, err string }{
		{"1", "s3://kionga-models/1/abc/artifacts/model", ""},
		{"2", "s3://kionga-models/1/abc/artifacts/model", ""},
		{"3", "s3://kionga-models/1/models/m-123/artifacts", ""},
		{"4", "s3://kionga-models/1/def/artifacts/model", ""},
		{"5", "", "KServe here reads s3:// only"},
		{"6", "", "registry reference MLflow did not resolve"},
		{"9", "", "MLflow could not locate churn version 9"},
	}
	for _, test := range cases {
		got, err := ResolveModelStorageURI(context.Background(), source, "s3://kionga-models/", "churn", test.version)
		if test.err != "" {
			if err == nil || !strings.Contains(err.Error(), test.err) {
				t.Errorf("version %s: error %v, want %q", test.version, err, test.err)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Errorf("version %s: got %q %v, want %q", test.version, got, err, test.want)
		}
	}
	if _, err := ResolveModelStorageURI(context.Background(), source, "", "churn", "4"); err == nil || !strings.Contains(err.Error(), "KIONGA_MLFLOW_ARTIFACTS_DESTINATION") {
		t.Fatalf("proxied artifacts without a destination must say how to fix it: %v", err)
	}
}

func modelDeployment(spec kiongav1.KiongaModelDeploymentSpec) *kiongav1.KiongaModelDeployment {
	return &kiongav1.KiongaModelDeployment{ObjectMeta: metav1.ObjectMeta{Name: "churn", Namespace: testNamespace, Generation: 1}, Spec: spec}
}

func storageSecret() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kionga-objectstore", Namespace: testNamespace}, Data: map[string][]byte{"AWS_ACCESS_KEY_ID": []byte("reader"), "AWS_SECRET_ACCESS_KEY": []byte("secret")}}
}

func modelReconciler(c client.Client, source ModelArtifactSource) *ModelDeploymentReconciler {
	return &ModelDeploymentReconciler{Client: c, MLflow: source, Capabilities: Capabilities{KServe: true}, Storage: ModelStorageConfig{
		CredentialsSecret: "kionga-objectstore", Endpoint: "rustfs.kionga-system.svc.cluster.local:9000",
	}}
}

// markInferenceServiceReady sets the status KServe reports once the
// predictor serves.
func markInferenceServiceReady(t *testing.T, c client.Client, name string) {
	t.Helper()
	service := unstructuredOf(InferenceServiceGVK)
	get(t, c, name, service)
	service.Object["status"] = map[string]any{
		"address":    map[string]any{"url": "http://" + name + "-predictor.team-a.svc.cluster.local"},
		"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
	}
	if err := c.Update(context.Background(), service); err != nil {
		t.Fatal(err)
	}
}

func TestModelDeploymentCreatesRawInferenceServiceFromMLflow(t *testing.T) {
	source := &fakeMLflow{versions: map[string]string{"churn/3": "s3://kionga-models/1/abc/artifacts/model"}}
	deployment := modelDeployment(kiongav1.KiongaModelDeploymentSpec{ModelName: "churn", Version: "3"})
	c := testClient(testScheme(t, InferenceServiceGVK), deployment, storageSecret())
	reconciler := modelReconciler(c, source)
	reconcileOnce(t, reconciler, "churn")

	service := unstructuredOf(InferenceServiceGVK)
	get(t, c, "churn", service)
	ownedBy(t, service, "churn")
	if service.GetAnnotations()[KServeDeploymentModeAnnotation] != "RawDeployment" {
		t.Fatalf("InferenceService must use raw mode: %v", service.GetAnnotations())
	}
	uri, _, _ := unstructured.NestedString(service.Object, "spec", "predictor", "model", "storageUri")
	format, _, _ := unstructured.NestedString(service.Object, "spec", "predictor", "model", "modelFormat", "name")
	account, _, _ := unstructured.NestedString(service.Object, "spec", "predictor", "serviceAccountName")
	if uri != "s3://kionga-models/1/abc/artifacts/model" || format != "mlflow" || account != "churn-storage" {
		t.Fatalf("predictor: %#v", service.Object["spec"])
	}
	var secret corev1.Secret
	get(t, c, "churn-storage", &secret)
	ownedBy(t, &secret, "churn")
	if secret.Annotations["serving.kserve.io/s3-endpoint"] != "rustfs.kionga-system.svc.cluster.local:9000" || secret.Annotations["serving.kserve.io/s3-usehttps"] != "0" || string(secret.Data["AWS_ACCESS_KEY_ID"]) != "reader" {
		t.Fatalf("storage secret: %#v %v", secret.Annotations, secret.Data)
	}
	var account2 corev1.ServiceAccount
	get(t, c, "churn-storage", &account2)
	ownedBy(t, &account2, "churn")
	if len(account2.Secrets) != 1 || account2.Secrets[0].Name != "churn-storage" {
		t.Fatalf("service account must reference the storage secret: %#v", account2.Secrets)
	}

	var stored kiongav1.KiongaModelDeployment
	get(t, c, "churn", &stored)
	if stored.Status.Phase != "Progressing" || stored.Status.StorageURI != uri {
		t.Fatalf("status before ready: %#v", stored.Status)
	}
	markInferenceServiceReady(t, c, "churn")
	reconcileOnce(t, reconciler, "churn")
	get(t, c, "churn", &stored)
	if stored.Status.Phase != "Ready" || stored.Status.URL != "http://churn-predictor.team-a.svc.cluster.local" || condition(stored.Status.Conditions, ConditionReady).Status != metav1.ConditionTrue {
		t.Fatalf("status after ready: %#v", stored.Status)
	}
	if source.calls != 1 {
		t.Fatalf("MLflow must be asked once per spec generation, asked %d times", source.calls)
	}
}

func TestModelDeploymentMLflowErrorSurfacesInStatus(t *testing.T) {
	source := &fakeMLflow{err: errors.New("integration returned 503: unavailable")}
	c := testClient(testScheme(t, InferenceServiceGVK), modelDeployment(kiongav1.KiongaModelDeploymentSpec{ModelName: "churn", Version: "3"}), storageSecret())
	result := reconcileOnce(t, modelReconciler(c, source), "churn")
	if result.RequeueAfter == 0 {
		t.Fatal("an MLflow outage must be retried")
	}
	absent(t, c, "churn", unstructuredOf(InferenceServiceGVK))
	var stored kiongav1.KiongaModelDeployment
	get(t, c, "churn", &stored)
	ready := condition(stored.Status.Conditions, ConditionReady)
	if stored.Status.Phase != "Failed" || ready.Reason != "ModelResolutionFailed" || !strings.Contains(ready.Message, "503") {
		t.Fatalf("status: %#v", stored.Status)
	}
}

func TestModelDeploymentCanaryPair(t *testing.T) {
	source := &fakeMLflow{versions: map[string]string{"churn/3": "s3://kionga-models/v3", "churn/4": "s3://kionga-models/v4"}}
	deployment := modelDeployment(kiongav1.KiongaModelDeploymentSpec{ModelName: "churn", Version: "3", Canary: &kiongav1.ModelCanarySpec{Version: "4", Weight: 20}})
	c := testClient(testScheme(t, InferenceServiceGVK), deployment, storageSecret())
	reconciler := modelReconciler(c, source)
	reconcileOnce(t, reconciler, "churn")

	canary := unstructuredOf(InferenceServiceGVK)
	get(t, c, "churn-canary", canary)
	ownedBy(t, canary, "churn")
	uri, _, _ := unstructured.NestedString(canary.Object, "spec", "predictor", "model", "storageUri")
	if uri != "s3://kionga-models/v4" || canary.GetLabels()["kionga.dev/track"] != "canary" {
		t.Fatalf("canary: %#v", canary.Object)
	}
	markInferenceServiceReady(t, c, "churn")
	reconcileOnce(t, reconciler, "churn")
	var stored kiongav1.KiongaModelDeployment
	get(t, c, "churn", &stored)
	if stored.Status.Phase != "Progressing" {
		t.Fatalf("not Ready until the canary is ready too: %#v", stored.Status)
	}
	markInferenceServiceReady(t, c, "churn-canary")
	reconcileOnce(t, reconciler, "churn")
	get(t, c, "churn", &stored)
	if stored.Status.Phase != "Ready" || stored.Status.CanaryWeight != 20 || stored.Status.CanaryURL != "http://churn-canary-predictor.team-a.svc.cluster.local" || stored.Status.CanaryStorageURI != "s3://kionga-models/v4" {
		t.Fatalf("canary status: %#v", stored.Status)
	}

	// Ending the canary deletes its InferenceService and clears the weight.
	stored.Spec.Canary = nil
	stored.Generation = 2
	if err := c.Update(context.Background(), &stored); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, reconciler, "churn")
	absent(t, c, "churn-canary", unstructuredOf(InferenceServiceGVK))
	get(t, c, "churn", &stored)
	if stored.Status.CanaryWeight != 0 || stored.Status.CanaryURL != "" {
		t.Fatalf("canary status not cleared: %#v", stored.Status)
	}
}

func TestModelDeploymentWithoutKServeDegrades(t *testing.T) {
	c := testClient(testScheme(t), modelDeployment(kiongav1.KiongaModelDeploymentSpec{ModelName: "churn", Version: "3"}))
	reconciler := modelReconciler(c, &fakeMLflow{})
	reconciler.Capabilities.KServe = false
	reconcileOnce(t, reconciler, "churn")
	var stored kiongav1.KiongaModelDeployment
	get(t, c, "churn", &stored)
	if stored.Status.Phase != "Failed" || condition(stored.Status.Conditions, ConditionReady).Reason != "KServeNotInstalled" {
		t.Fatalf("status: %#v", stored.Status)
	}
}

func TestModelDeploymentMissingStorageSecret(t *testing.T) {
	source := &fakeMLflow{versions: map[string]string{"churn/3": "s3://kionga-models/v3"}}
	c := testClient(testScheme(t, InferenceServiceGVK), modelDeployment(kiongav1.KiongaModelDeploymentSpec{ModelName: "churn", Version: "3"}))
	reconcileOnce(t, modelReconciler(c, source), "churn")
	absent(t, c, "churn", unstructuredOf(InferenceServiceGVK))
	var stored kiongav1.KiongaModelDeployment
	get(t, c, "churn", &stored)
	if condition(stored.Status.Conditions, ConditionReady).Reason != "StorageCredentialsMissing" {
		t.Fatalf("status: %#v", stored.Status)
	}
}

type recordingTransitioner struct{ stage string }

func (r *recordingTransitioner) TransitionStage(_ context.Context, _, _, stage string) error {
	r.stage = stage
	return nil
}

// Promotion only moves the MLflow stage; serving is KiongaModelDeployment.
func TestModelPromotionCreatesNoInferenceService(t *testing.T) {
	promotion := &kiongav1.KiongaModelPromotion{ObjectMeta: metav1.ObjectMeta{Name: "churn-3", Namespace: testNamespace}, Spec: kiongav1.KiongaModelPromotionSpec{ModelName: "churn", Version: "3", TargetStage: "production"}}
	c := testClient(testScheme(t, InferenceServiceGVK), promotion)
	mlflow := &recordingTransitioner{}
	reconcileOnce(t, &ModelPromotionReconciler{Client: c, MLflow: mlflow}, "churn-3")
	if mlflow.stage != "Production" {
		t.Fatalf("stage: %q", mlflow.stage)
	}
	var services unstructured.UnstructuredList
	services.SetGroupVersionKind(InferenceServiceGVK.GroupVersion().WithKind("InferenceServiceList"))
	if err := c.List(context.Background(), &services); err != nil || len(services.Items) != 0 {
		t.Fatalf("promotion created InferenceServices: %v %d", err, len(services.Items))
	}
	var stored kiongav1.KiongaModelPromotion
	get(t, c, "churn-3", &stored)
	if stored.Status.Phase != "Succeeded" {
		t.Fatalf("status: %#v", stored.Status)
	}
}

// A repeated MLflow failure must not rewrite status, so it cannot trigger an
// immediate reconcile and a second MLflow call; the 30 second retry governs.
func TestModelDeploymentRepeatedFailureDoesNotRewriteStatus(t *testing.T) {
	source := &fakeMLflow{err: errors.New("integration returned 503: unavailable")}
	c := testClient(testScheme(t, InferenceServiceGVK), modelDeployment(kiongav1.KiongaModelDeploymentSpec{ModelName: "churn", Version: "3"}), storageSecret())
	reconciler := modelReconciler(c, source)
	reconcileOnce(t, reconciler, "churn")
	var first kiongav1.KiongaModelDeployment
	get(t, c, "churn", &first)
	reconcileOnce(t, reconciler, "churn")
	var second kiongav1.KiongaModelDeployment
	get(t, c, "churn", &second)
	if second.ResourceVersion != first.ResourceVersion {
		t.Fatalf("identical failure rewrote status: rv %s -> %s", first.ResourceVersion, second.ResourceVersion)
	}
}

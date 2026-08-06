package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ml-ai-ops/platform/internal/integrations"
	mlaiopsv1 "github.com/ml-ai-ops/platform/pkg/kube/v1alpha1"
)

type PipelineReconciler struct {
	client.Client
	KFP          integrations.KFP
	ExperimentID string
}

func (r *PipelineReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var run mlaiopsv1.KiongaPipelineRun
	if err := r.Get(ctx, request.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if run.Status.WorkflowRef != "" || run.Status.Phase == "Succeeded" {
		return ctrl.Result{}, nil
	}
	now := metav1.Now()
	run.Status.Phase, run.Status.StartedAt = "Submitting", &now
	_ = r.Status().Update(ctx, &run)
	result, err := r.KFP.Submit(ctx, r.ExperimentID, run.Spec.PipelineRef, run.Name, run.Spec.Parameters)
	if err != nil {
		run.Status.Phase, run.Status.Message = "Failed", err.Error()
		_ = r.Status().Update(ctx, &run)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}
	reference := stringValue(result, "run_id", "runId", "id")
	run.Status.Phase, run.Status.WorkflowRef, run.Status.Message = "Running", reference, "Submitted to KFP"
	return ctrl.Result{}, r.Status().Update(ctx, &run)
}

func (r *PipelineReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&mlaiopsv1.KiongaPipelineRun{}).Complete(r)
}

type ModelPromotionReconciler struct {
	client.Client
	MLflow integrations.MLflow
}

func (r *ModelPromotionReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var promotion mlaiopsv1.KiongaModelPromotion
	if err := r.Get(ctx, request.NamespacedName, &promotion); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if promotion.Status.Phase == "Succeeded" {
		return ctrl.Result{}, nil
	}
	if promotion.Spec.TargetStage == "" {
		promotion.Status.Phase, promotion.Status.Message = "Failed", "targetStage is required"
		_ = r.Status().Update(ctx, &promotion)
		return ctrl.Result{}, fmt.Errorf("targetStage is required")
	}
	stage := strings.ToUpper(string(promotion.Spec.TargetStage[0])) + promotion.Spec.TargetStage[1:]
	if err := r.MLflow.TransitionStage(ctx, promotion.Spec.ModelName, promotion.Spec.Version, stage); err != nil {
		promotion.Status.Phase, promotion.Status.Message = "Failed", err.Error()
		_ = r.Status().Update(ctx, &promotion)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, err
	}
	name := dnsLabel(promotion.Spec.ModelName + "-" + promotion.Spec.Version)
	service := &unstructured.Unstructured{}
	service.SetGroupVersionKind(schema.GroupVersionKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"})
	service.SetName(name)
	service.SetNamespace(promotion.Namespace)
	service.Object["spec"] = map[string]any{"predictor": map[string]any{"model": map[string]any{
		"modelFormat": map[string]any{"name": "mlflow"}, "storageUri": fmt.Sprintf("models:/%s/%s", promotion.Spec.ModelName, promotion.Spec.Version),
	}}}
	if err := r.Create(ctx, service); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	promotion.Status.Phase, promotion.Status.Message, promotion.Status.InferenceServiceRef = "Succeeded", "MLflow stage transitioned and KServe resource created", name
	return ctrl.Result{}, r.Status().Update(ctx, &promotion)
}

func (r *ModelPromotionReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&mlaiopsv1.KiongaModelPromotion{}).Complete(r)
}

func stringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			return value
		}
	}
	return ""
}
func dnsLabel(value string) string {
	value = strings.ToLower(strings.NewReplacer(".", "-", "_", "-", "/", "-").Replace(value))
	return strings.Trim(value, "-")
}

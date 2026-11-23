package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

// StageTransitioner moves a registered model version to an MLflow stage.
type StageTransitioner interface {
	TransitionStage(ctx context.Context, name, version, stage string) error
}

// ModelPromotionReconciler applies a KiongaModelPromotion as an MLflow stage
// transition. Serving a version is KiongaModelDeployment's job.
type ModelPromotionReconciler struct {
	client.Client
	MLflow StageTransitioner
}

func (r *ModelPromotionReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var promotion kiongav1.KiongaModelPromotion
	if err := r.Get(ctx, request.NamespacedName, &promotion); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if promotion.Status.Phase == "Succeeded" {
		return ctrl.Result{}, nil
	}
	before := promotion.Status
	if promotion.Spec.TargetStage == "" {
		promotion.Status.Phase, promotion.Status.Message = "Failed", "targetStage is required"
		return ctrl.Result{}, updateStatusIfChanged(ctx, r.Client, &promotion, before, promotion.Status)
	}
	stage := strings.ToUpper(promotion.Spec.TargetStage[:1]) + promotion.Spec.TargetStage[1:]
	if err := r.MLflow.TransitionStage(ctx, promotion.Spec.ModelName, promotion.Spec.Version, stage); err != nil {
		promotion.Status.Phase, promotion.Status.Message = "Failed", err.Error()
		if updateErr := updateStatusIfChanged(ctx, r.Client, &promotion, before, promotion.Status); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	promotion.Status.Phase, promotion.Status.Message = "Succeeded", fmt.Sprintf("MLflow version %s of %s moved to %s", promotion.Spec.Version, promotion.Spec.ModelName, stage)
	return ctrl.Result{}, r.Status().Update(ctx, &promotion)
}

func (r *ModelPromotionReconciler) SetupWithManager(manager ctrl.Manager) error {
	// Status writes do not change the generation, so they do not re-trigger
	// a reconcile; a failed transition retries on its own schedule.
	return ctrl.NewControllerManagedBy(manager).For(&kiongav1.KiongaModelPromotion{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Complete(r)
}

func dnsLabel(value string) string {
	value = strings.ToLower(strings.NewReplacer(".", "-", "_", "-", "/", "-").Replace(value))
	return strings.Trim(value, "-")
}

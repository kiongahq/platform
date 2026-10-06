package operator

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mlaiopsv1 "github.com/ml-ai-ops/platform/pkg/kube/v1alpha1"
)

// WorkspaceReconciler turns an administrator's access grant into one bounded
// user workspace. Jupyter and the IDE share a PVC and split the granted pod
// resources; the pod is scaled to zero as soon as the grant is suspended.
type WorkspaceReconciler struct {
	client.Client
	WorkbenchImage string
	IDEImage       string
	GatewayURL     string
	FeatureURL     string
	StorageURL     string
	MLflowURL      string
	PrefectURL     string
	LangfuseURL    string
	KafkaRESTURL   string
	StorageClass   string
}

func (r *WorkspaceReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var workspace mlaiopsv1.KiongaWorkspace
	if err := r.Get(ctx, request.NamespacedName, &workspace); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if workspace.Spec.Subject == "" || workspace.Spec.Compute.VCPUs < 1 || workspace.Spec.Compute.MemoryGB < 1 || workspace.Spec.StorageGB < 1 {
		return r.fail(ctx, &workspace, "InvalidSpec", fmt.Errorf("subject, vcpus, memoryGB and storageGB must be positive"))
	}

	labels := map[string]string{
		"app.kubernetes.io/name":       workspace.Name,
		"app.kubernetes.io/component":  "workspace",
		"app.kubernetes.io/managed-by": "mlaiops-operator",
		"mlaiops.io/workspace":         workspace.Name,
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: workspace.Name, Namespace: workspace.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pvc, func() error {
		pvc.Labels = labels
		pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		requested := *resource.NewQuantity(int64(workspace.Spec.StorageGB)*1024*1024*1024, resource.BinarySI)
		if current := pvc.Spec.Resources.Requests.Storage(); current == nil || current.Cmp(requested) < 0 {
			pvc.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceStorage: requested}
		}
		if r.StorageClass != "" {
			pvc.Spec.StorageClassName = &r.StorageClass
		}
		return controllerutil.SetControllerReference(&workspace, pvc, r.Scheme())
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	authSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: workspace.Name + "-auth", Namespace: workspace.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, authSecret, func() error {
		authSecret.Labels = labels
		if len(authSecret.Data["token"]) == 0 {
			token := make([]byte, 24)
			if _, randomErr := rand.Read(token); randomErr != nil {
				return randomErr
			}
			authSecret.Data = map[string][]byte{"token": []byte(base64.RawURLEncoding.EncodeToString(token))}
		}
		return controllerutil.SetControllerReference(&workspace, authSecret, r.Scheme())
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	containers := r.containers(workspace, authSecret.Name)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: workspace.Name, Namespace: workspace.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		replicas := int32(1)
		if workspace.Spec.Disabled || len(containers) == 0 {
			replicas = 0
		}
		deployment.Labels = labels
		deployment.Spec.Replicas = &replicas
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"mlaiops.io/workspace": workspace.Name}}
		deployment.Spec.Template.ObjectMeta.Labels = labels
		deployment.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{FSGroup: ptrInt64(1000), RunAsNonRoot: ptrBool(true)}
		deployment.Spec.Template.Spec.Containers = containers
		deployment.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc.Name}}}}
		return controllerutil.SetControllerReference(&workspace, deployment, r.Scheme())
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: workspace.Name, Namespace: workspace.Namespace}}
	if len(containers) > 0 {
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
			service.Labels = labels
			service.Spec.Selector = deployment.Spec.Selector.MatchLabels
			service.Spec.Ports = workspacePorts(workspace.Spec.Services)
			return controllerutil.SetControllerReference(&workspace, service, r.Scheme())
		})
		if err != nil {
			return ctrl.Result{}, err
		}
	} else if err = r.Delete(ctx, service); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	workspace.Status.Phase = "Ready"
	if workspace.Spec.Disabled {
		workspace.Status.Phase = "Suspended"
	}
	workspace.Status.ReadyReplicas = deployment.Status.ReadyReplicas
	workspace.Status.ObservedGeneration = workspace.Generation
	workspace.Status.WorkbenchURL, workspace.Status.IDEURL = "", ""
	if slices.Contains(workspace.Spec.Services, "workbench") {
		workspace.Status.WorkbenchURL = fmt.Sprintf("http://%s.%s.svc:8888", service.Name, service.Namespace)
	}
	if slices.Contains(workspace.Spec.Services, "ide") {
		workspace.Status.IDEURL = fmt.Sprintf("http://%s.%s.svc:8080", service.Name, service.Namespace)
	}
	apimeta.SetStatusCondition(&workspace.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "WorkspaceReconciled", Message: "Persistent workspace and assigned services reconciled"})
	if err := r.Status().Update(ctx, &workspace); err != nil && !apierrors.IsConflict(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *WorkspaceReconciler) containers(workspace mlaiopsv1.KiongaWorkspace, authSecret string) []corev1.Container {
	services := make([]string, 0, 2)
	for _, service := range []string{"workbench", "ide"} {
		if slices.Contains(workspace.Spec.Services, service) {
			services = append(services, service)
		}
	}
	if len(services) == 0 {
		return nil
	}
	cpu := *resource.NewMilliQuantity(int64(workspace.Spec.Compute.VCPUs)*1000/int64(len(services)), resource.DecimalSI)
	memory := *resource.NewQuantity(int64(workspace.Spec.Compute.MemoryGB)*1024*1024*1024/int64(len(services)), resource.BinarySI)
	result := make([]corev1.Container, 0, len(services))
	for _, service := range services {
		image, port := r.WorkbenchImage, int32(8888)
		if service == "ide" {
			image, port = r.IDEImage, 8080
		}
		resources := corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory}, Limits: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory}}
		if service == "workbench" && workspace.Spec.Compute.GPUs > 0 {
			gpuType := workspace.Spec.Compute.GPUType
			if gpuType == "" {
				gpuType = "nvidia.com/gpu"
			}
			quantity := *resource.NewQuantity(int64(workspace.Spec.Compute.GPUs), resource.DecimalSI)
			resources.Requests[corev1.ResourceName(gpuType)] = quantity
			resources.Limits[corev1.ResourceName(gpuType)] = quantity
		}
		environment := []corev1.EnvVar{
			{Name: "MLAIOPS_URL", Value: r.GatewayURL}, {Name: "MLAIOPS_FEATURE_GATEWAY_URL", Value: r.FeatureURL}, {Name: "MLAIOPS_STORAGE_PROXY_URL", Value: r.StorageURL},
			{Name: "MLFLOW_TRACKING_URI", Value: r.MLflowURL}, {Name: "PREFECT_API_URL", Value: r.PrefectURL}, {Name: "LANGFUSE_HOST", Value: r.LangfuseURL}, {Name: "KAFKA_REST_URL", Value: r.KafkaRESTURL},
			{Name: "KIONGA_SUBJECT", Value: workspace.Spec.Subject}, {Name: "KIONGA_WORKSPACE", Value: "/workspace"},
		}
		credentialName := "PASSWORD"
		if service == "workbench" {
			credentialName = "JUPYTER_TOKEN"
			environment = append(environment, corev1.EnvVar{Name: "S3_MOUNT_OPTIONAL", Value: "true"})
		}
		environment = append(environment, corev1.EnvVar{Name: credentialName, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: authSecret}, Key: "token"}}})
		result = append(result, corev1.Container{
			Name: service, Image: image, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: port}},
			Env:       environment,
			Resources: resources, VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}},
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptrBool(false), RunAsNonRoot: ptrBool(true), RunAsUser: ptrInt64(1000), RunAsGroup: ptrInt64(1000)},
		})
	}
	return result
}

func workspacePorts(services []string) []corev1.ServicePort {
	result := make([]corev1.ServicePort, 0, 2)
	if slices.Contains(services, "workbench") {
		result = append(result, corev1.ServicePort{Name: "workbench", Port: 8888, TargetPort: intstr.FromInt(8888)})
	}
	if slices.Contains(services, "ide") {
		result = append(result, corev1.ServicePort{Name: "ide", Port: 8080, TargetPort: intstr.FromInt(8080)})
	}
	return result
}

func (r *WorkspaceReconciler) fail(ctx context.Context, workspace *mlaiopsv1.KiongaWorkspace, reason string, reconcileErr error) (ctrl.Result, error) {
	workspace.Status.Phase = "Failed"
	apimeta.SetStatusCondition(&workspace.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: reconcileErr.Error()})
	_ = r.Status().Update(ctx, workspace)
	return ctrl.Result{}, reconcileErr
}

func (r *WorkspaceReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&mlaiopsv1.KiongaWorkspace{}).Owns(&appsv1.Deployment{}).Owns(&corev1.Service{}).Owns(&corev1.PersistentVolumeClaim{}).Owns(&corev1.Secret{}).Complete(r)
}

func ptrBool(value bool) *bool    { return &value }
func ptrInt64(value int64) *int64 { return &value }

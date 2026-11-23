package operator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

// RouteServiceName is the ExternalName Service that sends a workload's
// traffic through the KEDA HTTP interceptor. The interceptor matches the
// request Host, <route>.<namespace>.svc.cluster.local, against the
// HTTPScaledObject hosts, counts the request and wakes a scaled-to-zero
// Deployment before forwarding.
func RouteServiceName(workload string) string {
	const suffix = "-route"
	if len(workload)+len(suffix) <= 63 {
		return workload + suffix
	}
	// Agent names already use the full 63 characters; keep the route name a
	// valid label and unique by hashing the full name.
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(workload)))[:10]
	stem := strings.TrimRight(workload[:63-len(suffix)-len(digest)-1], "-")
	return stem + "-" + digest + suffix
}

// SSOServiceName is the ExternalName Service that points a visibility=sso
// Ingress at the gateway.
func SSOServiceName(workload string) string { return workload + "-sso" }

func serviceFQDN(name, namespace string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", name, namespace)
}

// routeHosts are the Host header values the interceptor accepts for a
// workload: every in-cluster spelling of the route Service plus any public
// hosts.
func routeHosts(workload, namespace string, public ...string) []any {
	route := RouteServiceName(workload)
	hosts := []any{serviceFQDN(route, namespace), route + "." + namespace + ".svc", route + "." + namespace}
	for _, host := range public {
		if host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// scalingTarget names what an autoscaler drives and where its traffic lands.
// applyScaling returns the in-cluster URL to call and, for requests scaling,
// the Host header the interceptor routes on.
type scalingTarget struct {
	Deployment  string
	Service     string
	ServicePort int32
	Labels      map[string]string
}

// applyScaling creates the autoscaler the plan calls for and deletes the
// other kinds the owner left behind. It returns the in-cluster URL callers
// use: through the interceptor for requests scaling, the Service otherwise.
func applyScaling(ctx context.Context, c client.Client, owner client.Object, plan ScalingPlan, target scalingTarget, config WorkloadConfig, publicHosts ...string) (string, string, error) {
	namespace := owner.GetNamespace()
	hpaKey := client.ObjectKey{Name: target.Deployment, Namespace: namespace}
	routeKey := client.ObjectKey{Name: RouteServiceName(target.Deployment), Namespace: namespace}
	direct := fmt.Sprintf("http://%s", serviceFQDN(target.Service, namespace))
	if target.ServicePort != 80 {
		direct = fmt.Sprintf("%s:%d", direct, target.ServicePort)
	}
	if plan.Metric == kiongav1.ScalingMetricRequests {
		if err := deleteIfOwned(ctx, c, &autoscalingv2.HorizontalPodAutoscaler{}, hpaKey, owner); err != nil {
			return "", "", err
		}
		route := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: routeKey.Name, Namespace: namespace}}
		if _, err := controllerutil.CreateOrUpdate(ctx, c, route, func() error {
			route.Labels = target.Labels
			route.Spec.Type = corev1.ServiceTypeExternalName
			route.Spec.ExternalName = config.InterceptorHost
			route.Spec.Selector = nil
			route.Spec.ClusterIP = ""
			route.Spec.Ports = []corev1.ServicePort{{Name: "http", Port: config.InterceptorPort, TargetPort: intstr.FromInt32(config.InterceptorPort)}}
			return controllerutil.SetControllerReference(owner, route, c.Scheme())
		}); err != nil {
			return "", "", err
		}
		scaled := &unstructured.Unstructured{}
		scaled.SetGroupVersionKind(HTTPScaledObjectGVK)
		scaled.SetName(target.Deployment)
		scaled.SetNamespace(namespace)
		if _, err := controllerutil.CreateOrUpdate(ctx, c, scaled, func() error {
			scaled.SetLabels(target.Labels)
			scaled.Object["spec"] = map[string]any{
				// The Service FQDN is accepted too: callers that have not read
				// status.host send it.
				"hosts": routeHosts(target.Deployment, namespace, append([]string{serviceFQDN(target.Service, namespace)}, publicHosts...)...),
				"scaleTargetRef": map[string]any{
					"apiVersion": "apps/v1", "kind": "Deployment", "name": target.Deployment,
					"service": target.Service, "port": int64(target.ServicePort),
				},
				"replicas":        map[string]any{"min": int64(plan.Min), "max": int64(plan.Max)},
				"scalingMetric":   map[string]any{"concurrency": map[string]any{"targetValue": int64(plan.Target)}},
				"scaledownPeriod": int64(300),
			}
			return controllerutil.SetControllerReference(owner, scaled, c.Scheme())
		}); err != nil {
			return "", "", err
		}
		return fmt.Sprintf("http://%s:%d", serviceFQDN(routeKey.Name, namespace), config.InterceptorPort), serviceFQDN(routeKey.Name, namespace), nil
	}
	if err := deleteHTTPScaledObject(ctx, c, target.Deployment, owner); err != nil {
		return "", "", err
	}
	if err := deleteIfOwned(ctx, c, &corev1.Service{}, routeKey, owner); err != nil {
		return "", "", err
	}
	if plan.Max <= plan.Min {
		return direct, "", deleteIfOwned(ctx, c, &autoscalingv2.HorizontalPodAutoscaler{}, hpaKey, owner)
	}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: hpaKey.Name, Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, c, hpa, func() error {
		minimum, utilization := plan.Min, plan.Target
		hpa.Labels = target.Labels
		hpa.Spec = autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: target.Deployment},
			MinReplicas:    &minimum, MaxReplicas: plan.Max,
			Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{
				Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &utilization},
			}}},
		}
		return controllerutil.SetControllerReference(owner, hpa, c.Scheme())
	})
	return direct, "", err
}

// deleteHTTPScaledObject removes an owned HTTPScaledObject. On a cluster
// without the KEDA HTTP add-on there is nothing to delete.
func deleteHTTPScaledObject(ctx context.Context, c client.Client, name string, owner client.Object) error {
	scaled := &unstructured.Unstructured{}
	scaled.SetGroupVersionKind(HTTPScaledObjectGVK)
	err := deleteIfOwned(ctx, c, scaled, client.ObjectKey{Name: name, Namespace: owner.GetNamespace()}, owner)
	if isNoMatch(err) {
		return nil
	}
	return err
}

// deleteIfOwned deletes the object at key when owner controls it. Objects
// someone else created are left alone.
func deleteIfOwned(ctx context.Context, c client.Client, object client.Object, key client.ObjectKey, owner client.Object) error {
	if err := c.Get(ctx, key, object); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(object, owner) {
		return nil
	}
	if err := c.Delete(ctx, object); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// isNoMatch reports a kind the API server does not serve.
func isNoMatch(err error) bool {
	return err != nil && (apimeta.IsNoMatchError(err) || discovery.IsGroupDiscoveryFailedError(err))
}

// applyPodDisruptionBudget keeps one replica up through voluntary
// disruptions when the workload never runs fewer than two.
func applyPodDisruptionBudget(ctx context.Context, c client.Client, owner client.Object, name string, selector map[string]string, labels map[string]string, minReplicas int32) error {
	key := client.ObjectKey{Name: name, Namespace: owner.GetNamespace()}
	if minReplicas < 2 {
		return deleteIfOwned(ctx, c, &policyv1.PodDisruptionBudget{}, key, owner)
	}
	budget := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: key.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, c, budget, func() error {
		unavailable := intstr.FromInt32(1)
		budget.Labels = labels
		budget.Spec.MaxUnavailable = &unavailable
		budget.Spec.MinAvailable = nil
		budget.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
		return controllerutil.SetControllerReference(owner, budget, c.Scheme())
	})
	return err
}

// ingressBackend is where a workload Ingress sends traffic.
type ingressBackend struct {
	Service string
	Port    int32
}

// applyIngress serves host with TLS from the configured wildcard Secret, or
// deletes the owned Ingress when host is empty.
func applyIngress(ctx context.Context, c client.Client, owner client.Object, name, host string, backend ingressBackend, labels, annotations map[string]string, config WorkloadConfig) error {
	key := client.ObjectKey{Name: name, Namespace: owner.GetNamespace()}
	if host == "" {
		return deleteIfOwned(ctx, c, &networkingv1.Ingress{}, key, owner)
	}
	ingress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: key.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, c, ingress, func() error {
		pathType := networkingv1.PathTypePrefix
		ingress.Labels = labels
		ingress.Annotations = annotations
		ingress.Spec.IngressClassName = nil
		if config.IngressClass != "" {
			class := config.IngressClass
			ingress.Spec.IngressClassName = &class
		}
		ingress.Spec.TLS = nil
		if config.TLSSecret != "" {
			ingress.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{host}, SecretName: config.TLSSecret}}
		}
		ingress.Spec.Rules = []networkingv1.IngressRule{{Host: host, IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
			Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &pathType, Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
				Name: backend.Service, Port: networkingv1.ServiceBackendPort{Number: backend.Port},
			}}}},
		}}}}
		return controllerutil.SetControllerReference(owner, ingress, c.Scheme())
	})
	return err
}

// applyExternalNameService points name at host:port, or deletes it.
func applyExternalNameService(ctx context.Context, c client.Client, owner client.Object, name, host string, port int32, labels map[string]string, keep bool) error {
	key := client.ObjectKey{Name: name, Namespace: owner.GetNamespace()}
	if !keep {
		return deleteIfOwned(ctx, c, &corev1.Service{}, key, owner)
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: key.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, c, service, func() error {
		service.Labels = labels
		service.Spec.Type = corev1.ServiceTypeExternalName
		service.Spec.ExternalName = host
		service.Spec.Selector = nil
		service.Spec.ClusterIP = ""
		service.Spec.Ports = []corev1.ServicePort{{Name: "http", Port: port, TargetPort: intstr.FromInt32(port)}}
		return controllerutil.SetControllerReference(owner, service, c.Scheme())
	})
	return err
}

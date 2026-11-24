package operator

import (
	"context"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// refusedPodStart looks for a pod of the current pod template that the
// kubelet refuses to start because of the image's user, and returns the Ready
// condition that says so. Pods run with runAsNonRoot, so the kubelet refuses
// an image that runs as root, and one whose user is a name, since it cannot
// tell whether that name is root. It returns nil when no pod is refused.
func (r *WorkloadReconciler) refusedPodStart(ctx context.Context, namespace string, selector map[string]string, template *corev1.PodTemplateSpec) (*metav1.Condition, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels(selector)); err != nil {
		return nil, err
	}
	// By name, so the same pod is reported on every reconcile.
	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !pod.DeletionTimestamp.IsZero() || !fromTemplate(pod, template) {
			continue
		}
		for _, container := range pod.Status.ContainerStatuses {
			if container.State.Waiting == nil {
				continue
			}
			if reason, message := nonRootRefusal(containerImage(pod, container.Name), container.State.Waiting.Message); reason != "" {
				return &metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: reason, Message: message}, nil
			}
		}
	}
	return nil, nil
}

// containerImage is the image the pod spec gives a container. A container
// that never started has no reliable image in its status.
func containerImage(pod *corev1.Pod, name string) string {
	for _, container := range pod.Spec.Containers {
		if container.Name == name {
			return container.Image
		}
	}
	return ""
}

// fromTemplate reports whether a pod was made from the current pod template
// as far as the user check goes: same images, same user and same template
// annotations (a redeploy changes one). During a rollout that fixes the
// problem the pods of the previous template are still refused until the new
// ones are ready; they say nothing about the current spec.
func fromTemplate(pod *corev1.Pod, template *corev1.PodTemplateSpec) bool {
	for key, value := range template.Annotations {
		if pod.Annotations[key] != value {
			return false
		}
	}
	var podUser, templateUser *int64
	if pod.Spec.SecurityContext != nil {
		podUser = pod.Spec.SecurityContext.RunAsUser
	}
	if template.Spec.SecurityContext != nil {
		templateUser = template.Spec.SecurityContext.RunAsUser
	}
	if !equality.Semantic.DeepEqual(podUser, templateUser) || len(pod.Spec.Containers) != len(template.Spec.Containers) {
		return false
	}
	for i := range template.Spec.Containers {
		if pod.Spec.Containers[i].Image != template.Spec.Containers[i].Image {
			return false
		}
	}
	return true
}

// nonRootRefusal recognises the kubelet's two runAsNonRoot refusals in a
// container's waiting message (reason CreateContainerConfigError):
//
//	container has runAsNonRoot and image will run as root
//	container has runAsNonRoot and image has non-numeric user (app), cannot verify user is non-root
//
// and returns the condition reason and a message with the fix. An unrelated
// message returns "".
func nonRootRefusal(image, waiting string) (reason, message string) {
	if !strings.Contains(waiting, "runAsNonRoot") {
		return "", ""
	}
	subject := "the image"
	if image != "" {
		subject = "image " + image
	}
	const fix = "Set a numeric non-root user (runAsUser, for example 1000) or rebuild the image with a numeric non-root USER."
	if strings.Contains(waiting, "non-numeric user") {
		return ReasonImageUserNotNumeric, "The pods cannot start: " + subject + " names its user instead of numbering it, so Kubernetes cannot verify it is not root. " + fix
	}
	return ReasonImageRunsAsRoot, "The pods cannot start: " + subject + " runs as root, which is not allowed. " + fix
}

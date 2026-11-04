package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

const (
	containerName = "sandbox"
	podStartLimit = 3 * time.Minute
)

// PodExecer runs a command in a pod. It is an interface because the fake clientset
// has no exec, so the real SPDY and WebSocket path is only exercised on a live cluster.
type PodExecer interface {
	Exec(ctx context.Context, namespace, pod string, request ExecRequest) (int, error)
}

// Kubernetes runs sandboxes as Pods. The chart owns the default-deny NetworkPolicy and
// grants only get on it, so this driver verifies the policy and never writes one. It
// also never reads Secrets: a git credential reaches a helper over exec stdin.
type Kubernetes struct {
	Client        kubernetes.Interface
	Namespace     string
	StorageClass  string
	PolicyName    string
	Execer        PodExecer
	PollInterval  time.Duration
	StartDeadline time.Duration
}

var _ Driver = (*Kubernetes)(nil)

// Name identifies the driver in /healthz.
func (k *Kubernetes) Name() string { return "kubernetes" }

// RuntimeRegistered is a get on the named RuntimeClass, cluster-scoped.
func (k *Kubernetes) RuntimeRegistered(ctx context.Context, runtime string) (bool, error) {
	_, err := k.Client.NodeV1().RuntimeClasses().Get(ctx, runtime, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// verifyPolicy fails closed unless the policy exists. It is accepted by name only: the
// chart owns its contents, with an empty podSelector that is broader than any selector
// here, and a chart test keeps the name in step with KIONGA_SANDBOX_NAME_PREFIX.
func (k *Kubernetes) verifyPolicy(ctx context.Context) error {
	if _, err := k.Client.NetworkingV1().NetworkPolicies(k.Namespace).Get(ctx, k.PolicyName, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("default-deny NetworkPolicy %s: %w", k.PolicyName, err)
	}
	return nil
}

func quantity(value int64) *resource.Quantity { return resource.NewQuantity(value, resource.BinarySI) }

// podFor is the Kubernetes statement of the same isolation as createBody. Service
// links are off because they inject environment describing the cluster. Pods have no
// per-pod pid limit field: that limit is the kubelet's podPidsLimit, set by the operator.
func podFor(spec ContainerSpec, namespace string) *corev1.Pod {
	yes, no := true, false
	uid, gid := int64(UID), int64(GID)
	grace := int64(0)
	var mounts []corev1.VolumeMount
	var volumes []corev1.Volume
	for index, mount := range spec.Tmpfs {
		name := fmt.Sprintf("tmpfs-%d", index)
		volumes = append(volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: quantity(mount.SizeBytes)}}})
		mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: mount.Path})
	}
	if spec.Volume != nil {
		volumes = append(volumes, corev1.Volume{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: spec.Volume.Name}}})
		mounts = append(mounts, corev1.VolumeMount{Name: "workspace", MountPath: spec.Volume.Path})
	}
	var aliases []corev1.HostAlias
	for _, entry := range spec.ExtraHosts {
		aliases = append(aliases, corev1.HostAlias{IP: entry.Address, Hostnames: []string{entry.Host}})
	}
	runtime := spec.Runtime
	limits := corev1.ResourceList{
		corev1.ResourceMemory: *quantity(spec.MemoryBytes),
		corev1.ResourceCPU:    *resource.NewMilliQuantity(spec.NanoCPUs/1_000_000, resource.DecimalSI),
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: spec.Name, Namespace: namespace, Labels: spec.Labels},
		Spec: corev1.PodSpec{
			RuntimeClassName:              &runtime,
			RestartPolicy:                 corev1.RestartPolicyNever,
			AutomountServiceAccountToken:  &no,
			EnableServiceLinks:            &no,
			HostNetwork:                   false,
			HostPID:                       false,
			HostIPC:                       false,
			ShareProcessNamespace:         &no,
			TerminationGracePeriodSeconds: &grace,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &yes,
				RunAsUser:      &uid,
				RunAsGroup:     &gid,
				FSGroup:        &gid,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:         containerName,
				Image:        spec.Image,
				Command:      spec.Command,
				WorkingDir:   "/",
				Env:          nil,
				VolumeMounts: mounts,
				Resources:    corev1.ResourceRequirements{Limits: limits, Requests: limits},
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             &yes,
					ReadOnlyRootFilesystem:   &yes,
					AllowPrivilegeEscalation: &no,
					Privileged:               &no,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
			Volumes:     volumes,
			HostAliases: aliases,
		},
	}
}

func pvcFor(spec ContainerSpec, namespace, storageClass string) *corev1.PersistentVolumeClaim {
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: spec.Volume.Name, Namespace: namespace, Labels: spec.Labels},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: *quantity(spec.Volume.SizeBytes)}},
		},
	}
	if storageClass != "" {
		claim.Spec.StorageClassName = &storageClass
	}
	return claim
}

// EnsureNetwork is the chart's job here: egress follows the git-helper label.
func (k *Kubernetes) EnsureNetwork(context.Context, string, map[string]string) error { return nil }

// Create verifies the network policy first, so no pod ever exists without it. Then it
// waits for the pod to run: a pod that never starts under the RuntimeClass is a failure.
func (k *Kubernetes) Create(ctx context.Context, spec ContainerSpec) error {
	if err := k.verifyPolicy(ctx); err != nil {
		return err
	}
	if spec.Volume != nil {
		claim := pvcFor(spec, k.Namespace, k.StorageClass)
		if _, err := k.Client.CoreV1().PersistentVolumeClaims(k.Namespace).Create(ctx, claim, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	_, err := k.Client.CoreV1().Pods(k.Namespace).Create(ctx, podFor(spec, k.Namespace), metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	return k.waitRunning(ctx, spec.Name)
}

func (k *Kubernetes) waitRunning(ctx context.Context, name string) error {
	limit := k.StartDeadline
	if limit == 0 {
		limit = podStartLimit
	}
	interval := k.PollInterval
	if interval == 0 {
		interval = time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	for {
		pod, err := k.Client.CoreV1().Pods(k.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			switch pod.Status.Phase {
			case corev1.PodRunning:
				return nil
			case corev1.PodFailed, corev1.PodSucceeded:
				return fmt.Errorf("pod %s ended in phase %s", name, pod.Status.Phase)
			}
		} else if !apierrors.IsNotFound(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pod %s did not start: %w", name, ctx.Err())
		case <-time.After(interval):
		}
	}
}

// Inspect reports the pod.
func (k *Kubernetes) Inspect(ctx context.Context, name string) (Instance, error) {
	pod, err := k.Client.CoreV1().Pods(k.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Instance{}, ErrNotFound
	}
	if err != nil {
		return Instance{}, err
	}
	return instanceOf(pod), nil
}

func instanceOf(pod *corev1.Pod) Instance {
	runtime := ""
	if pod.Spec.RuntimeClassName != nil {
		runtime = *pod.Spec.RuntimeClassName
	}
	return Instance{Name: pod.Name, Labels: pod.Labels, Running: pod.Status.Phase == corev1.PodRunning, Runtime: runtime}
}

// List finds pods carrying all the labels.
func (k *Kubernetes) List(ctx context.Context, wanted map[string]string) ([]Instance, error) {
	pods, err := k.Client.CoreV1().Pods(k.Namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.Set(wanted).String()})
	if err != nil {
		return nil, err
	}
	instances := make([]Instance, 0, len(pods.Items))
	for index := range pods.Items {
		instances = append(instances, instanceOf(&pods.Items[index]))
	}
	return instances, nil
}

// ListVolumes finds claims carrying all the labels.
func (k *Kubernetes) ListVolumes(ctx context.Context, wanted map[string]string) ([]VolumeInfo, error) {
	claims, err := k.Client.CoreV1().PersistentVolumeClaims(k.Namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.Set(wanted).String()})
	if err != nil {
		return nil, err
	}
	volumes := make([]VolumeInfo, 0, len(claims.Items))
	for _, claim := range claims.Items {
		volumes = append(volumes, VolumeInfo{Name: claim.Name, Labels: claim.Labels})
	}
	return volumes, nil
}

// Exec runs in the pod.
func (k *Kubernetes) Exec(ctx context.Context, name string, request ExecRequest) (int, error) {
	if k.Execer == nil {
		return 0, errors.New("the kubernetes driver has no exec client")
	}
	return k.Execer.Exec(ctx, k.Namespace, name, request)
}

// Restart replaces the pod from its own spec, since a Pod cannot be restarted in place.
// The claim survives; the memory-backed /tmp does not, as with a container restart.
func (k *Kubernetes) Restart(ctx context.Context, name string) error {
	pod, err := k.Client.CoreV1().Pods(k.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	grace := int64(0)
	if err := k.Client.CoreV1().Pods(k.Namespace).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &grace}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	replacement := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace, Labels: pod.Labels}, Spec: pod.Spec}
	replacement.Spec.NodeName = ""
	limit := time.Now().Add(time.Minute)
	for {
		_, err := k.Client.CoreV1().Pods(k.Namespace).Create(ctx, replacement, metav1.CreateOptions{})
		if err == nil {
			return k.waitRunning(ctx, name)
		}
		if !apierrors.IsAlreadyExists(err) || time.Now().After(limit) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Stop deletes the pod, which is the only way to stop one. The claim stays. Disk limits
// never call this: a claim is bounded by its storage class.
func (k *Kubernetes) Stop(ctx context.Context, name string) error { return k.Remove(ctx, name) }

// Remove deletes the pod at once.
func (k *Kubernetes) Remove(ctx context.Context, name string) error {
	grace := int64(0)
	err := k.Client.CoreV1().Pods(k.Namespace).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &grace})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// RemoveVolume deletes the claim.
func (k *Kubernetes) RemoveVolume(ctx context.Context, name string) error {
	err := k.Client.CoreV1().PersistentVolumeClaims(k.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// RemoteExecer is the real exec path: WebSocket, falling back to SPDY.
type RemoteExecer struct {
	Client kubernetes.Interface
	Config *rest.Config
}

// Exec streams a command. The kubelet reports the exit status as an error type.
func (r *RemoteExecer) Exec(ctx context.Context, namespace, pod string, request ExecRequest) (int, error) {
	call := r.Client.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   request.Argv,
			Stdin:     request.Stdin != nil,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)
	websocket, err := remotecommand.NewWebSocketExecutor(r.Config, "GET", call.URL().String())
	if err != nil {
		return 0, err
	}
	spdy, err := remotecommand.NewSPDYExecutor(r.Config, "POST", call.URL())
	if err != nil {
		return 0, err
	}
	executor, err := fallbackExecutor(websocket, spdy)
	if err != nil {
		return 0, err
	}
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: request.Stdin, Stdout: request.Stdout, Stderr: request.Stderr})
	if err == nil {
		return 0, nil
	}
	var exit utilexec.CodeExitError
	if errors.As(err, &exit) {
		return exit.Code, nil
	}
	return 0, err
}

func fallbackExecutor(primary, secondary remotecommand.Executor) (remotecommand.Executor, error) {
	return remotecommand.NewFallbackExecutor(primary, secondary, func(err error) bool {
		return err != nil && (httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err))
	})
}

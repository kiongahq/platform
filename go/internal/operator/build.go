package operator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kiongav1 "github.com/kiongahq/platform/pkg/kube/v1alpha1"
)

const (
	buildContainer = "buildkit"
	// buildScript runs a daemonless rootless BuildKit build and writes only the
	// pushed image digest to the termination message, where the operator
	// reads it. The build arguments arrive as "$@", never through the shell.
	buildScript = `set -eu
buildctl-daemonless.sh build "$@" --metadata-file /tmp/kionga-build/metadata.json
digest=$(tr -d ' \n\t' < /tmp/kionga-build/metadata.json | sed -n 's/.*"containerimage.digest":"\(sha256:[0-9a-f]\{64\}\)".*/\1/p')
test -n "$digest"
printf '%s' "$digest" > /dev/termination-log
`
)

// digestGrace is how long a completed build may go without a readable
// digest before the operator calls it failed.
const digestGrace = 2 * time.Minute

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// BuildJobName is the Job that builds one source revision of a workload.
func BuildJobName(workload, sourceHash string) string { return workload + "-build-" + sourceHash }

// SourceHash identifies a build request: the source fields plus the
// redeploy annotation, so a redeploy of a moving ref builds again.
func SourceHash(source *kiongav1.SourceSpec, redeploy string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{source.Git, source.Ref, source.ContextDir, source.Dockerfile, redeploy}, "\x00")))
	return fmt.Sprintf("%x", sum)[:10]
}

// BuildContext is the BuildKit Git context: <url>#<ref>:<dir>.
func BuildContext(source *kiongav1.SourceSpec) string {
	if source.Ref == "" && source.ContextDir == "" {
		return source.Git
	}
	value := source.Git + "#" + source.Ref
	if source.ContextDir != "" {
		value += ":" + strings.Trim(source.ContextDir, "/")
	}
	return value
}

// BuildRepository is the image repository builds of a workload push to.
func BuildRepository(registry, namespace, workload string) string {
	return registry + "/" + namespace + "-" + workload
}

// resolveImage returns the image the Deployment should run. For a prebuilt
// image that is spec.image. For a source workload it drives the build Job
// and returns the last successfully built digest, or "" when no build has
// succeeded yet.
func (r *WorkloadReconciler) resolveImage(ctx context.Context, object client.Object, spec *kiongav1.WorkloadSpec, status *kiongav1.WorkloadStatus) (string, error) {
	if spec.Source == nil {
		apimeta.RemoveStatusCondition(&status.Conditions, ConditionBuild)
		return spec.Image, nil
	}
	if r.Config.Build.Registry == "" {
		return "", invalid("BuildNotConfigured", "source builds need KIONGA_BUILD_REGISTRY on the operator")
	}
	hash := SourceHash(spec.Source, object.GetAnnotations()[RedeployAnnotation])
	current := status.Image
	if build := status.LastBuild; build != nil && build.SourceHash == hash && build.Phase == "Succeeded" && build.Image != "" {
		return build.Image, nil
	}
	if build := status.LastBuild; build != nil && build.SourceHash == hash && build.Phase == "Failed" {
		// A failed build of this exact source is final until the source or
		// the redeploy annotation changes. Keep serving the previous image.
		return current, nil
	}
	name := BuildJobName(object.GetName(), hash)
	var job batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: object.GetNamespace()}, &job)
	if apierrors.IsNotFound(err) {
		created := r.buildJob(object, spec.Source, name, hash)
		if err := controllerutil.SetControllerReference(object, created, r.Scheme()); err != nil {
			return "", err
		}
		if err := r.Create(ctx, created); err != nil && !apierrors.IsAlreadyExists(err) {
			return "", err
		}
		started := r.now()
		status.LastBuild = &kiongav1.BuildStatus{Name: name, SourceHash: hash, Phase: "Running", StartedAt: &started, Message: "build started"}
		r.setBuildCondition(object, status, metav1.ConditionUnknown, "Building", "building "+BuildContext(spec.Source))
		return current, nil
	}
	if err != nil {
		return "", err
	}
	build := &kiongav1.BuildStatus{Name: name, SourceHash: hash, Phase: "Running", StartedAt: job.Status.StartTime, Message: "build running"}
	if status.LastBuild != nil && status.LastBuild.Name == name && build.StartedAt == nil {
		build.StartedAt = status.LastBuild.StartedAt
	}
	status.LastBuild = build
	switch {
	case jobFinished(&job, batchv1.JobComplete):
		build.CompletedAt = job.Status.CompletionTime
		digest, err := r.buildDigest(ctx, &job)
		if err != nil {
			return "", err
		}
		if digest == "" && job.Status.CompletionTime != nil && r.now().Sub(job.Status.CompletionTime.Time) < digestGrace {
			// The pod cache can trail the Job's Complete condition.
			build.Message = "build complete; reading the image digest"
			return current, nil
		}
		if digest == "" {
			build.Phase, build.Message = "Failed", "the build finished without reporting an image digest"
			r.setBuildCondition(object, status, metav1.ConditionFalse, "NoDigest", build.Message)
			return current, nil
		}
		build.Phase, build.Message = "Succeeded", "pushed "+digest
		build.Image = BuildRepository(r.Config.Build.Registry, object.GetNamespace(), object.GetName()) + "@" + digest
		r.setBuildCondition(object, status, metav1.ConditionTrue, "Built", "built "+build.Image)
		return build.Image, nil
	case jobFinished(&job, batchv1.JobFailed):
		now := r.now()
		build.CompletedAt = &now
		build.Phase, build.Message = "Failed", "the build failed; see the logs of Job "+name
		for _, condition := range job.Status.Conditions {
			if condition.Type == batchv1.JobFailed && condition.Message != "" {
				build.Message = "the build failed: " + condition.Message
			}
		}
		r.setBuildCondition(object, status, metav1.ConditionFalse, "BuildFailed", build.Message)
		return current, nil
	default:
		r.setBuildCondition(object, status, metav1.ConditionUnknown, "Building", "building "+BuildContext(spec.Source))
		return current, nil
	}
}

func (r *WorkloadReconciler) setBuildCondition(object client.Object, status *kiongav1.WorkloadStatus, value metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: ConditionBuild, Status: value, Reason: reason, Message: message, ObservedGeneration: object.GetGeneration()})
}

func jobFinished(job *batchv1.Job, kind batchv1.JobConditionType) bool {
	for _, condition := range job.Status.Conditions {
		if condition.Type == kind && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// buildDigest reads the digest the build script wrote to its termination
// message.
func (r *WorkloadReconciler) buildDigest(ctx context.Context, job *batchv1.Job) (string, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(job.Namespace), client.MatchingLabels{"job-name": job.Name}); err != nil {
		return "", err
	}
	for _, pod := range pods.Items {
		for _, container := range pod.Status.ContainerStatuses {
			terminated := container.State.Terminated
			if container.Name != buildContainer || terminated == nil || terminated.ExitCode != 0 {
				continue
			}
			if digest := strings.TrimSpace(terminated.Message); digestPattern.MatchString(digest) {
				return digest, nil
			}
		}
	}
	return "", nil
}

func (r *WorkloadReconciler) buildJob(object client.Object, source *kiongav1.SourceSpec, name, hash string) *batchv1.Job {
	config := r.Config.Build
	output := "type=image,name=" + BuildRepository(config.Registry, object.GetNamespace(), object.GetName()) + ":" + hash + ",push=true"
	if config.Insecure {
		output += ",registry.insecure=true"
	}
	args := []string{"build", "--frontend", "dockerfile.v0", "--opt", "context=" + BuildContext(source)}
	if source.Dockerfile != "" {
		args = append(args, "--opt", "filename="+source.Dockerfile)
	}
	args = append(args, "--output", output)
	backoff, deadline, ttl := int32(0), int64(3600), int32(86400)
	user, automount := int64(1000), false
	volumes := []corev1.Volume{
		{Name: "buildkitd", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "metadata", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	mounts := []corev1.VolumeMount{
		{Name: "buildkitd", MountPath: "/home/user/.local/share/buildkit"},
		{Name: "metadata", MountPath: "/tmp/kionga-build"},
	}
	env := []corev1.EnvVar{{Name: "BUILDKITD_FLAGS", Value: "--oci-worker-no-process-sandbox"}}
	if config.PushSecret != "" {
		volumes = append(volumes, corev1.Volume{Name: "push", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: config.PushSecret, Items: []corev1.KeyToPath{{Key: corev1.DockerConfigJsonKey, Path: "config.json"}},
		}}})
		mounts = append(mounts, corev1.VolumeMount{Name: "push", MountPath: "/home/user/.docker", ReadOnly: true})
		env = append(env, corev1.EnvVar{Name: "DOCKER_CONFIG", Value: "/home/user/.docker"})
	}
	labels := map[string]string{"app.kubernetes.io/managed-by": "kionga-operator", "app.kubernetes.io/component": "build", "kionga.dev/workload": object.GetName(), "kionga.dev/source-hash": hash}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: object.GetNamespace(), Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff, ActiveDeadlineSeconds: &deadline, TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &automount,
					ImagePullSecrets:             r.Config.ImagePullSecrets,
					Volumes:                      volumes,
					Containers: []corev1.Container{{
						Name: buildContainer, Image: config.Image,
						Command: append([]string{"sh", "-c", buildScript, "kionga-build"}, args...),
						Env:     env, VolumeMounts: mounts,
						TerminationMessagePolicy: corev1.TerminationMessageReadFile,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")},
							Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")},
						},
						// Rootless BuildKit needs user namespaces: an unconfined
						// seccomp and AppArmor profile, never privileged mode.
						SecurityContext: &corev1.SecurityContext{
							RunAsUser: &user, RunAsGroup: &user,
							SeccompProfile:  &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
							AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined},
						},
					}},
				},
			},
		},
	}
}

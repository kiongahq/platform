// Package k8sexec runs pipeline nodes as Kubernetes Jobs.
//
// Each node attempt is one batch/v1 Job in the run's namespace, labelled
// with tenant, project, run, node and attempt. Retries are driven here (the
// Job's backoffLimit is 0) so attempts, backoff and exit codes are reported
// the same way as the Docker executor. The node timeout becomes
// activeDeadlineSeconds. Pods run non-root with all capabilities dropped,
// no privilege escalation and no service-account token.
//
// Pod events (scheduling failures, image pull errors, OOM kills, restarts)
// and the Job's terminal condition are reported as `k8s` log events, and
// workloads are typed k8s-job (with the pod name in context), never as a
// local container.
package k8sexec

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/ml-ai-ops/platform/internal/execution"
	"github.com/ml-ai-ops/platform/pkg/api"
)

// Reporter receives step transitions and log events.
type Reporter interface {
	Step(api.UpdateRunStepRequest)
	Event(api.LogEntry)
}

type Executor struct {
	Client    kubernetes.Interface
	Namespace func(projectID string) string
	Tenant    string
	// Poll is the status polling interval (default 2s).
	Poll time.Duration
	// DefaultTimeout applies when a node sets none (default 1h).
	DefaultTimeout time.Duration
	Sleep          func(time.Duration)
	Now            func() time.Time
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

func (e *Executor) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		e.Sleep(d)
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

var nameUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// JobName is a DNS-1123 name unique per run, node and attempt (≤63 chars).
func JobName(runID, node string, attempt int) string {
	base := nameUnsafe.ReplaceAllString(strings.ToLower(fmt.Sprintf("kn-%s-%s", lastPart(runID, 20), node)), "-")
	suffix := fmt.Sprintf("-a%d", attempt)
	if len(base)+len(suffix) > 63 {
		base = base[:63-len(suffix)]
	}
	return strings.Trim(base, "-") + suffix
}

func lastPart(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[len(value)-n:]
}

func labelValue(value string) string {
	cleaned := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(value, "-")
	if len(cleaned) > 63 {
		cleaned = cleaned[:63]
	}
	return strings.Trim(cleaned, "-._")
}

// JobFor builds the Job for one node attempt.
func (e *Executor) JobFor(run api.PipelineRun, job api.PipelineJob, attempt int, env map[string]string) (*batchv1.Job, error) {
	timeout := e.DefaultTimeout
	if timeout == 0 {
		timeout = time.Hour
	}
	if job.TimeoutSeconds > 0 {
		timeout = time.Duration(job.TimeoutSeconds) * time.Second
	}
	cpu := job.Resources.CPU
	if cpu == "" {
		cpu = "500m"
	}
	memory := job.Resources.Memory
	if memory == "" {
		memory = "1Gi"
	}
	requests := corev1.ResourceList{}
	var err error
	if requests[corev1.ResourceCPU], err = resource.ParseQuantity(cpu); err != nil {
		return nil, fmt.Errorf("node %s CPU: %w", job.Name, err)
	}
	if requests[corev1.ResourceMemory], err = resource.ParseQuantity(memory); err != nil {
		return nil, fmt.Errorf("node %s memory: %w", job.Name, err)
	}
	limits := requests.DeepCopy()
	if job.Resources.GPU > 0 {
		limits["nvidia.com/gpu"] = *resource.NewQuantity(int64(job.Resources.GPU), resource.DecimalSI)
	}
	var envVars []corev1.EnvVar
	for _, key := range sortedKeys(env) {
		envVars = append(envVars, corev1.EnvVar{Name: key, Value: env[key]})
	}
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "kionga",
		"kionga.dev/tenant":            labelValue(e.Tenant),
		"kionga.dev/project":           labelValue(run.ProjectID),
		"kionga.dev/run":               labelValue(run.ID),
		"kionga.dev/node":              labelValue(job.Name),
		"kionga.dev/attempt":           fmt.Sprint(attempt),
	}
	deadline := int64(timeout.Seconds())
	backoff := int32(0)
	ttl := int32(3600)
	falseValue, trueValue := false, true
	runAsUser := int64(65532)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: JobName(run.ID, job.Name, attempt), Namespace: e.Namespace(run.ProjectID), Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff, ActiveDeadlineSeconds: &deadline, TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &falseValue,
					SecurityContext:              &corev1.PodSecurityContext{RunAsNonRoot: &trueValue, RunAsUser: &runAsUser, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
					Containers: []corev1.Container{{
						Name: "node", Image: job.Image, Command: job.Command, Env: envVars,
						Resources:       corev1.ResourceRequirements{Requests: requests, Limits: limits},
						SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &falseValue, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
					}},
				},
			},
		},
	}, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// Run executes a run's container nodes with ready-set scheduling.
func (e *Executor) Run(ctx context.Context, run api.PipelineRun, jobs []api.PipelineJob, reporter Reporter, maxParallel int) map[string]error {
	start := func(ctx context.Context, job api.PipelineJob, dependencies map[string]any) (any, error) {
		return e.runNode(ctx, run, job, dependencies, reporter)
	}
	_, failures := execution.Run(ctx, jobs, run.Parameters, start, func(step, status, message string) {
		reporter.Step(api.UpdateRunStepRequest{Step: step, Status: status, Message: message})
	}, maxParallel)
	return failures
}

func (e *Executor) runNode(ctx context.Context, run api.PipelineRun, job api.PipelineJob, dependencies map[string]any, reporter Reporter) (any, error) {
	if job.Kind != "container" {
		return nil, fmt.Errorf("node %s: the Kubernetes executor runs container nodes only", job.Name)
	}
	env := map[string]string{"KIONGA_RUN_ID": run.ID, "KIONGA_PROJECT_ID": run.ProjectID, "KIONGA_STEP_NAME": job.Name}
	for key, value := range job.Environment {
		env[key] = value
	}
	attempts := job.Retries + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		spec, err := e.JobFor(run, job, attempt, env)
		if err != nil {
			reporter.Step(api.UpdateRunStepRequest{Step: job.Name, Status: "failed", Message: err.Error(), Attempt: attempt})
			return nil, err
		}
		created, err := e.Client.BatchV1().Jobs(spec.Namespace).Create(ctx, spec, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			created, err = e.Client.BatchV1().Jobs(spec.Namespace).Get(ctx, spec.Name, metav1.GetOptions{})
		}
		if err != nil {
			reporter.Step(api.UpdateRunStepRequest{Step: job.Name, Status: "failed", Message: "Kubernetes rejected the Job: " + err.Error(), Attempt: attempt, WorkloadKind: "k8s-job", WorkloadID: spec.Name})
			return nil, err
		}
		reporter.Step(api.UpdateRunStepRequest{Step: job.Name, Status: "running", Message: "Job " + created.Namespace + "/" + created.Name + " created", Attempt: attempt, WorkloadKind: "k8s-job", WorkloadID: created.Namespace + "/" + created.Name})
		outcome, err := e.wait(ctx, run, job, created, attempt, reporter)
		if err == nil {
			reporter.Step(api.UpdateRunStepRequest{Step: job.Name, Status: "succeeded", Message: outcome, Attempt: attempt, WorkloadKind: "k8s-job", WorkloadID: created.Namespace + "/" + created.Name, ExitCode: intPtr(0)})
			return nil, nil
		}
		lastErr = err
		if attempt < attempts && job.RetryBackoffSeconds > 0 {
			if e.sleep(ctx, time.Duration(job.RetryBackoffSeconds)*time.Second) != nil {
				break
			}
		}
	}
	reporter.Step(api.UpdateRunStepRequest{Step: job.Name, Status: "failed", Message: lastErr.Error(), Attempt: attempts, WorkloadKind: "k8s-job", WorkloadID: e.Namespace(run.ProjectID) + "/" + JobName(run.ID, job.Name, attempts), ExitCode: exitCode(lastErr)})
	return nil, lastErr
}

func intPtr(value int) *int { return &value }

// nodeError carries the container exit code when Kubernetes reported one.
type nodeError struct {
	message string
	code    *int
}

func (n *nodeError) Error() string { return n.message }

func exitCode(err error) *int {
	if failure, ok := err.(*nodeError); ok {
		return failure.code
	}
	return nil
}

// wait polls the Job until it finishes, reporting new pod events.
func (e *Executor) wait(ctx context.Context, run api.PipelineRun, job api.PipelineJob, created *batchv1.Job, attempt int, reporter Reporter) (string, error) {
	poll := e.Poll
	if poll == 0 {
		poll = 2 * time.Second
	}
	seenEvents := map[string]bool{}
	for {
		current, err := e.Client.BatchV1().Jobs(created.Namespace).Get(ctx, created.Name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("lost Job %s/%s: %w", created.Namespace, created.Name, err)
		}
		pods, _ := e.Client.CoreV1().Pods(created.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + created.Name})
		e.reportEvents(ctx, run, job, attempt, created, pods, seenEvents, reporter)
		for _, condition := range current.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				continue
			}
			switch condition.Type {
			case batchv1.JobComplete:
				return "Job " + created.Name + " completed", nil
			case batchv1.JobFailed:
				message, code := failureDetail(pods, condition)
				return "", &nodeError{message: message, code: code}
			}
		}
		if err := e.sleep(ctx, poll); err != nil {
			_ = e.Client.BatchV1().Jobs(created.Namespace).Delete(context.Background(), created.Name, metav1.DeleteOptions{})
			return "", err
		}
	}
}

// failureDetail explains a failed Job from its pod's container state.
func failureDetail(pods *corev1.PodList, condition batchv1.JobCondition) (string, *int) {
	message := fmt.Sprintf("Job failed: %s %s", condition.Reason, condition.Message)
	if condition.Reason == "DeadlineExceeded" {
		message = "Job exceeded its timeout (activeDeadlineSeconds) and was stopped"
	}
	if pods == nil {
		return strings.TrimSpace(message), nil
	}
	for _, pod := range pods.Items {
		for _, status := range pod.Status.ContainerStatuses {
			if terminated := status.State.Terminated; terminated != nil {
				code := int(terminated.ExitCode)
				detail := fmt.Sprintf("container %s exited with code %d (%s)", status.Name, terminated.ExitCode, terminated.Reason)
				if terminated.Reason == "OOMKilled" {
					detail = fmt.Sprintf("container %s was killed for exceeding its memory limit (OOMKilled, exit %d)", status.Name, terminated.ExitCode)
				}
				return detail, &code
			}
			if waiting := status.State.Waiting; waiting != nil && waiting.Reason != "" {
				return fmt.Sprintf("container %s never started: %s %s", status.Name, waiting.Reason, waiting.Message), nil
			}
		}
	}
	return strings.TrimSpace(message), nil
}

func (e *Executor) reportEvents(ctx context.Context, run api.PipelineRun, job api.PipelineJob, attempt int, created *batchv1.Job, pods *corev1.PodList, seen map[string]bool, reporter Reporter) {
	objects := []string{created.Name}
	if pods != nil {
		for _, pod := range pods.Items {
			objects = append(objects, pod.Name)
		}
	}
	for _, name := range objects {
		events, err := e.Client.CoreV1().Events(created.Namespace).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + name})
		if err != nil {
			continue
		}
		for _, event := range events.Items {
			key := string(event.UID) + fmt.Sprint(event.Count)
			if seen[key] {
				continue
			}
			seen[key] = true
			severity := "info"
			if event.Type == corev1.EventTypeWarning {
				severity = "warn"
			}
			at := event.LastTimestamp.Time
			if at.IsZero() {
				at = e.now()
			}
			reporter.Event(api.LogEntry{Timestamp: at.UTC(), ProjectID: run.ProjectID, PipelineID: run.DefinitionID, RunID: run.ID, Node: job.Name, Attempt: attempt,
				WorkloadKind: "k8s-pod", WorkloadID: created.Namespace + "/" + name, Source: "k8s", Severity: severity,
				Message: event.Reason + ": " + event.Message, Context: map[string]any{"object": event.InvolvedObject.Kind + "/" + name, "count": event.Count}})
		}
	}
}

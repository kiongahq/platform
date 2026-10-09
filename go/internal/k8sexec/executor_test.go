package k8sexec

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kiongahq/platform/pkg/api"
)

type recorder struct {
	mu     sync.Mutex
	steps  []api.UpdateRunStepRequest
	events []api.LogEntry
}

func (r *recorder) Step(step api.UpdateRunStepRequest) {
	r.mu.Lock()
	r.steps = append(r.steps, step)
	r.mu.Unlock()
}
func (r *recorder) Event(entry api.LogEntry) {
	r.mu.Lock()
	r.events = append(r.events, entry)
	r.mu.Unlock()
}

// cluster simulates the Job controller: each created Job finishes with the
// outcome scripted for its node (succeeded, failed exit N, oom).
type cluster struct {
	client  *fake.Clientset
	outcome func(node string, attempt string) (bool, int32, string)
	mu      sync.Mutex
	started map[string]time.Time
}

func newCluster(outcome func(node, attempt string) (bool, int32, string)) *cluster {
	return &cluster{client: fake.NewSimpleClientset(), outcome: outcome, started: map[string]time.Time{}}
}

func (c *cluster) finish(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
		jobs, _ := c.client.BatchV1().Jobs("").List(ctx, metav1.ListOptions{})
		for _, job := range jobs.Items {
			if len(job.Status.Conditions) > 0 {
				continue
			}
			c.mu.Lock()
			first, seen := c.started[job.Name]
			if !seen {
				c.started[job.Name] = time.Now()
				first = time.Now()
			}
			c.mu.Unlock()
			if time.Since(first) < 30*time.Millisecond {
				continue
			}
			ok, code, reason := c.outcome(job.Labels["kionga.dev/node"], job.Labels["kionga.dev/attempt"])
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-pod", Namespace: job.Namespace, Labels: map[string]string{"job-name": job.Name}},
				Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "node", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Reason: reason}}}}}}
			_, _ = c.client.CoreV1().Pods(job.Namespace).Create(ctx, pod, metav1.CreateOptions{})
			if reason == "OOMKilled" {
				_, _ = c.client.CoreV1().Events(job.Namespace).Create(ctx, &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: pod.Name + ".oom", Namespace: job.Namespace, UID: "oom-" + "x"},
					InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: pod.Name}, Type: corev1.EventTypeWarning, Reason: "OOMKilling", Message: "Memory cgroup out of memory", Count: 1}, metav1.CreateOptions{})
			}
			condition := batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}
			if !ok {
				condition = batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}
			}
			job.Status.Conditions = []batchv1.JobCondition{condition}
			_, _ = c.client.BatchV1().Jobs(job.Namespace).UpdateStatus(ctx, &job, metav1.UpdateOptions{})
		}
	}
}

func executorFor(c *cluster) *Executor {
	return &Executor{Client: c.client, Namespace: func(project string) string { return "kionga-" + project }, Tenant: "acme", Poll: 5 * time.Millisecond, Sleep: func(time.Duration) {}}
}

func TestJobSpecIsHardenedLabelledAndBounded(t *testing.T) {
	e := executorFor(newCluster(nil))
	run := api.PipelineRun{ID: "run-123456789012345678901234567890", ProjectID: "prj-1"}
	job, err := e.JobFor(run, api.PipelineJob{Name: "train", Kind: "container", Image: "ghcr.io/a/b@sha256:abc", Command: []string{"python", "t.py"}, TimeoutSeconds: 600, Resources: api.JobResources{CPU: "2", Memory: "4Gi", GPU: 1}}, 2, map[string]string{"B": "2", "A": "1"})
	if err != nil {
		t.Fatal(err)
	}
	pod := job.Spec.Template.Spec
	container := pod.Containers[0]
	if len(job.Name) > 63 || !strings.HasSuffix(job.Name, "-a2") || job.Namespace != "kionga-prj-1" {
		t.Fatalf("name/namespace: %s %s", job.Name, job.Namespace)
	}
	if *job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != 600 || pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatal("retries/timeout must be driven by Kionga")
	}
	if *pod.AutomountServiceAccountToken || !*pod.SecurityContext.RunAsNonRoot || *container.SecurityContext.AllowPrivilegeEscalation || container.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatal("pod not hardened")
	}
	if container.Resources.Limits.Cpu().String() != "2" || container.Resources.Limits.Memory().String() != "4Gi" || gpuLimit(container) != "1" {
		t.Fatalf("resources: %+v", container.Resources)
	}
	if job.Labels["kionga.dev/tenant"] != "acme" || job.Labels["kionga.dev/node"] != "train" || job.Labels["kionga.dev/attempt"] != "2" || container.Env[0].Name != "A" {
		t.Fatalf("labels/env: %+v %+v", job.Labels, container.Env)
	}
}

func TestRunsDAGWithRetriesAndReportsKubernetesFacts(t *testing.T) {
	c := newCluster(func(node, attempt string) (bool, int32, string) {
		switch {
		case node == "flaky" && attempt == "1":
			return false, 3, "Error"
		case node == "oom":
			return false, 137, "OOMKilled"
		}
		return true, 0, "Completed"
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.finish(ctx)
	rec := &recorder{}
	jobs := []api.PipelineJob{
		{Name: "extract", Kind: "container", Image: "img"},
		{Name: "flaky", Kind: "container", Image: "img", DependsOn: []string{"extract"}, Retries: 1},
		{Name: "oom", Kind: "container", Image: "img", DependsOn: []string{"extract"}},
		{Name: "after-oom", Kind: "container", Image: "img", DependsOn: []string{"oom"}},
	}
	failures := executorFor(c).Run(ctx, api.PipelineRun{ID: "run-1", ProjectID: "prj-1"}, jobs, rec, 8)
	if len(failures) != 1 || failures["oom"] == nil || !strings.Contains(failures["oom"].Error(), "OOMKilled") {
		t.Fatalf("failures: %v", failures)
	}
	final := map[string]api.UpdateRunStepRequest{}
	for _, step := range rec.steps {
		final[step.Step] = step
	}
	if final["flaky"].Status != "succeeded" || final["flaky"].Attempt != 2 || final["flaky"].WorkloadKind != "k8s-job" || !strings.HasPrefix(final["flaky"].WorkloadID, "kionga-prj-1/") {
		t.Fatalf("flaky: %+v", final["flaky"])
	}
	if final["oom"].Status != "failed" || final["oom"].ExitCode == nil || *final["oom"].ExitCode != 137 {
		t.Fatalf("oom: %+v", final["oom"])
	}
	if final["after-oom"].Status != "skipped" {
		t.Fatalf("downstream of failure: %+v", final["after-oom"])
	}
	foundOOMEvent := false
	for _, event := range rec.events {
		if event.Source == "k8s" && event.WorkloadKind == "k8s-pod" && strings.Contains(event.Message, "OOMKilling") && event.Severity == "warn" && event.Node == "oom" {
			foundOOMEvent = true
		}
	}
	if !foundOOMEvent {
		t.Fatalf("pod OOM event not reported: %+v", rec.events)
	}
	jobsCreated, _ := c.client.BatchV1().Jobs("kionga-prj-1").List(ctx, metav1.ListOptions{})
	if len(jobsCreated.Items) != 4 { // extract, flaky×2, oom; after-oom is skipped, never created
		t.Fatalf("jobs created: %d", len(jobsCreated.Items))
	}
}

func TestFunctionNodesAreRejected(t *testing.T) {
	rec := &recorder{}
	failures := executorFor(newCluster(nil)).Run(context.Background(), api.PipelineRun{ID: "r", ProjectID: "p"}, []api.PipelineJob{{Name: "f", Kind: "function", Function: "x"}}, rec, 1)
	if failures["f"] == nil || !strings.Contains(failures["f"].Error(), "container nodes only") {
		t.Fatalf("failures: %v", failures)
	}
}

func gpuLimit(container corev1.Container) string {
	quantity := container.Resources.Limits["nvidia.com/gpu"]
	return quantity.String()
}

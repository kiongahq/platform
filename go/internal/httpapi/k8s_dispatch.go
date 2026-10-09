package httpapi

import (
	"context"
	"log"
	"os"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kiongahq/platform/internal/k8sexec"
	"github.com/kiongahq/platform/pkg/api"
)

// KIONGA_EXECUTOR=kubernetes runs container flows as Kubernetes Jobs from the
// gateway using its in-cluster service account, in KIONGA_K8S_NAMESPACE
// (default kionga-pipelines). Otherwise container flows go to the pipeline
// runner.
func kubernetesExecutorEnabled() bool { return os.Getenv("KIONGA_EXECUTOR") == "kubernetes" }

var (
	k8sOnce     sync.Once
	k8sExecutor *k8sexec.Executor
	k8sErr      error
)

func (s *Server) kubernetesExecutor() (*k8sexec.Executor, error) {
	k8sOnce.Do(func() {
		config, err := rest.InClusterConfig()
		if err != nil {
			k8sErr = err
			return
		}
		client, err := kubernetes.NewForConfig(config)
		if err != nil {
			k8sErr = err
			return
		}
		// One dedicated namespace (KIONGA_K8S_NAMESPACE) keeps the gateway's
		// permissions namespace-scoped; projects are separated by labels and
		// network policy there. A per-project prefix needs cluster-wide rights.
		fixed, prefix := os.Getenv("KIONGA_K8S_NAMESPACE"), os.Getenv("KIONGA_K8S_NAMESPACE_PREFIX")
		if fixed == "" && prefix == "" {
			fixed = "kionga-pipelines"
		}
		tenant := os.Getenv("MLAIOPS_TENANT")
		if tenant == "" {
			tenant = "default"
		}
		k8sExecutor = &k8sexec.Executor{Client: client, Tenant: tenant, Namespace: func(project string) string {
			if fixed != "" {
				return fixed
			}
			return prefix + project
		}}
	})
	return k8sExecutor, k8sErr
}

// stepReporter adapts a run to k8sexec.Reporter.
type stepReporter struct {
	s     *Server
	runID string
}

func (r stepReporter) Step(req api.UpdateRunStepRequest) {
	if _, err := r.s.reportStep(r.runID, req, "kubernetes-executor"); err != nil {
		log.Printf("record step %s for run %s: %v", req.Step, r.runID, err)
	}
}
func (r stepReporter) Event(entry api.LogEntry) { r.s.appendLogs([]api.LogEntry{entry}) }

func (s *Server) dispatchKubernetes(run api.PipelineRun) api.PipelineRun {
	executor, err := s.kubernetesExecutor()
	if err != nil {
		failed, _ := s.reportStep(run.ID, api.UpdateRunStepRequest{Step: "submit-to-engine", Status: "failed", Message: "Kubernetes executor unavailable: " + err.Error()}, "system")
		return failed
	}
	definition, err := s.executionDefinition(run)
	if err != nil {
		failed, _ := s.reportStep(run.ID, api.UpdateRunStepRequest{Step: "load-definition", Status: "failed", Message: err.Error()}, "system")
		return failed
	}
	maxParallel := 0
	if run.Provenance != nil && run.Provenance.Overrides != nil {
		maxParallel = run.Provenance.Overrides.MaxParallelism
	}
	go executor.Run(context.Background(), run, definition.Jobs, stepReporter{s: s, runID: run.ID}, maxParallel)
	return run
}

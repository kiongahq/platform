package httpapi

import (
	"context"
	"sync"
	"time"
)

// defaultRunCancelPoll is how often an in-process execution re-reads its run so
// a cancellation recorded by another gateway replica also stops it.
const defaultRunCancelPoll = 5 * time.Second

// runExecutions tracks the pipeline runs this process executes itself (the
// Kubernetes executor and the OpenFaaS function DAG). Each has a cancellable
// context; cancelRun cancels it, and the entry stays until the executor has
// stopped its workloads, which is what keeps the run counted against the
// concurrency quota while it drains.
type runExecutions struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	// poll overrides defaultRunCancelPoll. Set it only when building the Server.
	poll time.Duration
}

// startRunExecution returns the context an in-process executor must run
// under and a done func the executor calls once every workload has stopped.
func (s *Server) startRunExecution(runID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	s.executions.mu.Lock()
	if s.executions.cancels == nil {
		s.executions.cancels = map[string]context.CancelFunc{}
	}
	s.executions.cancels[runID] = cancel
	s.executions.mu.Unlock()
	watching, stopWatching := context.WithCancel(ctx)
	go s.watchRunCancellation(watching, runID, cancel)
	return ctx, func() {
		stopWatching()
		cancel()
		s.executions.mu.Lock()
		delete(s.executions.cancels, runID)
		s.executions.mu.Unlock()
	}
}

// watchRunCancellation cancels an execution whose run was cancelled through
// another replica (or directly in the store).
func (s *Server) watchRunCancellation(ctx context.Context, runID string, cancel context.CancelFunc) {
	poll := s.executions.poll
	if poll == 0 {
		poll = defaultRunCancelPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if run, err := s.store.Run(runID); err == nil && run.Status == "cancelled" {
				cancel()
				return
			}
		}
	}
}

// stopRunExecution cancels the in-process execution of runID, if any.
func (s *Server) stopRunExecution(runID string) bool {
	s.executions.mu.Lock()
	cancel, ok := s.executions.cancels[runID]
	s.executions.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// runExecuting reports whether this process still has workloads for runID.
func (s *Server) runExecuting(runID string) bool {
	s.executions.mu.Lock()
	defer s.executions.mu.Unlock()
	_, ok := s.executions.cancels[runID]
	return ok
}

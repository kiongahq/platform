package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/kiongahq/platform/internal/agentteam"
	"github.com/kiongahq/platform/internal/redact"
	"github.com/kiongahq/platform/internal/store"
)

// The scheduler runs on every gateway replica. Exactly one replica runs a worker
// at a time because a worker runs only under a lease: a random token written with
// compare-and-set through store.UpdateDoc. Every later write checks the token, so
// a gateway that stalled past its lease loses the right to write, and therefore
// the right to start a tool, the moment another replica takes over.
const (
	teamSlotKind        = "team_run_slots"
	teamLeaseTTL        = 60 * time.Second
	teamLeaseRenew      = 20 * time.Second
	teamScanEvery       = 5 * time.Second
	teamWatchEvery      = 2 * time.Second
	teamPoolDefault     = 32
	teamSandboxRetain   = 24 * time.Hour
	teamSandboxGiveUp   = 48 * time.Hour
	teamSchedulerSystem = "system:team-scheduler"
)

// teamSlots enforces max_concurrent_workers across replicas. Entries expire with
// the lease that holds them, so a dead gateway's slot frees itself.
type teamSlots struct {
	Held   map[string]time.Time `json:"held"`
	Tokens map[string]string    `json:"tokens,omitempty"`
}

var teamKick = make(chan struct{}, 1)

// kickTeamScheduler asks for a scan now rather than at the next tick.
func kickTeamScheduler() {
	select {
	case teamKick <- struct{}{}:
	default:
	}
}

type teamEngine struct {
	s          *Server
	owner      string
	now        func() time.Time
	jitter     func() float64
	leaseTTL   time.Duration
	renewEvery time.Duration
	scanEvery  time.Duration
	watchEvery time.Duration
	retain     time.Duration
	compactAt  int

	mu     sync.Mutex
	active map[string]bool
	sem    chan struct{}
	wg     sync.WaitGroup
}

func randomToken() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}

func secureJitter() float64 {
	value, err := rand.Int(rand.Reader, big.NewInt(1000))
	if err != nil {
		return 0
	}
	return float64(value.Int64()) / 1000
}

func newTeamEngine(s *Server, owner string) *teamEngine {
	pool := teamPoolDefault
	if value, err := strconv.Atoi(os.Getenv("KIONGA_TEAM_WORKER_POOL")); err == nil && value > 0 && value <= 512 {
		pool = value
	}
	retain := teamSandboxRetain
	if value, err := strconv.Atoi(os.Getenv("KIONGA_TEAM_SANDBOX_RETENTION_SECONDS")); err == nil && value >= 0 {
		retain = time.Duration(value) * time.Second
	}
	compactAt := agentteam.DefaultCompactThreshold
	if value, err := strconv.Atoi(os.Getenv("KIONGA_TEAM_COMPACT_THRESHOLD_TOKENS")); err == nil && value > 0 {
		compactAt = value
	}
	return &teamEngine{s: s, owner: owner, compactAt: compactAt, now: func() time.Time { return time.Now().UTC() }, jitter: secureJitter,
		leaseTTL: teamLeaseTTL, renewEvery: teamLeaseRenew, scanEvery: teamScanEvery, watchEvery: teamWatchEvery, retain: retain,
		active: map[string]bool{}, sem: make(chan struct{}, pool)}
}

// StartTeamRuns runs the Agent Teams scheduler until ctx ends. Every replica may
// call it. Set KIONGA_TEAM_SCHEDULER=off to disable it on a replica.
func StartTeamRuns(ctx context.Context, data store.Repository) {
	if os.Getenv("KIONGA_TEAM_SCHEDULER") == "off" {
		log.Printf("team scheduler disabled by KIONGA_TEAM_SCHEDULER=off")
		return
	}
	host, _ := os.Hostname()
	engine := newTeamEngine(&Server{store: data}, host+"-"+randomToken()[:8])
	go engine.run(ctx)
	log.Printf("team scheduler started (owner %s)", engine.owner)
}

func (e *teamEngine) run(ctx context.Context) {
	ticker := time.NewTicker(e.scanEvery)
	defer ticker.Stop()
	for {
		e.scan(ctx)
		select {
		case <-ctx.Done():
			e.wg.Wait()
			return
		case <-ticker.C:
		case <-teamKick:
		}
	}
}

func (e *teamEngine) docs() store.Documents { return e.s.store }

// scan looks at every active run once.
func (e *teamEngine) scan(ctx context.Context) {
	index, err := store.GetDoc[teamIndex](e.docs(), teamIndexKind, teamIndexID)
	if err != nil {
		return
	}
	for id := range index.Runs {
		if ctx.Err() != nil {
			return
		}
		e.processRun(ctx, id)
	}
}

func (e *teamEngine) processRun(ctx context.Context, runID string) {
	run, err := store.GetDoc[agentteam.Run](e.docs(), teamRunKind, runID)
	if errors.Is(err, store.ErrNotFound) {
		_ = teamIndexSet(e.docs(), runID, false)
		return
	}
	if err != nil {
		return
	}
	workers := teamWorkers(e.docs(), run)
	if run.Status == agentteam.RunRunning {
		for _, worker := range workers {
			if worker.NotifyPending {
				e.notifyCoordinator(run, worker)
			}
		}
		if e.driveRun(ctx, run, workers) {
			return
		}
		run, err = store.GetDoc[agentteam.Run](e.docs(), teamRunKind, runID)
		if err != nil {
			return
		}
		workers = teamWorkers(e.docs(), run)
	}
	if run.Status != agentteam.RunRunning {
		e.settleRun(ctx, run, workers)
	}
}

// driveRun enforces run-wide rules and schedules eligible workers. It reports
// true when the run stays running. These checks also cover a run with no active
// worker, which has no turn that could notice a suspended starter or a deadline.
func (e *teamEngine) driveRun(ctx context.Context, run agentteam.Run, workers []agentteam.Worker) bool {
	if _, lost, err := e.s.starterAccess(run); err == nil && lost {
		e.failRun(run.ID, agentteam.ErrOwnerAccessLost, "The person who started this run lost access.")
		return false
	}
	if reason := agentteam.TeamBudget(run.Snapshot, sumUsage(workers), run.CreatedAt, e.now()); reason != "" {
		e.failRun(run.ID, agentteam.ErrBudgetExceeded, reason)
		return false
	}
	for _, worker := range workers {
		if worker.ID != run.CoordinatorID {
			continue
		}
		switch worker.Status {
		case agentteam.WorkerCompleted:
			e.finishRun(run.ID, worker.Report)
			return false
		case agentteam.WorkerFailed:
			e.failRun(run.ID, worker.LastError, worker.LastErrorMessage)
			return false
		}
	}
	now := e.now()
	for _, worker := range workers {
		decided := false
		if worker.Status == agentteam.WorkerWaitingApproval {
			decided = e.approvalDecided(worker)
		}
		if agentteam.Eligible(worker, decided, now) {
			e.launch(ctx, run.ID, worker.ID, decided)
		}
	}
	return true
}

func (e *teamEngine) approvalDecided(worker agentteam.Worker) bool {
	call, ok := agentteam.PendingCall(worker)
	if !ok || call.ApprovalID == "" {
		return true
	}
	approval, err := store.GetDoc[agentApproval](e.docs(), agentApprovalKind, call.ApprovalID)
	return err != nil || approval.view().Status != "pending"
}

// launch starts a worker unless this gateway already runs it or its pool is full.
func (e *teamEngine) launch(ctx context.Context, runID, workerID string, decided bool) {
	e.mu.Lock()
	if e.active[workerID] {
		e.mu.Unlock()
		return
	}
	select {
	case e.sem <- struct{}{}:
	default:
		e.mu.Unlock()
		return
	}
	e.active[workerID] = true
	e.wg.Add(1)
	e.mu.Unlock()
	go func() {
		defer func() {
			e.mu.Lock()
			delete(e.active, workerID)
			e.mu.Unlock()
			<-e.sem
			e.wg.Done()
		}()
		e.runWorker(ctx, runID, workerID, decided)
	}()
}

func (e *teamEngine) acquireSlot(run agentteam.Run, workerID, token string) bool {
	expires := e.now().Add(e.leaseTTL)
	limit := run.Snapshot.Team.Limits.MaxConcurrentWorkers
	acquired := false
	_, err := store.UpdateDoc(e.docs(), teamSlotKind, run.ID, func(current teamSlots, _ bool) (teamSlots, error) {
		if current.Held == nil {
			current.Held = map[string]time.Time{}
		}
		now := e.now()
		for id, until := range current.Held {
			if !until.After(now) {
				delete(current.Held, id)
				delete(current.Tokens, id)
			}
		}
		// A held entry means another attempt owns this worker. Refusing here, rather
		// than sharing it, keeps a losing attempt from freeing the winner's slot.
		if _, held := current.Held[workerID]; held || len(current.Held) >= limit {
			return current, store.ErrSkipWrite
		}
		if current.Tokens == nil {
			current.Tokens = map[string]string{}
		}
		current.Tokens[workerID] = token
		current.Held[workerID] = expires
		acquired = true
		return current, nil
	}, "", "")
	return err == nil && acquired
}

func (e *teamEngine) renewSlot(runID, workerID, token string) bool {
	renewed := false
	_, err := store.UpdateDoc(e.docs(), teamSlotKind, runID, func(current teamSlots, _ bool) (teamSlots, error) {
		if _, held := current.Held[workerID]; !held || current.Tokens[workerID] != token {
			return current, store.ErrSkipWrite
		}
		if !current.Held[workerID].After(e.now()) {
			return current, store.ErrSkipWrite
		}
		current.Held[workerID] = e.now().Add(e.leaseTTL)
		renewed = true
		return current, nil
	}, "", "")
	return err == nil && renewed
}

func (e *teamEngine) releaseSlot(runID, workerID, token string) {
	_, _ = store.UpdateDoc(e.docs(), teamSlotKind, runID, func(current teamSlots, _ bool) (teamSlots, error) {
		if _, held := current.Held[workerID]; !held || current.Tokens[workerID] != token {
			return current, store.ErrSkipWrite
		}
		delete(current.Held, workerID)
		delete(current.Tokens, workerID)
		return current, nil
	}, "", "")
}

// teamLease is this gateway's hold on one worker.
type teamLease struct {
	workerID  string
	token     string
	slotToken string
}

// acquireLease takes the lease with compare-and-set and applies the crash rule in
// the same write. Two schedulers racing here serialize on the row lock; the loser
// finds the winner's unexpired lease and backs off.
func (e *teamEngine) acquireLease(run agentteam.Run, workerID string, decided bool) (agentteam.Worker, *teamLease, bool, []string) {
	token := randomToken()
	takeover := false
	var unknown []string
	worker, err := store.UpdateDoc(e.docs(), teamWorkerKind, workerID, func(current agentteam.Worker, exists bool) (agentteam.Worker, error) {
		now := e.now()
		if !exists || !agentteam.Eligible(current, decided, now) {
			return current, store.ErrSkipWrite
		}
		takeover = current.LeaseOwner != ""
		tools := agentteam.ToolsFor(run.Snapshot, current.Role, current.Agent)
		_, unknown = agentteam.RecoverInterrupted(&current, func(name string) (agentteam.Class, bool) {
			tool, ok := agentteam.FindTool(tools, name)
			return tool.Class, ok
		}, randomToken)
		redactSettledArguments(&current)
		expires := now.Add(e.leaseTTL)
		started := now
		current.LeaseOwner, current.LeaseToken, current.LeaseExpiresAt = e.owner, token, &expires
		current.ActiveSince = &started
		current.Status, current.NextAttemptAt = agentteam.WorkerRunning, nil
		current.UpdatedAt = now
		return current, nil
	}, "", "")
	if err != nil || worker.LeaseToken != token {
		return agentteam.Worker{}, nil, false, nil
	}
	return worker, &teamLease{workerID: workerID, token: token}, takeover, unknown
}

// apply writes under the lease. It fails with errLeaseLost once another gateway
// holds the worker, which is the fence that stops a stalled gateway.
func (e *teamEngine) apply(lease *teamLease, change func(*agentteam.Worker) error) (agentteam.Worker, error) {
	return store.UpdateDoc(e.docs(), teamWorkerKind, lease.workerID, func(current agentteam.Worker, exists bool) (agentteam.Worker, error) {
		if !exists {
			return current, store.ErrNotFound
		}
		if current.LeaseToken != lease.token {
			return current, errLeaseLost
		}
		if err := change(&current); err != nil {
			return current, err
		}
		redactSettledArguments(&current)
		now := e.now()
		if current.ActiveSince != nil {
			current.ActiveMillis += now.Sub(*current.ActiveSince).Milliseconds()
			current.ActiveSince = &now
		}
		current.UpdatedAt = now
		return current, nil
	}, "", "")
}

func (e *teamEngine) release(lease *teamLease) {
	_, err := e.apply(lease, func(worker *agentteam.Worker) error { return nil })
	if err != nil {
		return
	}
	_, _ = e.apply(lease, func(worker *agentteam.Worker) error {
		worker.ActiveSince = nil
		worker.LeaseOwner, worker.LeaseToken, worker.LeaseExpiresAt = "", "", nil
		return nil
	})
}

func (e *teamEngine) renew(lease *teamLease) error {
	_, err := e.apply(lease, func(worker *agentteam.Worker) error {
		expires := e.now().Add(e.leaseTTL)
		worker.LeaseExpiresAt = &expires
		return nil
	})
	return err
}

// keepAlive renews the lease and watches the run. It cancels the worker's context
// as soon as the lease is lost or the run stops, including from another replica.
func (e *teamEngine) keepAlive(ctx context.Context, cancel context.CancelFunc, run agentteam.Run, lease *teamLease, isWorker bool) {
	renew := time.NewTicker(e.renewEvery)
	watch := time.NewTicker(e.watchEvery)
	defer renew.Stop()
	defer watch.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew.C:
			if err := e.renew(lease); errors.Is(err, errLeaseLost) {
				cancel()
				return
			}
			if isWorker {
				if !e.renewSlot(run.ID, lease.workerID, lease.slotToken) {
					cancel()
					return
				}
			}
		case <-watch.C:
			current, err := store.GetDoc[agentteam.Run](e.docs(), teamRunKind, run.ID)
			if err == nil && current.Status != agentteam.RunRunning {
				cancel()
				return
			}
		}
	}
}

func (e *teamEngine) runWorker(parent context.Context, runID, workerID string, decided bool) {
	run, err := store.GetDoc[agentteam.Run](e.docs(), teamRunKind, runID)
	if err != nil || run.Status != agentteam.RunRunning {
		return
	}
	slotToken := randomToken()
	isWorker := workerID != run.CoordinatorID
	if isWorker {
		if !e.acquireSlot(run, workerID, slotToken) {
			return
		}
		defer e.releaseSlot(runID, workerID, slotToken)
	}
	worker, lease, takeover, unknown := e.acquireLease(run, workerID, decided)
	if lease == nil {
		return
	}
	lease.slotToken = slotToken
	// A message can wake this worker while its lease is still held, so look again.
	defer kickTeamScheduler()
	defer e.release(lease)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var alive sync.WaitGroup
	alive.Add(1)
	go func() {
		defer alive.Done()
		e.keepAlive(ctx, cancel, run, lease, isWorker)
	}()
	defer alive.Wait()
	defer cancel()
	if takeover {
		e.recordTakeover(run.ID, worker)
	}
	for _, id := range unknown {
		appendTeamEvent(e.docs(), teamEvent{Type: "worker.tool_result", RunID: run.ID, WorkerID: worker.ID, Agent: worker.Agent, Tool: eventToolName(toolNameOf(worker, id)), ToolCallID: id,
			Error: eventError(agentteam.ErrOutcomeUnknown, "The call was interrupted and was not run again.")})
	}
	e.loop(ctx, run.ID, worker, lease)
}

func toolNameOf(worker agentteam.Worker, id string) string {
	for _, message := range worker.Transcript {
		for _, call := range message.ToolCalls {
			if call.ID == id {
				return call.Name
			}
		}
	}
	return "unknown"
}

func (e *teamEngine) recordTakeover(runID string, worker agentteam.Worker) {
	saved, err := store.UpdateDoc(e.docs(), teamRunKind, runID, func(current agentteam.Run, exists bool) (agentteam.Run, error) {
		if !exists {
			return current, store.ErrNotFound
		}
		current.ResumeCount++
		current.UpdatedAt = e.now()
		return current, nil
	}, "team_run.recovered", teamSchedulerSystem)
	if err != nil {
		return
	}
	count := saved.ResumeCount
	appendTeamEvent(e.docs(), teamEvent{Type: "run.resumed", RunID: runID, WorkerID: worker.ID, Agent: worker.Agent, Status: saved.Status, ResumeCount: &count})
}

// loop is the turn loop. Each pass re-reads the run and re-resolves the starter,
// so a cancellation or lost access stops the worker before its next model call
// or tool call.
func (e *teamEngine) loop(ctx context.Context, runID string, worker agentteam.Worker, lease *teamLease) {
	for {
		if ctx.Err() != nil {
			e.stopIfRunEnded(runID, lease, worker)
			return
		}
		run, err := store.GetDoc[agentteam.Run](e.docs(), teamRunKind, runID)
		if err != nil {
			return
		}
		if run.Status != agentteam.RunRunning {
			e.cancelLeased(run, lease, worker)
			return
		}
		starter, lost, err := e.s.starterAccess(run)
		if err != nil {
			return
		}
		if lost {
			e.failRun(run.ID, agentteam.ErrOwnerAccessLost, "The person who started this run lost access.")
			e.cancelLeased(run, lease, worker)
			return
		}
		var proceed bool
		switch agentteam.NextStep(worker) {
		case agentteam.StepDeliver:
			worker, err = e.apply(lease, func(current *agentteam.Worker) error {
				agentteam.Deliver(current, randomToken)
				return nil
			})
			proceed = err == nil
		case agentteam.StepModel:
			worker, proceed = e.modelStep(ctx, run, worker, lease)
		case agentteam.StepTool:
			worker, proceed = e.toolStep(ctx, run, worker, lease, starter)
		case agentteam.StepEnd:
			worker, proceed = e.endStep(run, worker, lease)
		}
		if !proceed {
			if ctx.Err() != nil {
				e.stopIfRunEnded(runID, lease, worker)
			}
			return
		}
	}
}

// stopIfRunEnded handles a cancelled context: a stopped run cancels the worker,
// while a gateway shutdown leaves it for another replica.
func (e *teamEngine) stopIfRunEnded(runID string, lease *teamLease, worker agentteam.Worker) {
	run, err := store.GetDoc[agentteam.Run](e.docs(), teamRunKind, runID)
	if err == nil && run.Status != agentteam.RunRunning {
		e.cancelLeased(run, lease, worker)
	}
}

func (e *teamEngine) cancelLeased(run agentteam.Run, lease *teamLease, worker agentteam.Worker) {
	saved, err := e.apply(lease, func(current *agentteam.Worker) error {
		if current.Terminal() {
			return store.ErrSkipWrite
		}
		current.Status = agentteam.WorkerCancelled
		current.NotifyPending = false
		return nil
	})
	if err == nil && saved.Status == agentteam.WorkerCancelled {
		appendTeamEvent(e.docs(), teamEvent{Type: "worker.finished", RunID: run.ID, WorkerID: worker.ID, Agent: worker.Agent, Role: worker.Role, Status: agentteam.WorkerCancelled})
	}
}

func (e *teamEngine) failWorker(run agentteam.Run, lease *teamLease, worker agentteam.Worker, code, message string, resumable bool) agentteam.Worker {
	saved, err := e.apply(lease, func(current *agentteam.Worker) error {
		agentteam.Fail(current, code, previewText(message, 500), resumable)
		return nil
	})
	if err != nil {
		return worker
	}
	appendTeamEvent(e.docs(), teamEvent{Type: "worker.finished", RunID: run.ID, WorkerID: worker.ID, Agent: worker.Agent, Role: worker.Role, Status: agentteam.WorkerFailed, Error: eventError(code, message)})
	if saved.Role == agentteam.RoleCoordinator {
		e.failRun(run.ID, code, message)
	} else {
		e.notifyCoordinator(run, saved)
	}
	return saved
}

// modelStep checks budgets, then makes one model call.
func (e *teamEngine) modelStep(ctx context.Context, run agentteam.Run, worker agentteam.Worker, lease *teamLease) (agentteam.Worker, bool) {
	if reason := agentteam.WorkerBudget(worker, run.Snapshot); reason != "" {
		return e.failWorker(run, lease, worker, agentteam.ErrBudgetExceeded, reason, false), false
	}
	if reason := agentteam.TeamBudget(run.Snapshot, sumUsage(teamWorkers(e.docs(), run)), run.CreatedAt, e.now()); reason != "" {
		e.failRun(run.ID, agentteam.ErrBudgetExceeded, reason)
		e.cancelLeased(run, lease, worker)
		return worker, false
	}
	// Crossing the threshold only compacts more. It never fails a worker: the
	// provider decides what is too long, through invalid_request below.
	if worker.LastInputTokens > e.compactAt && worker.Turns > worker.CompactedAtTurn {
		saved, err := e.apply(lease, func(current *agentteam.Worker) error {
			agentteam.AdvanceCompaction(current, e.compactAt)
			return nil
		})
		if err != nil {
			return worker, false
		}
		worker = saved
	}
	model := run.Snapshot.Team.Coordinator.Model
	if member, ok := run.Snapshot.Member(worker.Agent); ok {
		model = member.Model
	}
	response, err := e.s.teamRunnerFor().Turn(ctx, turnRequest{
		RunID: run.ID, WorkerID: worker.ID, Turn: worker.Turns + 1, Model: model,
		System:          agentteam.SystemPrompt(run.Snapshot, worker.Role, worker.Agent),
		Messages:        turnMessages(worker),
		Tools:           agentteam.ToolsFor(run.Snapshot, worker.Role, worker.Agent),
		MaxOutputTokens: 8000,
	})
	if ctx.Err() != nil {
		return worker, false
	}
	if err != nil {
		log.Printf("team run %s worker %s: runner call failed: %s", run.ID, worker.ID, redact.String(err.Error()))
		return e.waitOrFail(run, worker, lease, agentteam.ErrProviderUnavailable, "The agent runner could not be reached.", 0)
	}
	if response.Error != nil {
		switch response.Error.Code {
		case "rate_limited", agentteam.ErrProviderUnavailable:
			return e.waitOrFail(run, worker, lease, response.Error.Code, response.Error.Message, response.Error.RetryAfterSeconds)
		case "invalid_request":
			return e.compactOrExhaust(run, worker, lease, response.Error.Message)
		}
		return e.failWorker(run, lease, worker, response.Error.Code, response.Error.Message, true), false
	}
	saved, err := e.apply(lease, func(current *agentteam.Worker) error {
		agentteam.ApplyReply(current, agentteam.ModelReply{Content: redact.String(response.Content), ToolCalls: response.ToolCalls, Usage: response.Usage})
		return nil
	})
	if err != nil {
		return worker, false
	}
	usage := response.Usage
	appendTeamEvent(e.docs(), teamEvent{Type: "worker.turn", RunID: run.ID, WorkerID: saved.ID, Agent: saved.Agent, Role: saved.Role, Turn: saved.Turns, Status: agentteam.WorkerRunning, Usage: &usage, OutputPreview: preview(response.Content)})
	return saved, true
}

// compactOrExhaust answers invalid_request. Below the top level it compacts further
// and tries again without spending a turn, so one level gets one retry and a request
// that is simply bad stops within agentteam.MaxCompactionLevel attempts. At the top
// level there is nothing left to remove.
func (e *teamEngine) compactOrExhaust(run agentteam.Run, worker agentteam.Worker, lease *teamLease, message string) (agentteam.Worker, bool) {
	raised := false
	saved, err := e.apply(lease, func(current *agentteam.Worker) error {
		raised = agentteam.RaiseCompaction(current)
		return nil
	})
	if err != nil {
		return worker, false
	}
	if raised {
		return saved, true
	}
	return e.failWorker(run, lease, saved, agentteam.ErrContextExhausted, "The model refused the request with old tool results already removed: "+message, true), false
}

func (e *teamEngine) waitOrFail(run agentteam.Run, worker agentteam.Worker, lease *teamLease, code, message string, retryAfter int) (agentteam.Worker, bool) {
	jitter := e.jitter()
	failed := false
	saved, err := e.apply(lease, func(current *agentteam.Worker) error {
		now := e.now()
		if code == "rate_limited" {
			agentteam.ApplyRateLimit(current, retryAfter, jitter, now)
			return nil
		}
		failed = agentteam.ApplyProviderFailure(current, previewText(message, 500), jitter, now)
		return nil
	})
	if err != nil {
		return worker, false
	}
	if failed {
		appendTeamEvent(e.docs(), teamEvent{Type: "worker.finished", RunID: run.ID, WorkerID: saved.ID, Agent: saved.Agent, Role: saved.Role, Status: agentteam.WorkerFailed, Error: eventError(code, message)})
		if saved.Role == agentteam.RoleCoordinator {
			e.failRun(run.ID, code, message)
		} else {
			e.notifyCoordinator(run, saved)
		}
		return saved, false
	}
	event := teamEvent{Type: "worker.retrying", RunID: run.ID, WorkerID: saved.ID, Agent: saved.Agent, Role: saved.Role, Status: agentteam.WorkerWaitingRetry,
		Attempt: saved.RetryAttempts, NextAttemptAt: saved.NextAttemptAt, Error: eventError(code, message)}
	if retryAfter > 0 {
		event.RetryAfterSeconds = &retryAfter
	}
	appendTeamEvent(e.docs(), event)
	return saved, false
}

// turnMessages sends only what the contract lists, never scheduler state. Old tool
// results go as markers; the stored transcript stays complete.
func turnMessages(worker agentteam.Worker) []turnMessage {
	transcript := agentteam.CompactedTranscript(worker)
	messages := make([]turnMessage, 0, len(transcript))
	for _, item := range transcript {
		message := turnMessage{Role: item.Role, Content: item.Content, ToolCallID: item.ToolCallID}
		for _, call := range item.ToolCalls {
			arguments := call.Arguments
			var object map[string]any
			if json.Unmarshal(arguments, &object) != nil || object == nil {
				arguments = json.RawMessage(`{}`)
			}
			message.ToolCalls = append(message.ToolCalls, turnToolCall{ID: call.ID, Name: eventToolName(call.Name), Arguments: arguments})
		}
		messages = append(messages, message)
	}
	return messages
}

// endStep handles a model turn with no tool call. A worker hands back what it
// said. A coordinator waits for live workers, sleeping until its inbox fills, or
// ends the run when none remain.
func (e *teamEngine) endStep(run agentteam.Run, worker agentteam.Worker, lease *teamLease) (agentteam.Worker, bool) {
	lastSaid := ""
	if n := len(worker.Transcript); n > 0 {
		lastSaid = worker.Transcript[n-1].Content
	}
	report := agentteam.Cap(redact.String(lastSaid), agentteam.MaxToolOutput)
	if worker.Role != agentteam.RoleCoordinator {
		saved, err := e.apply(lease, func(current *agentteam.Worker) error {
			if len(current.Inbox) > 0 {
				return nil
			}
			agentteam.Complete(current, report)
			return nil
		})
		if err != nil {
			return worker, false
		}
		if saved.Status != agentteam.WorkerCompleted {
			return saved, true
		}
		appendTeamEvent(e.docs(), teamEvent{Type: "worker.finished", RunID: run.ID, WorkerID: saved.ID, Agent: saved.Agent, Role: saved.Role, Status: agentteam.WorkerCompleted, OutputPreview: preview(report)})
		e.notifyCoordinator(run, saved)
		return saved, false
	}
	current, err := store.GetDoc[agentteam.Run](e.docs(), teamRunKind, run.ID)
	if err != nil {
		return worker, false
	}
	live := agentteam.LiveWorkers(teamWorkers(e.docs(), current))
	saved, err := e.apply(lease, func(item *agentteam.Worker) error {
		if len(item.Inbox) > 0 {
			return nil
		}
		if live > 0 {
			item.Status = agentteam.WorkerIdle
			return nil
		}
		agentteam.Complete(item, report)
		return nil
	})
	if err != nil {
		return worker, false
	}
	switch saved.Status {
	case agentteam.WorkerCompleted:
		appendTeamEvent(e.docs(), teamEvent{Type: "worker.finished", RunID: run.ID, WorkerID: saved.ID, Agent: saved.Agent, Role: saved.Role, Status: agentteam.WorkerCompleted, OutputPreview: preview(report)})
		e.finishRun(run.ID, report)
		return saved, false
	case agentteam.WorkerIdle:
		return saved, false
	}
	return saved, true
}

// notifyCoordinator moves a hand-back or failure into the coordinator's inbox.
// The ID is stable until NotifyPending clears, so a re-drive cannot deliver twice.
func (e *teamEngine) notifyCoordinator(run agentteam.Run, worker agentteam.Worker) {
	body := fmt.Sprintf("Worker %s (%s) finished with status %s.\n\n%s", worker.ID, worker.Agent, worker.Status, worker.Report)
	if worker.Status == agentteam.WorkerFailed {
		body = fmt.Sprintf("Worker %s (%s) failed: %s: %s", worker.ID, worker.Agent, worker.LastError, worker.LastErrorMessage)
	}
	id := fmt.Sprintf("%s:notify:%d", worker.ID, worker.CompletionGeneration)
	_, err := store.UpdateDoc(e.docs(), teamWorkerKind, run.CoordinatorID, func(current agentteam.Worker, exists bool) (agentteam.Worker, error) {
		if !exists {
			return current, store.ErrNotFound
		}
		if err := agentteam.AddInbox(&current, agentteam.InboxMessage{ID: id, Source: "worker:" + worker.Agent, Kind: "hand-back", Body: body}); err != nil {
			return current, err
		}
		current.UpdatedAt = e.now()
		return current, nil
	}, "", "")
	if err != nil && !errors.Is(err, agentteam.ErrWorkerClosed) {
		return
	}
	_, err = store.UpdateDoc(e.docs(), teamWorkerKind, worker.ID, func(current agentteam.Worker, exists bool) (agentteam.Worker, error) {
		if !exists || !current.NotifyPending || current.CompletionGeneration != worker.CompletionGeneration {
			return current, store.ErrSkipWrite
		}
		current.NotifyPending = false
		return current, nil
	}, "", "")
	if err == nil {
		appendTeamEvent(e.docs(), teamEvent{Type: "worker.message", RunID: run.ID, From: worker.ID, To: run.CoordinatorID, WorkerID: worker.ID, Agent: worker.Agent, MessagePreview: preview(body)})
		kickTeamScheduler()
	}
}

// finishRun ends a running run successfully.
func (e *teamEngine) finishRun(runID, report string) {
	e.endRun(runID, agentteam.RunSucceeded, "", "", report, "team_run.succeeded")
}

// failRun fails a running run. The settle sweep then cancels its live workers.
func (e *teamEngine) failRun(runID, code, message string) {
	e.endRun(runID, agentteam.RunFailed, code, message, "", "team_run.failed")
	kickTeamScheduler()
}

func (e *teamEngine) endRun(runID, status, code, message, report, action string) {
	_, err := store.UpdateDoc(e.docs(), teamRunKind, runID, func(current agentteam.Run, exists bool) (agentteam.Run, error) {
		if !exists || current.Status != agentteam.RunRunning {
			return current, store.ErrSkipWrite
		}
		now := e.now()
		current.Status, current.Error, current.ErrorMessage, current.Report = status, code, previewText(message, 500), report
		current.FinishedAt, current.UpdatedAt = &now, now
		return current, nil
	}, action, teamSchedulerSystem)
	if err != nil {
		log.Printf("team run %s: could not record %s: %v", runID, status, err)
	}
}

// settleRun takes a run that stopped running to its final state: cancels workers
// nobody holds, waits out leases, writes run.finished once, then cleans up.
func (e *teamEngine) settleRun(ctx context.Context, run agentteam.Run, workers []agentteam.Worker) {
	now := e.now()
	settled := true
	for _, worker := range workers {
		if worker.NotifyPending {
			e.clearNotify(worker.ID)
		}
		if worker.Terminal() {
			continue
		}
		if worker.LeaseOwner != "" && worker.LeaseExpiresAt != nil && worker.LeaseExpiresAt.After(now) {
			settled = false
			continue
		}
		e.cancelUnleased(run, worker)
	}
	if !settled {
		return
	}
	if run.SettledAt == nil {
		e.closeApprovals(workers)
		final := sumUsage(teamWorkers(e.docs(), run))
		status := run.Status
		if status == agentteam.RunCancelling {
			status = agentteam.RunCancelled
		}
		if err := writeTeamEvent(e.docs(), teamEvent{Type: "run.finished", RunID: run.ID, Status: status, Usage: &final, Error: runEventError(run)}, true); err != nil {
			return
		}
		action := ""
		if run.Status == agentteam.RunCancelling {
			action = "team_run.cancelled"
		}
		saved, err := store.UpdateDoc(e.docs(), teamRunKind, run.ID, func(current agentteam.Run, exists bool) (agentteam.Run, error) {
			if !exists || current.Status == agentteam.RunRunning {
				return current, store.ErrSkipWrite
			}
			stamp := e.now()
			if current.Status == agentteam.RunCancelling {
				current.Status = agentteam.RunCancelled
				current.FinishedAt = &stamp
			}
			current.SettledAt, current.Usage, current.UpdatedAt = &stamp, final, stamp
			return current, nil
		}, action, teamSchedulerSystem)
		if err != nil {
			return
		}
		run = saved
	}
	if run.SettledAt != nil {
		e.cleanupRun(ctx, run)
	}
}

// closeApprovals expires requests a stopped run left open, so nobody is asked to
// decide something that can no longer happen.
func (e *teamEngine) closeApprovals(workers []agentteam.Worker) {
	for _, worker := range workers {
		for _, message := range worker.Transcript {
			for _, call := range message.ToolCalls {
				if call.Status != agentteam.CallWaitingApproval || call.ApprovalID == "" {
					continue
				}
				_, _ = store.UpdateDoc(e.docs(), agentApprovalKind, call.ApprovalID, func(current agentApproval, exists bool) (agentApproval, error) {
					if !exists || current.view().Status != "pending" {
						return current, store.ErrSkipWrite
					}
					current.ExpiresAt = time.Now().UTC().Add(-time.Second)
					return current, nil
				}, "agent_approval.expired", teamSchedulerSystem)
			}
		}
	}
}

func runEventError(run agentteam.Run) *teamEventError {
	if run.Error == "" {
		return nil
	}
	return eventError(run.Error, run.ErrorMessage)
}

func (e *teamEngine) clearNotify(workerID string) {
	_, _ = store.UpdateDoc(e.docs(), teamWorkerKind, workerID, func(current agentteam.Worker, exists bool) (agentteam.Worker, error) {
		if !exists || !current.NotifyPending {
			return current, store.ErrSkipWrite
		}
		current.NotifyPending = false
		return current, nil
	}, "", "")
}

func (e *teamEngine) cancelUnleased(run agentteam.Run, worker agentteam.Worker) {
	changed := false
	_, err := store.UpdateDoc(e.docs(), teamWorkerKind, worker.ID, func(current agentteam.Worker, exists bool) (agentteam.Worker, error) {
		now := e.now()
		if !exists || current.Terminal() || (current.LeaseOwner != "" && current.LeaseExpiresAt != nil && current.LeaseExpiresAt.After(now)) {
			return current, store.ErrSkipWrite
		}
		current.Status, current.NotifyPending, current.UpdatedAt = agentteam.WorkerCancelled, false, now
		current.LeaseOwner, current.LeaseToken, current.LeaseExpiresAt, current.ActiveSince = "", "", nil, nil
		changed = true
		return current, nil
	}, "", "")
	if err == nil && changed {
		appendTeamEvent(e.docs(), teamEvent{Type: "worker.finished", RunID: run.ID, WorkerID: worker.ID, Agent: worker.Agent, Role: worker.Role, Status: agentteam.WorkerCancelled})
	}
}

// cleanupRun deletes sandboxes after the retention period, so reports can still
// be verified, then drops the run from the scan. The manager's TTL reaper is the
// backstop if the manager stays unreachable past the give-up time.
func (e *teamEngine) cleanupRun(ctx context.Context, run agentteam.Run) {
	workers := teamWorkers(e.docs(), run)
	sandboxes := false
	for _, worker := range workers {
		sandboxes = sandboxes || worker.SandboxID != ""
	}
	if !sandboxes {
		_ = teamIndexSet(e.docs(), run.ID, false)
		return
	}
	since := e.now().Sub(*run.SettledAt)
	if since < e.retain {
		return
	}
	clean := true
	for _, worker := range workers {
		if worker.SandboxID == "" {
			continue
		}
		if err := e.s.teamSandbox().Delete(ctx, worker.SandboxID); err != nil {
			var failure *sandboxError
			if !errors.As(err, &failure) || failure.Code != "not_found" {
				clean = false
				continue
			}
		}
		_, _ = store.UpdateDoc(e.docs(), teamWorkerKind, worker.ID, func(current agentteam.Worker, exists bool) (agentteam.Worker, error) {
			if !exists {
				return current, store.ErrSkipWrite
			}
			current.SandboxID = ""
			return current, nil
		}, "", "")
	}
	if clean || since >= e.retain+teamSandboxGiveUp {
		_ = teamIndexSet(e.docs(), run.ID, false)
	}
}

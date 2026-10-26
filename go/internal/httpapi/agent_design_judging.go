package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
)

// Judged evaluation metrics. A case may ask the designer runner's LLM rubric
// judge to score its reply (groundedness, task completion, relevance,
// safety). It may also name the tool and API nodes the run must call, which
// is scored deterministically from the run's events. A case whose judge is
// unavailable fails with judge_unavailable rather than passing on the
// deterministic checks alone, so a quality gate never passes on scores that
// were never produced.
var judgeCriteria = map[string]bool{"groundedness": true, "task_completion": true, "relevance": true, "safety": true}

const defaultJudgeMinScore = 0.7

type judgeExpectation struct {
	Criteria  []string `json:"criteria"`
	Reference string   `json:"reference,omitempty"`
	MinScore  *float64 `json:"min_score,omitempty"`
}

func (j *judgeExpectation) minScore() float64 {
	if j.MinScore == nil {
		return defaultJudgeMinScore
	}
	return *j.MinScore
}

type judgeScore struct {
	Score  float64 `json:"score"`
	Reason string  `json:"reason,omitempty"`
}
type judgeProvenance struct {
	Backend       string `json:"backend"`
	Model         string `json:"model"`
	RubricVersion string `json:"rubric_version"`
}
type caseReview struct {
	Status     string    `json:"status"`
	Note       string    `json:"note,omitempty"`
	Reviewer   string    `json:"reviewer"`
	ReviewedAt time.Time `json:"reviewed_at"`
}
type sliceResult struct {
	Tag         string             `json:"tag"`
	Cases       int                `json:"cases"`
	Passed      int                `json:"passed"`
	PassRate    float64            `json:"pass_rate"`
	JudgeScores map[string]float64 `json:"judge_scores,omitempty"`
}

func validateJudgeExpectations(label string, expected fixtureExpected) []string {
	issues := []string{}
	if judge := expected.Judge; judge != nil {
		seen := map[string]bool{}
		for _, criterion := range judge.Criteria {
			if !judgeCriteria[criterion] || seen[criterion] {
				issues = append(issues, fmt.Sprintf("Case %s: judge criteria must be distinct names from groundedness, task_completion, relevance, safety.", label))
				break
			}
			seen[criterion] = true
		}
		if len(judge.Criteria) == 0 || len(judge.Reference) > 8000 || judge.minScore() < 0 || judge.minScore() > 1 {
			issues = append(issues, fmt.Sprintf("Case %s: judged cases need 1-4 criteria, a reference of at most 8000 characters and a minimum score from 0 to 1.", label))
		}
	}
	if len(expected.Calls) > 20 {
		issues = append(issues, fmt.Sprintf("Case %s can require at most 20 calls.", label))
	}
	for _, call := range expected.Calls {
		if !designIDPattern.MatchString(call) {
			issues = append(issues, fmt.Sprintf("Case %s: %q is not a node ID.", label, call))
			break
		}
	}
	return issues
}

type completedNode struct {
	Type          string `json:"type"`
	NodeID        string `json:"node_id"`
	Kind          string `json:"kind"`
	OutputPreview string `json:"output_preview"`
}

func runNodes(run designRun) []completedNode {
	nodes := []completedNode{}
	for _, raw := range run.Events {
		var event completedNode
		if json.Unmarshal(raw, &event) == nil && event.Type == "node.completed" {
			nodes = append(nodes, event)
		}
	}
	return nodes
}

// scoreCalls returns the share of required nodes that completed, and a
// missing_call failure for each one that did not.
func scoreCalls(calls []string, run designRun) (*float64, []string) {
	if len(calls) == 0 {
		return nil, nil
	}
	done := map[string]bool{}
	for _, node := range runNodes(run) {
		done[node.NodeID] = true
	}
	failures, hit := []string{}, 0
	for _, call := range calls {
		if done[call] {
			hit++
		} else {
			failures = append(failures, "missing_call: "+call)
		}
	}
	accuracy := float64(hit) / float64(len(calls))
	return &accuracy, failures
}

// retrievedContext is the text the run retrieved (knowledge, dataset and
// API results), the evidence a groundedness verdict is checked against.
func retrievedContext(run designRun) string {
	parts := []string{}
	for _, node := range runNodes(run) {
		if node.Kind == "knowledge" || node.Kind == "storage" || node.Kind == "api" || node.Kind == "tool" {
			parts = append(parts, node.OutputPreview)
		}
	}
	return truncateRunes(strings.Join(parts, "\n\n"), 32000)
}

type judgeVerdict struct {
	Scores map[string]judgeScore `json:"scores"`
	Judge  judgeProvenance       `json:"judge"`
	Usage  designRunUsage        `json:"usage"`
}

var errJudgeUnavailable = errors.New("judge unavailable")

// callJudge asks the designer runner's /judge endpoint to score one reply.
func callJudge(ctx context.Context, judge *judgeExpectation, input, output, context_ string) (judgeVerdict, error) {
	if runnerURL() == "" {
		return judgeVerdict{}, fmt.Errorf("%w: the agent designer runner is not configured", errJudgeUnavailable)
	}
	body, _ := json.Marshal(map[string]any{"criteria": judge.Criteria, "input": input, "output": output,
		"reference": judge.Reference, "context": context_})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, runnerURL()+"/judge", bytes.NewReader(body)) // #nosec G704 -- operator-configured runner
	if err != nil {
		return judgeVerdict{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+os.Getenv("KIONGA_AGENT_DESIGNER_RUNNER_TOKEN"))
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request) // #nosec G704 -- operator-configured runner
	if err != nil {
		return judgeVerdict{}, fmt.Errorf("%w: the runner is unreachable", errJudgeUnavailable)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Detail struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			} `json:"detail"`
		}
		_ = json.Unmarshal(raw, &failure)
		message := failure.Detail.Message
		if message == "" {
			message = fmt.Sprintf("the judge returned %d", response.StatusCode)
		}
		if response.StatusCode == http.StatusServiceUnavailable {
			return judgeVerdict{}, fmt.Errorf("%w: %s", errJudgeUnavailable, message)
		}
		return judgeVerdict{}, errors.New(message)
	}
	var verdict judgeVerdict
	if err := json.Unmarshal(raw, &verdict); err != nil {
		return judgeVerdict{}, errors.New("the judge returned an invalid response")
	}
	for _, criterion := range judge.Criteria {
		score, ok := verdict.Scores[criterion]
		if !ok || score.Score < 0 || score.Score > 1 || math.IsNaN(score.Score) {
			return judgeVerdict{}, fmt.Errorf("the judge returned no valid score for %s", criterion)
		}
	}
	return verdict, nil
}

// judgeCase applies a case's judged expectations to a finished run.
func judgeCase(ctx context.Context, item fixtureCase, run designRun, result *evaluationCase) {
	accuracy, missing := scoreCalls(item.Expected.Calls, run)
	result.ToolAccuracy = accuracy
	result.Failures = append(result.Failures, missing...)
	judge := item.Expected.Judge
	if judge == nil || run.Status != "succeeded" {
		return
	}
	verdict, err := callJudge(ctx, judge, item.Input, run.Output, retrievedContext(run))
	if err != nil {
		reason := "judge_failed"
		if errors.Is(err, errJudgeUnavailable) {
			reason = "judge_unavailable"
		}
		result.Failures = append(result.Failures, reason+": "+err.Error())
		return
	}
	result.Judge, result.JudgeProvenance = verdict.Scores, &verdict.Judge
	result.Usage.InputTokens += verdict.Usage.InputTokens
	result.Usage.OutputTokens += verdict.Usage.OutputTokens
	result.Usage.CostUSD += verdict.Usage.CostUSD
	for _, criterion := range judge.Criteria {
		if score := verdict.Scores[criterion].Score; score < judge.minScore() {
			result.Failures = append(result.Failures, fmt.Sprintf("low_%s: %.2f < %.2f", criterion, score, judge.minScore()))
		}
	}
}

// summarizeJudging computes mean judge scores, tool accuracy and per-tag
// slices over the finished cases.
func (e *agentEvaluation) summarizeJudging() {
	sums, counts := map[string]float64{}, map[string]int{}
	accuracy, accuracyCases := 0.0, 0
	type tally struct {
		cases, passed int
		sums          map[string]float64
		counts        map[string]int
	}
	slices := map[string]*tally{}
	for _, item := range e.Cases {
		if item.Status == "pending" {
			continue
		}
		for criterion, score := range item.Judge {
			sums[criterion] += score.Score
			counts[criterion]++
		}
		if item.ToolAccuracy != nil {
			accuracy += *item.ToolAccuracy
			accuracyCases++
		}
		tags := item.Tags
		if len(tags) == 0 {
			tags = []string{"untagged"}
		}
		for _, tag := range tags {
			slice := slices[tag]
			if slice == nil {
				slice = &tally{sums: map[string]float64{}, counts: map[string]int{}}
				slices[tag] = slice
			}
			slice.cases++
			if item.Passed {
				slice.passed++
			}
			for criterion, score := range item.Judge {
				slice.sums[criterion] += score.Score
				slice.counts[criterion]++
			}
		}
		if item.JudgeProvenance != nil {
			e.Judge = item.JudgeProvenance
		}
	}
	e.JudgeScores = map[string]float64{}
	for criterion, sum := range sums {
		e.JudgeScores[criterion] = math.Round(sum/float64(counts[criterion])*1e4) / 1e4
	}
	e.ToolAccuracy = nil
	if accuracyCases > 0 {
		value := math.Round(accuracy/float64(accuracyCases)*1e4) / 1e4
		e.ToolAccuracy = &value
	}
	e.Slices = []sliceResult{}
	for tag, slice := range slices {
		result := sliceResult{Tag: tag, Cases: slice.cases, Passed: slice.passed, PassRate: float64(slice.passed) / float64(slice.cases)}
		if len(slice.sums) > 0 {
			result.JudgeScores = map[string]float64{}
			for criterion, sum := range slice.sums {
				result.JudgeScores[criterion] = math.Round(sum/float64(slice.counts[criterion])*1e4) / 1e4
			}
		}
		e.Slices = append(e.Slices, result)
	}
	sort.Slice(e.Slices, func(i, j int) bool { return e.Slices[i].Tag < e.Slices[j].Tag })
	e.Reviews = map[string]int{}
	for _, item := range e.Cases {
		if item.Review != nil {
			e.Reviews[item.Review.Status]++
		}
	}
}

// reviewCase records a person's verdict on one evaluated case. Reviews are
// annotations; they do not change the scored pass rate or the gate.
func (s *Server) reviewCase(w http.ResponseWriter, r *http.Request) {
	caller := principal(r)
	if machineCredential(caller) || caller.Credential == "api_token" || !auth.Allowed(caller, http.MethodPost, "/api/v1/agents") {
		writeError(w, 403, "access_denied", "Reviewing cases needs a signed-in person with agents write access.")
		return
	}
	item, ok := s.ownedEvaluation(r)
	if !ok {
		writeError(w, 404, "not_found", "evaluation not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var req struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if (req.Status != "accepted" && req.Status != "rejected" && req.Status != "needs_changes") || len([]rune(req.Note)) > 1000 {
		writeError(w, 422, "validation_error", "status must be accepted, rejected or needs_changes, with a note of at most 1000 characters")
		return
	}
	caseID := r.PathValue("case_id")
	index := -1
	for i, candidate := range item.Cases {
		if candidate.ID == caseID {
			index = i
		}
	}
	if index < 0 {
		writeError(w, 404, "not_found", "case not found")
		return
	}
	updated, err := store.UpdateDoc(s.store, agentEvaluationKind, item.ID, func(current agentEvaluation, exists bool) (agentEvaluation, error) {
		if !exists || index >= len(current.Cases) {
			return agentEvaluation{}, store.ErrNotFound
		}
		current.Cases[index].Review = &caseReview{Status: req.Status, Note: strings.TrimSpace(req.Note), Reviewer: caller.Subject, ReviewedAt: time.Now().UTC()}
		current.summarizeJudging()
		return current, nil
	}, "agent_evaluation.case_reviewed", actor(r))
	writeMutation(w, updated, err, 200)
}

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/agent-evaluations/{id}/cases/{case_id}/review", s.reviewCase)
	})
}

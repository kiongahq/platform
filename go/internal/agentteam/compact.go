package agentteam

import "fmt"

// Compaction keeps a long worker inside the model's context without a model call.
// The runner does none, so a long transcript would end in a provider 400. The
// stored transcript is never changed: a persisted level says which old tool results
// are sent as markers, so one worker document always sends the same messages, across
// restarts and resumes. The level only rises.
const (
	// DefaultCompactThreshold is input tokens on the last turn that raise the level.
	DefaultCompactThreshold = 120000
	// MaxCompactionLevel is the most that can be removed.
	MaxCompactionLevel = 2
)

// compactionKeepTurns is how many recent model turns each level leaves untouched.
func compactionKeepTurns(level int) int {
	if level >= MaxCompactionLevel {
		return 3
	}
	return 10
}

// compactionMarker is deterministic: it has no clock and no random nonce, so one
// transcript always produces identical messages. It holds only gateway-known text.
func compactionMarker(toolName, callID string) string {
	name := nameUnsafe.ReplaceAllString(toolName, "_")
	return Frame("tool:"+name, "tool-result", "compacted-"+callID, fmt.Sprintf("[Output removed to save context. tool=%s call_id=%s. It is kept in the run transcript; repeat the call if you need it.]", name, callID))
}

// protectedFrom returns the index of the keepTurns-th most recent assistant
// message. Tool results from there on are never compacted.
func protectedFrom(transcript []Message, keepTurns int) int {
	seen := 0
	for i := len(transcript) - 1; i >= 0; i-- {
		if transcript[i].Role == "assistant" {
			seen++
			if seen == keepTurns {
				return i
			}
		}
	}
	return 0
}

// RaiseCompaction moves up one level and reports whether it could.
func RaiseCompaction(w *Worker) bool {
	if w.CompactionLevel >= MaxCompactionLevel {
		return false
	}
	w.CompactionLevel++
	return true
}

// AdvanceCompaction acts on a measurement over threshold, once per turn, so a retry
// of the same turn cannot raise the level again. It reports whether the level rose.
func AdvanceCompaction(w *Worker, threshold int) bool {
	if w.LastInputTokens <= threshold || w.Turns <= w.CompactedAtTurn {
		return false
	}
	w.CompactedAtTurn = w.Turns
	return RaiseCompaction(w)
}

// CompactedTranscript is what the model is sent. Every tool message stays in place
// with its call ID, so each call is still answered immediately. Task and other user
// messages, assistant messages and tool call arguments are never changed.
func CompactedTranscript(w Worker) []Message {
	if w.CompactionLevel <= 0 {
		return w.Transcript
	}
	names := map[string]string{}
	for _, message := range w.Transcript {
		for _, call := range message.ToolCalls {
			names[call.ID] = call.Name
		}
	}
	limit := protectedFrom(w.Transcript, compactionKeepTurns(w.CompactionLevel))
	out := make([]Message, len(w.Transcript))
	copy(out, w.Transcript)
	for i := 0; i < limit; i++ {
		if out[i].Role == "tool" {
			out[i].Content = compactionMarker(names[out[i].ToolCallID], out[i].ToolCallID)
		}
	}
	return out
}

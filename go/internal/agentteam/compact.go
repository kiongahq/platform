package agentteam

import "fmt"

// Compaction keeps a long worker inside the model's context without a model call.
// The runner does none, so a long transcript would end in a provider 400. The
// stored transcript is never changed: a persisted watermark says which old tool
// results are sent as markers, so the same worker document always sends the same
// messages, across restarts and resumes.
const (
	// DefaultCompactThreshold is input tokens on the last turn that start compaction.
	DefaultCompactThreshold = 120000
	// CompactKeepTurns is how many recent model turns are never touched.
	CompactKeepTurns = 10
	// approxBytesPerToken estimates what a marker saves. Each later turn measures
	// real input tokens, so an estimate that was too low simply compacts further.
	approxBytesPerToken = 4
)

// compactionMarker is deterministic: it has no clock and no random nonce, so one
// transcript always produces identical messages. It holds only gateway-known text.
func compactionMarker(toolName, callID string) string {
	source := "tool:" + nameUnsafe.ReplaceAllString(toolName, "_")
	return Frame(source, "tool-result", "compacted-"+callID, fmt.Sprintf("[Output removed to save context. tool=%s call_id=%s. It is kept in the run transcript; repeat the call if you need it.]", nameUnsafe.ReplaceAllString(toolName, "_"), callID))
}

func callNames(transcript []Message) map[string]string {
	names := map[string]string{}
	for _, message := range transcript {
		for _, call := range message.ToolCalls {
			names[call.ID] = call.Name
		}
	}
	return names
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

// AdvanceCompaction moves the watermark over the oldest tool results until the
// estimate falls under threshold. It reports true when that cannot be reached,
// because what remains is recent turns, tasks and messages, which stay intact. One
// measurement is acted on once, so a retry of the same turn cannot compact again.
func AdvanceCompaction(w *Worker, threshold, keepTurns int) (exhausted bool) {
	w.CompactedAtTurn = w.Turns
	names := callNames(w.Transcript)
	limit := protectedFrom(w.Transcript, keepTurns)
	remaining := w.LastInputTokens
	index := w.CompactedBefore
	for ; index < limit && remaining > threshold; index++ {
		message := w.Transcript[index]
		if message.Role != "tool" {
			continue
		}
		saved := (len(message.Content) - len(compactionMarker(names[message.ToolCallID], message.ToolCallID))) / approxBytesPerToken
		if saved > 0 {
			remaining -= saved
		}
	}
	if index > w.CompactedBefore {
		w.CompactedBefore = index
	}
	return remaining > threshold
}

// CompactedTranscript is what the model is sent. Every tool message stays in its
// place with its call ID, so each call is still answered immediately.
func CompactedTranscript(w Worker) []Message {
	if w.CompactedBefore <= 0 {
		return w.Transcript
	}
	names := callNames(w.Transcript)
	out := make([]Message, len(w.Transcript))
	copy(out, w.Transcript)
	for i := 0; i < w.CompactedBefore && i < len(out); i++ {
		if out[i].Role == "tool" {
			out[i].Content = compactionMarker(names[out[i].ToolCallID], out[i].ToolCallID)
		}
	}
	return out
}

package httpapi

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// scriptedKubelet serves pod logs from memory the way the kubelet does:
// SinceTime keeps lines at or after it, TailLines keeps the last ones, and a
// pod with no script is an error.
type scriptedKubelet struct {
	mu   sync.Mutex
	logs map[string][]string
}

func newScriptedKubelet(t *testing.T) *scriptedKubelet {
	t.Helper()
	kubelet := &scriptedKubelet{logs: map[string][]string{}}
	previous := openPodLog
	openPodLog = kubelet.open
	t.Cleanup(func() { openPodLog = previous })
	return kubelet
}

// write appends lines to a pod's log: "<offset> <text>" is stamped base+offset.
func (k *scriptedKubelet) write(pod string, base time.Time, lines ...string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, line := range lines {
		offset, text, _ := strings.Cut(line, " ")
		after, err := time.ParseDuration(offset)
		if err != nil {
			panic(err)
		}
		k.logs[pod] = append(k.logs[pod], base.Add(after).UTC().Format(time.RFC3339Nano)+" "+text)
	}
}

func (k *scriptedKubelet) open(_ context.Context, _ *kubeAccess, pod string, options *corev1.PodLogOptions) (io.ReadCloser, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	lines, known := k.logs[pod]
	if !known {
		return nil, errors.New("container is waiting to start")
	}
	kept := []string{}
	for _, line := range lines {
		at, _ := parseLogLine(line)
		if options.SinceTime != nil && !at.IsZero() && at.Before(options.SinceTime.Time) {
			continue
		}
		kept = append(kept, line)
	}
	if options.TailLines != nil && int64(len(kept)) > *options.TailLines {
		kept = kept[int64(len(kept))-*options.TailLines:]
	}
	text := ""
	if len(kept) > 0 {
		text = strings.Join(kept, "\n") + "\n"
	}
	return io.NopCloser(strings.NewReader(text)), nil
}

type followedLine struct {
	ID, Pod, Line string
}

// follow runs one connection of the stream to its end and returns the log
// events in the order they were sent.
func follow(t *testing.T, resumeID, query string) []followedLine {
	t.Helper()
	access, _ := kubeAccessFactory()
	request := newTestRequest(http.MethodGet, "/api/v1/applications/billing/logs?follow=true&max_seconds=30"+query, nil)
	if resumeID != "" {
		request.Header.Set("Last-Event-ID", resumeID)
	}
	recorder := httptest.NewRecorder()
	tail := int64(defaultLogTail)
	if _, value, found := strings.Cut(query, "tail="); found {
		_, _ = fmt.Sscanf(value, "%d", &tail)
	}
	(&Server{}).streamApplicationLogs(recorder, request, access, "billing", "", tail)
	if recorder.Code != http.StatusOK {
		t.Fatalf("follow: %d %s", recorder.Code, recorder.Body.String())
	}
	out, id, kind := []followedLine{}, "", ""
	scanner := bufio.NewScanner(recorder.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		field, value, _ := strings.Cut(scanner.Text(), ": ")
		switch field {
		case "id":
			id = value
		case "event":
			kind = value
		case "data":
			if kind == "log" {
				var line logLine
				if err := json.Unmarshal([]byte(value), &line); err != nil {
					t.Fatalf("data %q: %v", value, err)
				}
				out = append(out, followedLine{ID: id, Pod: line.Pod, Line: line.Line})
			}
		case "":
			id, kind = "", ""
		}
	}
	return out
}

// byPod groups delivered lines per pod, in delivery order.
func byPod(lines []followedLine) map[string][]string {
	out := map[string][]string{}
	for _, line := range lines {
		out[line.Pod] = append(out[line.Pod], line.Line)
	}
	return out
}

func expectLines(t *testing.T, label string, got []followedLine, want map[string][]string) {
	t.Helper()
	grouped := byPod(got)
	if len(grouped) != len(want) {
		t.Fatalf("%s: delivered %v, want %v", label, grouped, want)
	}
	for pod, lines := range want {
		if !slices.Equal(grouped[pod], lines) {
			t.Fatalf("%s: pod %s delivered %v, want %v (all: %v)", label, pod, grouped[pod], lines, grouped)
		}
	}
}

func lastID(t *testing.T, lines []followedLine) string {
	t.Helper()
	if len(lines) == 0 || lines[len(lines)-1].ID == "" {
		t.Fatalf("no id to resume from: %v", lines)
	}
	return lines[len(lines)-1].ID
}

func TestLogCursorRoundTripsAndStaysSmall(t *testing.T) {
	base := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	cursor := logCursor{floor: base.UnixNano(), pods: map[logKey]podPosition{}}
	for i := range maxLogPods {
		pod := fmt.Sprintf("billing-7d9f8c6b5d-%05d-with-a-long-deployment-name", i)
		cursor.pods[podLogKey(pod, "application-container")] = podPosition{at: base.Add(time.Duration(i)*37*time.Minute).UnixNano() + 123456789, count: uint64(i)}
	}
	id := cursor.encode()
	if len(id) > 480 || strings.ContainsAny(id, "\r\n ") || !strings.HasPrefix(id, logCursorPrefix) {
		t.Fatalf("a full cursor must stay a small single token, got %d characters: %s", len(id), id)
	}
	if id != cursor.encode() {
		t.Fatal("the same position must encode to the same id")
	}
	decoded, err := decodeLogCursor(id)
	if err != nil || decoded.floor != cursor.floor || len(decoded.pods) != maxLogPods {
		t.Fatalf("decode: %+v %v", decoded, err)
	}
	for key, position := range cursor.pods {
		if decoded.pods[key] != position {
			t.Fatalf("key %x: %+v, want %+v", key, decoded.pods[key], position)
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, id); err == nil {
		t.Fatal("a cursor must not parse as a timestamp")
	}
	if podLogKey("a", "bc") == podLogKey("ab", "c") {
		t.Fatal("pod and container must not run together in the key")
	}
}

func TestLogCursorRejectsWhatItDidNotIssue(t *testing.T) {
	entry := func(raw []byte, key byte, delta int64, count uint64) []byte {
		raw = append(raw, key, 0, 0, 0, 0, 0, 0, 0)
		return binary.AppendUvarint(binary.AppendVarint(raw, delta), count)
	}
	header := func(total uint64) []byte { return binary.AppendUvarint(binary.AppendVarint(nil, 1000), total) }
	wrap := func(raw []byte) string { return logCursorPrefix + base64.RawURLEncoding.EncodeToString(raw) }
	tooMany := header(maxLogPods + 1)
	for i := range maxLogPods + 1 {
		tooMany = entry(tooMany, byte(i), 1, 1)
	}
	for name, id := range map[string]string{
		"no prefix":         base64.RawURLEncoding.EncodeToString(entry(header(1), 1, 1, 1)),
		"other version":     "v2." + base64.RawURLEncoding.EncodeToString(entry(header(1), 1, 1, 1)),
		"not base64url":     logCursorPrefix + "!!!",
		"padded base64":     wrap(entry(header(1), 1, 1, 1)) + "=",
		"empty":             logCursorPrefix,
		"missing entry":     wrap(header(1)),
		"truncated entry":   wrap(entry(header(2), 1, 1, 1)[:14]),
		"trailing bytes":    wrap(append(entry(header(1), 1, 1, 1), 7)),
		"duplicate pod":     wrap(entry(entry(header(2), 1, 1, 1), 1, 2, 1)),
		"more than 20 pods": wrap(tooMany),
		"oversized":         logCursorPrefix + strings.Repeat("A", maxLogCursorLength),
	} {
		if _, err := decodeLogCursor(id); !errors.Is(err, errLogCursor) {
			t.Errorf("%s: %q decoded, err=%v", name, id, err)
		}
	}
	if cursor, err := decodeLogCursor(wrap(entry(header(1), 1, -5, 3))); err != nil || cursor.pods[logKey{1}] != (podPosition{at: 995, count: 3}) {
		t.Fatalf("a well-formed cursor must decode: %+v %v", cursor, err)
	}
}

func TestPodResumeSkipsExactlyWhatWasDelivered(t *testing.T) {
	at := time.Date(2026, 11, 24, 10, 0, 0, 500, time.UTC)
	resume := podResume{anchored: true, at: at, skip: 2}
	got := []bool{}
	for _, line := range []time.Time{at.Add(-time.Nanosecond), {}, at, at, at, at.Add(-time.Second), {}, at.Add(time.Second)} {
		got = append(got, resume.keep(line))
	}
	// Before the position, a line without an instant ahead of it, the two
	// delivered at the instant; then the third at that instant and all after.
	if want := []bool{false, false, false, false, true, true, true, true}; !slices.Equal(got, want) {
		t.Fatalf("keep: %v, want %v", got, want)
	}
	// A container that restarted has only newer lines: nothing is skipped.
	restarted := podResume{anchored: true, at: at, skip: 2}
	if !restarted.keep(at.Add(time.Minute)) || !restarted.keep(at.Add(time.Minute)) {
		t.Fatal("lines after the position must all be kept")
	}
}

// The limit this cursor removes: pod b delivers a line more than five seconds
// late, after pod a delivered a newer one. Resuming from one instant loses
// it; resuming from the cursor does not.
func TestFollowResumeRecoversAPodsLateLineAfterANewerLineFromAnotherPod(t *testing.T) {
	newKubeHarness(t, appPod("billing-a", "billing", "billing-rs"), appPod("billing-b", "billing", "billing-rs"))
	kubelet, base := newScriptedKubelet(t), time.Now().Add(-30*time.Second).Truncate(time.Second)
	kubelet.write("billing-a", base, "1s a1", "20s a2")
	kubelet.write("billing-b", base, "2s b1")
	first := follow(t, "", "")
	expectLines(t, "first connection", first, map[string][]string{"billing-a": {"a1", "a2"}, "billing-b": {"b1"}})

	// b's line stamped 12s reaches the kubelet only now: 8 seconds older than
	// a2, which the client already has.
	kubelet.write("billing-b", base, "12s b2-late", "21s b3")
	kubelet.write("billing-a", base, "22s a3")
	resumed := follow(t, lastID(t, first), "")
	expectLines(t, "resume", resumed, map[string][]string{"billing-a": {"a3"}, "billing-b": {"b2-late", "b3"}})

	if again := follow(t, lastID(t, resumed), ""); len(again) != 0 {
		t.Fatalf("nothing is new after the last id, got %v", again)
	}
}

// Resuming from the id of any event, not only the last, delivers exactly the
// lines sent after it: each pod's remaining lines, in order, once.
func TestFollowResumeIsExactFromEveryEvent(t *testing.T) {
	newKubeHarness(t, appPod("billing-a", "billing", "billing-rs"), appPod("billing-b", "billing", "billing-rs"), appPod("billing-c", "billing", "billing-rs"))
	kubelet, base := newScriptedKubelet(t), time.Now().Add(-30*time.Second).Truncate(time.Second)
	kubelet.write("billing-a", base, "1s tick", "1s tick", "1s tick", "1500ms a-mid", "3s tick", "3s tick")
	kubelet.write("billing-b", base, "1s tick", "9s b-late", "9s b-late")
	kubelet.write("billing-c", base, "2s c1")
	all := map[string][]string{"billing-a": {"tick", "tick", "tick", "a-mid", "tick", "tick"}, "billing-b": {"tick", "b-late", "b-late"}, "billing-c": {"c1"}}
	first := follow(t, "", "")
	expectLines(t, "first connection", first, all)
	for sent := range first {
		want, seen := map[string][]string{}, byPod(first[:sent+1])
		for pod, lines := range all {
			if rest := lines[len(seen[pod]):]; len(rest) > 0 {
				want[pod] = rest
			}
		}
		expectLines(t, fmt.Sprintf("resume after event %d", sent+1), follow(t, first[sent].ID, ""), want)
	}
}

func TestFollowResumeCountsIdenticalLinesAtOneInstant(t *testing.T) {
	newKubeHarness(t, appPod("billing-a", "billing", "billing-rs"))
	kubelet, base := newScriptedKubelet(t), time.Now().Add(-30*time.Second).Truncate(time.Second)
	kubelet.write("billing-a", base, "1s old", "5s same", "5s same")
	first := follow(t, "", "")
	expectLines(t, "first connection", first, map[string][]string{"billing-a": {"old", "same", "same"}})

	// Two more identical lines share the instant: both are new, the two
	// delivered are not sent again.
	kubelet.write("billing-a", base, "5s same", "5s same", "6s next")
	resumed := follow(t, lastID(t, first), "")
	expectLines(t, "resume", resumed, map[string][]string{"billing-a": {"same", "same", "next"}})

	// A tail that would cut through lines sharing an instant starts at the
	// first of them, so the count in the cursor is exact.
	kubelet.logs = map[string][]string{}
	kubelet.write("billing-a", base, "1s old", "5s same", "5s same", "5s same")
	tailed := follow(t, "", "&tail=1")
	expectLines(t, "tail=1", tailed, map[string][]string{"billing-a": {"same", "same", "same"}})
	kubelet.write("billing-a", base, "5s same")
	expectLines(t, "resume after tail", follow(t, lastID(t, tailed), ""), map[string][]string{"billing-a": {"same"}})
}

func TestFollowResumeWithAPodThatAppearedAndOneThatDisappeared(t *testing.T) {
	harness := newKubeHarness(t, appPod("billing-a", "billing", "billing-rs"), appPod("billing-b", "billing", "billing-rs"))
	kubelet, base := newScriptedKubelet(t), time.Now().Add(-30*time.Second).Truncate(time.Second)
	kubelet.write("billing-a", base, "10s a1")
	kubelet.write("billing-b", base, "11s b1")
	first := follow(t, "", "")
	expectLines(t, "first connection", first, map[string][]string{"billing-a": {"a1"}, "billing-b": {"b1"}})
	issued, err := decodeLogCursor(lastID(t, first))
	if err != nil || len(issued.pods) != 2 {
		t.Fatalf("cursor: %+v %v", issued, err)
	}

	// b is gone. c appeared after the cursor was issued; its lines are older
	// than a1 on the clock and all of them are new to the client.
	pods := harness.core.CoreV1().Pods(testNamespace)
	if err := pods.Delete(context.Background(), "billing-b", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := pods.Create(context.Background(), appPod("billing-c", "billing", "billing-rs"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	kubelet.write("billing-c", base, "3s c1", "3s c1", "4s c2")
	kubelet.write("billing-a", base, "12s a2")
	resumed := follow(t, lastID(t, first), "")
	expectLines(t, "resume", resumed, map[string][]string{"billing-a": {"a2"}, "billing-c": {"c1", "c1", "c2"}})

	// The new cursor places a and c, drops b and keeps the floor.
	next, err := decodeLogCursor(lastID(t, resumed))
	if err != nil || len(next.pods) != 2 || next.floor != issued.floor {
		t.Fatalf("cursor after the change: %+v %v", next, err)
	}
	if _, kept := next.pods[podLogKey("billing-b", "app")]; kept {
		t.Fatal("a pod that is gone must leave the cursor")
	}
	if again := follow(t, lastID(t, resumed), ""); len(again) != 0 {
		t.Fatalf("nothing is new, got %v", again)
	}

	// A pod whose container has not started is reported, and does not stop
	// the others or enter the cursor.
	if _, err := pods.Create(context.Background(), appPod("billing-d", "billing", "billing-rs"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	kubelet.write("billing-a", base, "13s a3")
	waiting := follow(t, lastID(t, resumed), "")
	expectLines(t, "waiting pod", waiting, map[string][]string{"billing-a": {"a3"}, "billing-d": {"log stream unavailable: container is waiting to start"}})
}

func TestFollowStillResumesFromATimestamp(t *testing.T) {
	newKubeHarness(t, appPod("billing-a", "billing", "billing-rs"), appPod("billing-b", "billing", "billing-rs"))
	kubelet, base := newScriptedKubelet(t), time.Now().Add(-30*time.Second).Truncate(time.Second)
	kubelet.write("billing-a", base, "1s a1", "5s a2", "5500ms a3", "7s a4")
	kubelet.write("billing-b", base, "2s b1", "5s b2", "6s b3")
	stamp := func(after time.Duration) string { return base.Add(after).UTC().Format(time.RFC3339Nano) }
	want := map[string][]string{"billing-a": {"a3", "a4"}, "billing-b": {"b3"}}

	// An old client: every pod skips lines at or before the instant.
	expectLines(t, "Last-Event-ID timestamp", follow(t, stamp(5*time.Second), ""), want)
	expectLines(t, "since timestamp", follow(t, "", "&since="+stamp(5*time.Second)), want)
	expectLines(t, "the later of two timestamps", follow(t, stamp(5*time.Second), "&since="+stamp(time.Second)), want)

	// Its events carry cursors from then on, and resuming with one is exact.
	resumed := follow(t, stamp(5*time.Second), "")
	cursor, err := decodeLogCursor(lastID(t, resumed))
	if err != nil || cursor.floor != base.Add(5*time.Second).UnixNano() || len(cursor.pods) != 2 {
		t.Fatalf("cursor after a timestamp resume: %+v %v", cursor, err)
	}
	kubelet.write("billing-b", base, "6s b3", "6500ms b4-late")
	expectLines(t, "cursor after a timestamp", follow(t, lastID(t, resumed), ""), map[string][]string{"billing-b": {"b3", "b4-late"}})

	// A cursor wins over a timestamp, in either place.
	expectLines(t, "cursor over since", follow(t, lastID(t, resumed), "&since="+stamp(time.Second)), map[string][]string{"billing-b": {"b3", "b4-late"}})
	expectLines(t, "cursor in since", follow(t, stamp(time.Second), "&since="+lastID(t, resumed)), map[string][]string{"billing-b": {"b3", "b4-late"}})
}

func TestFollowRejectsAMalformedCursor(t *testing.T) {
	harness := newKubeHarness(t, appPod("billing-7d9-abcde", "billing", "billing-7d9"))
	projectID := createTestProject(t, harness.server)
	if created := harness.do(t, http.MethodPost, "/api/v1/applications", createAppBody(projectID, "")); created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	for _, id := range []string{"v1.!!!", "v1.", "v1.AAAA_trailing", "v9.AAAA", "not-a-timestamp"} {
		request := newTestRequest(http.MethodGet, "/api/v1/applications/billing/logs?follow=true&max_seconds=5", nil)
		request.Header.Set("Last-Event-ID", id)
		refused := httptest.NewRecorder()
		harness.server.ServeHTTP(refused, request)
		if refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "invalid_request") || refused.Header().Get("Content-Type") == "text/event-stream" {
			t.Fatalf("Last-Event-ID %q: %d %s", id, refused.Code, refused.Body.String())
		}
	}
	if refused := harness.do(t, http.MethodGet, "/api/v1/applications/billing/logs?follow=true&since=v1.!!!", ""); refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "not a log cursor") {
		t.Fatalf("since cursor: %d %s", refused.Code, refused.Body.String())
	}
}

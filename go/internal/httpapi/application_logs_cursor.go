package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// A followed log stream is resumed from a cursor: for each pod and container,
// the instant of the last line delivered and how many lines at that exact
// instant were delivered. Pods stream concurrently, so one instant for the
// whole stream cannot say what each pod has delivered; the cursor can.
//
// The cursor travels as the id of every log event:
//
//	v1.<base64url of: varint floor, uvarint n, n × (8-byte key, varint instant-floor, uvarint count)>
//
// Instants are nanoseconds since the Unix epoch. The key is the first 8 bytes
// of SHA-256 over "pod\x00container", which keeps the id near 16 bytes per
// pod: about 440 characters for the 20 pods a stream may carry. The floor is
// where pods without an entry start (lines at or before it are skipped): the
// instant an old client resumed from, or just before the stream first opened.
//
// The cursor is a position, not a credential: it only selects which lines of
// logs the caller may already read are skipped, so it is not signed.

const (
	logCursorPrefix = "v1."
	// maxLogCursorLength bounds what is decoded: a full cursor is about 440
	// characters.
	maxLogCursorLength = 1024
	// logFloorAllowance is how far before a new stream's start its floor
	// sits, so a pod that first writes after the stream opened is read from
	// its first line even when its node's clock runs behind the gateway's.
	logFloorAllowance = time.Minute
)

type logKey [8]byte

func podLogKey(pod, container string) logKey {
	sum := sha256.Sum256([]byte(pod + "\x00" + container))
	var key logKey
	copy(key[:], sum[:])
	return key
}

// podPosition is one pod's place in its log: count lines stamped exactly at
// were delivered, and every line before them.
type podPosition struct {
	at    int64
	count uint64
}

type logCursor struct {
	floor int64
	pods  map[logKey]podPosition
}

// advance records one delivered line. A line stamped before the pod's
// position (a node clock that stepped back) leaves the position unchanged.
func (c *logCursor) advance(key logKey, at time.Time) {
	nanos := at.UnixNano()
	position, known := c.pods[key]
	switch {
	case !known || nanos > position.at:
		c.pods[key] = podPosition{at: nanos, count: 1}
	case nanos == position.at:
		position.count++
		c.pods[key] = position
	}
}

func (c logCursor) encode() string {
	keys := make([]logKey, 0, len(c.pods))
	for key := range c.pods {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
	raw := binary.AppendVarint(make([]byte, 0, 12+len(keys)*18), c.floor)
	raw = binary.AppendUvarint(raw, uint64(len(keys)))
	for _, key := range keys {
		position := c.pods[key]
		raw = append(raw, key[:]...)
		raw = binary.AppendVarint(raw, position.at-c.floor)
		raw = binary.AppendUvarint(raw, position.count)
	}
	return logCursorPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

var errLogCursor = errors.New("the resume point is not a log cursor this gateway issued; reconnect with since set to an RFC 3339 timestamp, or without a resume point")

func decodeLogCursor(text string) (logCursor, error) {
	body, ok := strings.CutPrefix(text, logCursorPrefix)
	if !ok || len(text) > maxLogCursorLength {
		return logCursor{}, errLogCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return logCursor{}, errLogCursor
	}
	floor, used := binary.Varint(raw)
	if used <= 0 {
		return logCursor{}, errLogCursor
	}
	raw = raw[used:]
	total, used := binary.Uvarint(raw)
	if used <= 0 || total > maxLogPods {
		return logCursor{}, errLogCursor
	}
	raw = raw[used:]
	cursor := logCursor{floor: floor, pods: make(map[logKey]podPosition, total)}
	for range total {
		var key logKey
		if len(raw) < len(key) {
			return logCursor{}, errLogCursor
		}
		copy(key[:], raw)
		raw = raw[len(key):]
		delta, used := binary.Varint(raw)
		if used <= 0 {
			return logCursor{}, errLogCursor
		}
		raw = raw[used:]
		count, used := binary.Uvarint(raw)
		if used <= 0 {
			return logCursor{}, errLogCursor
		}
		raw = raw[used:]
		if _, duplicate := cursor.pods[key]; duplicate {
			return logCursor{}, errLogCursor
		}
		cursor.pods[key] = podPosition{at: floor + delta, count: count}
	}
	if len(raw) != 0 {
		return logCursor{}, errLogCursor
	}
	return cursor, nil
}

// logResumePoint reads where a followed stream resumes, from ?since= and
// Last-Event-ID. Each may be a cursor (the id of a log event) or, as older
// clients send, an RFC 3339 timestamp. A cursor wins over a timestamp and
// Last-Event-ID wins between two cursors; between two timestamps the later
// wins, and it becomes the floor every pod resumes from. resumed is false
// when neither is set. Anything else is an error: silently starting from the
// tail would repeat lines the client already has.
func logResumePoint(r *http.Request) (cursor logCursor, resumed bool, err error) {
	var since time.Time
	for _, raw := range []string{r.URL.Query().Get("since"), r.Header.Get("Last-Event-ID")} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, logCursorPrefix) {
			if cursor, err = decodeLogCursor(raw); err != nil {
				return logCursor{}, false, err
			}
			resumed = true
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return logCursor{}, false, fmt.Errorf("since must be an RFC 3339 timestamp or the id of the last event received")
		}
		if parsed.After(since) {
			since = parsed
		}
	}
	if resumed {
		return cursor, true, nil
	}
	if since.IsZero() {
		return logCursor{}, false, nil
	}
	return logCursor{floor: since.UnixNano(), pods: map[logKey]podPosition{}}, true, nil
}

// podResume filters one pod's lines on a connection, in log order, down to
// the ones the client does not have. It is used by one goroutine.
type podResume struct {
	// anchored: the pod has a position. Lines before at are skipped, then
	// the first skip lines stamped exactly at.
	anchored bool
	at       time.Time
	skip     uint64
	// floor applies to a pod without a position: lines at or before it are
	// skipped. Zero skips nothing.
	floor time.Time
	// passed is set at the first line kept; every later line is kept.
	passed bool
}

func (p *podResume) keep(at time.Time) bool {
	if p.passed {
		return true
	}
	if p.anchored {
		// A line without an instant ahead of the position was delivered
		// before the position was.
		if at.IsZero() || at.Before(p.at) {
			return false
		}
		if at.Equal(p.at) && p.skip > 0 {
			p.skip--
			return false
		}
	} else if !p.floor.IsZero() && !at.IsZero() && !at.After(p.floor) {
		return false
	}
	p.passed = true
	return true
}

// podStart is how one pod's log is opened on a connection.
type podStart struct {
	// since is the kubelet's SinceTime (second precision); zero reads the
	// log from its start, unless tail is set.
	since time.Time
	// tail reads the last tail lines, with nothing skipped.
	tail   bool
	resume podResume
}

// resumedPodStart opens a pod from the client's cursor: from its position,
// or from the floor when the cursor has none for it (a pod that started
// after the cursor was issued, or had not written yet).
func resumedPodStart(cursor logCursor, key logKey) podStart {
	if position, known := cursor.pods[key]; known {
		at := time.Unix(0, position.at)
		return podStart{since: at, resume: podResume{anchored: true, at: at, skip: position.count}}
	}
	if cursor.floor == 0 {
		return podStart{tail: true}
	}
	floor := time.Unix(0, cursor.floor)
	return podStart{since: floor, resume: podResume{floor: floor}}
}

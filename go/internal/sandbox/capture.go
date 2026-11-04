package sandbox

import (
	"fmt"
	"strings"
)

// capture keeps the head and tail of an untrusted stream in bounded memory and
// counts everything it was given. Test output usually matters most at the end, so
// plain head-truncation would hide exactly what a worker needs. It never returns an
// error: a process blocked on a full pipe would be a worse outcome than dropped text.
type capture struct {
	limit int
	head  []byte
	tail  []byte // ring buffer once full
	next  int
	total int64
	// onFlood fires once when total passes flood, so `yes` cannot burn the manager's
	// CPU for the full exec timeout.
	flood   int64
	onFlood func()
	flooded bool
}

func newCapture(limit int) *capture { return &capture{limit: limit} }

func (c *capture) Write(data []byte) (int, error) {
	written := len(data)
	c.total += int64(written)
	if c.flood > 0 && c.total > c.flood && !c.flooded {
		c.flooded = true
		if c.onFlood != nil {
			c.onFlood()
		}
	}
	half := c.limit / 2
	if room := half - len(c.head); room > 0 {
		take := min(room, len(data))
		c.head = append(c.head, data[:take]...)
		data = data[take:]
	}
	tailSize := c.limit - half
	if len(data) >= tailSize {
		c.tail = append(c.tail[:0], data[len(data)-tailSize:]...)
		c.next = 0
		return written, nil
	}
	for len(data) > 0 {
		if len(c.tail) < tailSize {
			take := min(tailSize-len(c.tail), len(data))
			c.tail = append(c.tail, data[:take]...)
			data = data[take:]
			continue
		}
		copied := copy(c.tail[c.next:], data)
		c.next = (c.next + copied) % tailSize
		data = data[copied:]
	}
	return written, nil
}

func (c *capture) truncated() bool { return c.total > int64(c.limit) }

// String returns valid UTF-8 no longer than limit bytes before replacement.
func (c *capture) String() string {
	ordered := append(append([]byte(nil), c.tail[c.next:]...), c.tail[:c.next]...)
	if !c.truncated() {
		return strings.ToValidUTF8(string(c.head)+string(ordered), "�")
	}
	marker := fmt.Sprintf("\n[... %d bytes truncated ...]\n", c.total-int64(c.limit))
	if len(ordered) > len(marker) {
		ordered = ordered[len(marker):]
	}
	return strings.ToValidUTF8(string(c.head)+marker+string(ordered), "�")
}

// headBuffer keeps the first limit bytes and notes whether more arrived. It is for
// structured output that a tail would corrupt.
type headBuffer struct {
	limit    int
	data     []byte
	overflow bool
}

func (b *headBuffer) Write(data []byte) (int, error) {
	if room := b.limit - len(b.data); room > 0 {
		take := min(room, len(data))
		b.data = append(b.data, data[:take]...)
		b.overflow = b.overflow || take < len(data)
		return len(data), nil
	}
	b.overflow = b.overflow || len(data) > 0
	return len(data), nil
}

package metrics

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Collector struct {
	mu      sync.RWMutex
	client  *http.Client
	targets map[string]string
	up      map[string]float64
	checked map[string]time.Time
}

func New(targets map[string]string) *Collector {
	return &Collector{client: &http.Client{Timeout: 3 * time.Second}, targets: targets, up: make(map[string]float64), checked: make(map[string]time.Time)}
}

func (c *Collector) Poll(ctx context.Context) {
	for name, endpoint := range c.targets {
		up := 0.0
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err == nil {
			response, requestErr := c.client.Do(request)
			if requestErr == nil {
				if response.StatusCode >= 200 && response.StatusCode < 300 {
					up = 1
				}
				_ = response.Body.Close()
			}
		}
		c.mu.Lock()
		c.up[name], c.checked[name] = up, time.Now().UTC()
		c.mu.Unlock()
	}
}

func (c *Collector) Prometheus() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var b strings.Builder
	b.WriteString("# HELP kionga_component_up Whether a platform component health endpoint is reachable.\n# TYPE kionga_component_up gauge\n")
	names := make([]string, 0, len(c.targets))
	for name := range c.targets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		_, _ = fmt.Fprintf(&b, "kionga_component_up{component=%q} %g\n", name, c.up[name])
	}
	b.WriteString("# HELP kionga_component_last_check_timestamp_seconds Last component health check.\n# TYPE kionga_component_last_check_timestamp_seconds gauge\n")
	for _, name := range names {
		_, _ = fmt.Fprintf(&b, "kionga_component_last_check_timestamp_seconds{component=%q} %d\n", name, c.checked[name].Unix())
	}
	return b.String()
}

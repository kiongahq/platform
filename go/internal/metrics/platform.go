package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// PlatformCollector aggregates the platform metrics that
// Prometheus cannot scrape directly: agent token usage and cost from the
// gateway, live session counts, pipeline run outcomes, and real-time
// processing statistics. All values are read from the gateway's control-plane
// API — measured state, never estimated.
type PlatformCollector struct {
	GatewayURL string
	Token      string

	mu      sync.RWMutex
	client  *http.Client
	metrics map[string]float64
	scraped time.Time
}

func NewPlatformCollector(gatewayURL, token string) *PlatformCollector {
	return &PlatformCollector{
		GatewayURL: strings.TrimRight(gatewayURL, "/"),
		Token:      token,
		client:     &http.Client{Timeout: 5 * time.Second},
		metrics:    map[string]float64{},
	}
}

func (p *PlatformCollector) get(ctx context.Context, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.GatewayURL+path, nil)
	if err != nil {
		return err
	}
	if p.Token != "" {
		request.Header.Set("Authorization", "Bearer "+p.Token)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway returned %s for %s", response.Status, path)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(target)
}

// Poll refreshes every platform metric from gateway state.
func (p *PlatformCollector) Poll(ctx context.Context) error {
	if p.GatewayURL == "" {
		return nil
	}
	collected := map[string]float64{}

	var agents struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := p.get(ctx, "/api/v1/agents", &agents); err == nil {
		for _, agent := range agents.Items {
			var usage struct {
				Sessions     int     `json:"sessions"`
				Active       int     `json:"active"`
				InputTokens  float64 `json:"input_tokens"`
				OutputTokens float64 `json:"output_tokens"`
				CostUSD      float64 `json:"cost_usd"`
			}
			if err := p.get(ctx, "/api/v1/agents/"+agent.ID+"/usage", &usage); err != nil {
				continue
			}
			label := fmt.Sprintf("{agent=%q}", agent.ID)
			collected["kionga_agent_active_sessions"+label] = float64(usage.Active)
			collected["kionga_agent_sessions_total"+label] = float64(usage.Sessions)
			collected["kionga_llm_tokens_used_total"+fmt.Sprintf("{agent=%q,type=%q}", agent.ID, "input")] = usage.InputTokens
			collected["kionga_llm_tokens_used_total"+fmt.Sprintf("{agent=%q,type=%q}", agent.ID, "output")] = usage.OutputTokens
			collected["kionga_llm_cost_usd_total"+label] = usage.CostUSD
		}
	}

	var runs []struct {
		Status string `json:"status"`
	}
	if err := p.get(ctx, "/api/v1/pipelines/runs", &runs); err == nil {
		byStatus := map[string]float64{}
		for _, run := range runs {
			byStatus[run.Status]++
		}
		for status, count := range byStatus {
			collected[fmt.Sprintf("kionga_pipeline_runs_total{status=%q}", status)] = count
		}
	}

	var realtime struct {
		Demos map[string]map[string]any `json:"demos"`
	}
	if err := p.get(ctx, "/api/v1/realtime", &realtime); err == nil {
		for demo, stats := range realtime.Demos {
			if events, ok := stats["events"].(float64); ok {
				collected[fmt.Sprintf("kionga_realtime_events_total{demo=%q}", demo)] = events
			}
			if latency, ok := stats["avg_latency_ms"].(float64); ok {
				collected[fmt.Sprintf("kionga_realtime_avg_latency_ms{demo=%q}", demo)] = latency
			}
			if flagged, ok := stats["flagged"].(float64); ok {
				collected[fmt.Sprintf("kionga_realtime_flagged_total{demo=%q}", demo)] = flagged
			}
		}
	}

	p.mu.Lock()
	p.metrics, p.scraped = collected, time.Now().UTC()
	p.mu.Unlock()
	return nil
}

// Prometheus renders the collected metrics in exposition format.
func (p *PlatformCollector) Prometheus() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	keys := make([]string, 0, len(p.metrics))
	for key := range p.metrics {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Platform metrics aggregated from control-plane state.\n")
	for _, key := range keys {
		_, _ = fmt.Fprintf(&b, "%s %g\n", key, p.metrics[key])
	}
	if !p.scraped.IsZero() {
		_, _ = fmt.Fprintf(&b, "kionga_platform_last_scrape_timestamp_seconds %d\n", p.scraped.Unix())
	}
	return b.String()
}

package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/ml-ai-ops/platform/internal/feature"
	"github.com/ml-ai-ops/platform/internal/runtimeconfig"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// gateway is the online feature service. lookup serves reads; store, when
// non-nil, accepts materialization writes; ping reports online-store health.
type gateway struct {
	mode    string
	store   feature.Store
	lookup  func(feature.Request) (feature.Response, error)
	ping    func(context.Context) error
	token   string
	metrics *metrics
}

// metrics counts usage and failures with bounded labels: mode is one of
// memory/redis/feast and outcome one of a fixed set.
type metrics struct {
	registry *prometheus.Registry
	lookups  *prometheus.CounterVec
	entities *prometheus.CounterVec
	missing  *prometheus.CounterVec
	writes   *prometheus.CounterVec
	health   *prometheus.CounterVec
	latency  *prometheus.HistogramVec
}

func newMetrics() *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		lookups: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "kionga", Subsystem: "feature_gateway", Name: "lookups_total",
			Help: "Online feature lookups by store mode and outcome (ok, invalid_request, failed)."}, []string{"mode", "outcome"}),
		entities: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "kionga", Subsystem: "feature_gateway", Name: "lookup_entities_total",
			Help: "Entities requested in successful online lookups."}, []string{"mode"}),
		missing: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "kionga", Subsystem: "feature_gateway", Name: "lookup_entities_missing_total",
			Help: "Entities in successful lookups that had no online row (NOT_FOUND)."}, []string{"mode"}),
		writes: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "kionga", Subsystem: "feature_gateway", Name: "writes_total",
			Help: "Materialization writes by outcome (ok, unauthorized, invalid_request, unsupported)."}, []string{"mode", "outcome"}),
		health: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "kionga", Subsystem: "feature_gateway", Name: "health_checks_total",
			Help: "Health checks by outcome (ok, failed)."}, []string{"mode", "outcome"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "kionga", Subsystem: "feature_gateway", Name: "lookup_duration_seconds",
			Help: "Online lookup duration.", Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.5, 1}}, []string{"mode"}),
	}
	m.registry.MustRegister(m.lookups, m.entities, m.missing, m.writes, m.health, m.latency)
	return m
}

func main() {
	if err := runtimeconfig.Load(); err != nil {
		log.Fatal(err)
	}
	// Serving mode, most real first:
	//   FEAST_URL  -> delegate to a running Feast feature server
	//   REDIS_URL  -> direct Redis lookups on the platform key convention
	//   otherwise  -> in-memory store (tests and dependency-free development)
	g := &gateway{token: os.Getenv("MLAIOPS_INTERNAL_TOKEN"), metrics: newMetrics()}
	switch {
	case os.Getenv("FEAST_URL") != "":
		adapter := &feature.FeastAdapter{Client: feature.NewFeastClient(os.Getenv("FEAST_URL"))}
		g.mode, g.lookup = "feast", adapter.Client.Lookup
		g.ping = func(ctx context.Context) error {
			if health := adapter.Health(ctx); health.State != feature.StateHealthy {
				return errText(health.Detail)
			}
			return nil
		}
	case os.Getenv("REDIS_URL") != "":
		redisStore, err := feature.NewRedisStore(os.Getenv("REDIS_URL"), 0)
		if err != nil {
			log.Fatalf("invalid REDIS_URL: %v", err)
		}
		g.mode, g.store, g.ping = "redis", redisStore, redisStore.Ping
	default:
		g.mode, g.store = "memory", feature.NewMemoryStore()
	}
	if g.lookup == nil {
		store := g.store
		g.lookup = func(request feature.Request) (feature.Response, error) { return feature.Lookup(store, request) }
	}
	log.Printf("feature-gateway online store mode: %s", g.mode)
	serve("feature-gateway", env("PORT", "8083"), g.handler())
}

func (g *gateway) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", g.healthz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(g.metrics.registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("POST /get-online-features", g.getOnlineFeatures)
	mux.HandleFunc("PUT /internal/v1/features/{service}/{entity}", g.put)
	return mux
}

// healthz reports the online store's real state: 503 when Redis or Feast
// cannot be reached, so the control plane can show it as unavailable.
func (g *gateway) healthz(w http.ResponseWriter, r *http.Request) {
	if g.ping != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := g.ping(ctx); err != nil {
			g.metrics.health.WithLabelValues(g.mode, "failed").Inc()
			write(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "mode": g.mode, "error": err.Error()})
			return
		}
	}
	g.metrics.health.WithLabelValues(g.mode, "ok").Inc()
	write(w, http.StatusOK, map[string]string{"status": "ok", "mode": g.mode})
}

func (g *gateway) getOnlineFeatures(w http.ResponseWriter, r *http.Request) {
	var request feature.Request
	if err := decode(w, r, &request); err != nil {
		g.metrics.lookups.WithLabelValues(g.mode, "invalid_request").Inc()
		fail(w, http.StatusBadRequest, err)
		return
	}
	started := time.Now()
	response, err := g.lookup(request)
	g.metrics.latency.WithLabelValues(g.mode).Observe(time.Since(started).Seconds())
	if err != nil {
		g.metrics.lookups.WithLabelValues(g.mode, "failed").Inc()
		fail(w, http.StatusUnprocessableEntity, err)
		return
	}
	g.metrics.lookups.WithLabelValues(g.mode, "ok").Inc()
	g.metrics.entities.WithLabelValues(g.mode).Add(float64(len(request.Entities)))
	missing := 0
	for _, result := range response.Results {
		for _, status := range result.Statuses {
			if status == "NOT_FOUND" {
				missing++
				break
			}
		}
	}
	g.metrics.missing.WithLabelValues(g.mode).Add(float64(missing))
	write(w, http.StatusOK, response)
}

func (g *gateway) put(w http.ResponseWriter, r *http.Request) {
	if g.token != "" && r.Header.Get("Authorization") != "Bearer "+g.token {
		g.metrics.writes.WithLabelValues(g.mode, "unauthorized").Inc()
		fail(w, http.StatusUnauthorized, errText("unauthorized"))
		return
	}
	if g.store == nil {
		g.metrics.writes.WithLabelValues(g.mode, "unsupported").Inc()
		fail(w, http.StatusNotImplemented, errText("materialization writes go through Feast in feast mode"))
		return
	}
	var values map[string]any
	if err := decode(w, r, &values); err != nil {
		g.metrics.writes.WithLabelValues(g.mode, "invalid_request").Inc()
		fail(w, http.StatusBadRequest, err)
		return
	}
	g.store.Put(r.PathValue("service"), r.PathValue("entity"), values)
	g.metrics.writes.WithLabelValues(g.mode, "ok").Inc()
	w.WriteHeader(http.StatusNoContent)
}

func decode(w http.ResponseWriter, r *http.Request, target any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, err error) {
	write(w, status, map[string]string{"error": err.Error()})
}

type errText string

func (e errText) Error() string { return string(e) }
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func serve(name, port string, handler http.Handler) {
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("%s listening on :%s", name, port)
	log.Fatal(server.ListenAndServe())
}

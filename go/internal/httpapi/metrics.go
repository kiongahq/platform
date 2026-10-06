package httpapi

import (
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var httpRegistry = prometheus.NewRegistry()
var requestCount = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "kionga", Subsystem: "gateway", Name: "requests_total", Help: "Gateway requests grouped by method and bounded route category.",
}, []string{"method", "category"})
var requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "kionga", Subsystem: "gateway", Name: "request_duration_seconds", Help: "Gateway request duration grouped by bounded route category.",
	Buckets: prometheus.DefBuckets,
}, []string{"category"})

func init() {
	httpRegistry.MustRegister(requestCount, requestDuration)
}

func MetricsHandler() http.Handler { return promhttp.HandlerFor(httpRegistry, promhttp.HandlerOpts{}) }

func requestCategory(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/v1/admin/"):
		return "admin"
	case strings.HasPrefix(path, "/api/v1/workspaces"):
		return "workspace_api"
	case strings.HasPrefix(path, "/api/"):
		return "api"
	case strings.HasPrefix(path, "/auth/"):
		return "auth"
	case strings.HasPrefix(path, "/workspaces/"):
		return "workspace"
	default:
		return "web"
	}
}

package main

import (
	"github.com/kiongahq/platform/internal/runtimeconfig"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/kiongahq/platform/internal/traceproxy"
)

func main() {
	if err := runtimeconfig.Load(); err != nil {
		log.Fatal(err)
	}
	raw := env("LLM_UPSTREAM_URL", "http://localhost:8000")
	upstream, err := url.Parse(raw)
	if err != nil {
		log.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	var sink traceproxy.Sink = traceproxy.HTTPSink{URL: os.Getenv("TRACE_SINK_URL"), Token: os.Getenv("TRACE_SINK_TOKEN"), Client: client}
	if os.Getenv("TRACE_SINK_FORMAT") == "kafka-rest" {
		sink = traceproxy.KafkaRESTSink{URL: os.Getenv("TRACE_SINK_URL"), Client: client}
	}
	handler := traceproxy.New(upstream, env("MLAIOPS_AGENT_NAME", "unknown"), env("MLAIOPS_AGENT_VERSION", "unknown"), sink)
	server := &http.Server{Addr: ":" + env("PORT", "8081"), Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 120 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 120 * time.Second}
	log.Printf("trace proxy listening on %s and forwarding to %s", server.Addr, upstream)
	log.Fatal(server.ListenAndServe())
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

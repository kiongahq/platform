package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	platformmetrics "github.com/kiongahq/platform/internal/metrics"
)

func main() {
	targets := parseTargets(os.Getenv("MLAIOPS_METRICS_TARGETS"))
	collector := platformmetrics.New(targets)
	platform := platformmetrics.NewPlatformCollector(os.Getenv("MLAIOPS_URL"), os.Getenv("MLAIOPS_TOKEN"))
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			collector.Poll(context.Background())
			if err := platform.Poll(context.Background()); err != nil {
				log.Printf("platform metrics poll failed: %v", err)
			}
			<-ticker.C
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(collector.Prometheus()))
		_, _ = w.Write([]byte(platform.Prometheus()))
	})
	server := &http.Server{Addr: ":" + env("PORT", "9090"), Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("metrics collector listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
func parseTargets(raw string) map[string]string {
	targets := map[string]string{"gateway": "http://mlaiops-gateway:8080/api/v1/health"}
	if raw == "" {
		return targets
	}
	targets = make(map[string]string)
	for _, item := range strings.Split(raw, ",") {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			targets[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return targets
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/ml-ai-ops/platform/internal/runtimeconfig"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/ml-ai-ops/platform/internal/integrations"
)

var lastSuccessfulPoll atomic.Int64

func main() {
	if err := runtimeconfig.Load(); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		last := lastSuccessfulPoll.Load()
		if last == 0 || time.Since(time.Unix(last, 0)) > 90*time.Second {
			http.Error(w, "Kafka lifecycle consumer not ready", 503)
			return
		}
		w.WriteHeader(200)
	})
	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "kionga", Subsystem: "worker", Name: "last_success_timestamp_seconds", Help: "Unix time of the last successful Kafka lifecycle poll.",
	}, func() float64 { return float64(lastSuccessfulPoll.Load()) }))
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	server := &http.Server{Addr: ":" + env("PORT", "8086"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("worker health listener failed")
		}
	}()
	defer server.Close()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}
	namespace := env("MLAIOPS_TARGET_NAMESPACE", "default")
	consumer := integrations.NewKafkaConsumer(env("KAFKA_REST_URL", "http://kafka-rest:8082"), "mlaiops-integration", env("HOSTNAME", "worker"), os.Getenv("KAFKA_REST_TOKEN"))
	topics := []string{"mlaiops.pipeline.commands", "mlaiops.model.commands", "mlaiops.agent.commands", "mlaiops.tool.commands", "mlaiops.connection.commands", "mlaiops.workspace.commands"}
	if err := consumer.Connect(ctx, topics); err != nil {
		return err
	}
	lastSuccessfulPoll.Store(time.Now().Unix())
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = consumer.Close(closeCtx)
	}()
	dispatcher := integrations.NewDispatcher(client, namespace)
	worker := integrations.NewLifecycleWorker(consumer, dispatcher)
	for ctx.Err() == nil {
		records, err := consumer.Poll(ctx)
		if err != nil {
			log.Printf("Kafka poll failed: %v", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		if err := worker.ProcessBatch(ctx, records); err != nil {
			// Stop before polling another batch. Continuing could later commit past
			// the failed offsets; process restart resumes from the last durable commit.
			return fmt.Errorf("Kafka lifecycle batch failed: %w", err)
		}
		lastSuccessfulPoll.Store(time.Now().Unix())
	}
	return ctx.Err()
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

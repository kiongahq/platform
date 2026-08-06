package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/ml-ai-ops/platform/internal/integrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
	consumer := integrations.NewKafkaConsumer(env("KAFKA_REST_URL", "http://kafka-rest:8082"), "mlaiops-integration", env("HOSTNAME", "worker"))
	topics := []string{"mlaiops.pipeline.commands", "mlaiops.model.commands", "mlaiops.agent.commands", "mlaiops.tool.commands", "mlaiops.connection.commands", "mlaiops.workspace.commands"}
	if err := consumer.Connect(ctx, topics); err != nil {
		return err
	}
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
	}
	return ctx.Err()
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

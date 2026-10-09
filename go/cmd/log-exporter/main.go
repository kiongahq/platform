// Command log-exporter ships one tenant's Kionga logs to Elasticsearch or
// OpenSearch. It is optional: the gateway never waits on it, and when it is
// absent or failing the Platform page says so.
//
// Environment:
//
//	DATABASE_URL                      PostgreSQL with the log_entries table
//	MLAIOPS_TENANT                    tenant to export (default "default")
//	KIONGA_LOG_EXPORT_TARGET          elasticsearch | opensearch
//	KIONGA_LOG_EXPORT_URL             https://cluster:9200
//	KIONGA_LOG_EXPORT_API_KEY_FILE    Elasticsearch API key (file)
//	KIONGA_LOG_EXPORT_USERNAME        basic-auth user (OpenSearch)
//	KIONGA_LOG_EXPORT_PASSWORD_FILE   basic-auth password (file)
//	KIONGA_LOG_EXPORT_CA_FILE         PEM bundle for private CAs
//	KIONGA_LOG_EXPORT_BATCH           documents per bulk request (default 500)
//	PORT                              status port (default 8086)
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ml-ai-ops/platform/internal/logexport"
	"github.com/ml-ai-ops/platform/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8086"
	}
	config, configErr := logexport.ConfigFromEnv()
	var exporter *logexport.Exporter
	if configErr == nil {
		repository, err := store.OpenPostgres(ctx, os.Getenv("DATABASE_URL"), config.Tenant)
		if err != nil {
			log.Fatalf("open PostgreSQL: %v", err)
		}
		defer repository.Close()
		sink, err := logexport.NewSink(config)
		if err != nil {
			log.Fatalf("configure %s sink: %v", config.Target, err)
		}
		batch, _ := strconv.Atoi(os.Getenv("KIONGA_LOG_EXPORT_BATCH"))
		exporter = &logexport.Exporter{Logs: repository, Docs: repository, Sink: sink, Tenant: config.Tenant, Destination: config.URL, BatchSize: batch}
		go exporter.Run(ctx, 2*time.Second)
		log.Printf("exporting tenant %q logs to %s", config.Tenant, config.Target)
	} else {
		log.Printf("log export not configured: %v", configErr)
	}
	mux := http.NewServeMux()
	status := func() any {
		if exporter == nil {
			return map[string]any{"state": "not_configured", "detail": configErr.Error()}
		}
		return exporter.Status()
	}
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

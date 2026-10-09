package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	// Distroless images ship no zoneinfo; schedules need IANA timezones.
	_ "time/tzdata"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/httpapi"
	"github.com/ml-ai-ops/platform/internal/integrations"
	"github.com/ml-ai-ops/platform/internal/runtimeconfig"
	"github.com/ml-ai-ops/platform/internal/store"
)

//go:embed web/*
var web embed.FS

func main() {
	if err := runtimeconfig.Load(); err != nil {
		log.Fatal(err)
	}
	if err := runtimeconfig.ValidateGateway(); err != nil {
		log.Fatal(err)
	}
	static, err := fs.Sub(web, "web")
	if err != nil {
		log.Fatal(err)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var repository store.Repository
	var postgres *store.Postgres
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		postgres, err = store.OpenPostgres(ctx, databaseURL, os.Getenv("MLAIOPS_TENANT"))
		if err != nil {
			log.Fatalf("open PostgreSQL repository: %v", err)
		}
		defer postgres.Close()
		go func() {
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := postgres.PurgeExpiredWorkspaceEdgeSessions(ctx); err != nil {
						log.Printf("workspace session cleanup failed: %v", err)
					}
				}
			}
		}()
		repository = postgres
		if kafkaURL := os.Getenv("KAFKA_REST_URL"); kafkaURL != "" {
			worker := store.NewOutboxWorker(postgres, integrations.NewKafkaREST(kafkaURL, os.Getenv("KAFKA_REST_TOKEN")), time.Second)
			go worker.Run(ctx)
		}
		log.Printf("using PostgreSQL control-plane repository")
	} else {
		dataPath := os.Getenv("MLAIOPS_DATA_PATH")
		if dataPath == "" {
			dataPath = "data/platform.json"
		}
		repository = store.New(dataPath)
		log.Printf("using local file repository at %s", dataPath)
	}
	handler := httpapi.New(repository, static)
	httpapi.StartScheduler(ctx, repository)
	if issuer := os.Getenv("OIDC_ISSUER"); issuer != "" {
		jwksURL := os.Getenv("OIDC_JWKS_URL")
		if jwksURL == "" {
			log.Fatal("OIDC_JWKS_URL is required when OIDC_ISSUER is configured")
		}
		verifier := auth.New(auth.Config{Issuer: issuer, Audience: os.Getenv("OIDC_AUDIENCE"), JWKSURL: jwksURL, Tenant: os.Getenv("MLAIOPS_TENANT")})
		var revokeWorkspaceSessions func(context.Context, string) error
		if postgres != nil {
			revokeWorkspaceSessions = postgres.RevokeWorkspaceEdgeSessionsForSubject
		}
		session, sessionErr := auth.NewSessionManager(auth.SessionConfig{
			ClientID: os.Getenv("OIDC_CLIENT_ID"), ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
			AuthURL: os.Getenv("OIDC_AUTH_URL"), TokenURL: os.Getenv("OIDC_TOKEN_URL"),
			RedirectURL: os.Getenv("OIDC_REDIRECT_URL"), Secure: true, OnLogout: revokeWorkspaceSessions,
		}, verifier)
		if sessionErr != nil {
			log.Fatalf("configure OIDC browser login: %v", sessionErr)
		}
		handler = verifier.Middleware(session.Handler(handler))
		log.Printf("OIDC authentication and browser login enabled")
	} else {
		username := os.Getenv("MLAIOPS_LOCAL_USERNAME")
		if username == "" {
			username = "admin"
		}
		password := os.Getenv("MLAIOPS_LOCAL_PASSWORD")
		if password == "" {
			password = "mlaiops-local"
		}
		handler = auth.NewLocalSessionManager(username, password, httpapi.LocalAccountStore{Docs: repository}).Handler(handler)
		log.Printf("WARNING: OIDC authentication disabled; local development mode only")
	}
	handler = auth.APITokenMiddleware(repository.ResolveAPIToken, handler)
	if metricsPort := os.Getenv("METRICS_PORT"); metricsPort != "" {
		metricsServer := &http.Server{Addr: ":" + metricsPort, Handler: httpapi.MetricsHandler(), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("gateway metrics listener failed: %v", err)
			}
		}()
		defer metricsServer.Close()
	}
	if os.Getenv("KIONGA_ENVIRONMENT") == "production" && os.Getenv("KIONGA_WORKSPACE_BASE_DOMAIN") != "" {
		handler = httpapi.WorkspaceHostRouter(handler, repository)
	}
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("ml-ai-ops-platform is ready at http://localhost:%s", port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

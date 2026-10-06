package main

import (
	"encoding/json"
	"github.com/ml-ai-ops/platform/internal/runtimeconfig"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/ml-ai-ops/platform/internal/storage"
)

func main() {
	if err := runtimeconfig.Load(); err != nil {
		log.Fatal(err)
	}
	config := storage.Config{Endpoint: os.Getenv("S3_ENDPOINT"), Region: env("S3_REGION", "us-east-1"), AccessKey: os.Getenv("S3_ACCESS_KEY"), SecretKey: os.Getenv("S3_SECRET_KEY")}
	browser := storage.NewBrowser(config)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /buckets", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			write(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		buckets, err := browser.ListBuckets()
		if err != nil {
			write(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		write(w, http.StatusOK, map[string]any{"buckets": buckets})
	})
	mux.HandleFunc("GET /objects", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			write(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		listing, err := browser.ListObjects(r.URL.Query().Get("bucket"), r.URL.Query().Get("prefix"))
		if err != nil {
			write(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		write(w, http.StatusOK, listing)
	})
	mux.HandleFunc("GET /object", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			write(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		preview, err := browser.PreviewObject(r.URL.Query().Get("bucket"), r.URL.Query().Get("key"))
		if err != nil {
			write(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		write(w, http.StatusOK, preview)
	})
	mux.HandleFunc("POST /presign", func(w http.ResponseWriter, r *http.Request) {
		if token := os.Getenv("MLAIOPS_INTERNAL_TOKEN"); token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			write(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		var request storage.Request
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			write(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		signed, err := storage.Presign(config, request, time.Now())
		if err != nil {
			write(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		write(w, http.StatusOK, map[string]any{"url": signed, "expires_in": request.TTLSeconds})
	})
	server := &http.Server{Addr: ":" + env("PORT", "8084"), Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("storage proxy listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}

func authorized(r *http.Request) bool {
	token := os.Getenv("MLAIOPS_INTERNAL_TOKEN")
	return token == "" || r.Header.Get("Authorization") == "Bearer "+token
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

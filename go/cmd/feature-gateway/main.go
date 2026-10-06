package main

import (
	"encoding/json"
	"github.com/ml-ai-ops/platform/internal/runtimeconfig"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/ml-ai-ops/platform/internal/feature"
)

func main() {
	if err := runtimeconfig.Load(); err != nil {
		log.Fatal(err)
	}
	// Serving mode, most real first:
	//   FEAST_URL  -> delegate to a running Feast feature server
	//   REDIS_URL  -> direct Redis lookups on the platform key convention
	//   otherwise  -> in-memory store (tests and dependency-free development)
	var store feature.Store
	var lookup func(feature.Request) (feature.Response, error)
	mode := "memory"
	switch {
	case os.Getenv("FEAST_URL") != "":
		client := feature.NewFeastClient(os.Getenv("FEAST_URL"))
		lookup, mode = client.Lookup, "feast"
	case os.Getenv("REDIS_URL") != "":
		redisStore, err := feature.NewRedisStore(os.Getenv("REDIS_URL"), 0)
		if err != nil {
			log.Fatalf("invalid REDIS_URL: %v", err)
		}
		store, mode = redisStore, "redis"
		lookup = func(request feature.Request) (feature.Response, error) {
			return feature.Lookup(redisStore, request)
		}
	default:
		memory := feature.NewMemoryStore()
		store = memory
		lookup = func(request feature.Request) (feature.Response, error) {
			return feature.Lookup(memory, request)
		}
	}
	log.Printf("feature-gateway online store mode: %s", mode)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("POST /get-online-features", func(w http.ResponseWriter, r *http.Request) {
		var request feature.Request
		if err := decode(w, r, &request); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		response, err := lookup(request)
		if err != nil {
			fail(w, http.StatusUnprocessableEntity, err)
			return
		}
		write(w, http.StatusOK, response)
	})
	mux.HandleFunc("PUT /internal/v1/features/{service}/{entity}", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			fail(w, http.StatusUnauthorized, errText("unauthorized"))
			return
		}
		if store == nil {
			fail(w, http.StatusNotImplemented, errText("materialization writes go through Feast in feast mode"))
			return
		}
		var values map[string]any
		if err := decode(w, r, &values); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		store.Put(r.PathValue("service"), r.PathValue("entity"), values)
		w.WriteHeader(http.StatusNoContent)
	})
	serve("feature-gateway", env("PORT", "8083"), mux)
}

func authorized(r *http.Request) bool {
	token := os.Getenv("MLAIOPS_INTERNAL_TOKEN")
	return token == "" || r.Header.Get("Authorization") == "Bearer "+token
}
func health(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, map[string]string{"status": "ok"})
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

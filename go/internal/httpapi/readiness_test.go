package httpapi

import (
	"testing"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// The bundled object store is RustFS; generic S3 connections are also valid.
func TestObjectStorageReadinessAcceptsRustFSAndS3(t *testing.T) {
	for _, kind := range []string{"rustfs", "s3"} {
		t.Run(kind, func(t *testing.T) {
			repository := store.New()
			storageStatus := func() string {
				for _, item := range readinessFor(repository).Items {
					if item.Key == "storage" {
						return item.Status
					}
				}
				t.Fatal("no storage readiness item")
				return ""
			}
			if got := storageStatus(); got != "pending" {
				t.Fatalf("before any connection: %q", got)
			}
			connection, err := repository.CreateConnection(api.CreateConnectionRequest{
				Name: "objects-" + kind, Type: kind, Endpoint: "http://objectstore:9000", SecretRef: "secret/objects",
			}, "admin")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.UpdateConnectionStatus(connection.ID, "healthy", "", "admin"); err != nil {
				t.Fatal(err)
			}
			if got := storageStatus(); got != "ready" {
				t.Fatalf("healthy %s connection: storage %q, want ready", kind, got)
			}
		})
	}
}

func TestLegacyMinIOConnectionDoesNotMarkBundledStorageReady(t *testing.T) {
	repository := store.New()
	connection, err := repository.CreateConnection(api.CreateConnectionRequest{
		Name: "legacy-store", Type: "minio", Endpoint: "http://objectstore:9000", SecretRef: "secret/objects",
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateConnectionStatus(connection.ID, "healthy", "", "admin"); err != nil {
		t.Fatal(err)
	}
	for _, item := range readinessFor(repository).Items {
		if item.Key == "storage" && item.Status != "pending" {
			t.Fatalf("legacy connection must not mark RustFS ready: %q", item.Status)
		}
	}
}

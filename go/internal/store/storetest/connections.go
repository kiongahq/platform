package storetest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Connections checks the create and update rules of infrastructure
// connections on any Repository. Names are unique per run so a shared
// integration database can be reused.
func Connections(t *testing.T, repository store.Repository) {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	request := func(name string) api.CreateConnectionRequest {
		return api.CreateConnectionRequest{Name: name + "-" + suffix, Type: "mlflow", Endpoint: "http://mlflow:5000", SecretRef: "none"}
	}
	find := func(id string) api.Connection {
		t.Helper()
		for _, item := range repository.Connections() {
			if item.ID == id {
				return item
			}
		}
		t.Fatalf("connection %s is not listed", id)
		return api.Connection{}
	}
	first, err := repository.CreateConnection(request("tracking"), "admin")
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreateConnection(request("events"), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateConnection(request("TRACKING"), "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a second connection with the same name: %v", err)
	}

	// Required fields are the ones create requires, and nothing is saved.
	for _, invalid := range []api.CreateConnectionRequest{
		{Type: "mlflow", Endpoint: "http://x", SecretRef: "none"},
		{Name: "x-" + suffix, Endpoint: "http://x", SecretRef: "none"},
		{Name: "x-" + suffix, Type: "mlflow", Endpoint: "http://x"},
	} {
		if _, err := repository.UpdateConnection(first.ID, invalid, false, "admin"); err == nil || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
			t.Fatalf("invalid update %+v: %v", invalid, err)
		}
	}
	if _, err := repository.UpdateConnection("conn-missing-"+suffix, request("other"), false, "admin"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing connection: %v", err)
	}
	// A rename cannot take another connection's name, whatever the case.
	taken := request("EVENTS")
	if _, err := repository.UpdateConnection(first.ID, taken, false, "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("rename to a used name: %v", err)
	}
	if got := find(first.ID); got.Name != first.Name {
		t.Fatalf("refused updates changed the connection: %+v", got)
	}

	// A rename alone keeps the last check; changing its own case is allowed.
	if _, err := repository.UpdateConnectionStatus(first.ID, "healthy", "Connection succeeded", "admin"); err != nil {
		t.Fatal(err)
	}
	renamed := request("Tracking")
	updated, err := repository.UpdateConnection(first.ID, renamed, false, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != first.ID || updated.Name != renamed.Name || updated.Status != "healthy" || updated.CheckedAt == nil || !updated.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("rename: %+v", updated)
	}
	// A new target is unchecked: back to pending until it is tested.
	moved := renamed
	moved.Endpoint, moved.SecretRef = "http://mlflow-2:5000", "env:KIONGA_PLATFORM_SECRET_MLFLOW"
	updated, err = repository.UpdateConnection(first.ID, moved, false, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Endpoint != moved.Endpoint || updated.SecretRef != moved.SecretRef || updated.Status != "pending" || updated.CheckedAt != nil || updated.ActivatedAt != nil {
		t.Fatalf("target change: %+v", updated)
	}
	if got := find(first.ID); got.Endpoint != moved.Endpoint || got.Status != "pending" {
		t.Fatalf("target change not stored: %+v", got)
	}
	audited := false
	for _, event := range repository.Audit() {
		if event.Action == "connection.updated" && event.ResourceID == first.ID && event.Actor == "operator" {
			audited = true
			if strings.Contains(fmt.Sprint(event.Metadata), "KIONGA_PLATFORM_SECRET_MLFLOW") {
				t.Fatalf("audit metadata names the credential reference: %v", event.Metadata)
			}
		}
	}
	if !audited {
		t.Fatal("update was not audited")
	}

	// An activated runtime keeps its service and only takes a verified target.
	runtime := api.CreateConnectionRequest{Name: "runtime-" + suffix, Type: "prefect", Endpoint: "http://prefect:4200/api", SecretRef: "none"}
	created, err := repository.CreateConnection(runtime, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActivateConnection(created.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	retyped := runtime
	retyped.Type = "openfaas"
	if _, err := repository.UpdateConnection(created.ID, retyped, true, "admin"); !errors.Is(err, store.ErrConnectionTypeLocked) {
		t.Fatalf("activated connection changed service: %v", err)
	}
	repointed := runtime
	repointed.Endpoint = "http://prefect-2:4200/api"
	if _, err := repository.UpdateConnection(created.ID, repointed, false, "admin"); !errors.Is(err, store.ErrConnectionUnverified) {
		t.Fatalf("activated connection took an unverified target: %v", err)
	}
	if got := find(created.ID); got.Endpoint != runtime.Endpoint || got.Type != "prefect" || got.ActivatedAt == nil {
		t.Fatalf("refused updates changed the active runtime: %+v", got)
	}
	label := runtime
	label.Name = "runtime-renamed-" + suffix
	if got, err := repository.UpdateConnection(created.ID, label, false, "admin"); err != nil || got.ActivatedAt == nil || got.Status != "healthy" {
		t.Fatalf("rename of the active runtime: %+v %v", got, err)
	}
	repointed.Name = label.Name
	got, err := repository.UpdateConnection(created.ID, repointed, true, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != repointed.Endpoint || got.ActivatedAt == nil || got.Status != "healthy" || got.CheckedAt == nil {
		t.Fatalf("verified change of the active runtime: %+v", got)
	}
	names := []string{}
	for _, item := range repository.Connections() {
		if slices.Contains([]string{first.ID, second.ID, created.ID}, item.ID) {
			names = append(names, strings.ToLower(item.Name))
		}
	}
	slices.Sort(names)
	if len(slices.Compact(names)) != 3 {
		t.Fatalf("names are not unique: %v", names)
	}
}

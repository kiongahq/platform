package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestKafkaConsumerUsesManualCommitAndCommitsNextOffsets(t *testing.T) {
	var createPayload map[string]any
	var committed []KafkaOffset
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/consumers/lifecycle":
			if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
				t.Errorf("decode create payload: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"base_uri": server.URL + "/instances/worker"})
		case r.Method == http.MethodPost && r.URL.Path == "/instances/worker/subscription":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/instances/worker/records":
			_ = json.NewEncoder(w).Encode([]KafkaRecord{
				{Topic: "agents", Partition: 1, Offset: 7, Value: json.RawMessage(`{"id":"a"}`)},
				{Topic: "agents", Partition: 1, Offset: 9, Value: json.RawMessage(`{"id":"b"}`)},
				{Topic: "models", Partition: 0, Offset: 3, Value: json.RawMessage(`{"id":"m"}`)},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/instances/worker/offsets":
			var payload struct {
				Offsets []KafkaOffset `json:"offsets"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode commit payload: %v", err)
			}
			committed = payload.Offsets
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	consumer := NewKafkaConsumer(server.URL, "lifecycle", "worker")
	ctx := context.Background()
	if err := consumer.Connect(ctx, []string{"agents", "models"}); err != nil {
		t.Fatal(err)
	}
	if got := createPayload["auto.commit.enable"]; got != "false" {
		t.Fatalf("auto commit must be disabled, got %#v", got)
	}
	records, err := consumer.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[0].Partition != 1 || records[0].Offset != 7 {
		t.Fatalf("poll must expose partition and offset: %#v", records)
	}
	if err := consumer.Commit(ctx, records); err != nil {
		t.Fatal(err)
	}
	want := []KafkaOffset{
		{Topic: "agents", Partition: 1, Offset: 10},
		{Topic: "models", Partition: 0, Offset: 4},
	}
	if !reflect.DeepEqual(committed, want) {
		t.Fatalf("committed offsets = %#v, want %#v", committed, want)
	}
}

func TestKafkaConsumerTokenCannotLeaveConfiguredOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing credential")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"base_uri": "https://different.invalid/consumer"})
	}))
	defer server.Close()
	consumer := NewKafkaConsumer(server.URL, "lifecycle", "worker", "test-secret")
	if err := consumer.Connect(context.Background(), []string{"commands"}); err == nil {
		t.Fatal("cross-origin consumer credential forwarding allowed")
	}
}

func TestKafkaConsumerEmptyBatchDoesNotCommit(t *testing.T) {
	consumer := NewKafkaConsumer("http://unused.example", "lifecycle", "worker")
	consumer.baseURI = "http://unused.example/instances/worker"
	if err := consumer.Commit(context.Background(), nil); err != nil {
		t.Fatalf("empty batch commit should be a no-op: %v", err)
	}
}

package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCollectorExportsComponentHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	collector := New(map[string]string{"gateway": server.URL})
	collector.Poll(context.Background())
	if !strings.Contains(collector.Prometheus(), `kionga_component_up{component="gateway"} 1`) {
		t.Fatal(collector.Prometheus())
	}
}

package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
)

// newTestRequest is httptest.NewRequest for API calls: a request with a body
// declares it as JSON, as the console, CLI and SDK clients do. Tests that
// exercise another media type set Content-Type explicitly afterwards.
func newTestRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

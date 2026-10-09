package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	base := env("MLAIOPS_URL", "http://localhost:8080")
	client := &http.Client{Timeout: 15 * time.Second}
	if len(os.Args) >= 4 && os.Args[1] == "pipeline" && (os.Args[2] == "export" || os.Args[2] == "validate" || os.Args[2] == "apply") {
		os.Exit(pipelineFile(client, base, os.Args[2:], os.Stdout, os.Stderr))
	}
	var method, path string
	var body any
	switch strings.Join(os.Args[1:], " ") {
	case "project list":
		method, path = http.MethodGet, "/api/v1/projects"
	case "pipeline list":
		method, path = http.MethodGet, "/api/v1/pipelines/runs"
	case "pipeline definitions":
		method, path = http.MethodGet, "/api/v1/pipelines/definitions"
	case "function list":
		method, path = http.MethodGet, "/api/v1/functions"
	case "model list":
		method, path = http.MethodGet, "/api/v1/models"
	case "agent list":
		method, path = http.MethodGet, "/api/v1/agents"
	case "tool list":
		method, path = http.MethodGet, "/api/v1/tools"
	case "connection list":
		method, path = http.MethodGet, "/api/v1/connections"
	case "audit list":
		method, path = http.MethodGet, "/api/v1/audit"
	default:
		if len(os.Args) == 4 && os.Args[1] == "pipeline" && os.Args[2] == "submit" {
			method, path, body = http.MethodPost, "/api/v1/pipelines/submit", map[string]string{"project_id": os.Args[3], "name": "training-pipeline"}
		} else if len(os.Args) == 5 && os.Args[1] == "pipeline" && os.Args[2] == "submit" {
			method, path, body = http.MethodPost, "/api/v1/pipelines/submit", map[string]string{"project_id": os.Args[3], "definition_id": os.Args[4]}
		} else if len(os.Args) >= 6 && os.Args[1] == "function" && os.Args[2] == "deploy" {
			method, path, body = http.MethodPost, "/api/v1/functions", map[string]string{"project_id": os.Args[3], "name": os.Args[4], "image": os.Args[5]}
		} else if len(os.Args) >= 4 && os.Args[1] == "function" && os.Args[2] == "invoke" {
			method, path, body = http.MethodPost, "/api/v1/functions/"+url.PathEscape(os.Args[3])+"/invoke", json.RawMessage(env("MLAIOPS_FUNCTION_PAYLOAD", "{}"))
		} else if len(os.Args) >= 4 && os.Args[1] == "function" && os.Args[2] == "invoke-async" {
			method, path, body = http.MethodPost, "/api/v1/functions/"+url.PathEscape(os.Args[3])+"/invoke-async", json.RawMessage(env("MLAIOPS_FUNCTION_PAYLOAD", "{}"))
		} else if len(os.Args) >= 5 && os.Args[1] == "project" && os.Args[2] == "connect" {
			branch := "main"
			if len(os.Args) > 5 {
				branch = os.Args[5]
			}
			method, path, body = http.MethodPut, "/api/v1/projects/"+url.PathEscape(os.Args[3])+"/repository", map[string]string{"url": os.Args[4], "default_branch": branch}
		} else {
			usage()
			os.Exit(2)
		}
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		fatal(err)
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, base+path, reader)
	fatal(err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-MLAIOps-Actor", env("USER", "cli"))
	if token := os.Getenv("MLAIOPS_TOKEN"); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	fatal(err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	fatal(err)
	if response.StatusCode >= 300 {
		fmt.Fprintln(os.Stderr, string(raw))
		os.Exit(1)
	}
	var value any
	if json.Unmarshal(raw, &value) == nil {
		formatted, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(formatted))
	} else {
		fmt.Println(string(raw))
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: mlaiops <project|pipeline|function|model|agent|tool|connection|audit> list")
	fmt.Fprintln(os.Stderr, "       mlaiops pipeline definitions | pipeline submit <project-id> [definition-id]")
	fmt.Fprintln(os.Stderr, "       mlaiops function deploy <project-id> <name> <image> | function <invoke|invoke-async> <name>")
	fmt.Fprintln(os.Stderr, "       mlaiops project connect <project-id> <repository-url> [branch]")
	fmt.Fprintln(os.Stderr, "       mlaiops pipeline export <definition-id>            (canonical YAML to stdout)")
	fmt.Fprintln(os.Stderr, "       mlaiops pipeline validate <file.kionga.yaml>")
	fmt.Fprintln(os.Stderr, "       mlaiops pipeline apply <file.kionga.yaml> [definition-id] [message]")
}
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

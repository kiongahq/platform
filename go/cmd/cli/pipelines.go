package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

// pipelineFile implements the Git-friendly definition commands:
//
//	export <id>                 print the definition's canonical YAML
//	validate <file>             print every issue with its line; exit 1 if invalid
//	apply <file> [id] [message] create or update from YAML; prints the revision
//
// It returns the process exit code.
func pipelineFile(client *http.Client, base string, args []string, stdout, stderr io.Writer) int {
	call := func(method, path string, body any) (int, []byte, error) {
		var reader io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				return 0, nil, err
			}
			reader = bytes.NewReader(raw)
		}
		request, err := http.NewRequest(method, base+path, reader)
		if err != nil {
			return 0, nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		if token := os.Getenv("MLAIOPS_TOKEN"); token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		return response.StatusCode, raw, err
	}
	printIssues := func(raw []byte) {
		var body struct {
			Issues  []issue `json:"issues"`
			Details []issue `json:"details"`
			Message string  `json:"message"`
		}
		_ = json.Unmarshal(raw, &body)
		issues := append(body.Issues, body.Details...)
		if len(issues) == 0 && body.Message != "" {
			fmt.Fprintln(stderr, body.Message)
		}
		for _, item := range issues {
			fmt.Fprintln(stderr, item.String())
		}
	}
	switch args[0] {
	case "export":
		status, raw, err := call(http.MethodGet, "/api/v1/pipelines/definitions/"+url.PathEscape(args[1])+"/yaml", nil)
		if err != nil || status >= 300 {
			fmt.Fprintln(stderr, failure(err, raw))
			return 1
		}
		var body struct {
			YAML string `json:"yaml"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprint(stdout, body.YAML)
		return 0
	case "validate", "apply":
		text, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if args[0] == "validate" {
			status, raw, err := call(http.MethodPost, "/api/v1/pipelines/validate", map[string]string{"yaml": string(text)})
			if err != nil || status >= 300 {
				fmt.Fprintln(stderr, failure(err, raw))
				return 1
			}
			var body struct {
				Valid  bool       `json:"valid"`
				SHA256 string     `json:"sha256"`
				Layers [][]string `json:"layers"`
			}
			_ = json.Unmarshal(raw, &body)
			if !body.Valid {
				printIssues(raw)
				return 1
			}
			fmt.Fprintf(stdout, "valid: %d stage(s), sha256 %s\n", len(body.Layers), body.SHA256)
			return 0
		}
		method, path := http.MethodPost, "/api/v1/pipelines/definitions/yaml"
		if len(args) >= 3 && args[2] != "" {
			method, path = http.MethodPut, "/api/v1/pipelines/definitions/"+url.PathEscape(args[2])+"/yaml"
		}
		message := ""
		if len(args) >= 4 {
			message = args[3]
		}
		status, raw, err := call(method, path, map[string]string{"yaml": string(text), "message": message})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if status >= 300 {
			printIssues(raw)
			return 1
		}
		var definition struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
			SHA256   string `json:"sha256"`
		}
		_ = json.Unmarshal(raw, &definition)
		fmt.Fprintf(stdout, "%s revision %d (sha256 %s)\n", definition.ID, definition.Revision, definition.SHA256)
		return 0
	}
	return 2
}

type issue struct {
	Path    string `json:"path"`
	Node    string `json:"node"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func (i issue) String() string {
	where := i.Path
	if i.Line > 0 {
		where = fmt.Sprintf("line %d", i.Line)
	}
	if i.Node != "" {
		where += " (node " + i.Node + ")"
	}
	return where + ": " + i.Message
}

func failure(err error, raw []byte) string {
	if err != nil {
		return err.Error()
	}
	return string(raw)
}

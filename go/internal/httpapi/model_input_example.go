package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kiongahq/platform/internal/integrations"
)

// A model-specific sample for the console's Test dialog. The gateway asks the
// MLflow server for two files of the registered model version, through the
// server's own artifact route, so it holds no artifact store credentials:
//
//  1. MLmodel, which names the saved input example and carries the signature.
//  2. The saved input example, when MLmodel names one.
//
// Without a saved example the answer is a skeleton built from the signature's
// input columns or tensors, marked as derived, with a placeholder per type.
// Artifact contents are returned to the caller and never logged.

const (
	// maxMLmodelBytes bounds the MLmodel file. It is a few kilobytes in
	// practice; a signature with thousands of columns stays well below this.
	maxMLmodelBytes = 256 << 10
	// maxInputExampleBytes bounds the sample this route returns, saved or derived.
	maxInputExampleBytes = 64 << 10
	// maxSkeletonCells bounds the values in a skeleton derived from a tensor shape.
	maxSkeletonCells = 4096
	// maxSkeletonDepth bounds nested array, object and map column types.
	maxSkeletonDepth      = 16
	inputExampleSaved     = "input_example"
	inputExampleSignature = "signature"
)

// inputExampleTimeout covers every MLflow call one request makes. A variable
// so a test can shorten it.
var inputExampleTimeout = 10 * time.Second

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/models/{id}/input-example", s.modelInputExample)
	})
	openapiPaths["/api/v1/models/{id}/input-example"] = map[string]any{"get": map[string]any{"summary": "A sample request payload for a model version: its saved MLflow input example, or a skeleton derived from its signature (source says which)"}}
}

// modelInputExampleSteps tell an administrator how to enable the route.
var modelInputExampleSteps = []string{
	"Set MLFLOW_URL on the gateway to the MLflow tracking server (the address the operator and the pipelines already use).",
	"When MLflow requires a bearer token, supply MLFLOW_TOKEN to the gateway as a secret (MLFLOW_TOKEN_FILE), never in the configuration file.",
}

// modelArtifactReader is the one MLflow call this route makes.
type modelArtifactReader interface {
	ModelVersionArtifact(ctx context.Context, name, version, path string, limit int64) ([]byte, error)
}

// inputExampleUnavailable is a model with no usable sample: MLflow answered,
// and nothing about the answer will change on a retry. The reason is a fixed
// sentence, never artifact content.
type inputExampleUnavailable struct{ reason string }

func (e *inputExampleUnavailable) Error() string { return e.reason }

func unavailable(reason string) error { return &inputExampleUnavailable{reason: reason} }

// inputExampleResponse is the 200 body. Payload is a complete /invocations
// request body.
type inputExampleResponse struct {
	ModelID string          `json:"model_id"`
	Name    string          `json:"name"`
	Version string          `json:"version"`
	Source  string          `json:"source"`
	Payload json.RawMessage `json:"payload"`
}

func (s *Server) modelInputExample(w http.ResponseWriter, r *http.Request) {
	if !modelAllowed(s.store, principal(r), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "model not found")
		return
	}
	model, _ := findModel(s.store.Models(), r.PathValue("id"))
	mlflow := integrations.NewMLflow(os.Getenv("MLFLOW_URL"), os.Getenv("MLFLOW_TOKEN"))
	if !mlflow.Configured() {
		writeNotConfigured(w, "Model input examples are not configured: MLFLOW_URL is not set on the gateway, so it cannot read a model's saved input example or signature.", modelInputExampleSteps)
		return
	}
	if model.Source != nil {
		// A Hub reference has no MLflow model version behind it.
		writeError(w, http.StatusNotFound, "input_example_unavailable", "this model is a "+model.Source.Provider+" reference, not an MLflow model version, so it has no saved input example or signature")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), inputExampleTimeout)
	defer cancel()
	source, payload, err := readModelInputExample(ctx, mlflow, model.Name, model.Version)
	var missing *inputExampleUnavailable
	switch {
	case errors.As(err, &missing):
		writeError(w, http.StatusNotFound, "input_example_unavailable", missing.reason)
	case err != nil:
		// The error names the MLflow address and status only; see
		// integrations.StatusError.
		log.Printf("model input example: model=%s reading MLflow failed: %v", model.ID, err)
		writeError(w, http.StatusBadGateway, "mlflow_unavailable", mlflowFailureMessage(err))
	default:
		writeJSON(w, http.StatusOK, inputExampleResponse{ModelID: model.ID, Name: model.Name, Version: model.Version, Source: source, Payload: payload})
	}
}

// mlflowFailureMessage describes a failed MLflow call without the server's
// address or anything from its response body.
func mlflowFailureMessage(err error) string {
	var status *integrations.StatusError
	switch {
	case errors.As(err, &status) && (status.Status == http.StatusUnauthorized || status.Status == http.StatusForbidden):
		return fmt.Sprintf("MLflow refused the gateway's credentials (HTTP %d); check MLFLOW_TOKEN on the gateway", status.Status)
	case errors.As(err, &status):
		return fmt.Sprintf("MLflow could not serve the model's MLmodel file or saved input example (HTTP %d)", status.Status)
	case errors.Is(err, context.DeadlineExceeded):
		return "MLflow did not answer in time"
	default:
		return "the gateway could not reach MLflow at MLFLOW_URL"
	}
}

// mlmodelFile is the part of an MLmodel file this route reads.
type mlmodelFile struct {
	SavedInputExampleInfo *struct {
		ArtifactPath     string `yaml:"artifact_path"`
		ServingInputPath string `yaml:"serving_input_path"`
		Type             string `yaml:"type"`
		PandasOrient     string `yaml:"pandas_orient"`
	} `yaml:"saved_input_example_info"`
	Signature *struct {
		Inputs string `yaml:"inputs"`
	} `yaml:"signature"`
}

// readModelInputExample returns the sample's source and its /invocations
// payload. An *inputExampleUnavailable error means the model has none; any
// other error means MLflow could not be read.
func readModelInputExample(ctx context.Context, mlflow modelArtifactReader, name, version string) (string, json.RawMessage, error) {
	raw, err := mlflow.ModelVersionArtifact(ctx, name, version, "MLmodel", maxMLmodelBytes)
	var status *integrations.StatusError
	switch {
	case errors.Is(err, integrations.ErrTooLarge):
		return "", nil, unavailable("the model's MLmodel file is larger than the gateway reads")
	case errors.As(err, &status) && status.Status == http.StatusNotFound:
		return "", nil, unavailable("MLflow has no registered model version " + name + " " + version + " with an MLmodel file")
	case err != nil:
		return "", nil, err
	}
	var model mlmodelFile
	if yaml.Unmarshal(raw, &model) != nil {
		return "", nil, unavailable("the model's MLmodel file could not be parsed")
	}
	if info := model.SavedInputExampleInfo; info != nil {
		payload, ok, err := savedInputExample(ctx, mlflow, name, version, info.ServingInputPath, info.ArtifactPath, info.Type, info.PandasOrient)
		if err != nil {
			return "", nil, err
		}
		if ok {
			return inputExampleSaved, payload, nil
		}
	}
	if model.Signature != nil && strings.TrimSpace(model.Signature.Inputs) != "" {
		if payload, ok := signatureSkeleton(model.Signature.Inputs); ok {
			return inputExampleSignature, payload, nil
		}
	}
	return "", nil, unavailable("the model has no readable saved input example and no signature inputs to derive one from")
}

// safeArtifactName accepts the file names MLflow writes beside MLmodel. The
// name comes from the artifact itself, so anything that could address another
// location is refused.
func safeArtifactName(name string) bool {
	return name != "" && len(name) <= 255 && name != "." && name != ".." && !strings.ContainsAny(name, `/\?#%`+"\x00")
}

// exampleMissing reports a saved example file that is absent or larger than
// the gateway reads: the model then has no usable saved example. Every other
// failure is MLflow refusing or failing, which the caller must report rather
// than hide behind a derived skeleton.
func exampleMissing(err error) bool {
	var status *integrations.StatusError
	return errors.Is(err, integrations.ErrTooLarge) || (errors.As(err, &status) && status.Status == http.StatusNotFound)
}

// savedInputExample reads the model's saved example as an /invocations
// payload. MLflow saves the example twice: serving_input_path already holds
// the scoring request body, and artifact_path holds the raw example, which
// is wrapped by its declared type. ok false means the model has no usable
// saved example and the caller falls back to the signature; an error means
// MLflow could not be read.
func savedInputExample(ctx context.Context, mlflow modelArtifactReader, name, version, servingPath, artifactPath, kind, orient string) (json.RawMessage, bool, error) {
	if safeArtifactName(servingPath) {
		raw, err := mlflow.ModelVersionArtifact(ctx, name, version, servingPath, maxInputExampleBytes)
		if err != nil && !exampleMissing(err) {
			return nil, false, err
		}
		if err == nil {
			if payload, ok := compactJSONObject(raw); ok {
				return payload, true, nil
			}
		}
	}
	if !safeArtifactName(artifactPath) {
		return nil, false, nil
	}
	raw, err := mlflow.ModelVersionArtifact(ctx, name, version, artifactPath, maxInputExampleBytes)
	if err != nil && !exampleMissing(err) {
		return nil, false, err
	}
	if err != nil || !json.Valid(raw) {
		return nil, false, nil
	}
	payload, ok := wrapRawExample(raw, kind, orient)
	return payload, ok, nil
}

// wrapRawExample turns the raw saved example into a scoring request body by
// its declared type.
func wrapRawExample(raw []byte, kind, orient string) (json.RawMessage, bool) {
	var wrapped any
	switch kind {
	case "dataframe":
		switch orient {
		case "", "split":
			var frame struct {
				Columns json.RawMessage `json:"columns"`
				Data    json.RawMessage `json:"data"`
			}
			if json.Unmarshal(raw, &frame) != nil || frame.Data == nil {
				return nil, false
			}
			split := map[string]json.RawMessage{"data": frame.Data}
			if frame.Columns != nil {
				split["columns"] = frame.Columns
			}
			wrapped = map[string]any{"dataframe_split": split}
		case "records":
			wrapped = map[string]any{"dataframe_records": json.RawMessage(raw)}
		default:
			return nil, false
		}
	case "ndarray":
		// Saved as {"inputs": ...} (or {"instances": ...}); older releases
		// saved the bare array.
		var keyed map[string]json.RawMessage
		if json.Unmarshal(raw, &keyed) == nil && (keyed["inputs"] != nil || keyed["instances"] != nil) {
			return compactJSONObject(raw)
		}
		wrapped = map[string]any{"inputs": json.RawMessage(raw)}
	case "json_object":
		wrapped = map[string]any{"inputs": json.RawMessage(raw)}
	default:
		// Sparse matrices and unknown types have no scoring request form.
		return nil, false
	}
	payload, err := json.Marshal(wrapped)
	if err != nil || len(payload) > maxInputExampleBytes {
		return nil, false
	}
	return payload, true
}

// compactJSONObject returns raw as compact JSON when it is a JSON object.
func compactJSONObject(raw []byte) (json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) == 0 {
		return nil, false
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return nil, false
	}
	return compact.Bytes(), true
}

// signatureSkeleton builds an /invocations payload from the signature's
// inputs, a JSON list of column or tensor specs. Column specs become one
// dataframe_split row; tensor specs become inputs shaped like the tensor with
// each variable dimension (-1) at length one. Every value is a placeholder
// for its type, never real data.
func signatureSkeleton(inputs string) (json.RawMessage, bool) {
	var specs []map[string]any
	if json.Unmarshal([]byte(inputs), &specs) != nil || len(specs) == 0 || len(specs) > maxSkeletonCells {
		return nil, false
	}
	var skeleton any
	if specs[0]["type"] == "tensor" {
		tensors, ok := tensorSkeleton(specs)
		if !ok {
			return nil, false
		}
		skeleton = map[string]any{"inputs": tensors}
	} else {
		columns, row := make([]any, len(specs)), make([]any, len(specs))
		named := true
		for _, spec := range specs {
			if name, _ := spec["name"].(string); name == "" {
				named = false
			}
		}
		for index, spec := range specs {
			if spec["type"] == "tensor" {
				return nil, false
			}
			// A signature without column names matches columns by position.
			columns[index] = index
			if named {
				columns[index] = spec["name"]
			}
			row[index] = columnPlaceholder(spec, 0)
		}
		skeleton = map[string]any{"dataframe_split": map[string]any{"columns": columns, "data": []any{row}}}
	}
	payload, err := json.Marshal(skeleton)
	if err != nil || len(payload) > maxInputExampleBytes {
		return nil, false
	}
	return payload, true
}

// columnPlaceholder is the placeholder for one MLflow column type. Floating
// point types use 0.5 rather than 0 so the value still reads as a float after
// a JSON round trip.
func columnPlaceholder(spec map[string]any, depth int) any {
	if depth > maxSkeletonDepth {
		return nil
	}
	switch spec["type"] {
	case "double", "float":
		return 0.5
	case "long", "integer":
		return 0
	case "boolean":
		return false
	case "string":
		return "string"
	case "binary":
		return "" // base64
	case "datetime":
		return "2024-01-01T00:00:00"
	case "array":
		if items, ok := spec["items"].(map[string]any); ok {
			return []any{columnPlaceholder(items, depth+1)}
		}
		return []any{}
	case "object":
		object := map[string]any{}
		properties, _ := spec["properties"].(map[string]any)
		for name, property := range properties {
			if nested, ok := property.(map[string]any); ok {
				object[name] = columnPlaceholder(nested, depth+1)
			}
		}
		return object
	case "map":
		if values, ok := spec["values"].(map[string]any); ok {
			return map[string]any{"key": columnPlaceholder(values, depth+1)}
		}
		return map[string]any{}
	}
	return nil
}

// tensorSkeleton shapes the placeholders for tensor specs: the bare nested
// array for one unnamed tensor, otherwise an object keyed by tensor name.
func tensorSkeleton(specs []map[string]any) (any, bool) {
	cells := 0
	build := func(spec map[string]any) (any, bool) {
		tensor, _ := spec["tensor-spec"].(map[string]any)
		if spec["type"] != "tensor" || tensor == nil {
			return nil, false
		}
		dtype, _ := tensor["dtype"].(string)
		value := tensorPlaceholder(dtype)
		shape, _ := tensor["shape"].([]any)
		dimensions := make([]int, 0, len(shape))
		total := 1
		for _, raw := range shape {
			size, ok := raw.(float64)
			if !ok || size != float64(int(size)) || size < -1 || size == 0 {
				return nil, false
			}
			if size == -1 {
				size = 1
			}
			if total *= int(size); total > maxSkeletonCells {
				return nil, false
			}
			dimensions = append(dimensions, int(size))
		}
		if cells += total; cells > maxSkeletonCells {
			return nil, false
		}
		if len(dimensions) == 0 {
			// No declared shape: one row of one value.
			return []any{value}, true
		}
		return nestedTensor(dimensions, value), true
	}
	if len(specs) == 1 {
		if name, _ := specs[0]["name"].(string); name == "" {
			return build(specs[0])
		}
	}
	named := map[string]any{}
	for _, spec := range specs {
		name, _ := spec["name"].(string)
		value, ok := build(spec)
		if name == "" || !ok {
			return nil, false
		}
		named[name] = value
	}
	return named, true
}

func nestedTensor(dimensions []int, value any) any {
	if len(dimensions) == 0 {
		return value
	}
	level := make([]any, dimensions[0])
	for index := range level {
		level[index] = nestedTensor(dimensions[1:], value)
	}
	return level
}

// tensorPlaceholder is the placeholder for a numpy dtype name.
func tensorPlaceholder(dtype string) any {
	switch {
	case strings.HasPrefix(dtype, "float"):
		return 0.5
	case strings.HasPrefix(dtype, "int"), strings.HasPrefix(dtype, "uint"):
		return 0
	case dtype == "bool":
		return false
	case strings.HasPrefix(dtype, "datetime"):
		return "2024-01-01T00:00:00"
	case dtype == "object", strings.HasPrefix(dtype, "str"), strings.HasPrefix(dtype, "<U"), strings.HasPrefix(dtype, "bytes"):
		return "string"
	}
	return nil
}

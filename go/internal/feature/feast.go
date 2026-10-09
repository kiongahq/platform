package feature

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// FeastClient serves lookups by delegating to a real Feast feature server
// (feast serve / feastdev/feature-server). It translates between the platform
// request shape (one map per entity) and Feast's columnar HTTP contract.
type FeastClient struct {
	BaseURL string
	Client  *http.Client
	// Token, when set, is sent as a bearer credential (Feast's OIDC or
	// Kubernetes auth). It is resolved from a secret reference, never stored.
	Token string
}

func NewFeastClient(baseURL string) *FeastClient {
	return &FeastClient{BaseURL: baseURL, Client: &http.Client{Timeout: 5 * time.Second}}
}

type feastRequest struct {
	FeatureService string           `json:"feature_service"`
	Entities       map[string][]any `json:"entities"`
}

type feastResponse struct {
	Metadata struct {
		FeatureNames []string `json:"feature_names"`
	} `json:"metadata"`
	Results []struct {
		Values   []any    `json:"values"`
		Statuses []string `json:"statuses"`
	} `json:"results"`
}

// Lookup implements the platform lookup contract against Feast. Feast returns
// one column per feature; the platform returns one row per entity.
func (c *FeastClient) Lookup(request Request) (Response, error) {
	return c.LookupContext(context.Background(), request)
}

// LookupContext is Lookup bounded by ctx.
func (c *FeastClient) LookupContext(ctx context.Context, request Request) (Response, error) {
	if request.FeatureService == "" || len(request.Entities) == 0 {
		return Response{}, errors.New("feature_service and at least one entity are required")
	}
	entities := map[string][]any{}
	for _, entity := range request.Entities {
		if _, err := EntityKey(entity); err != nil {
			return Response{}, err
		}
		for key, value := range entity {
			entities[key] = append(entities[key], value)
		}
	}
	body, _ := json.Marshal(feastRequest{FeatureService: request.FeatureService, Entities: entities})
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/get-online-features", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.Token)
	}
	httpResponse, err := c.Client.Do(httpRequest)
	if err != nil {
		return Response{}, fmt.Errorf("feast server unreachable: %w", err)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	if httpResponse.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("feast server returned %s", httpResponse.Status)
	}
	var decoded feastResponse
	if err := json.NewDecoder(httpResponse.Body).Decode(&decoded); err != nil {
		return Response{}, fmt.Errorf("invalid feast response: %w", err)
	}
	if len(decoded.Results) != len(decoded.Metadata.FeatureNames) {
		return Response{}, errors.New("feast response feature names and results are misaligned")
	}
	response := Response{Results: make([]Result, len(request.Entities))}
	for i := range request.Entities {
		values, statuses := map[string]any{}, map[string]string{}
		for j, name := range decoded.Metadata.FeatureNames {
			column := decoded.Results[j]
			if i < len(column.Values) {
				values[name] = column.Values[i]
			}
			if i < len(column.Statuses) {
				statuses[name] = column.Statuses[i]
			}
		}
		response.Results[i] = Result{Values: values, Statuses: statuses}
	}
	return response, nil
}

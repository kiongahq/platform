package store

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ml-ai-ops/platform/pkg/api"
)

func validateAccessRequest(req api.CreateAccessRequest) (api.CreateAccessRequest, error) {
	req.Reason = strings.TrimSpace(req.Reason)
	req.RequestedServices = unique(req.RequestedServices)
	if len(req.Reason) < 10 {
		return req, errors.New("reason must contain at least 10 characters")
	}
	if len(req.RequestedServices) == 0 {
		return req, errors.New("at least one service is required")
	}
	for _, service := range req.RequestedServices {
		if !slices.Contains(validServices, service) {
			return req, errors.New("unknown service: " + service)
		}
	}
	return req, nil
}

func (s *Store) AccessRequests() []api.AccessRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.data.AccessRequests)
}

func (s *Store) AccessRequestsFor(subject string) []api.AccessRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]api.AccessRequest, 0)
	for _, request := range s.data.AccessRequests {
		if request.Subject == subject {
			result = append(result, request)
		}
	}
	return result
}

func (s *Store) CreateAccessRequest(subject, email string, req api.CreateAccessRequest) (api.AccessRequest, error) {
	req, err := validateAccessRequest(req)
	if err != nil {
		return api.AccessRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.data.AccessRequests {
		if request.Subject == subject && request.Status == "pending" {
			return api.AccessRequest{}, errors.New("a pending access request already exists")
		}
	}
	now := time.Now().UTC()
	request := api.AccessRequest{ID: id("access-request"), Subject: subject, Email: email, Reason: req.Reason, RequestedServices: req.RequestedServices, Status: "pending", CreatedAt: now, UpdatedAt: now}
	s.data.AccessRequests = append([]api.AccessRequest{request}, s.data.AccessRequests...)
	s.record("access.requested", "access_request", request.ID, subject, nil)
	return request, s.persist()
}

func (s *Store) ReviewAccessRequest(requestID string, req api.ReviewAccessRequest, reviewer string) (api.AccessRequest, error) {
	if req.Status != "approved" && req.Status != "rejected" {
		return api.AccessRequest{}, errors.New("status must be approved or rejected")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.AccessRequests {
		if s.data.AccessRequests[i].ID == requestID {
			if s.data.AccessRequests[i].Status != "pending" {
				return api.AccessRequest{}, errors.New("access request has already been reviewed")
			}
			s.data.AccessRequests[i].Status = req.Status
			s.data.AccessRequests[i].Reviewer = reviewer
			s.data.AccessRequests[i].ReviewNote = strings.TrimSpace(req.Note)
			s.data.AccessRequests[i].UpdatedAt = time.Now().UTC()
			s.record("access.request_"+req.Status, "access_request", requestID, reviewer, nil)
			return s.data.AccessRequests[i], s.persist()
		}
	}
	return api.AccessRequest{}, ErrNotFound
}

var validServices = []string{
	"overview", "projects", "pipelines", "models", "agents", "features",
	"storage", "realtime", "catalog", "platform", "functions", "git", "workbench", "ide",
}

// ResourceProfiles is the canonical sizing catalog used by both API
// validation and the admin console. Non-custom profiles are intentionally
// opinionated and replace individual limits to prevent ambiguous partial
// presets.
func ResourceProfiles() []api.ResourceProfile {
	return []api.ResourceProfile{
		{Name: "starter", Label: "Starter", Description: "Notebook exploration and small development runs", Compute: api.ComputeGrant{Profile: "starter", VCPUs: 2, MemoryGB: 4, MaxVMs: 1, MaxProjects: 2, MaxRuns: 1, MaxFunctions: 3}, StorageGB: 25},
		{Name: "team", Label: "Team", Description: "Daily model development and shared pipelines", Compute: api.ComputeGrant{Profile: "team", VCPUs: 4, MemoryGB: 8, MaxVMs: 2, MaxProjects: 5, MaxRuns: 3, MaxFunctions: 10}, StorageGB: 100},
		{Name: "power", Label: "Power", Description: "Large training runs and production delivery", Compute: api.ComputeGrant{Profile: "power", VCPUs: 8, MemoryGB: 16, MaxVMs: 4, MaxProjects: 10, MaxRuns: 6, MaxFunctions: 25}, StorageGB: 250},
		{Name: "gpu", Label: "GPU", Description: "Accelerated model and generative AI development", Compute: api.ComputeGrant{Profile: "gpu", VCPUs: 8, MemoryGB: 32, GPUs: 1, GPUType: "nvidia.com/gpu", MaxVMs: 2, MaxProjects: 10, MaxRuns: 4, MaxFunctions: 15}, StorageGB: 500},
		{Name: "custom", Label: "Custom", Description: "Set each resource boundary explicitly", Compute: api.ComputeGrant{Profile: "custom"}},
	}
}

func applyResourceProfile(req *api.UpsertUserAccessRequest) error {
	profile := strings.TrimSpace(req.Compute.Profile)
	if profile == "" {
		profile = "custom"
		req.Compute.Profile = profile
	}
	for _, candidate := range ResourceProfiles() {
		if candidate.Name != profile {
			continue
		}
		if profile != "custom" {
			req.Compute = candidate.Compute
			req.Storage.SizeGB = candidate.StorageGB
		}
		return nil
	}
	return errors.New("unknown resource profile: " + profile)
}

func validateAccess(subject string, req api.UpsertUserAccessRequest) (api.UserAccess, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return api.UserAccess{}, errors.New("subject is required")
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if !slices.Contains([]string{"admin", "operator", "engineer", "viewer", "user"}, req.Role) {
		return api.UserAccess{}, errors.New("role must be admin, operator, engineer, viewer or user")
	}
	req.Services = unique(req.Services)
	req.ProjectIDs = unique(req.ProjectIDs)
	req.Storage.Buckets = unique(req.Storage.Buckets)
	for _, service := range req.Services {
		if !slices.Contains(validServices, service) {
			return api.UserAccess{}, errors.New("unknown service: " + service)
		}
	}
	if err := applyResourceProfile(&req); err != nil {
		return api.UserAccess{}, err
	}
	if req.Storage.SizeGB < 0 || req.Compute.VCPUs < 0 || req.Compute.MemoryGB < 0 ||
		req.Compute.GPUs < 0 || req.Compute.MaxVMs < 0 || req.Compute.MaxProjects < 0 ||
		req.Compute.MaxRuns < 0 || req.Compute.MaxFunctions < 0 {
		return api.UserAccess{}, errors.New("resource limits cannot be negative")
	}
	return api.UserAccess{
		Subject: subject, Email: strings.TrimSpace(req.Email), Role: req.Role,
		Services: req.Services, ProjectIDs: req.ProjectIDs, Storage: req.Storage,
		Compute: req.Compute, Disabled: req.Disabled,
	}, nil
}

func unique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

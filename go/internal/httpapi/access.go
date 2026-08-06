package httpapi

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

func principal(r interface{ Context() context.Context }) auth.Principal {
	value, _ := auth.PrincipalFrom(r.Context())
	return value
}

func privileged(value auth.Principal) bool {
	return slices.Contains(value.Roles, auth.RoleAdmin) ||
		slices.Contains(value.Roles, auth.RoleOperator) ||
		slices.Contains(value.Roles, auth.RoleService)
}

func accessFor(repository store.Repository, value auth.Principal) any {
	if privileged(value) {
		return nil
	}
	access, err := repository.AccessFor(value.Subject)
	if err != nil {
		return nil
	}
	return access
}

func allowedProjectIDs(repository store.Repository, value auth.Principal) map[string]bool {
	if privileged(value) || !slices.Contains(value.Roles, auth.RoleUser) {
		return nil
	}
	allowed := make(map[string]bool, len(value.ProjectIDs))
	for _, id := range value.ProjectIDs {
		allowed[id] = true
	}
	if value.Credential == "api_token" {
		return allowed
	}
	for _, project := range repository.Projects() {
		if project.OwnerSubject == value.Subject {
			allowed[project.ID] = true
		}
	}
	return allowed
}

func projectAllowed(repository store.Repository, value auth.Principal, projectID string) bool {
	allowed := allowedProjectIDs(repository, value)
	return allowed == nil || allowed[projectID]
}

func filterProjects(items []api.Project, value auth.Principal) []api.Project {
	if !slices.Contains(value.Roles, auth.RoleUser) {
		return items
	}
	allowed := make(map[string]bool, len(value.ProjectIDs))
	for _, id := range value.ProjectIDs {
		allowed[id] = true
	}
	result := make([]api.Project, 0)
	for _, item := range items {
		if allowed[item.ID] || item.OwnerSubject == value.Subject {
			result = append(result, item)
		}
	}
	return result
}

func filterRuns(items []api.PipelineRun, allowed map[string]bool) []api.PipelineRun {
	if allowed == nil {
		return items
	}
	result := make([]api.PipelineRun, 0)
	for _, item := range items {
		if allowed[item.ProjectID] {
			result = append(result, item)
		}
	}
	return result
}

func filterPipelineDefinitions(items []api.PipelineDefinition, allowed map[string]bool) []api.PipelineDefinition {
	if allowed == nil {
		return items
	}
	result := make([]api.PipelineDefinition, 0)
	for _, item := range items {
		if allowed[item.ProjectID] {
			result = append(result, item)
		}
	}
	return result
}

func filterFunctions(items []api.Function, allowed map[string]bool) []api.Function {
	if allowed == nil {
		return items
	}
	result := make([]api.Function, 0)
	for _, item := range items {
		if allowed[item.ProjectID] {
			result = append(result, item)
		}
	}
	return result
}

func filterModels(items []api.Model, allowed map[string]bool) []api.Model {
	if allowed == nil {
		return items
	}
	result := make([]api.Model, 0)
	for _, item := range items {
		if allowed[item.ProjectID] {
			result = append(result, item)
		}
	}
	return result
}

func filterAgents(items []api.Agent, allowed map[string]bool) []api.Agent {
	if allowed == nil {
		return items
	}
	result := make([]api.Agent, 0)
	for _, item := range items {
		if allowed[item.ProjectID] {
			result = append(result, item)
		}
	}
	return result
}

type scopedCatalog struct {
	repository store.Repository
	allowed    map[string]bool
}

func (s scopedCatalog) Models() []api.Model             { return filterModels(s.repository.Models(), s.allowed) }
func (s scopedCatalog) Agents() []api.Agent             { return filterAgents(s.repository.Agents(), s.allowed) }
func (s scopedCatalog) Tools() []api.Tool               { return s.repository.Tools() }
func (s scopedCatalog) FeatureViews() []api.FeatureView { return s.repository.FeatureViews() }

func modelAllowed(repository store.Repository, value auth.Principal, modelID string) bool {
	for _, model := range repository.Models() {
		if model.ID == modelID {
			return projectAllowed(repository, value, model.ProjectID)
		}
	}
	return false
}

func agentAllowed(repository store.Repository, value auth.Principal, agentID string) bool {
	for _, agent := range repository.Agents() {
		if agent.ID == agentID {
			return projectAllowed(repository, value, agent.ProjectID)
		}
	}
	return false
}

func enforceProjectQuota(repository store.Repository, value auth.Principal) error {
	if privileged(value) || !slices.Contains(value.Roles, auth.RoleUser) {
		return nil
	}
	access, err := repository.AccessFor(value.Subject)
	if err != nil || access.Compute.MaxProjects == 0 {
		return errors.New("no project capacity has been provisioned")
	}
	owned := 0
	for _, project := range repository.Projects() {
		if project.OwnerSubject == value.Subject {
			owned++
		}
	}
	if owned >= access.Compute.MaxProjects {
		return fmt.Errorf("project quota reached (%d)", access.Compute.MaxProjects)
	}
	return nil
}

// enforceProjectTemplateAccess prevents project metadata from promising a
// runtime the user's administrator has not provisioned. Privileged operators
// can create any template; normal users are constrained by both services and
// accelerator capacity in their access grant.
func enforceProjectTemplateAccess(repository store.Repository, value auth.Principal, req *api.CreateProjectRequest) error {
	if privileged(value) || !slices.Contains(value.Roles, auth.RoleUser) {
		return nil
	}
	template, err := api.NormalizeProjectRequest(req)
	if err != nil {
		return err
	}
	access, err := repository.AccessFor(value.Subject)
	if err != nil {
		return errors.New("no resource grant has been provisioned")
	}
	for _, service := range template.RequiredServices {
		if !slices.Contains(access.Services, service) {
			return fmt.Errorf("the %s service is required by template %s but is not provisioned", service, template.ID)
		}
	}
	if req.RequestedProfile != "custom" {
		for _, profile := range store.ResourceProfiles() {
			if profile.Name != req.RequestedProfile {
				continue
			}
			if access.Compute.VCPUs < profile.Compute.VCPUs ||
				access.Compute.MemoryGB < profile.Compute.MemoryGB ||
				access.Compute.GPUs < profile.Compute.GPUs ||
				access.Storage.SizeGB < profile.StorageGB {
				return fmt.Errorf("template %s requests the %s profile, which exceeds the provisioned compute or storage grant", template.ID, profile.Name)
			}
			break
		}
	}
	requiredGPUs := 0
	switch req.Accelerator {
	case "single-gpu":
		requiredGPUs = 1
	case "multi-gpu":
		requiredGPUs = 2
	}
	if access.Compute.GPUs < requiredGPUs {
		return fmt.Errorf("template %s requests %s but the grant provides %d GPUs", template.ID, req.Accelerator, access.Compute.GPUs)
	}
	return nil
}

type pipelineResourceUsage struct {
	cpuMilli    int64
	memoryBytes int64
	gpus        int
}

// enforcePipelineResources evaluates the widest concurrently runnable layer
// of a DAG. This lets users compose large sequential flows while preventing a
// branch from requesting more CPU, memory, or accelerators than their grant.
func enforcePipelineResources(repository store.Repository, value auth.Principal, jobs []api.PipelineJob) error {
	if privileged(value) || !slices.Contains(value.Roles, auth.RoleUser) {
		return nil
	}
	access, err := repository.AccessFor(value.Subject)
	if err != nil {
		return errors.New("no compute grant has been provisioned")
	}
	byName := make(map[string]api.PipelineJob, len(jobs))
	for _, job := range jobs {
		byName[job.Name] = job
	}
	layers, visiting := map[string]int{}, map[string]bool{}
	var layerOf func(string) (int, error)
	layerOf = func(name string) (int, error) {
		if layer, ok := layers[name]; ok {
			return layer, nil
		}
		if visiting[name] {
			return 0, errors.New("pipeline graph contains a cycle")
		}
		job, ok := byName[name]
		if !ok {
			return 0, fmt.Errorf("pipeline dependency %s does not exist", name)
		}
		visiting[name] = true
		layer := 0
		for _, dependency := range job.DependsOn {
			parentLayer, err := layerOf(dependency)
			if err != nil {
				return 0, err
			}
			if parentLayer+1 > layer {
				layer = parentLayer + 1
			}
		}
		visiting[name] = false
		layers[name] = layer
		return layer, nil
	}
	usageByLayer := map[int]pipelineResourceUsage{}
	for _, job := range jobs {
		layer, err := layerOf(job.Name)
		if err != nil {
			return err
		}
		cpu, err := pipelineQuantity(job.Resources.CPU, "cpu")
		if err != nil {
			return err
		}
		memory, err := pipelineQuantity(job.Resources.Memory, "memory")
		if err != nil {
			return err
		}
		usage := usageByLayer[layer]
		usage.cpuMilli += cpu
		usage.memoryBytes += memory
		usage.gpus += job.Resources.GPU
		usageByLayer[layer] = usage
	}
	for layer, usage := range usageByLayer {
		if usage.cpuMilli > int64(access.Compute.VCPUs)*1000 {
			return fmt.Errorf("pipeline layer %d requests %.3g CPUs but the grant provides %d", layer+1, float64(usage.cpuMilli)/1000, access.Compute.VCPUs)
		}
		if usage.memoryBytes > int64(access.Compute.MemoryGB)*(1<<30) {
			return fmt.Errorf("pipeline layer %d requests more than the %d GiB memory grant", layer+1, access.Compute.MemoryGB)
		}
		if usage.gpus > access.Compute.GPUs {
			return fmt.Errorf("pipeline layer %d requests %d GPUs but the grant provides %d", layer+1, usage.gpus, access.Compute.GPUs)
		}
	}
	return nil
}

func enforceFunctionResources(repository store.Repository, value auth.Principal, req api.DeployFunctionRequest) error {
	if privileged(value) || !slices.Contains(value.Roles, auth.RoleUser) {
		return nil
	}
	access, err := repository.AccessFor(value.Subject)
	if err != nil {
		return errors.New("no compute grant has been provisioned")
	}
	cpu, err := pipelineQuantity(req.CPU, "cpu")
	if err != nil {
		return err
	}
	memory, err := pipelineQuantity(req.Memory, "memory")
	if err != nil {
		return err
	}
	if cpu > int64(access.Compute.VCPUs)*1000 {
		return fmt.Errorf("function requests %.3g CPUs but the grant provides %d", float64(cpu)/1000, access.Compute.VCPUs)
	}
	if memory > int64(access.Compute.MemoryGB)*(1<<30) {
		return fmt.Errorf("function requests more than the %d GiB memory grant", access.Compute.MemoryGB)
	}
	return nil
}

func enforceAgentResources(repository store.Repository, value auth.Principal, req api.DeployAgentRequest) error {
	if privileged(value) || !slices.Contains(value.Roles, auth.RoleUser) {
		return nil
	}
	access, err := repository.AccessFor(value.Subject)
	if err != nil {
		return errors.New("no compute grant has been provisioned")
	}
	requested, err := agentCapacity(req.Autoscaling, req.Resources, req.Replicas)
	if err != nil {
		return err
	}
	total := requested
	allowed := allowedProjectIDs(repository, value)
	for _, agent := range repository.Agents() {
		// New records are charged to their creator. Legacy records did not carry
		// ownership, so conservatively charge them when they are in an assigned
		// project instead of silently allowing a tenant to exceed its grant.
		if agent.OwnerSubject != "" && agent.OwnerSubject != value.Subject {
			continue
		}
		if agent.OwnerSubject == "" && (allowed == nil || !allowed[agent.ProjectID]) {
			continue
		}
		used, capacityErr := agentCapacity(agent.Autoscaling, agent.Resources, agent.Replicas)
		if capacityErr != nil {
			return fmt.Errorf("existing agent %s has invalid resources: %w", agent.ID, capacityErr)
		}
		total.add(used)
	}
	if total.cpuMilli > int64(access.Compute.VCPUs)*1000 {
		return fmt.Errorf("agent capacity requests %.3g CPUs including autoscaling and sidecars, but the grant provides %d", float64(total.cpuMilli)/1000, access.Compute.VCPUs)
	}
	if total.memoryBytes > int64(access.Compute.MemoryGB)*(1<<30) {
		return fmt.Errorf("agent capacity requests more than the %d GiB memory grant including autoscaling and sidecars", access.Compute.MemoryGB)
	}
	if total.gpus > access.Compute.GPUs {
		return fmt.Errorf("agent capacity requests %d GPUs but the grant provides %d", total.gpus, access.Compute.GPUs)
	}
	if req.Resources.GPU > 0 && access.Compute.GPUType != "" && req.Resources.GPUType != access.Compute.GPUType {
		return fmt.Errorf("agent requests GPU type %s but the grant provides %s", req.Resources.GPUType, access.Compute.GPUType)
	}
	if access.Compute.MaxVMs < 1 || total.replicas > access.Compute.MaxVMs {
		return fmt.Errorf("agent capacity reserves %d maximum replicas but the provisioned workload limit is %d", total.replicas, access.Compute.MaxVMs)
	}
	return nil
}

const (
	agentSidecarCPUMilli    int64 = 100
	agentSidecarMemoryBytes int64 = 128 << 20
)

type agentCapacityUsage struct {
	cpuMilli    int64
	memoryBytes int64
	gpus        int
	replicas    int
}

func (u *agentCapacityUsage) add(other agentCapacityUsage) {
	u.cpuMilli += other.cpuMilli
	u.memoryBytes += other.memoryBytes
	u.gpus += other.gpus
	u.replicas += other.replicas
}

func agentCapacity(autoscaling api.AgentAutoscaling, resources api.AgentResources, legacyReplicas int) (agentCapacityUsage, error) {
	maximum := autoscaling.MaxReplicas
	if maximum < 1 {
		maximum = autoscaling.MinReplicas
	}
	if maximum < 1 {
		maximum = legacyReplicas
	}
	if maximum < 1 {
		maximum = 1
	}
	cpuValue := resources.CPU
	if cpuValue == "" {
		cpuValue = "500m"
	}
	memoryValue := resources.Memory
	if memoryValue == "" {
		memoryValue = "1Gi"
	}
	cpu, err := pipelineQuantity(cpuValue, "cpu")
	if err != nil {
		return agentCapacityUsage{}, err
	}
	memory, err := pipelineQuantity(memoryValue, "memory")
	if err != nil {
		return agentCapacityUsage{}, err
	}
	return agentCapacityUsage{
		cpuMilli:    (cpu + agentSidecarCPUMilli) * int64(maximum),
		memoryBytes: (memory + agentSidecarMemoryBytes) * int64(maximum),
		gpus:        resources.GPU * maximum,
		replicas:    maximum,
	}, nil
}

func pipelineQuantity(value, kind string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	multiplier, number := int64(1), value
	switch kind {
	case "cpu":
		multiplier = 1000
		if strings.HasSuffix(value, "m") {
			multiplier, number = 1, strings.TrimSuffix(value, "m")
		}
	case "memory":
		switch {
		case strings.HasSuffix(value, "Gi"):
			multiplier, number = 1<<30, strings.TrimSuffix(value, "Gi")
		case strings.HasSuffix(value, "Mi"):
			multiplier, number = 1<<20, strings.TrimSuffix(value, "Mi")
		}
	}
	parsed, err := strconv.ParseInt(number, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid %s quantity %q", kind, value)
	}
	return parsed * multiplier, nil
}

func enforceRunQuota(repository store.Repository, value auth.Principal) error {
	if privileged(value) || !slices.Contains(value.Roles, auth.RoleUser) {
		return nil
	}
	access, err := repository.AccessFor(value.Subject)
	if err != nil || access.Compute.MaxRuns == 0 {
		return errors.New("no concurrent run capacity has been provisioned")
	}
	active, allowed := 0, allowedProjectIDs(repository, value)
	for _, run := range repository.Runs() {
		if allowed[run.ProjectID] && (run.Status == "queued" || run.Status == "running") {
			active++
		}
	}
	if active >= access.Compute.MaxRuns {
		return fmt.Errorf("concurrent run quota reached (%d)", access.Compute.MaxRuns)
	}
	return nil
}

func enforceFunctionQuota(repository store.Repository, value auth.Principal) error {
	if privileged(value) || !slices.Contains(value.Roles, auth.RoleUser) {
		return nil
	}
	access, err := repository.AccessFor(value.Subject)
	if err != nil || access.Compute.MaxFunctions == 0 {
		return errors.New("no function capacity has been provisioned")
	}
	count := 0
	for _, function := range repository.Functions() {
		if function.OwnerSubject == value.Subject {
			count++
		}
	}
	if count >= access.Compute.MaxFunctions {
		return fmt.Errorf("function quota reached (%d)", access.Compute.MaxFunctions)
	}
	return nil
}

func functionAllowed(repository store.Repository, value auth.Principal, name string) bool {
	for _, function := range repository.Functions() {
		if function.Name == name {
			return projectAllowed(repository, value, function.ProjectID)
		}
	}
	return privileged(value)
}

func storageAllowed(repository store.Repository, value auth.Principal, bucket string) bool {
	if privileged(value) || !slices.Contains(value.Roles, auth.RoleUser) {
		return true
	}
	access, err := repository.AccessFor(value.Subject)
	if err != nil || access.Storage.SizeGB == 0 {
		return false
	}
	if bucket == "" {
		return true
	}
	return slices.Contains(access.Storage.Buckets, bucket)
}

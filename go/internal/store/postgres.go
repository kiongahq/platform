package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiongahq/platform/pkg/api"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Postgres struct {
	pool   *pgxpool.Pool
	tenant string
}

type OutboxEvent struct {
	Sequence int64
	ID       string
	Topic    string
	Key      string
	Payload  json.RawMessage
	Attempts int
}

func OpenPostgres(ctx context.Context, databaseURL, tenant string) (*Postgres, error) {
	if tenant == "" {
		tenant = "default"
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	config.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		_, err := connection.Exec(ctx, `SELECT set_config('app.tenant_id', $1, false)`, tenant)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	repository := &Postgres{pool: pool, tenant: tenant}
	if err := repository.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return repository, nil
}

func (p *Postgres) Close()                         { p.pool.Close() }
func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

func (p *Postgres) Migrate(ctx context.Context) error {
	connection, err := p.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	// Every gateway replica may start together. Hold a session-level advisory
	// lock until all embedded migrations have completed.
	const migrationLock int64 = 7033064489216845271
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLock); err != nil {
		return err
	}
	defer connection.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLock)
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err := connection.Exec(ctx, string(raw)); err != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (p *Postgres) Projects() []api.Project { return list[api.Project](p, "project") }
func (p *Postgres) Runs() []api.PipelineRun { return list[api.PipelineRun](p, "pipeline_run") }
func (p *Postgres) PipelineDefinitions() []api.PipelineDefinition {
	return list[api.PipelineDefinition](p, "pipeline_definition")
}
func (p *Postgres) Functions() []api.Function     { return list[api.Function](p, "function") }
func (p *Postgres) Models() []api.Model           { return list[api.Model](p, "model") }
func (p *Postgres) Agents() []api.Agent           { return list[api.Agent](p, "agent") }
func (p *Postgres) Tools() []api.Tool             { return list[api.Tool](p, "tool") }
func (p *Postgres) Connections() []api.Connection { return list[api.Connection](p, "connection") }
func (p *Postgres) UserAccess() []api.UserAccess  { return list[api.UserAccess](p, "user_access") }
func (p *Postgres) AccessRequests() []api.AccessRequest {
	return list[api.AccessRequest](p, "access_request")
}

func (p *Postgres) APITokensFor(subject string) []api.APIToken {
	result := make([]api.APIToken, 0)
	for _, token := range list[api.APIToken](p, "api_token") {
		if token.Subject == subject {
			result = append(result, token)
		}
	}
	return result
}

func (p *Postgres) BlogPosts() []api.BlogPost { return list[api.BlogPost](p, "blog_post") }
func (p *Postgres) BlogPost(identifier string) (api.BlogPost, error) {
	if post, err := get[api.BlogPost](p, "blog_post", identifier); err == nil {
		return post, nil
	}
	for _, post := range p.BlogPosts() {
		if post.Slug == identifier {
			return post, nil
		}
	}
	return api.BlogPost{}, ErrNotFound
}

func (p *Postgres) UpsertBlogPost(postID string, req api.UpsertBlogPostRequest, actor string) (api.BlogPost, error) {
	req, err := validateBlog(req)
	if err != nil {
		return api.BlogPost{}, err
	}
	for _, existing := range p.BlogPosts() {
		if existing.Slug == req.Slug && existing.ID != postID {
			return api.BlogPost{}, ErrConflict
		}
	}
	now := time.Now().UTC()
	if postID != "" {
		post, getErr := p.BlogPost(postID)
		if getErr != nil {
			return post, getErr
		}
		post.Slug, post.Title, post.Summary, post.Content = req.Slug, req.Title, req.Summary, req.Content
		post.Author, post.Tags, post.Status, post.UpdatedAt = req.Author, req.Tags, req.Status, now
		if post.Status == "published" && post.PublishedAt == nil {
			post.PublishedAt = &now
		}
		return post, p.write("blog_post", post.ID, post, "blog.updated", actor, map[string]any{"status": post.Status})
	}
	post := api.BlogPost{ID: id("blog"), Slug: req.Slug, Title: req.Title, Summary: req.Summary, Content: req.Content, Author: req.Author, Tags: req.Tags, Status: req.Status, CreatedAt: now, UpdatedAt: now}
	if post.Status == "published" {
		post.PublishedAt = &now
	}
	return post, p.write("blog_post", post.ID, post, "blog.created", actor, map[string]any{"status": post.Status})
}

func (p *Postgres) DeleteBlogPost(postID, actor string) error {
	post, err := p.BlogPost(postID)
	if err != nil {
		return err
	}
	tag, err := p.pool.Exec(context.Background(), `DELETE FROM platform_resources WHERE tenant_id=$1 AND kind='blog_post' AND id=$2`, p.tenant, post.ID)
	if err != nil || tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	event := api.AuditEvent{ID: id("evt"), Action: "blog.deleted", Resource: "blog_post", ResourceID: post.ID, Actor: actorOrAnonymous(actor), CreatedAt: time.Now().UTC()}
	_, err = p.pool.Exec(context.Background(), `INSERT INTO audit_events (tenant_id,id,action,resource,resource_id,actor,metadata,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, p.tenant, event.ID, event.Action, event.Resource, event.ResourceID, event.Actor, event.Metadata, event.CreatedAt)
	return err
}

func (p *Postgres) CreateAPIToken(subject string, req api.CreateAPITokenRequest) (api.CreatedAPIToken, error) {
	req, err := validateTokenRequest(req)
	if err != nil {
		return api.CreatedAPIToken{}, err
	}
	tokenID, prefix, secret, hash, err := generateAPIToken()
	if err != nil {
		return api.CreatedAPIToken{}, err
	}
	now := time.Now().UTC()
	token := api.APIToken{ID: tokenID, Subject: subject, Name: req.Name, Prefix: prefix, SecretHash: hash, Services: req.Services, ProjectIDs: req.ProjectIDs, CreatedAt: now, ExpiresAt: now.Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)}
	return api.CreatedAPIToken{Token: token, Secret: secret}, p.write("api_token", token.ID, token, "api_token.created", subject, map[string]any{"services": token.Services, "expires_at": token.ExpiresAt})
}

func (p *Postgres) RevokeAPIToken(subject, tokenID string) error {
	token, err := get[api.APIToken](p, "api_token", tokenID)
	if err != nil || token.Subject != subject {
		return ErrNotFound
	}
	if token.RevokedAt == nil {
		now := time.Now().UTC()
		token.RevokedAt = &now
	}
	return p.write("api_token", token.ID, token, "api_token.revoked", subject, nil)
}

func (p *Postgres) ResolveAPIToken(secret string) (api.APIToken, error) {
	now := time.Now().UTC()
	for _, token := range list[api.APIToken](p, "api_token") {
		if strings.HasPrefix(secret, token.Prefix+"_") && token.RevokedAt == nil && now.Before(token.ExpiresAt) && tokenMatches(token, secret) {
			token.LastUsedAt = &now
			if payload, err := json.Marshal(token); err == nil {
				_, _ = p.pool.Exec(context.Background(), `UPDATE platform_resources SET payload=$4,updated_at=now() WHERE tenant_id=$1 AND kind=$2 AND id=$3`, p.tenant, "api_token", token.ID, payload)
			}
			return token, nil
		}
	}
	return api.APIToken{}, ErrNotFound
}

func (p *Postgres) AccessRequestsFor(subject string) []api.AccessRequest {
	result := make([]api.AccessRequest, 0)
	for _, request := range p.AccessRequests() {
		if request.Subject == subject {
			result = append(result, request)
		}
	}
	return result
}

func (p *Postgres) CreateAccessRequest(subject, email string, req api.CreateAccessRequest) (api.AccessRequest, error) {
	req, err := validateAccessRequest(req)
	if err != nil {
		return api.AccessRequest{}, err
	}
	for _, existing := range p.AccessRequestsFor(subject) {
		if existing.Status == "pending" {
			return api.AccessRequest{}, errors.New("a pending access request already exists")
		}
	}
	now := time.Now().UTC()
	request := api.AccessRequest{ID: id("access-request"), Subject: subject, Email: email, Reason: req.Reason, RequestedServices: req.RequestedServices, Status: "pending", CreatedAt: now, UpdatedAt: now}
	return request, p.write("access_request", request.ID, request, "access.requested", subject, nil)
}

func (p *Postgres) ReviewAccessRequest(requestID string, req api.ReviewAccessRequest, reviewer string) (api.AccessRequest, error) {
	if req.Status != "approved" && req.Status != "rejected" {
		return api.AccessRequest{}, errors.New("status must be approved or rejected")
	}
	request, err := get[api.AccessRequest](p, "access_request", requestID)
	if err != nil {
		return request, err
	}
	if request.Status != "pending" {
		return request, errors.New("access request has already been reviewed")
	}
	request.Status, request.Reviewer, request.ReviewNote = req.Status, reviewer, strings.TrimSpace(req.Note)
	request.UpdatedAt = time.Now().UTC()
	return request, p.write("access_request", request.ID, request, "access.request_"+req.Status, reviewer, nil)
}

func (p *Postgres) AccessFor(subject string) (api.UserAccess, error) {
	return get[api.UserAccess](p, "user_access", subject)
}

func (p *Postgres) UpsertUserAccess(subject string, req api.UpsertUserAccessRequest, actor string) (api.UserAccess, error) {
	access, err := validateAccess(subject, req)
	if err != nil {
		return api.UserAccess{}, err
	}
	now := time.Now().UTC()
	access.UpdatedAt = now
	if existing, getErr := p.AccessFor(subject); getErr == nil {
		access.CreatedAt = existing.CreatedAt
	} else {
		access.CreatedAt = now
	}
	return access, p.write("user_access", subject, access, "access.upserted", actor, nil)
}

func (p *Postgres) DeleteUserAccess(subject, actor string) error {
	ctx := context.Background()
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM platform_resources WHERE tenant_id=$1 AND kind='user_access' AND id=$2`, p.tenant, subject)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	event := api.AuditEvent{ID: id("evt"), Action: "access.deleted", Resource: "user_access", ResourceID: subject, Actor: actorOrAnonymous(actor), CreatedAt: time.Now().UTC()}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events (tenant_id,id,action,resource,resource_id,actor,metadata,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		p.tenant, event.ID, event.Action, event.Resource, event.ResourceID, event.Actor, event.Metadata, event.CreatedAt)
	if err != nil {
		return err
	}
	commandPayload, _ := json.Marshal(map[string]any{
		"id": subject, "kind": "user_access", "action": event.Action,
		"resource": map[string]any{"subject": subject}, "actor": event.Actor, "tenant": p.tenant, "occurred_at": event.CreatedAt,
	})
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (tenant_id,id,topic,event_key,payload) VALUES ($1,$2,$3,$4,$5)`,
		p.tenant, id("out"), "mlaiops.workspace.commands", subject, commandPayload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Postgres) Audit() []api.AuditEvent {
	rows, err := p.pool.Query(context.Background(), `SELECT id, action, resource, resource_id, actor, metadata, created_at FROM audit_events WHERE tenant_id=$1 ORDER BY sequence DESC LIMIT 500`, p.tenant)
	if err != nil {
		return []api.AuditEvent{}
	}
	defer rows.Close()
	result := make([]api.AuditEvent, 0)
	for rows.Next() {
		var event api.AuditEvent
		if rows.Scan(&event.ID, &event.Action, &event.Resource, &event.ResourceID, &event.Actor, &event.Metadata, &event.CreatedAt) == nil {
			result = append(result, event)
		}
	}
	return result
}

func (p *Postgres) CreateProject(req api.CreateProjectRequest, actors ...string) (api.Project, error) {
	if len(strings.TrimSpace(req.Name)) < 3 {
		return api.Project{}, errors.New("name must contain at least 3 characters")
	}
	template, err := api.NormalizeProjectRequest(&req)
	if err != nil {
		return api.Project{}, err
	}
	namespace := slug(req.Name)
	for _, project := range p.Projects() {
		if strings.EqualFold(project.Name, req.Name) || project.Namespace == namespace {
			return api.Project{}, ErrConflict
		}
	}
	project := api.Project{
		ID: id("prj"), Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description),
		Template: template.ID, TemplateVersion: template.Version, Framework: req.Framework,
		Accelerator: req.Accelerator, RequestedProfile: req.RequestedProfile,
		Capabilities:    append([]string(nil), template.Capabilities...),
		ScaffoldCommand: scaffoldCommand(namespace, template, req),
		Namespace:       namespace, Status: "ready", CreatedAt: time.Now().UTC(), OwnerSubject: req.OwnerSubject,
	}
	if strings.TrimSpace(req.RepositoryURL) != "" {
		repository, err := validateGitRepository(api.SetProjectRepositoryRequest{URL: req.RepositoryURL, DefaultBranch: req.DefaultBranch})
		if err != nil {
			return api.Project{}, err
		}
		project.Repository = &repository
	}
	err = p.write("project", project.ID, project, "project.created", first(actors), nil)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return api.Project{}, ErrConflict
	}
	return project, err
}

func (p *Postgres) SubmitPipeline(req api.SubmitPipelineRequest, actors ...string) (api.PipelineRun, error) {
	if !p.exists("project", req.ProjectID) {
		return api.PipelineRun{}, ErrNotFound
	}
	steps, mode := defaultSteps("pending"), "prefect"
	var definition *api.PipelineDefinition
	if req.DefinitionID != "" {
		found, err := p.PipelineDefinition(req.DefinitionID)
		if err != nil || found.ProjectID != req.ProjectID {
			return api.PipelineRun{}, ErrNotFound
		}
		definition = &found
		steps, mode = stepsFromDefinition(found), found.ExecutionMode
		if strings.TrimSpace(req.Name) == "" {
			req.Name = found.Name
		}
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = "training-pipeline"
	}
	now := time.Now().UTC()
	run := api.PipelineRun{ID: id("run"), ProjectID: req.ProjectID, Name: req.Name, Status: "queued", CreatedAt: now, UpdatedAt: now, DefinitionID: req.DefinitionID, ExecutionMode: mode, Parameters: req.Parameters, Steps: steps, Logs: []api.RunLog{{Timestamp: now, Level: "info", Message: "Run accepted by control plane"}}}
	applyRunProvenance(&run, definition, req, first(actors))
	return run, p.write("pipeline_run", run.ID, run, "pipeline.submitted", first(actors), nil)
}

func (p *Postgres) Run(runID string) (api.PipelineRun, error) {
	return get[api.PipelineRun](p, "pipeline_run", runID)
}
func (p *Postgres) CancelRun(runID, actor string) (api.PipelineRun, error) {
	return mutate(p, "pipeline_run", runID, func(run *api.PipelineRun) error {
		if run.Status == "succeeded" || run.Status == "failed" || run.Status == "cancelled" {
			return errors.New("completed runs cannot be cancelled")
		}
		run.Status, run.UpdatedAt = "cancelled", time.Now().UTC()
		run.Logs = append(run.Logs, api.RunLog{Timestamp: run.UpdatedAt, Level: "warning", Message: "Run cancelled by " + actor})
		return nil
	}, "pipeline.cancelled", actor, nil)
}
func (p *Postgres) RetryRun(runID, actor string) (api.PipelineRun, error) {
	previous, err := p.Run(runID)
	if err != nil {
		return api.PipelineRun{}, err
	}
	now := time.Now().UTC()
	run := api.PipelineRun{ID: id("run"), ProjectID: previous.ProjectID, Name: previous.Name, ParentRunID: previous.ID, Status: "queued", CreatedAt: now, UpdatedAt: now, DefinitionID: previous.DefinitionID, ExecutionMode: previous.ExecutionMode, Parameters: previous.Parameters, Steps: resetSteps(previous.Steps), Logs: []api.RunLog{{Timestamp: now, Level: "info", Message: "Retry created from " + previous.ID}}}
	run.Trigger, run.Provenance, run.OwnerSubject = "retry", previous.Provenance, actor
	return run, p.write("pipeline_run", run.ID, run, "pipeline.retried", actor, map[string]any{"parent_run_id": previous.ID})
}

// SetRunEngine links a control-plane run to its execution engine run id.
func (p *Postgres) SetRunEngine(runID, engineRunID string) (api.PipelineRun, error) {
	return mutate(p, "pipeline_run", runID, func(run *api.PipelineRun) error {
		run.EngineRunID, run.UpdatedAt = engineRunID, time.Now().UTC()
		return nil
	}, "pipeline.engine_linked", "system", map[string]any{"engine_run_id": engineRunID})
}

// UpdateRunStep applies a reported step transition under a row lock; it
// shares the deterministic transition logic with the file store.
func (p *Postgres) UpdateRunStep(runID string, req api.UpdateRunStepRequest, actor string) (api.PipelineRun, error) {
	if req.Step == "" || req.Status == "" {
		return api.PipelineRun{}, errors.New("step and status are required")
	}
	if !validStepStatus(req.Status) {
		return api.PipelineRun{}, fmt.Errorf("invalid step status %q", req.Status)
	}
	return mutate(p, "pipeline_run", runID, func(run *api.PipelineRun) error {
		recordStepReport(run, req, time.Now().UTC())
		return nil
	}, "pipeline.step_reported", actor, map[string]any{"step": req.Step, "status": req.Status})
}

func (p *Postgres) RegisterModel(req api.RegisterModelRequest, actor string) (api.Model, error) {
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.Name = strings.TrimSpace(req.Name)
	req.Version = strings.TrimSpace(req.Version)
	req.ArtifactURI = strings.TrimSpace(req.ArtifactURI)
	servingImage, err := normalizeServingImage(req.ServingImage)
	if err != nil {
		return api.Model{}, err
	}
	if !p.exists("project", req.ProjectID) {
		return api.Model{}, ErrNotFound
	}
	if req.Name == "" || req.Version == "" || req.ArtifactURI == "" {
		return api.Model{}, errors.New("name, version and artifact_uri are required")
	}
	for _, model := range p.Models() {
		if model.ProjectID == req.ProjectID && model.Name == req.Name && model.Version == req.Version {
			return api.Model{}, ErrConflict
		}
	}
	gate := "passed"
	if strings.HasPrefix(req.ArtifactURI, "hf://") {
		gate = "needs_evaluation"
	}
	if accuracy, ok := req.Metrics["accuracy"]; ok && accuracy < .8 {
		gate = "failed"
	}
	model := api.Model{ID: id("mdl"), ProjectID: req.ProjectID, Name: req.Name, Version: req.Version, Stage: "candidate", ArtifactURI: req.ArtifactURI, ServingImage: servingImage, Metrics: req.Metrics, GateStatus: gate, DeploymentStatus: "not_deployed", CreatedAt: time.Now().UTC()}
	model.Source = req.Source
	return model, p.write("model", model.ID, model, "model.registered", actor, nil)
}

func (p *Postgres) PromoteModel(modelID, stage, actor string) (api.Model, error) {
	if stage != "staging" && stage != "production" && stage != "archived" {
		return api.Model{}, errors.New("stage must be staging, production or archived")
	}
	model, err := get[api.Model](p, "model", modelID)
	if err != nil {
		return model, err
	}
	if stage == "production" && model.GateStatus != "passed" {
		return model, errors.New("model evaluation gates have not passed")
	}
	model.PreviousStage = model.Stage
	model.Stage = stage
	return model, p.write("model", model.ID, model, "model.promoted", actor, map[string]any{"stage": stage})
}

func (p *Postgres) DeployModel(modelID string, weight int, actor string) (api.Model, error) {
	if weight < 0 || weight > 100 {
		return api.Model{}, errors.New("canary_weight must be between 0 and 100")
	}
	model, err := get[api.Model](p, "model", modelID)
	if err != nil {
		return model, err
	}
	if model.GateStatus != "passed" {
		return model, errors.New("model evaluation gates have not passed")
	}
	model.DeploymentStatus, model.CanaryWeight, model.EndpointURL = "deploying", weight, "/v1/models/"+model.Name+":predict"
	return model, p.write("model", model.ID, model, "model.deployed", actor, map[string]any{"canary_weight": weight})
}

// SetModelEndpoint records the live serving endpoint after the serving
// manager has actually started the model server.
func (p *Postgres) SetModelEndpoint(modelID, endpoint, status string) (api.Model, error) {
	model, err := get[api.Model](p, "model", modelID)
	if err != nil {
		return model, err
	}
	model.EndpointURL, model.DeploymentStatus = endpoint, status
	return model, p.write("model", model.ID, model, "model.endpoint_updated", "system", map[string]any{"endpoint": endpoint, "status": status})
}

func (p *Postgres) RollbackModel(modelID, actor string) (api.Model, error) {
	model, err := get[api.Model](p, "model", modelID)
	if err != nil {
		return model, err
	}
	if model.PreviousStage == "" {
		return model, errors.New("no previous stage is available")
	}
	model.Stage, model.PreviousStage, model.DeploymentStatus, model.CanaryWeight = model.PreviousStage, model.Stage, "rolled_back", 0
	return model, p.write("model", model.ID, model, "model.rolled_back", actor, nil)
}

func (p *Postgres) DeployAgent(req api.DeployAgentRequest, actor string) (api.Agent, error) {
	if err := NormalizeAgentRequest(&req); err != nil {
		return api.Agent{}, err
	}
	if !p.exists("project", req.ProjectID) {
		return api.Agent{}, ErrNotFound
	}
	agent := api.Agent{ID: id("agt"), ProjectID: req.ProjectID, OwnerSubject: req.OwnerSubject, Name: req.Name, Version: req.Version, Image: req.Image, GraphModule: req.GraphModule, LLMBackend: req.LLMBackend, Status: "pending", Replicas: req.Replicas, Autoscaling: req.Autoscaling, Resources: req.Resources, Tools: req.Tools, CreatedAt: time.Now().UTC()}
	return agent, p.write("agent", agent.ID, agent, "agent.deployed", actor, nil)
}

func (p *Postgres) SetAgentTraffic(agentID string, weight int, actor string) (api.Agent, error) {
	if weight < 0 || weight > 100 {
		return api.Agent{}, errors.New("canary_weight must be between 0 and 100")
	}
	agent, err := get[api.Agent](p, "agent", agentID)
	if err != nil {
		return agent, err
	}
	agent.CanaryWeight = weight
	return agent, p.write("agent", agent.ID, agent, "agent.traffic_updated", actor, map[string]any{"canary_weight": weight})
}

func (p *Postgres) RegisterTool(req api.RegisterToolRequest, actor string) (api.Tool, error) {
	if req.Name == "" || req.Version == "" || len(req.InputSchema) == 0 {
		return api.Tool{}, errors.New("name, version and input_schema are required")
	}
	for _, tool := range p.Tools() {
		if tool.Name == req.Name && tool.Version == req.Version {
			return api.Tool{}, ErrConflict
		}
	}
	tool := api.Tool{ID: id("tool"), Name: req.Name, Version: req.Version, Description: req.Description, Tags: req.Tags, InputSchema: req.InputSchema, Status: "ready", CreatedAt: time.Now().UTC()}
	return tool, p.write("tool", tool.ID, tool, "tool.registered", actor, nil)
}

func (p *Postgres) FeatureViews() []api.FeatureView {
	return list[api.FeatureView](p, "feature_view")
}

// ApplyFeatureView upserts by name, mirroring `feast apply` semantics.
func (p *Postgres) ApplyFeatureView(req api.ApplyFeatureViewRequest, actor string) (api.FeatureView, error) {
	if req.Name == "" || req.Entity == "" || len(req.Fields) == 0 {
		return api.FeatureView{}, errors.New("name, entity and at least one field are required")
	}
	for _, existing := range p.FeatureViews() {
		if existing.Name == req.Name {
			existing.Entity = req.Entity
			existing.Fields = req.Fields
			existing.Tags = req.Tags
			existing.Source = req.Source
			existing.TTLSeconds = req.TTLSeconds
			return existing, p.write("feature_view", existing.ID, existing, "feature_view.applied", actor, nil)
		}
	}
	view := api.FeatureView{ID: id("fv"), Name: req.Name, Entity: req.Entity, Fields: req.Fields, Tags: req.Tags, Source: req.Source, TTLSeconds: req.TTLSeconds, Status: "registered", CreatedAt: time.Now().UTC()}
	return view, p.write("feature_view", view.ID, view, "feature_view.applied", actor, nil)
}

func (p *Postgres) ReportMaterialization(name string, entityCount int, actor string) (api.FeatureView, error) {
	if entityCount < 0 {
		return api.FeatureView{}, errors.New("entity_count cannot be negative")
	}
	for _, view := range p.FeatureViews() {
		if view.Name == name {
			now := time.Now().UTC()
			view.Status = "materialized"
			view.OnlineEntityCount = entityCount
			view.MaterializedAt = &now
			return view, p.write("feature_view", view.ID, view, "feature_view.materialized", actor, map[string]any{"entity_count": entityCount})
		}
	}
	return api.FeatureView{}, ErrNotFound
}

func (p *Postgres) CreateConnection(req api.CreateConnectionRequest, actor string) (api.Connection, error) {
	if req.Name == "" || req.Type == "" || req.SecretRef == "" {
		return api.Connection{}, errors.New("name, type and secret_ref are required")
	}
	connection := api.Connection{ID: id("conn"), Name: req.Name, Type: req.Type, Endpoint: req.Endpoint, SecretRef: req.SecretRef, Status: "pending", CreatedAt: time.Now().UTC()}
	return connection, p.write("connection", connection.ID, connection, "connection.created", actor, nil)
}

func (p *Postgres) UpdateConnectionStatus(connectionID, status, message, actor string) (api.Connection, error) {
	connection, err := get[api.Connection](p, "connection", connectionID)
	if err != nil {
		return connection, err
	}
	now := time.Now().UTC()
	connection.Status, connection.Message, connection.CheckedAt = status, message, &now
	return connection, p.write("connection", connection.ID, connection, "connection.checked", actor, map[string]any{"status": status})
}
func (p *Postgres) AgentSessions(agentID string) []api.AgentSession {
	values := list[api.AgentSession](p, "agent_session")
	result := []api.AgentSession{}
	for _, value := range values {
		if agentID == "" || value.AgentID == agentID {
			result = append(result, value)
		}
	}
	return result
}
func (p *Postgres) AgentTraces(agentID string) []api.AgentTrace {
	values := list[api.AgentTrace](p, "agent_trace")
	result := []api.AgentTrace{}
	for _, value := range values {
		if agentID == "" || value.AgentID == agentID {
			result = append(result, value)
		}
	}
	return result
}
func (p *Postgres) RecordTrace(req api.RecordTraceRequest) (api.AgentTrace, error) {
	if req.AgentID == "" || req.SessionID == "" {
		return api.AgentTrace{}, errors.New("agent_id and session_id are required")
	}
	now := time.Now().UTC()
	trace := api.AgentTrace{ID: id("trace"), AgentID: req.AgentID, SessionID: req.SessionID, Name: req.Name, Status: req.Status, DurationMS: req.DurationMS, Tokens: req.InputTokens + req.OutputTokens, Metadata: req.Metadata, CreatedAt: now}
	if err := p.write("agent_trace", trace.ID, trace, "agent.trace_recorded", "trace-proxy", nil); err != nil {
		return trace, err
	}
	// Sessions are scoped per agent: two agents may legitimately use the same
	// session id (e.g. a shared conversation id), so the resource key is the
	// composite while the exposed session id stays client-chosen.
	sessionKey := req.AgentID + "/" + req.SessionID
	session, err := get[api.AgentSession](p, "agent_session", sessionKey)
	if errors.Is(err, ErrNotFound) {
		session = api.AgentSession{ID: req.SessionID, AgentID: req.AgentID, UserID: req.UserID, StartedAt: now}
	} else if err != nil {
		return trace, err
	}
	session.Status, session.CurrentNode, session.UpdatedAt = req.Status, req.CurrentNode, now
	session.Turns++
	session.InputTokens += req.InputTokens
	session.OutputTokens += req.OutputTokens
	session.CostUSD += req.CostUSD
	return trace, p.write("agent_session", sessionKey, session, "agent.session_updated", "trace-proxy", nil)
}

func (p *Postgres) write(kind, resourceID string, value any, action, actor string, metadata map[string]any) error {
	ctx := context.Background()
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := p.writeTx(ctx, tx, kind, resourceID, value, action, actor, metadata); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// mutate is write with a row lock: fn sees the current value and returns
// the next one, so concurrent reporters (parallel pipeline nodes) cannot
// overwrite each other's changes.
func mutate[T any](p *Postgres, kind, resourceID string, fn func(*T) error, action, actor string, metadata map[string]any) (T, error) {
	var value T
	ctx := context.Background()
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return value, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT payload FROM platform_resources WHERE tenant_id=$1 AND kind=$2 AND id=$3 FOR UPDATE`, p.tenant, kind, resourceID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrNotFound
	}
	if err != nil {
		return value, err
	}
	if value, err = decodeStoredValue[T](raw); err != nil {
		return value, err
	}
	if err = fn(&value); err != nil {
		return value, err
	}
	if err = p.writeTx(ctx, tx, kind, resourceID, value, action, actor, metadata); err != nil {
		return value, err
	}
	return value, tx.Commit(ctx)
}

func (p *Postgres) writeTx(ctx context.Context, tx pgx.Tx, kind, resourceID string, value any, action, actor string, metadata map[string]any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform_resources (tenant_id,kind,id,payload) VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id,kind,id) DO UPDATE SET payload=EXCLUDED.payload, updated_at=now()`, p.tenant, kind, resourceID, payload)
	if err != nil {
		return err
	}
	event := api.AuditEvent{ID: id("evt"), Action: action, Resource: kind, ResourceID: resourceID, Actor: actorOrAnonymous(actor), Metadata: metadata, CreatedAt: time.Now().UTC()}
	eventPayload, _ := json.Marshal(event)
	_, err = tx.Exec(ctx, `INSERT INTO audit_events (tenant_id,id,action,resource,resource_id,actor,metadata,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		p.tenant, event.ID, event.Action, event.Resource, event.ResourceID, event.Actor, event.Metadata, event.CreatedAt)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events (tenant_id,id,topic,event_key,payload) VALUES ($1,$2,$3,$4,$5)`,
		p.tenant, id("out"), "mlaiops.audit.operations", resourceID, eventPayload)
	if err != nil {
		return err
	}
	if commandTopic := topicForAction(action); commandTopic != "" {
		commandPayload, marshalErr := json.Marshal(map[string]any{
			"id": resourceID, "kind": kind, "action": action, "resource": json.RawMessage(payload),
			"actor": event.Actor, "tenant": p.tenant, "occurred_at": event.CreatedAt,
		})
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO outbox_events (tenant_id,id,topic,event_key,payload) VALUES ($1,$2,$3,$4,$5)`,
			p.tenant, id("out"), commandTopic, resourceID, commandPayload); err != nil {
			return err
		}
	}
	return nil
}

func (p *Postgres) PendingOutbox(ctx context.Context, limit int) ([]OutboxEvent, error) {
	rows, err := p.pool.Query(ctx, `SELECT sequence,id,topic,event_key,payload,attempts FROM outbox_events WHERE tenant_id=$1 AND published_at IS NULL ORDER BY sequence FOR UPDATE SKIP LOCKED LIMIT $2`, p.tenant, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []OutboxEvent
	for rows.Next() {
		var event OutboxEvent
		if err := rows.Scan(&event.Sequence, &event.ID, &event.Topic, &event.Key, &event.Payload, &event.Attempts); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (p *Postgres) MarkPublished(ctx context.Context, sequence int64) error {
	_, err := p.pool.Exec(ctx, `UPDATE outbox_events SET published_at=now(), attempts=attempts+1, last_error=NULL WHERE tenant_id=$1 AND sequence=$2`, p.tenant, sequence)
	return err
}

func (p *Postgres) MarkFailed(ctx context.Context, sequence int64, publishErr error) error {
	_, err := p.pool.Exec(ctx, `UPDATE outbox_events SET attempts=attempts+1,last_error=$3 WHERE tenant_id=$1 AND sequence=$2`, p.tenant, sequence, publishErr.Error())
	return err
}

func list[T any](p *Postgres, kind string) []T {
	rows, err := p.pool.Query(context.Background(), `SELECT payload FROM platform_resources WHERE tenant_id=$1 AND kind=$2 ORDER BY created_at DESC`, p.tenant, kind)
	if err != nil {
		return []T{}
	}
	defer rows.Close()
	result := make([]T, 0)
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		value, decodeErr := decodeStoredValue[T](raw)
		if decodeErr == nil {
			result = append(result, value)
		}
	}
	return result
}

func get[T any](p *Postgres, kind, resourceID string) (T, error) {
	var raw []byte
	err := p.pool.QueryRow(context.Background(), `SELECT payload FROM platform_resources WHERE tenant_id=$1 AND kind=$2 AND id=$3`, p.tenant, kind, resourceID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		var value T
		return value, ErrNotFound
	}
	if err != nil {
		var value T
		return value, err
	}
	return decodeStoredValue[T](raw)
}

func decodeStoredValue[T any](raw []byte) (T, error) {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, err
	}
	if agent, ok := any(&value).(*api.Agent); ok {
		NormalizeAgentRecord(agent)
	}
	return value, nil
}

func (p *Postgres) exists(kind, resourceID string) bool {
	var exists bool
	_ = p.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM platform_resources WHERE tenant_id=$1 AND kind=$2 AND id=$3)`, p.tenant, kind, resourceID).Scan(&exists)
	return exists
}

func actorOrAnonymous(actor string) string {
	if actor == "" {
		return "anonymous"
	}
	return actor
}

func topicForAction(action string) string {
	switch {
	case strings.HasPrefix(action, "pipeline."):
		return "mlaiops.pipeline.commands"
	case strings.HasPrefix(action, "model."):
		return "mlaiops.model.commands"
	case strings.HasPrefix(action, "agent."):
		return "mlaiops.agent.commands"
	case strings.HasPrefix(action, "tool."):
		return "mlaiops.tool.commands"
	case strings.HasPrefix(action, "connection."):
		return "mlaiops.connection.commands"
	case strings.HasPrefix(action, "access."):
		return "mlaiops.workspace.commands"
	default:
		return ""
	}
}

func (p *Postgres) String() string { return fmt.Sprintf("postgres(tenant=%s)", p.tenant) }

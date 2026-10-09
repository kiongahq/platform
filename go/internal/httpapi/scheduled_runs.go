package httpapi

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/ml-ai-ops/platform/internal/auth"
	"github.com/ml-ai-ops/platform/internal/policy"
	"github.com/ml-ai-ops/platform/internal/scheduler"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

// StartScheduler runs the pipeline scheduler until ctx ends. Every gateway
// replica may call it; the scheduler's lease keeps exactly one active.
// Set KIONGA_SCHEDULER=off to disable it on a replica.
func StartScheduler(ctx context.Context, data store.Repository) {
	if os.Getenv("KIONGA_SCHEDULER") == "off" {
		log.Printf("pipeline scheduler disabled by KIONGA_SCHEDULER=off")
		return
	}
	host, _ := os.Hostname()
	server := &Server{store: data}
	s := &scheduler.Scheduler{Store: data, Holder: host + "-" + time.Now().UTC().Format("150405.000"), Submit: server.submitScheduledRun}
	go s.Run(ctx)
	log.Printf("pipeline scheduler started (holder %s)", s.Holder)
}

// ownerPrincipal resolves the identity a schedule runs as. Schedules never
// run with more access than their owner currently has; an owner without a
// provisioned profile (other than the local bootstrap administrator) cannot
// run schedules.
func ownerPrincipal(data store.Repository, owner string) (auth.Principal, error) {
	if owner == "" {
		return auth.Principal{}, errors.New("schedule has no owner; save the definition again to claim it")
	}
	if access, err := data.AccessFor(owner); err == nil {
		if access.Disabled {
			return auth.Principal{}, errors.New("schedule owner " + owner + " is suspended")
		}
		return auth.Principal{Subject: owner, Roles: []string{access.Role}, Services: access.Services, ProjectIDs: access.ProjectIDs, Provisioned: true}, nil
	}
	bootstrap := os.Getenv("MLAIOPS_LOCAL_USERNAME")
	if bootstrap == "" {
		bootstrap = "admin"
	}
	if os.Getenv("OIDC_ISSUER") == "" && owner == bootstrap {
		role := os.Getenv("MLAIOPS_LOCAL_ROLE")
		if role == "" {
			role = auth.RoleAdmin
		}
		return auth.Principal{Subject: owner, Roles: []string{role}}, nil
	}
	return auth.Principal{}, errors.New("schedule owner " + owner + " has no provisioned access profile")
}

func (s *Server) submitScheduledRun(ctx context.Context, definition api.PipelineDefinition, slot time.Time) (string, error) {
	owner, err := ownerPrincipal(s.store, definition.OwnerSubject)
	if err != nil {
		return "", err
	}
	if !auth.Allowed(owner, "POST", "/api/v1/pipelines/submit") {
		return "", errors.New("schedule owner may not submit pipeline runs")
	}
	decision := newAuthorizer(s.store, owner, policy.Context{Time: slot.UTC()}).check(policy.PipelineRun, definitionResource(definition))
	if !decision.Allowed {
		return "", errors.New("schedule owner may not run this pipeline: " + decision.Reason)
	}
	if err := enforceRunQuota(s.store, owner); err != nil {
		return "", err
	}
	run, err := s.store.SubmitPipeline(api.SubmitPipelineRequest{ProjectID: definition.ProjectID, DefinitionID: definition.ID, Trigger: "schedule", ScheduledFor: &slot}, owner.Subject)
	if err != nil {
		return "", err
	}
	s.dispatchPipeline(ctx, run)
	return run.ID, nil
}

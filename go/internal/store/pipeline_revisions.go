package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ml-ai-ops/platform/internal/pipelinespec"
	"github.com/ml-ai-ops/platform/pkg/api"
)

// PipelineRevisionKind stores immutable definition revisions as documents.
const PipelineRevisionKind = "pipeline_revision"

func revisionID(definitionID string, revision int) string {
	return fmt.Sprintf("%s@%d", definitionID, revision)
}

// applyDefinitionRequest copies a validated request onto a definition. The
// revision advances only when the canonical YAML changes; scheduler state is
// kept for triggers whose schedule is unchanged. It returns the canonical
// YAML and whether a new revision is needed.
func applyDefinitionRequest(definition *api.PipelineDefinition, req api.UpsertPipelineDefinitionRequest, now time.Time) (string, bool, error) {
	sha, text, err := pipelinespec.SpecHash(req)
	if err != nil {
		return "", false, err
	}
	previous := map[string]api.PipelineTrigger{}
	for _, trigger := range definition.Triggers {
		previous[triggerKey(trigger)] = trigger
	}
	triggers := make([]api.PipelineTrigger, len(req.Triggers))
	for i, trigger := range req.Triggers {
		if old, ok := previous[triggerKey(trigger)]; ok {
			trigger.NextRunAt, trigger.LastRunAt, trigger.LastRunID = old.NextRunAt, old.LastRunAt, old.LastRunID
		}
		if trigger.Paused {
			trigger.NextRunAt = nil
		} else if trigger.Type == "schedule" && trigger.NextRunAt == nil {
			if next, err := pipelinespec.NextRun(trigger.Cron, trigger.Timezone, now); err == nil {
				trigger.NextRunAt = &next
			}
		}
		triggers[i] = trigger
	}
	changed := sha != definition.SHA256
	definition.ProjectID, definition.Name, definition.Version = req.ProjectID, req.Name, req.Version
	definition.ExecutionMode, definition.Jobs, definition.Description = req.ExecutionMode, req.Jobs, req.Description
	definition.RepositoryURL, definition.CommitSHA = req.RepositoryURL, req.CommitSHA
	definition.Triggers, definition.Overridable, definition.Parameters = triggers, req.Overridable, req.Parameters
	if changed {
		definition.Revision++
		definition.SHA256 = sha
	}
	definition.UpdatedAt = now
	return string(text), changed, nil
}

func triggerKey(trigger api.PipelineTrigger) string {
	return strings.Join([]string{trigger.Type, trigger.Cron, trigger.Timezone, trigger.Topic}, "|")
}

// recordRevision writes the immutable snapshot. An existing revision with a
// different hash is a conflict, never an overwrite.
func recordRevision(docs Documents, definition api.PipelineDefinition, yamlText string, req api.UpsertPipelineDefinitionRequest, actor string) error {
	revision := api.PipelineRevision{
		DefinitionID: definition.ID, Revision: definition.Revision, SHA256: definition.SHA256, YAML: yamlText,
		Spec: req, Author: actorOrAnonymous(actor), Message: req.Message, CreatedAt: definition.UpdatedAt,
	}
	_, err := UpdateDoc(docs, PipelineRevisionKind, revisionID(definition.ID, definition.Revision), func(current api.PipelineRevision, exists bool) (api.PipelineRevision, error) {
		if exists {
			if current.SHA256 == revision.SHA256 {
				return current, ErrSkipWrite
			}
			return current, ErrConflict
		}
		return revision, nil
	}, "pipeline_definition.revision_created", actor)
	return err
}

// PipelineRevisions lists a definition's revisions, newest first.
func PipelineRevisions(docs Documents, definitionID string) []api.PipelineRevision {
	out := []api.PipelineRevision{}
	for _, revision := range ListDocs[api.PipelineRevision](docs, PipelineRevisionKind) {
		if revision.DefinitionID == definitionID {
			out = append(out, revision)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Revision > out[j-1].Revision; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// PipelineRevision returns one revision.
func PipelineRevision(docs Documents, definitionID string, revision int) (api.PipelineRevision, error) {
	return GetDoc[api.PipelineRevision](docs, PipelineRevisionKind, revisionID(definitionID, revision))
}

// ParseRevision parses the "@n" suffix form used in URLs.
func ParseRevision(value string) (int, error) {
	revision, err := strconv.Atoi(value)
	if err != nil || revision < 1 {
		return 0, errors.New("revision must be a positive integer")
	}
	return revision, nil
}

// TriggerState is scheduler bookkeeping on a definition's trigger. It is not
// part of the definition's content, so updating it never creates a revision.
type TriggerState struct {
	NextRunAt *time.Time
	LastRunAt *time.Time
	LastRunID string
}

func applyTriggerState(definition *api.PipelineDefinition, index int, state TriggerState) error {
	if index < 0 || index >= len(definition.Triggers) {
		return ErrNotFound
	}
	trigger := &definition.Triggers[index]
	trigger.NextRunAt, trigger.LastRunAt, trigger.LastRunID = state.NextRunAt, state.LastRunAt, state.LastRunID
	return nil
}

func (s *Store) SetPipelineTriggerState(definitionID string, index int, state TriggerState) (api.PipelineDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Definitions {
		if s.data.Definitions[i].ID == definitionID {
			if err := applyTriggerState(&s.data.Definitions[i], index, state); err != nil {
				return api.PipelineDefinition{}, err
			}
			s.record("pipeline_definition.trigger_state", "pipeline_definition", definitionID, "scheduler", nil)
			return cloneDefinition(s.data.Definitions[i]), s.persist()
		}
	}
	return api.PipelineDefinition{}, ErrNotFound
}

func (p *Postgres) SetPipelineTriggerState(definitionID string, index int, state TriggerState) (api.PipelineDefinition, error) {
	return mutate(p, "pipeline_definition", definitionID, func(definition *api.PipelineDefinition) error {
		return applyTriggerState(definition, index, state)
	}, "pipeline_definition.trigger_state", "scheduler", nil)
}

// applyRunProvenance pins what a new run will execute: trigger, owner and
// the exact definition revision and hash. Overrides and image digests are
// added by the API and runner as they are resolved.
func applyRunProvenance(run *api.PipelineRun, definition *api.PipelineDefinition, req api.SubmitPipelineRequest, actor string) {
	run.Trigger = req.Trigger
	if run.Trigger == "" {
		run.Trigger = "manual"
	}
	run.OwnerSubject = actor
	run.ScheduledFor = req.ScheduledFor
	provenance := &api.RunProvenance{Overrides: req.Overrides}
	if definition != nil {
		provenance.DefinitionRevision, provenance.DefinitionSHA256 = definition.Revision, definition.SHA256
		// Merge definition parameter defaults under run parameters.
		if len(definition.Parameters) > 0 {
			merged := map[string]any{}
			for key, value := range definition.Parameters {
				merged[key] = value
			}
			for key, value := range run.Parameters {
				merged[key] = value
			}
			run.Parameters = merged
		}
	}
	run.Provenance = provenance
}

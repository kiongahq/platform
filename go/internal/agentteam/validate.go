package agentteam

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	definitionName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	modelName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
	repository     = regexp.MustCompile(`^https://[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?(/[A-Za-z0-9_~+-][A-Za-z0-9._~+-]*)+/?$`)
	connectionRef  = regexp.MustCompile(`^api-[a-f0-9]+$`)
	knowledgeRef   = regexp.MustCompile(`^kb-[a-f0-9]+$`)
	datasetRef     = regexp.MustCompile(`^ds-[a-f0-9]+$`)
	definitionID   = regexp.MustCompile(`^adf-[a-f0-9]{24}$`)
)

// ReservedName is the coordinator's name in a run, so no member can take it.
const ReservedName = "coordinator"

// DefaultDefinition fills the contract's defaults for omitted fields.
func DefaultDefinition(d DefinitionContent) DefinitionContent {
	if d.Model == "" {
		d.Model = "llm"
	}
	if d.Tools == nil {
		d.Tools = []ToolGrant{}
	}
	if d.Workspace == "" {
		d.Workspace = "none"
	}
	if d.Limits.MaxTurns == 0 {
		d.Limits.MaxTurns = 100
	}
	if d.Limits.MaxTokens == 0 {
		d.Limits.MaxTokens = 200000
	}
	if d.Limits.MaxSeconds == 0 {
		d.Limits.MaxSeconds = 3600
	}
	return d
}

// DefaultTeam fills the contract's defaults for omitted fields.
func DefaultTeam(team TeamContent) TeamContent {
	if team.Coordinator.Model == "" {
		team.Coordinator.Model = "llm"
	}
	if team.Limits.MaxWorkersTotal == 0 {
		team.Limits.MaxWorkersTotal = 20
	}
	if team.Limits.MaxConcurrentWorkers == 0 {
		team.Limits.MaxConcurrentWorkers = 5
	}
	if team.Limits.MaxTokens == 0 {
		team.Limits.MaxTokens = 2000000
	}
	if team.Limits.MaxSeconds == 0 {
		team.Limits.MaxSeconds = 14400
	}
	return team
}

// ValidateDefinition returns every problem with a definition's content.
func ValidateDefinition(d DefinitionContent) []string {
	issues := []string{}
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }
	if !definitionName.MatchString(d.Name) {
		add("name: use lowercase letters, digits and hyphens, starting with a letter (max 63)")
	}
	if d.Name == ReservedName {
		add("name: %q is reserved", ReservedName)
	}
	if strings.TrimSpace(d.Description) == "" || utf8.RuneCountInString(d.Description) > 1000 {
		add("description: required, at most 1000 characters")
	}
	if strings.TrimSpace(d.Instructions) == "" || utf8.RuneCountInString(d.Instructions) > 32000 {
		add("instructions: required, at most 32000 characters")
	}
	if !modelName.MatchString(d.Model) {
		add("model: required model alias")
	}
	if d.Workspace != "none" && d.Workspace != "sandbox" {
		add("workspace: must be none or sandbox")
	}
	if d.Limits.MaxTurns < 1 || d.Limits.MaxTurns > 500 {
		add("limits.max_turns: must be 1-500")
	}
	if d.Limits.MaxTokens < 1 || d.Limits.MaxTokens > 5_000_000 {
		add("limits.max_tokens: must be 1-5000000")
	}
	if d.Limits.MaxSeconds < 60 || d.Limits.MaxSeconds > 14400 {
		add("limits.max_seconds: must be 60-14400")
	}
	if len(d.Tools) > 64 {
		add("tools: at most 64 grants")
	}
	seen := map[string]bool{}
	for i, grant := range d.Tools {
		key := grant.Kind + "/" + grant.Ref + "/" + grant.Repository
		if seen[key] {
			add("tools[%d]: duplicate grant", i)
		}
		seen[key] = true
		switch grant.Kind {
		case "sandbox":
			if d.Workspace != "sandbox" {
				add("tools[%d]: sandbox requires workspace sandbox", i)
			}
			if grant.Ref != "" || grant.Repository != "" || grant.Connection != "" {
				add("tools[%d]: sandbox takes no other fields", i)
			}
		case "git":
			if d.Workspace != "sandbox" {
				add("tools[%d]: git requires workspace sandbox", i)
			}
			if !ValidRepository(grant.Repository) {
				add("tools[%d]: repository must be an https URL without credentials", i)
			}
			if grant.Connection != "" && !connectionRef.MatchString(grant.Connection) {
				add("tools[%d]: connection must be an api- reference", i)
			}
			if grant.Ref != "" {
				add("tools[%d]: git takes repository and connection", i)
			}
		case "tool", "connection", "knowledge", "dataset":
			valid := grant.Ref != "" && len(grant.Ref) <= 256
			switch grant.Kind {
			case "connection":
				valid = connectionRef.MatchString(grant.Ref)
			case "knowledge":
				valid = knowledgeRef.MatchString(grant.Ref)
			case "dataset":
				valid = datasetRef.MatchString(grant.Ref)
			}
			if !valid {
				add("tools[%d]: %s has an invalid ref", i, grant.Kind)
			}
			if grant.Repository != "" || grant.Connection != "" {
				add("tools[%d]: %s takes only ref", i, grant.Kind)
			}
		default:
			add("tools[%d]: unknown kind %q", i, grant.Kind)
		}
	}
	return issues
}

// ValidRepository accepts an https URL with a host and no embedded credentials.
func ValidRepository(raw string) bool {
	if len(raw) > 2048 || !repository.MatchString(raw) || strings.Contains(raw, "..") {
		return false
	}
	target, err := url.Parse(raw)
	return err == nil && target.User == nil && target.RawQuery == "" && target.Fragment == ""
}

// ValidateTeam returns every problem with a team's content.
func ValidateTeam(team TeamContent) []string {
	issues := []string{}
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }
	if !definitionName.MatchString(team.Name) {
		add("name: use lowercase letters, digits and hyphens, starting with a letter (max 63)")
	}
	if utf8.RuneCountInString(team.Description) > 1000 {
		add("description: at most 1000 characters")
	}
	if strings.TrimSpace(team.Coordinator.Instructions) == "" || utf8.RuneCountInString(team.Coordinator.Instructions) > 32000 {
		add("coordinator.instructions: required, at most 32000 characters")
	}
	if !modelName.MatchString(team.Coordinator.Model) {
		add("coordinator.model: required model alias")
	}
	if len(team.Members) < 1 || len(team.Members) > 16 {
		add("members: 1-16 agent definitions")
	}
	seen := map[string]bool{}
	for i, id := range team.Members {
		if seen[id] {
			add("members[%d]: duplicate", i)
		}
		if !definitionID.MatchString(id) {
			add("members[%d]: must be an adf- definition ID", i)
		}
		seen[id] = true
	}
	if utf8.RuneCountInString(team.Brief) > 16000 {
		add("brief: at most 16000 characters")
	}
	if team.Limits.MaxWorkersTotal < 1 || team.Limits.MaxWorkersTotal > 50 {
		add("limits.max_workers_total: must be 1-50")
	}
	if team.Limits.MaxConcurrentWorkers < 1 || team.Limits.MaxConcurrentWorkers > 16 {
		add("limits.max_concurrent_workers: must be 1-16")
	}
	if team.Limits.MaxConcurrentWorkers > team.Limits.MaxWorkersTotal {
		add("limits.max_concurrent_workers: cannot exceed max_workers_total")
	}
	if team.Limits.MaxTokens < 1 || team.Limits.MaxTokens > 20_000_000 {
		add("limits.max_tokens: must be 1-20000000")
	}
	if team.Limits.MaxSeconds < 60 || team.Limits.MaxSeconds > 86400 {
		add("limits.max_seconds: must be 60-86400")
	}
	return issues
}

// ContentSHA256 hashes a snapshot's canonical JSON: sorted keys, no
// insignificant whitespace, HTML left unescaped.
func ContentSHA256(snapshot Snapshot) (string, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(generic); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimRight(buffer.Bytes(), "\n"))
	return hex.EncodeToString(sum[:]), nil
}

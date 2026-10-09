package pipelinespec

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/ml-ai-ops/platform/pkg/api"
)

// Issue is one actionable validation problem tied to a field and, when it
// concerns a node, to that node (and its YAML line when parsed from YAML).
type Issue struct {
	Path    string `json:"path"`
	Node    string `json:"node,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

type Issues []Issue

// Error lets Issues be returned as an error; the message is the first issue
// so older callers that only show err.Error() stay useful.
func (i Issues) Error() string {
	if len(i) == 0 {
		return ""
	}
	if len(i) == 1 {
		return i[0].Message
	}
	return fmt.Sprintf("%s (and %d more issues)", i[0].Message, len(i)-1)
}

var (
	nodeID           = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	resourceQuantity = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?)(m|Ki|Mi|Gi|Ti)?$`)
	paramName        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	cronParser       = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
)

const (
	MaxNodes          = 200
	MaxTimeoutSeconds = 24 * 60 * 60
	MaxRetries        = 10
	MaxBackoffSeconds = 3600
)

var triggerTypes = map[string]bool{"manual": true, "schedule": true, "api": true, "event": true}

// ParseCron validates a five-field cron expression in an IANA timezone.
func ParseCron(expression, timezone string) (cron.Schedule, *time.Location, error) {
	location := time.UTC
	if timezone != "" {
		loaded, err := time.LoadLocation(timezone)
		if err != nil {
			return nil, nil, fmt.Errorf("timezone %q is not an IANA timezone such as Europe/Berlin", timezone)
		}
		location = loaded
	}
	if strings.HasPrefix(strings.TrimSpace(expression), "@") || strings.Contains(expression, "TZ=") {
		return nil, nil, fmt.Errorf("cron %q must be five fields (minute hour day month weekday); set timezone separately", expression)
	}
	schedule, err := cronParser.Parse(expression)
	if err != nil {
		return nil, nil, fmt.Errorf("cron %q is invalid: %v", expression, err)
	}
	return schedule, location, nil
}

// NextRun returns the first schedule slot strictly after from.
func NextRun(expression, timezone string, from time.Time) (time.Time, error) {
	schedule, location, err := ParseCron(expression, timezone)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(from.In(location)).UTC(), nil
}

// Validate normalizes a request and returns every problem found. A nil
// result means the definition can be stored and executed.
func Validate(req api.UpsertPipelineDefinitionRequest) (api.UpsertPipelineDefinitionRequest, Issues) {
	var issues Issues
	add := func(path, node, format string, args ...any) {
		issues = append(issues, Issue{Path: path, Node: node, Message: fmt.Sprintf(format, args...)})
	}
	req.Name, req.Version, req.ProjectID = strings.TrimSpace(req.Name), strings.TrimSpace(req.Version), strings.TrimSpace(req.ProjectID)
	if req.ProjectID == "" {
		add("metadata.project", "", "project is required")
	}
	if req.Name == "" {
		add("metadata.name", "", "name is required")
	}
	if req.Version == "" {
		add("metadata.version", "", "version is required")
	}
	if req.ExecutionMode == "" {
		req.ExecutionMode = "prefect"
	}
	if req.ExecutionMode != "prefect" && req.ExecutionMode != "functions" {
		add("spec.executionMode", "", "executionMode must be prefect (containers) or functions (OpenFaaS)")
	}
	if len(req.Jobs) == 0 {
		add("spec.nodes", "", "at least one node is required")
	}
	if len(req.Jobs) > MaxNodes {
		add("spec.nodes", "", "at most %d nodes are allowed", MaxNodes)
	}

	known := map[string]int{}
	for i := range req.Jobs {
		job := &req.Jobs[i]
		path := fmt.Sprintf("spec.nodes[%d]", i)
		job.Name, job.Kind = strings.TrimSpace(job.Name), strings.TrimSpace(job.Kind)
		switch {
		case job.Name == "":
			add(path+".id", "", "node %d needs an id", i+1)
		case !nodeID.MatchString(job.Name):
			add(path+".id", job.Name, "node id %q must be lowercase letters, digits and hyphens (max 63)", job.Name)
		}
		if previous, duplicate := known[job.Name]; duplicate && job.Name != "" {
			add(path+".id", job.Name, "node id %q is also used by node %d", job.Name, previous+1)
		} else if job.Name != "" {
			known[job.Name] = i
		}
		node := job.Name
		switch job.Kind {
		case "container":
			if strings.TrimSpace(job.Image) == "" {
				add(path+".image", node, "container node %q needs an image", node)
			}
			if job.Resources.CPU == "" {
				job.Resources.CPU = "500m"
			}
			if job.Resources.Memory == "" {
				job.Resources.Memory = "1Gi"
			}
			if req.ExecutionMode == "functions" {
				add(path+".type", node, "node %q is a container but executionMode is functions", node)
			}
		case "function":
			if strings.TrimSpace(job.Function) == "" {
				add(path+".function", node, "function node %q needs a function name", node)
			}
			if req.ExecutionMode == "prefect" {
				add(path+".type", node, "node %q is a function but executionMode is prefect (containers)", node)
			}
		default:
			add(path+".type", node, "node %q type must be container or function", node)
		}
		if job.Resources.CPU != "" && !resourceQuantity.MatchString(job.Resources.CPU) {
			add(path+".resources.cpu", node, "CPU %q must be a quantity such as 500m or 2", job.Resources.CPU)
		}
		if job.Resources.Memory != "" && !resourceQuantity.MatchString(job.Resources.Memory) {
			add(path+".resources.memory", node, "memory %q must be a quantity such as 512Mi or 2Gi", job.Resources.Memory)
		}
		if job.Resources.GPU < 0 {
			add(path+".resources.gpu", node, "GPU count cannot be negative")
		}
		if job.Retries < 0 || job.Retries > MaxRetries {
			add(path+".retry.max", node, "retries must be between 0 and %d", MaxRetries)
		}
		if job.RetryBackoffSeconds < 0 || job.RetryBackoffSeconds > MaxBackoffSeconds {
			add(path+".retry.backoffSeconds", node, "retry backoff must be between 0 and %d seconds", MaxBackoffSeconds)
		}
		if job.TimeoutSeconds < 0 || job.TimeoutSeconds > MaxTimeoutSeconds {
			add(path+".timeoutSeconds", node, "timeout must be between 1 and %d seconds (0 uses the default)", MaxTimeoutSeconds)
		}
		if job.When != nil && !paramName.MatchString(job.When.Param) {
			add(path+".when.param", node, "condition parameter %q is not a valid parameter name", job.When.Param)
		}
		seen := map[string]bool{}
		for _, dependency := range job.DependsOn {
			if seen[dependency] {
				add(path+".dependsOn", node, "node %q lists dependency %q twice", node, dependency)
			}
			seen[dependency] = true
		}
	}
	for i, job := range req.Jobs {
		for _, dependency := range job.DependsOn {
			switch {
			case dependency == job.Name:
				add(fmt.Sprintf("spec.nodes[%d].dependsOn", i), job.Name, "node %q cannot depend on itself", job.Name)
			case dependency != "":
				if _, ok := known[dependency]; !ok {
					add(fmt.Sprintf("spec.nodes[%d].dependsOn", i), job.Name, "node %q depends on %q, which does not exist", job.Name, dependency)
				}
			}
		}
	}
	if cycle := findCycle(req.Jobs); len(cycle) > 0 {
		add("spec.nodes", cycle[0], "dependency cycle: %s", strings.Join(cycle, " → "))
	}

	for key := range req.Parameters {
		if !paramName.MatchString(key) {
			add("spec.parameters."+key, "", "parameter name %q must start with a letter or underscore", key)
		}
	}
	if o := req.Overridable; o != nil {
		for _, name := range o.Parameters {
			if _, declared := req.Parameters[name]; !declared {
				add("spec.overridable.parameters", "", "overridable parameter %q is not declared in spec.parameters", name)
			}
		}
	}
	for i := range req.Triggers {
		trigger := &req.Triggers[i]
		path := fmt.Sprintf("spec.triggers[%d]", i)
		trigger.Type = strings.TrimSpace(trigger.Type)
		if !triggerTypes[trigger.Type] {
			add(path+".type", "", "trigger type must be manual, schedule, api or event")
			continue
		}
		switch trigger.Type {
		case "schedule":
			if trigger.Timezone == "" {
				trigger.Timezone = "UTC"
			}
			if _, _, err := ParseCron(trigger.Cron, trigger.Timezone); err != nil {
				add(path+".cron", "", "%v", err)
			}
		case "event":
			if strings.TrimSpace(trigger.Topic) == "" {
				add(path+".topic", "", "event triggers need a Kafka topic")
			}
		}
	}
	return req, issues
}

// findCycle returns one dependency cycle as an ordered path that starts and
// ends on the same node, or nil. Missing dependencies are ignored here.
func findCycle(jobs []api.PipelineJob) []string {
	parents := map[string][]string{}
	order := []string{}
	for _, job := range jobs {
		if _, seen := parents[job.Name]; !seen {
			order = append(order, job.Name)
		}
		parents[job.Name] = job.DependsOn
	}
	const (
		unvisited = iota
		active
		done
	)
	state := map[string]int{}
	var stack []string
	var cycle []string
	var visit func(string) bool
	visit = func(name string) bool {
		state[name] = active
		stack = append(stack, name)
		for _, parent := range parents[name] {
			if _, exists := parents[parent]; !exists || parent == name {
				continue
			}
			switch state[parent] {
			case active:
				for i, entry := range stack {
					if entry == parent {
						// stack holds child→parent edges; reverse so the
						// path reads in execution order.
						path := append([]string{}, stack[i:]...)
						for l, r := 0, len(path)-1; l < r; l, r = l+1, r-1 {
							path[l], path[r] = path[r], path[l]
						}
						cycle = append(path, path[0])
						return true
					}
				}
			case unvisited:
				if visit(parent) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		return false
	}
	for _, name := range order {
		if state[name] == unvisited && visit(name) {
			return cycle
		}
	}
	return nil
}

// Layers groups nodes into dependency depth for display and quota checks.
// Execution itself is ready-set based: a node starts when its own
// dependencies finish, not when its whole layer does.
func Layers(jobs []api.PipelineJob) [][]string {
	depth := map[string]int{}
	byName := map[string]api.PipelineJob{}
	for _, job := range jobs {
		byName[job.Name] = job
	}
	var compute func(string, map[string]bool) int
	compute = func(name string, path map[string]bool) int {
		if d, ok := depth[name]; ok {
			return d
		}
		if path[name] {
			return 0
		}
		path[name] = true
		d := 0
		for _, parent := range byName[name].DependsOn {
			if _, ok := byName[parent]; ok {
				if pd := compute(parent, path) + 1; pd > d {
					d = pd
				}
			}
		}
		delete(path, name)
		depth[name] = d
		return d
	}
	maxDepth := 0
	for _, job := range jobs {
		if d := compute(job.Name, map[string]bool{}); d > maxDepth {
			maxDepth = d
		}
	}
	layers := make([][]string, maxDepth+1)
	for _, job := range jobs {
		layers[depth[job.Name]] = append(layers[depth[job.Name]], job.Name)
	}
	return layers
}

// SpecHash fingerprints the executable content of a request (its canonical
// YAML), so identical saves do not create new revisions.
func SpecHash(req api.UpsertPipelineDefinitionRequest) (string, []byte, error) {
	text, err := Render(req)
	if err != nil {
		return "", nil, err
	}
	return Hash(text), text, nil
}

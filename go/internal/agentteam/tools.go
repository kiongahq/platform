package agentteam

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

// Class says what a resume may do with a call that started and has no result.
type Class int

const (
	// ClassReadOnly tools have no effect, so they re-run.
	ClassReadOnly Class = iota
	// ClassGateway built-ins change only gateway state. They need no approval and
	// are not re-run: the contract's crash table gives them outcome_unknown.
	ClassGateway
	// ClassSandbox tools change only the sandbox. They need no approval, but
	// re-running a half-finished command is not safe, so they get outcome_unknown.
	ClassSandbox
	// ClassSideEffect tools reach outside and need a person's approval.
	ClassSideEffect
)

// Built-in tool names.
const (
	ToolSpawnWorker    = "spawn_worker"
	ToolSendMessage    = "send_message"
	ToolListWorkers    = "list_workers"
	ToolReadWorkerFile = "read_worker_file"
	ToolWorkerDiff     = "worker_diff"
	ToolFinish         = "finish"
	ToolExec           = "exec"
	ToolReadFile       = "read_file"
	ToolWriteFile      = "write_file"
	ToolEditFile       = "edit_file"
	ToolListFiles      = "list_files"
	ToolSearchFiles    = "search_files"
	ToolGitClone       = "git_clone"
	ToolGitDiff        = "git_diff"
	ToolGitPush        = "git_push"
)

// Tool is one callable tool. Kind and Ref route the call in the gateway; only
// name, description and schema go to the model.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	Kind        string         `json:"-"`
	Ref         string         `json:"-"`
	Class       Class          `json:"-"`
}

func object(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func text() map[string]any    { return map[string]any{"type": "string"} }
func integer() map[string]any { return map[string]any{"type": "integer"} }

var finishTool = Tool{Name: ToolFinish, Kind: "builtin", Class: ClassGateway,
	Description: "Finish with your report. A worker hands back to the coordinator. The coordinator ends the run, and is refused while workers are live.",
	InputSchema: object([]string{"report"}, map[string]any{"report": text()})}

func sendMessageTool(description string) Tool {
	return Tool{Name: ToolSendMessage, Kind: "builtin", Class: ClassGateway, Description: description,
		InputSchema: object([]string{"to", "text"}, map[string]any{"to": text(), "text": text()})}
}

var coordinatorTools = []Tool{
	{Name: ToolSpawnWorker, Kind: "builtin", Class: ClassGateway,
		Description: "Start a team member on a task. Returns its worker id at once; it does not wait for the worker.",
		InputSchema: object([]string{"agent", "task"}, map[string]any{"agent": text(), "task": text()})},
	sendMessageTool("Send a message to a worker by id, or by name when exactly one worker has that name. A completed worker resumes with its transcript."),
	{Name: ToolListWorkers, Kind: "builtin", Class: ClassReadOnly,
		Description: "List workers: id, name, status and last error.", InputSchema: object([]string{}, map[string]any{})},
	{Name: ToolReadWorkerFile, Kind: "builtin", Class: ClassReadOnly,
		Description: "Read a file from a worker's sandbox.",
		InputSchema: object([]string{"worker", "path"}, map[string]any{"worker": text(), "path": text()})},
	{Name: ToolWorkerDiff, Kind: "builtin", Class: ClassReadOnly,
		Description: "Show the git diff of a worker's sandbox.",
		InputSchema: object([]string{"worker"}, map[string]any{"worker": text()})},
	finishTool,
}

var sandboxTools = []Tool{
	{Name: ToolExec, Kind: "sandbox", Class: ClassSandbox, Description: "Run a shell command in your sandbox.",
		InputSchema: object([]string{"command"}, map[string]any{"command": text(), "cwd": text(), "timeout_seconds": integer()})},
	{Name: ToolReadFile, Kind: "sandbox", Class: ClassReadOnly, Description: "Read a file under /workspace.",
		InputSchema: object([]string{"path"}, map[string]any{"path": text()})},
	{Name: ToolWriteFile, Kind: "sandbox", Class: ClassSandbox, Description: "Write a file under /workspace.",
		InputSchema: object([]string{"path", "content"}, map[string]any{"path": text(), "content": text()})},
	{Name: ToolEditFile, Kind: "sandbox", Class: ClassSandbox, Description: "Replace one exact, unique occurrence of old with new in a file.",
		InputSchema: object([]string{"path", "old", "new"}, map[string]any{"path": text(), "old": text(), "new": text()})},
	{Name: ToolListFiles, Kind: "sandbox", Class: ClassReadOnly, Description: "List a directory under /workspace.",
		InputSchema: object([]string{"path"}, map[string]any{"path": text()})},
	{Name: ToolSearchFiles, Kind: "sandbox", Class: ClassReadOnly, Description: "Search files under /workspace for a regular expression.",
		InputSchema: object([]string{"pattern"}, map[string]any{"pattern": text(), "path": text()})},
}

var gitTools = []Tool{
	{Name: ToolGitClone, Kind: "git", Class: ClassSandbox, Description: "Clone a granted repository into your sandbox.",
		InputSchema: object([]string{"repository"}, map[string]any{"repository": text(), "ref": text()})},
	{Name: ToolGitDiff, Kind: "git", Class: ClassReadOnly, Description: "Show the git diff of your sandbox.",
		InputSchema: object([]string{}, map[string]any{})},
	{Name: ToolGitPush, Kind: "git", Class: ClassSideEffect, Description: "Push your work to a branch. Give the short name only; the platform places it under kionga/<run-id>/. Needs a person's approval.",
		InputSchema: object([]string{"branch"}, map[string]any{"branch": text(), "repository": text()})},
}

var nameUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// grantTool names a tool so it fits the contract's 64 characters. A long ref
// keeps a hash suffix, so two grants can never collapse into one name.
func grantTool(prefix, kind, ref, description string, class Class, schema map[string]any) Tool {
	name := prefix + "_" + nameUnsafe.ReplaceAllString(ref, "_")
	if len(name) > 64 {
		sum := sha256.Sum256([]byte(ref))
		name = name[:47] + "_" + hex.EncodeToString(sum[:8])
	}
	return Tool{Name: name, Kind: kind, Ref: ref, Class: class, Description: description, InputSchema: schema}
}

// ToolsFor returns the tools a worker may call, derived only from the role and
// its snapshot definition. Nothing a model says can change this list.
func ToolsFor(snapshot Snapshot, role, agent string) []Tool {
	if role == RoleCoordinator {
		return append([]Tool(nil), coordinatorTools...)
	}
	tools := []Tool{sendMessageTool("Send a message to the coordinator. Use to: \"coordinator\"."), finishTool}
	member, ok := snapshot.Member(agent)
	if !ok {
		return tools
	}
	sandbox, git := false, false
	anyInput := map[string]any{"type": "object"}
	for _, grant := range member.Tools {
		switch grant.Kind {
		case "sandbox":
			if !sandbox {
				tools = append(tools, sandboxTools...)
			}
			sandbox = true
		case "git":
			if !git {
				tools = append(tools, gitTools...)
			}
			git = true
		case "tool":
			tools = append(tools, grantTool("tool", "tool", grant.Ref, "Platform tool "+grant.Ref+". Pass its input as {\"input\": \"...\"}. Needs a person's approval.", ClassSideEffect, anyInput))
		case "connection":
			tools = append(tools, grantTool("connection", "connection", grant.Ref, "Call API connection "+grant.Ref+" with a JSON body. Needs a person's approval.", ClassSideEffect, anyInput))
		case "knowledge":
			tools = append(tools, grantTool("knowledge", "knowledge", grant.Ref, "Search knowledge base "+grant.Ref+" for text matching your query.", ClassReadOnly,
				object([]string{"query"}, map[string]any{"query": text(), "limit": integer()})))
		case "dataset":
			tools = append(tools, grantTool("dataset", "dataset", grant.Ref, "List objects, or read one by key, in dataset "+grant.Ref+".", ClassReadOnly,
				object([]string{}, map[string]any{"prefix": text(), "key": text()})))
		}
	}
	return tools
}

// FindTool looks a called name up in a worker's list.
func FindTool(tools []Tool, name string) (Tool, bool) {
	for _, tool := range tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

// Rerunnable reports whether a resume may run a started call again.
func Rerunnable(class Class) bool { return class == ClassReadOnly }

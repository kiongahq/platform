package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/kiongahq/platform/internal/sandbox/client"
)

// These patterns mirror contracts/agent-team/sandbox-api.v1.schema.json. The gateway
// validates too, but the manager never relies on that.
var (
	runIDPattern    = regexp.MustCompile(`^atr-[a-f0-9]{24}$`)
	workerIDPattern = regexp.MustCompile(`^atw-[a-f0-9]{24}$`)
	sandboxID       = regexp.MustCompile(`^sbx-[a-f0-9]{24}$`)
	repositoryURL   = regexp.MustCompile(`^https://[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?(/[A-Za-z0-9_~+-][A-Za-z0-9._~+-]*)+/?$`)
	refPattern      = regexp.MustCompile(`^[A-Za-z0-9_]+([./-][A-Za-z0-9_]+)*$`)
	branchPattern   = regexp.MustCompile(`^kionga/(atr-[a-f0-9]{24})/([A-Za-z0-9]+([._-][A-Za-z0-9]+)*)$`)
	repoName        = regexp.MustCompile(`^[A-Za-z0-9_~+-][A-Za-z0-9._~+-]*$`)
	commitPattern   = regexp.MustCompile(`^[a-f0-9]{40,64}$`)
	xferPattern     = regexp.MustCompile(`^/workspace/\.kionga-xfer\.[A-Za-z0-9]+$`)
)

// DeriveID is deterministic so a create is idempotent with no stored state.
func DeriveID(projectID, runID, workerID string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + runID + "\x00" + workerID))
	return "sbx-" + hex.EncodeToString(sum[:12])
}

func projectLabel(projectID string) string {
	sum := sha256.Sum256([]byte(projectID))
	return hex.EncodeToString(sum[:16])
}

// workspacePath is the lexical check. It rejects ".." outright rather than cleaning
// it away, and control characters, which could not survive the in-sandbox resolve
// reliably ($(...) strips trailing newlines). The authoritative symlink check runs
// inside the sandbox, because only there can it see the agent's filesystem.
func workspacePath(raw string) (string, error) {
	if raw == "" {
		return "", rejectPath("path is required")
	}
	if len(raw) > 4096 {
		return "", rejectPath("path is too long")
	}
	for _, char := range raw {
		if char < 0x20 || char == 0x7f {
			return "", rejectPath("path contains control characters")
		}
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", rejectPath("path contains ..")
		}
	}
	joined := raw
	if !strings.HasPrefix(raw, "/") {
		joined = WorkspacePath + "/" + raw
	}
	cleaned := path.Clean(joined)
	if cleaned != WorkspacePath && !strings.HasPrefix(cleaned, WorkspacePath+"/") {
		return "", rejectPath("path is outside /workspace")
	}
	return cleaned, nil
}

func rejectPath(message string) error {
	return &client.Error{Status: 403, Code: client.CodePathRejected, Message: message}
}

// canonicalRepository applies the URL rule and the host allowlist, and returns the
// URL rebuilt from its parts. Parsing and matching a regular expression both have to
// agree, because a URL parser and git may read a strange URL differently.
func canonicalRepository(raw string, hosts []string) (string, error) {
	invalid := &client.Error{Status: 400, Code: client.CodeInvalid, Message: "repository must be an https URL with no credentials, query or fragment"}
	if len(raw) > 2048 || !repositoryURL.MatchString(raw) {
		return "", invalid
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Host == "" {
		return "", invalid
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return "", invalid
		}
	}
	if !hostAllowed(parsed.Host, hosts) {
		return "", &client.Error{Status: 403, Code: client.CodeHostNotAllowed, Message: "repository host is not in KIONGA_SANDBOX_GIT_HOSTS"}
	}
	return "https://" + strings.ToLower(parsed.Host) + strings.TrimRight(parsed.Path, "/"), nil
}

// hostAllowed matches exactly. An entry with no port allows only the HTTPS default,
// so allowing a host never allows some other service on the same machine.
func hostAllowed(hostPort string, hosts []string) bool {
	host := strings.ToLower(hostPort)
	if name, port, err := net.SplitHostPort(host); err == nil {
		if port == "443" {
			host = name
		}
	}
	for _, allowed := range hosts {
		if host == allowed || (strings.TrimSuffix(allowed, ":443") == host) {
			return true
		}
	}
	return false
}

// repositoryDir is where a clone lands and where a push looks for it.
func repositoryDir(repository string) (string, error) {
	name := strings.TrimSuffix(path.Base(repository), ".git")
	if !repoName.MatchString(name) || name == "." || name == ".." {
		return "", &client.Error{Status: 400, Code: client.CodeInvalid, Message: "repository name is not usable as a directory"}
	}
	return WorkspacePath + "/" + name, nil
}

// checkBranch is the push rule: kionga/<this sandbox's run>/<name> and nothing else.
// The run comes from the container's label, never the request. Force and delete are
// impossible because the push refspec is built here, with no + and a non-empty source.
func checkBranch(branch, runID string) error {
	rejected := &client.Error{Status: 403, Code: client.CodeBranchRejected, Message: "branch must be kionga/<run-id>/<name> for this sandbox's run"}
	if len(branch) > 255 {
		return rejected
	}
	match := branchPattern.FindStringSubmatch(branch)
	if match == nil || match[1] != runID {
		return rejected
	}
	if strings.HasSuffix(match[2], ".lock") {
		return rejected
	}
	return nil
}

func checkCredential(credential string, required bool) error {
	if credential == "" {
		if required {
			return &client.Error{Status: 400, Code: client.CodeInvalid, Message: "credential is required"}
		}
		return nil
	}
	if len(credential) > maxCredentialLength {
		return &client.Error{Status: 400, Code: client.CodeInvalid, Message: "credential is too long"}
	}
	for _, char := range credential {
		if char < 0x20 || char == 0x7f {
			return &client.Error{Status: 400, Code: client.CodeInvalid, Message: "credential contains control characters"}
		}
	}
	return nil
}

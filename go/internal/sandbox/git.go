package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kiongahq/platform/internal/sandbox/client"
)

// Git has network and credentials; a sandbox has neither. So git that needs either
// runs in a short-lived helper on the same runtime, and the helper and the sandbox
// never share a filesystem. Only opaque bundle bytes cross between them, streamed
// through the manager, which keeps none on disk. The helper therefore can never run
// git inside a .git the agent touched, and repository config, hooks, core.sshCommand,
// core.fsmonitor and filters have no way to reach the credentialed process.

const (
	helperRunDir  = "/run/kionga"
	helperScratch = "/scratch"
	stderrLimit   = 8 << 10
)

func gitFailed(stderr *capture, secret, fallback string) error {
	message := strings.TrimSpace(scrub(stderr.String(), secret))
	if message == "" {
		message = fallback
	}
	return &client.Error{Status: http.StatusBadGateway, Code: client.CodeGitFailed, Message: message}
}

// helperEnv is the helper's whole environment. Nothing secret is in it: the secret
// is a tmpfs file that the askpass program reads, so an environment dump shows none.
func helperEnv(withAskpass, withCA bool, program ...string) []string {
	argv := []string{"/usr/bin/env", "-i", "-C", helperScratch, safePath, "HOME=" + helperRunDir + "/home", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https"}
	if withAskpass {
		argv = append(argv, "GIT_ASKPASS="+helperRunDir+"/askpass")
	}
	if withCA {
		argv = append(argv, "GIT_SSL_CAINFO="+helperRunDir+"/ca.pem")
	}
	return append(argv, program...)
}

// startHelper creates a helper with a network and no workspace, and loads the secret
// over exec stdin. Callers defer the returned cleanup.
func (s *Service) startHelper(ctx context.Context, sandboxID, secret string, hosts []HostEntry) (string, func(), error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", func() {}, err
	}
	name := s.config.NamePrefix + "git-" + hex.EncodeToString(suffix)
	spec := s.hardenedSpec(name, s.config.HelperImage, RoleGitHelper)
	spec.Labels[LabelID] = sandboxID
	spec.Network = s.config.GitNetwork
	spec.ExtraHosts = hosts
	spec.MemoryBytes += 2 * s.config.BundleBytes
	spec.FileSizeBytes = 2 * s.config.BundleBytes
	spec.Tmpfs = append(spec.Tmpfs,
		TmpfsMount{Path: helperRunDir, SizeBytes: 4 << 20},
		TmpfsMount{Path: helperScratch, SizeBytes: 2 * s.config.BundleBytes},
	)
	cleanup := func() { s.removeQuietly(name) }
	if err := s.driver.EnsureNetwork(ctx, s.config.GitNetwork, s.labels(RoleGitHelper)); err != nil {
		s.logger.Printf("sandbox: git helper network: %v", err)
		return "", func() {}, unavailable("the git helper network is not available")
	}
	if err := s.driver.Create(ctx, spec); err != nil {
		cleanup()
		s.logger.Printf("sandbox: git helper failed to start: %v", err)
		return "", func() {}, unavailable("the git helper could not be started")
	}
	if err := s.attest(ctx, name); err != nil {
		cleanup()
		s.logger.Printf("sandbox: git helper failed verification: %v", err)
		return "", func() {}, unavailable("the git helper failed runtime verification")
	}
	stdin := io.MultiReader(strings.NewReader(secret), strings.NewReader(string(s.config.GitCAPEM)))
	argv := helperEnv(false, false, "/bin/sh", "-c", helperSetupScript, "kionga",
		strconv.Itoa(len(secret)), strconv.Itoa(len(s.config.GitCAPEM)), s.config.GitUsername)
	stderr := newCapture(stderrLimit)
	code, err := s.scriptExec(ctx, name, argv, stdin, io.Discard, stderr)
	if err != nil || code != 0 {
		cleanup()
		s.logger.Printf("sandbox: git helper setup failed: exit %d: %s", code, scrub(stderr.String(), secret))
		return "", func() {}, unavailable("the git helper could not be prepared")
	}
	return name, cleanup, nil
}

func (s *Service) gitArgv(secret string, program ...string) []string {
	return helperEnv(secret != "", len(s.config.GitCAPEM) > 0, program...)
}

// stream copies at most size bytes from one exec's stdout to another's stdin, with no
// disk and bounded memory, and returns both exit codes. The sender is limited to size,
// so one that lies about the length cannot overfill the receiver. If it sends less,
// closing the receiver's stdin ends the wait, and the receiver's size check fails.
func (s *Service) stream(ctx context.Context, fromName string, fromArgv []string, toName string, toArgv []string, size int64, stdout io.Writer) (fromCode, toCode int, toStderr *capture, err error) {
	reader, writer := io.Pipe()
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		code int
		err  error
	}
	sender := make(chan outcome, 1)
	go func() {
		code, sendErr := s.execAttested(streamCtx, fromName, ExecRequest{Argv: fromArgv, Stdout: &sizeLimitWriter{w: writer, remaining: size}, Stderr: io.Discard})
		_ = writer.Close()
		sender <- outcome{code, sendErr}
	}()
	toStderr = newCapture(stderrLimit)
	toCode, err = s.execAttested(streamCtx, toName, ExecRequest{Argv: toArgv, Stdin: reader, Stdout: stdout, Stderr: toStderr})
	_ = reader.Close()
	if err != nil {
		cancel()
	}
	result := <-sender
	if err == nil && result.err != nil && !errors.Is(result.err, errStreamFull) && !errors.Is(result.err, io.ErrClosedPipe) {
		err = result.err
	}
	return result.code, toCode, toStderr, err
}

var errStreamFull = errors.New("stream exceeded its declared size")

type sizeLimitWriter struct {
	w         io.Writer
	remaining int64
}

func (l *sizeLimitWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > l.remaining {
		written, err := l.w.Write(data[:l.remaining])
		l.remaining -= int64(written)
		if err == nil {
			err = errStreamFull
		}
		return written, err
	}
	written, err := l.w.Write(data)
	l.remaining -= int64(written)
	return written, err
}

// GitClone clones into /workspace/<repository name> through a helper.
func (s *Service) GitClone(ctx context.Context, id string, request client.GitCloneRequest) (client.GitCloneResult, error) {
	secret := string(request.Credential)
	repository, err := canonicalRepository(request.Repository, s.config.GitHosts)
	if err != nil {
		return client.GitCloneResult{}, err
	}
	if request.Ref != "" && (len(request.Ref) > 255 || !refPattern.MatchString(request.Ref)) {
		return client.GitCloneResult{}, invalid("ref is malformed")
	}
	if err := checkCredential(secret, false); err != nil {
		return client.GitCloneResult{}, err
	}
	target, err := repositoryDir(repository)
	if err != nil {
		return client.GitCloneResult{}, err
	}
	release, err := s.admitGit(ctx, id, true)
	if err != nil {
		return client.GitCloneResult{}, err
	}
	defer release()
	hosts, err := s.pinRepositoryHost(ctx, repository)
	if err != nil {
		return client.GitCloneResult{}, err
	}
	found, err := s.writable(ctx, id)
	if err != nil {
		return client.GitCloneResult{}, err
	}
	code, err := s.internalExec(ctx, found.Name, scriptArgv(cloneTargetScript, target), nil, io.Discard, io.Discard)
	if err != nil {
		return client.GitCloneResult{}, err
	}
	switch code {
	case 0:
	case exitTargetExists:
		return client.GitCloneResult{}, &client.Error{Status: http.StatusConflict, Code: client.CodeConflict, Message: target + " already exists in the workspace"}
	case exitPathEscape, exitResolveFailed:
		return client.GitCloneResult{}, rejectPath("the clone target resolves outside /workspace")
	default:
		return client.GitCloneResult{}, unavailable("the clone target could not be checked")
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.GitTimeout)
	defer cancel()
	helper, cleanup, err := s.startHelper(ctx, id, secret, hosts)
	if err != nil {
		return client.GitCloneResult{}, err
	}
	defer cleanup()

	stdout, stderr := &headBuffer{limit: 64}, newCapture(stderrLimit)
	code, err = s.scriptExec(ctx, helper, s.gitArgv(secret, "/bin/sh", "-c", helperCloneScript, "kionga", repository, request.Ref), nil, stdout, stderr)
	if err != nil {
		return client.GitCloneResult{}, err
	}
	switch code {
	case 0:
	case exitEmptyRepo:
		return client.GitCloneResult{}, &client.Error{Status: http.StatusBadGateway, Code: client.CodeGitFailed, Message: "the remote repository is empty"}
	case exitRefNotFound:
		return client.GitCloneResult{}, invalid("ref was not found in the repository")
	default:
		return client.GitCloneResult{}, gitFailed(stderr, secret, "git clone failed")
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(stdout.data)), 10, 64)
	if err != nil || size <= 0 {
		return client.GitCloneResult{}, &client.Error{Status: http.StatusBadGateway, Code: client.CodeGitFailed, Message: "the helper did not produce a bundle"}
	}
	if size > s.config.BundleBytes {
		return client.GitCloneResult{}, &client.Error{Status: http.StatusRequestEntityTooLarge, Code: client.CodeTooLarge, Message: "the repository exceeds the bundle limit"}
	}

	ingest := scriptArgv(ingestScript, target, repository, request.Ref, strconv.FormatInt(size, 10))
	outCommit := &headBuffer{limit: 128}
	send := helperEnv(false, false, "/bin/cat", helperScratch+"/out.bundle")
	fromCode, toCode, toStderr, err := s.stream(ctx, helper, send, found.Name, ingest, size, outCommit)
	if err != nil {
		s.logger.Printf("sandbox: clone transfer failed: %v", err)
		return client.GitCloneResult{}, unavailable("the clone could not be moved to the workspace")
	}
	if fromCode != 0 {
		return client.GitCloneResult{}, &client.Error{Status: http.StatusBadGateway, Code: client.CodeGitFailed, Message: "the helper could not send the bundle"}
	}
	switch toCode {
	case 0:
	case exitTargetExists:
		return client.GitCloneResult{}, &client.Error{Status: http.StatusConflict, Code: client.CodeConflict, Message: target + " already exists in the workspace"}
	case exitPathEscape, exitResolveFailed:
		return client.GitCloneResult{}, rejectPath("the clone target resolves outside /workspace")
	default:
		return client.GitCloneResult{}, gitFailed(toStderr, secret, "the workspace could not unpack the clone")
	}
	commit := strings.TrimSpace(string(outCommit.data))
	if !commitPattern.MatchString(commit) {
		return client.GitCloneResult{}, &client.Error{Status: http.StatusBadGateway, Code: client.CodeGitFailed, Message: "the clone did not produce a commit"}
	}
	return client.GitCloneResult{Commit: commit}, nil
}

// GitPush bundles a branch in the sandbox and pushes it from a fresh clone in a
// helper. The checks here are the authority; the gateway's are only a convenience.
func (s *Service) GitPush(ctx context.Context, id string, request client.GitPushRequest) (client.GitPushResult, error) {
	secret := string(request.Credential)
	repository, err := canonicalRepository(request.Repository, s.config.GitHosts)
	if err != nil {
		return client.GitPushResult{}, err
	}
	if err := checkCredential(secret, true); err != nil {
		return client.GitPushResult{}, err
	}
	release, err := s.admitGit(ctx, id, true)
	if err != nil {
		return client.GitPushResult{}, err
	}
	defer release()
	found, err := s.writable(ctx, id)
	if err != nil {
		return client.GitPushResult{}, err
	}
	if err := checkBranch(request.Branch, found.Labels[LabelRun]); err != nil {
		return client.GitPushResult{}, err
	}
	source, err := repositoryDir(repository)
	if err != nil {
		return client.GitPushResult{}, err
	}
	hosts, err := s.pinRepositoryHost(ctx, repository)
	if err != nil {
		return client.GitPushResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.GitTimeout)
	defer cancel()

	// Everything in stdout and stderr here comes from a sandbox the agent controls.
	stdout, stderr := &headBuffer{limit: 256}, newCapture(stderrLimit)
	code, err := s.scriptExec(ctx, found.Name, scriptArgv(bundleScript, source, request.Branch), nil, stdout, stderr)
	if err != nil {
		return client.GitPushResult{}, err
	}
	switch code {
	case 0:
	case exitNotFound:
		return client.GitPushResult{}, invalid(source + " is not a git repository in the workspace")
	case exitPathEscape, exitResolveFailed:
		return client.GitPushResult{}, rejectPath("the repository resolves outside /workspace")
	default:
		return client.GitPushResult{}, gitFailed(stderr, secret, "the branch could not be bundled; commit to a local branch of the same name first")
	}
	lines := strings.Split(strings.TrimSpace(string(stdout.data)), "\n")
	if len(lines) != 2 || !xferPattern.MatchString(lines[0]) {
		return client.GitPushResult{}, &client.Error{Status: http.StatusBadGateway, Code: client.CodeGitFailed, Message: "the sandbox returned a malformed bundle description"}
	}
	bundle := lines[0]
	defer s.removeSandboxFile(found.Name, bundle)
	size, err := strconv.ParseInt(lines[1], 10, 64)
	if err != nil || size <= 0 {
		return client.GitPushResult{}, &client.Error{Status: http.StatusBadGateway, Code: client.CodeGitFailed, Message: "the sandbox returned a malformed bundle description"}
	}
	if size > s.config.BundleBytes {
		return client.GitPushResult{}, &client.Error{Status: http.StatusRequestEntityTooLarge, Code: client.CodeTooLarge, Message: "the branch exceeds the bundle limit"}
	}

	helper, cleanup, err := s.startHelper(ctx, id, secret, hosts)
	if err != nil {
		return client.GitPushResult{}, err
	}
	defer cleanup()

	receive := helperEnv(false, false, "/bin/sh", "-c", helperReceiveScript, "kionga", strconv.FormatInt(size, 10))
	send := userArgv(WorkspacePath, nil, "/bin/cat", "--", bundle)
	_, toCode, toStderr, err := s.stream(ctx, found.Name, send, helper, receive, size, io.Discard)
	if err != nil {
		s.logger.Printf("sandbox: push transfer failed: %v", err)
		return client.GitPushResult{}, unavailable("the bundle could not be moved to the git helper")
	}
	if toCode != 0 {
		return client.GitPushResult{}, gitFailed(toStderr, secret, "the bundle did not match its declared size")
	}

	commitOut, pushErr := &headBuffer{limit: 128}, newCapture(stderrLimit)
	code, err = s.scriptExec(ctx, helper, s.gitArgv(secret, "/bin/sh", "-c", helperPushScript, "kionga", repository, request.Branch), nil, commitOut, pushErr)
	if err != nil {
		return client.GitPushResult{}, err
	}
	switch code {
	case 0:
	case exitDefaultBranch:
		return client.GitPushResult{}, &client.Error{Status: http.StatusForbidden, Code: client.CodeBranchRejected, Message: "pushing to the default branch is not allowed"}
	default:
		return client.GitPushResult{}, gitFailed(pushErr, secret, "git push failed")
	}
	commit := strings.TrimSpace(string(commitOut.data))
	if !commitPattern.MatchString(commit) {
		return client.GitPushResult{}, &client.Error{Status: http.StatusBadGateway, Code: client.CodeGitFailed, Message: "the push did not report a commit"}
	}
	return client.GitPushResult{Branch: request.Branch, Commit: commit}, nil
}

func (s *Service) removeSandboxFile(name, file string) {
	ctx, cancel := context.WithTimeout(context.Background(), internalTimeout)
	defer cancel()
	_, _ = s.execAttested(ctx, name, ExecRequest{Argv: userArgv(WorkspacePath, nil, "/bin/rm", "-f", "--", file), Stdout: io.Discard, Stderr: io.Discard})
}

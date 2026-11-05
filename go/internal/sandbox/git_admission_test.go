package sandbox

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kiongahq/platform/internal/sandbox/client"
)

func TestGitHelperLimitConfig(t *testing.T) {
	for _, value := range []string{"0", "-1", "65", "no"} {
		if _, err := LoadConfig(validEnv(map[string]string{"KIONGA_SANDBOX_MAX_GIT_HELPERS": value})); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestGitAdmissionCountsPersistedHelpers(t *testing.T) {
	for _, sameSandbox := range []bool{false, true} {
		t.Run(fmt.Sprint(sameSandbox), func(t *testing.T) {
			s, d, _ := newTestService(t, nil)
			id := gitFixture(t, s, d)
			count := 8
			if sameSandbox {
				count = 1
			}
			for i := 0; i < count; i++ {
				labels := s.labels(RoleGitHelper)
				labels[LabelID] = fmt.Sprint(i)
				if sameSandbox {
					labels[LabelID] = id
				}
				name := fmt.Sprint("old-helper-", i)
				d.instances[name] = &Instance{Name: name, Labels: labels}
			}
			restarted := NewService(s.config, d, s.logger)
			restarted.resolve = s.resolve
			_, err := restarted.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://github.com/o/r.git"})
			wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
			if len(d.specNamed(RoleGitHelper)) != 0 {
				t.Fatal("started another helper")
			}
		})
	}
}

func TestCloneExistingTargetNeverFetches(t *testing.T) {
	s, d, _ := newTestService(t, nil)
	id := gitFixture(t, s, d)
	original := d.onExec
	d.onExec = func(call execCall, req ExecRequest) (int, error) {
		if strings.Contains(strings.Join(call.Argv, " "), `if [ -e "$p" ]`) {
			return exitTargetExists, nil
		}
		return original(call, req)
	}
	_, err := s.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://github.com/o/r.git"})
	wantStatus(t, err, http.StatusConflict, client.CodeConflict)
	if len(d.specNamed(RoleGitHelper)) != 0 {
		t.Fatal("fetched before rejecting existing target")
	}
}

func TestGitAdmissionConcurrentOperations(t *testing.T) {
	s, d, _ := newTestService(t, map[string]string{"KIONGA_SANDBOX_MAX_GIT_HELPERS": "1"})
	id := gitFixture(t, s, d)
	other, err := s.Create(context.Background(), client.CreateRequest{RunID: testOtherRun, WorkerID: testWorker, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	original := d.onExec
	d.onExec = func(call execCall, req ExecRequest) (int, error) {
		if containsArg(call.Argv, helperCloneScript) {
			close(entered)
			<-release
		}
		return original(call, req)
	}
	request := client.GitCloneRequest{Repository: "https://github.com/o/r.git"}
	done := make(chan error, 1)
	go func() { _, err := s.GitClone(context.Background(), id, request); done <- err }()
	<-entered
	for _, target := range []string{id, other.ID} {
		_, err := s.GitClone(context.Background(), target, request)
		wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	}
	_, err = s.GitDiff(context.Background(), id)
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	_, err = s.GitPush(context.Background(), id, client.GitPushRequest{Repository: request.Repository, Credential: client.Credential(secretValue())})
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	if len(d.specNamed(RoleGitHelper)) != 1 {
		t.Fatal("concurrent request started a helper")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	d.onExec = original
	if _, err := s.GitClone(context.Background(), other.ID, request); err != nil {
		t.Fatalf("capacity not released: %v", err)
	}
}

func TestGitPendingReservationsAndPersistedHelpersAreNotDoubleCounted(t *testing.T) {
	s, d, _ := newTestService(t, map[string]string{"KIONGA_SANDBOX_MAX_GIT_HELPERS": "2"})
	first, err := s.admitGit(context.Background(), "first", true)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	labels := s.labels(RoleGitHelper)
	labels[LabelID] = "first"
	d.instances["helper"] = &Instance{Name: "helper", Labels: labels}
	second, err := s.admitGit(context.Background(), "second", true)
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	if _, err := s.admitGit(context.Background(), "third", true); client.Code(err) != client.CodeUnavailable {
		t.Fatalf("pending reservation not counted: %v", err)
	}
}

func TestGitAdmissionFailsClosedAndReleasesOnFailure(t *testing.T) {
	s, d, _ := newTestService(t, map[string]string{"KIONGA_SANDBOX_MAX_GIT_HELPERS": "1"})
	id := gitFixture(t, s, d)
	request := client.GitCloneRequest{Repository: "https://github.com/o/r.git"}
	d.listErr = fmt.Errorf("cannot list")
	_, err := s.GitClone(context.Background(), id, request)
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	d.listErr = nil
	d.networkErr = fmt.Errorf("cannot create network")
	_, err = s.GitClone(context.Background(), id, request)
	wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
	d.networkErr = nil
	if _, err := s.GitClone(context.Background(), id, request); err != nil {
		t.Fatalf("failure leaked reservation: %v", err)
	}
}

func TestGitHelperLimitDefaultsAndCeiling(t *testing.T) {
	config, err := LoadConfig(validEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxGitHelpers != 8 {
		t.Fatalf("default=%d", config.MaxGitHelpers)
	}
	config, err = LoadConfig(validEnv(map[string]string{"KIONGA_SANDBOX_MAX_GIT_HELPERS": "64"}))
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxGitHelpers != 64 {
		t.Fatalf("ceiling=%d", config.MaxGitHelpers)
	}
}

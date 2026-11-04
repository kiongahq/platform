package sandbox

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kiongahq/platform/internal/sandbox/client"
)

func TestAttestationRequiredAfterRestart(t *testing.T) {
	for _, managerRestart := range []bool{false, true} {
		t.Run(map[bool]string{false: "container", true: "manager"}[managerRestart], func(t *testing.T) {
			s, d, _ := newTestService(t, nil)
			id := createTest(t, s)
			if managerRestart {
				s = NewService(s.config, d, s.logger)
			} else {
				d.instances[s.containerName(id)].Running = false
			}
			d.kernel = "host-kernel"
			ran := 0
			d.onExec = func(execCall, ExecRequest) (int, error) { ran++; return 0, nil }
			_, err := s.Exec(context.Background(), id, client.ExecRequest{Command: "must-not-run"})
			wantStatus(t, err, http.StatusServiceUnavailable, client.CodeUnavailable)
			if ran != 0 {
				t.Fatalf("ran %d user commands", ran)
			}
			if _, ok := d.instances[s.containerName(id)]; ok {
				t.Fatal("failed sandbox was retained")
			}
		})
	}
}

type pausedAttestation struct {
	*fakeDriver
	entered, release chan struct{}
}

func (d *pausedAttestation) Exec(ctx context.Context, name string, req ExecRequest) (int, error) {
	if containsArg(req.Argv, "uname -r") || containsArg(req.Argv, "/bin/uname") {
		select {
		case d.entered <- struct{}{}:
		default:
		}
		select {
		case <-d.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return d.fakeDriver.Exec(ctx, name, req)
}

func TestConcurrentCreateWaitsForAttestation(t *testing.T) {
	s, d, _ := newTestService(t, nil)
	s.probe = func(context.Context) error { return nil }
	paused := &pausedAttestation{d, make(chan struct{}, 1), make(chan struct{})}
	s.driver = paused
	request := client.CreateRequest{RunID: testRun, WorkerID: testWorker, ProjectID: testProject}
	first, second := make(chan error, 1), make(chan error, 1)
	command := make(chan error, 1)
	go func() { _, err := s.Create(context.Background(), request); first <- err }()
	<-paused.entered
	go func() { _, err := s.Create(context.Background(), request); second <- err }()
	go func() {
		_, err := s.Exec(context.Background(), DeriveID(testProject, testRun, testWorker), client.ExecRequest{Command: "true"})
		command <- err
	}()
	select {
	case err := <-second:
		close(paused.release)
		<-first
		t.Fatalf("second create escaped attestation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case err := <-command:
		close(paused.release)
		<-first
		<-second
		t.Fatalf("command escaped attestation: %v", err)
	default:
	}
	close(paused.release)
	if err := <-command; err != nil {
		t.Fatal(err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestRestartAndReplacementAttestNewIdentity(t *testing.T) {
	s, d, _ := newTestService(t, nil)
	id := createTest(t, s)
	name := s.containerName(id)
	for _, replace := range []bool{false, true} {
		before := len(d.execs)
		if replace {
			d.instances[name].Identity += "/replacement"
		} else {
			d.instances[name].Running = false
		}
		if _, err := s.Exec(context.Background(), id, client.ExecRequest{Command: "true"}); err != nil {
			t.Fatal(err)
		}
		checks := 0
		for _, call := range d.execs[before:] {
			if call.Name == name && containsArg(call.Argv, "/bin/uname") {
				checks++
			}
		}
		if checks != 1 {
			t.Fatalf("runtime checks=%d, want 1", checks)
		}
	}
}

func TestManagerRestartGatesFilesAndGit(t *testing.T) {
	for name, op := range map[string]func(*Service, string) error{
		"read": func(s *Service, id string) error { _, err := s.ReadPath(context.Background(), id, "x"); return err },
		"write": func(s *Service, id string) error {
			_, err := s.WriteFile(context.Background(), id, "x", []byte("x"))
			return err
		},
		"diff": func(s *Service, id string) error { _, err := s.GitDiff(context.Background(), id); return err },
		"clone": func(s *Service, id string) error {
			_, err := s.GitClone(context.Background(), id, client.GitCloneRequest{Repository: "https://github.com/o/r.git"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, d, _ := newTestService(t, nil)
			id := createTest(t, s)
			restarted := NewService(s.config, d, s.logger)
			restarted.resolve = s.resolve
			d.kernel = "host-kernel"
			ran := 0
			d.onExec = func(execCall, ExecRequest) (int, error) { ran++; return 0, nil }
			wantStatus(t, op(restarted, id), http.StatusServiceUnavailable, client.CodeUnavailable)
			if ran != 0 {
				t.Fatalf("ran %d operations", ran)
			}
		})
	}
}

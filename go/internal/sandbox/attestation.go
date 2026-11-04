package sandbox

import (
	"context"
	"sync"
)

// The lock serializes attestation, commands and manager-initiated restarts for
// one name. The cached identity comes exclusively from the engine, never labels
// or files that a sandbox can write.
type attestationState struct {
	mu       sync.Mutex
	identity string
}

func (s *Service) attestationFor(name string) *attestationState {
	value, _ := s.attestations.LoadOrStore(name, &attestationState{})
	return value.(*attestationState)
}

func (s *Service) ensureAttested(ctx context.Context, name string) (Instance, error) {
	state := s.attestationFor(name)
	state.mu.Lock()
	defer state.mu.Unlock()
	return s.attestLocked(ctx, name, state)
}

func (s *Service) attestLocked(ctx context.Context, name string, state *attestationState) (Instance, error) {
	found, err := s.driver.Inspect(ctx, name)
	if err != nil {
		return Instance{}, unavailable("the sandbox could not be inspected for runtime verification")
	}
	if !found.Running || found.Identity == "" {
		state.identity = ""
		s.removeQuietly(name)
		return Instance{}, unavailable("the sandbox has no running instance identity")
	}
	if state.identity == found.Identity {
		return found, nil
	}
	state.identity = ""
	if err := s.attest(ctx, name); err != nil {
		s.removeQuietly(name)
		return Instance{}, unavailable("the sandbox failed runtime verification")
	}
	current, err := s.driver.Inspect(ctx, name)
	if err != nil || !current.Running || current.Identity != found.Identity {
		s.removeQuietly(name)
		return Instance{}, unavailable("the sandbox changed during runtime verification")
	}
	state.identity = found.Identity
	return found, nil
}

// Every command boundary checks again, including streaming and cleanup calls
// made after an earlier operation admitted the sandbox.
func (s *Service) execAttested(ctx context.Context, name string, request ExecRequest) (int, error) {
	state := s.attestationFor(name)
	state.mu.Lock()
	defer state.mu.Unlock()
	found, err := s.driver.Inspect(ctx, name)
	if err != nil {
		return 0, unavailable("the command instance could not be inspected")
	}
	if found.Labels[LabelRole] == RoleSandbox {
		if _, err := s.attestLocked(ctx, name, state); err != nil {
			return 0, err
		}
	}
	return s.driver.Exec(ctx, name, request)
}

func (s *Service) restartInstance(ctx context.Context, name string) error {
	state := s.attestationFor(name)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.identity = ""
	return s.driver.Restart(ctx, name)
}

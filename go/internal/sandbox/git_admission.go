package sandbox

import "context"

// Admission includes durable engine labels so a manager restart cannot erase
// helpers that still consume resources or hold credentials. Pending reservations
// cover the interval before the helper has been created.
func (s *Service) admitGit(ctx context.Context, id string, needsHelper bool) (func(), error) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	if _, busy := s.gitActive[id]; busy {
		return nil, unavailable("a git operation is already running for this sandbox")
	}
	helpers, err := s.driver.List(ctx, s.roleLabels(RoleGitHelper))
	if err != nil {
		return nil, unavailable("git helper admission could not list existing helpers")
	}
	present := make(map[string]bool)
	for _, helper := range helpers {
		owner := helper.Labels[LabelID]
		if owner == id {
			return nil, unavailable("a git helper is already running for this sandbox")
		}
		present[owner] = true
	}
	count := int64(len(helpers))
	for owner, helper := range s.gitActive {
		if helper && !present[owner] {
			count++
		}
	}
	if needsHelper && count >= s.config.MaxGitHelpers {
		return nil, unavailable("the git helper limit has been reached")
	}
	if s.gitActive == nil {
		s.gitActive = make(map[string]bool)
	}
	s.gitActive[id] = needsHelper
	return func() { s.gitMu.Lock(); delete(s.gitActive, id); s.gitMu.Unlock() }, nil
}

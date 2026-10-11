package httpapi

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/kiongahq/platform/internal/auth"
	"github.com/kiongahq/platform/internal/store"
	"github.com/kiongahq/platform/pkg/api"
)

// Sign-up modes for people who sign in through single sign-on (OAuth, SAML,
// LDAP via Dex) and have no Kionga account yet.
const (
	SignupOff      = "off"      // no account is created; access only from mapped groups
	SignupApproval = "approval" // an access request is filed for an administrator to approve
	SignupOpen     = "open"     // an active account is created from the sign-up template
)

// DefaultSignupServices is what a new account can use: building and running
// work, not platform administration.
var DefaultSignupServices = []string{"overview", "projects", "pipelines", "models", "features", "storage", "catalog", "workbench", "ide"}

// SignupPolicy decides what a first single sign-on creates.
type SignupPolicy struct {
	Mode     string
	Domains  []string // allowed email domains; empty means any verified or unverified email
	Profile  string   // resource profile for open sign-up (store.ResourceProfiles)
	Services []string
}

// ParseSignupPolicy reads KIONGA_SIGNUP, KIONGA_SIGNUP_EMAIL_DOMAINS,
// KIONGA_SIGNUP_PROFILE and KIONGA_SIGNUP_SERVICES.
func ParseSignupPolicy(mode, domains, profile, services string) (SignupPolicy, error) {
	policy := SignupPolicy{Mode: strings.ToLower(strings.TrimSpace(mode)), Profile: strings.TrimSpace(profile)}
	if policy.Mode == "" {
		policy.Mode = SignupApproval
	}
	if !slices.Contains([]string{SignupOff, SignupApproval, SignupOpen}, policy.Mode) {
		return SignupPolicy{}, fmt.Errorf("KIONGA_SIGNUP must be off, approval or open, got %q", mode)
	}
	for _, domain := range strings.Split(domains, ",") {
		if domain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "@")); domain != "" {
			policy.Domains = append(policy.Domains, domain)
		}
	}
	if policy.Profile == "" {
		policy.Profile = "starter"
	}
	known := false
	for _, candidate := range store.ResourceProfiles() {
		known = known || (candidate.Name == policy.Profile && candidate.Name != "custom")
	}
	if !known {
		return SignupPolicy{}, fmt.Errorf("KIONGA_SIGNUP_PROFILE %q is not a named resource profile", policy.Profile)
	}
	policy.Services = DefaultSignupServices
	if strings.TrimSpace(services) != "" {
		policy.Services = nil
		for _, service := range strings.Split(services, ",") {
			if service = strings.TrimSpace(service); service != "" {
				policy.Services = append(policy.Services, service)
			}
		}
	}
	return policy, nil
}

// allowedEmail applies the domain allowlist. With an allowlist, only an
// email the identity provider verified counts, so an unverified address on
// a permissive provider cannot claim a company domain.
func (p SignupPolicy) allowedEmail(principal auth.Principal) bool {
	if len(p.Domains) == 0 {
		return true
	}
	at := strings.LastIndex(principal.Email, "@")
	if at < 0 || !principal.EmailVerified {
		return false
	}
	return slices.Contains(p.Domains, strings.ToLower(principal.Email[at+1:]))
}

// Outcome names what SignUp did, for logs and tests.
type SignupOutcome string

const (
	SignupSkipped     SignupOutcome = "skipped"
	SignupNotAllowed  SignupOutcome = "not_allowed"
	SignupRequested   SignupOutcome = "requested"
	SignupProvisioned SignupOutcome = "provisioned"
)

// SignUp runs after a successful single sign-on. It never blocks the login:
// failures are returned for logging and the person can still request access
// from the console.
func (p SignupPolicy) SignUp(repository store.Repository, principal auth.Principal) (SignupOutcome, error) {
	if p.Mode == SignupOff || principal.Subject == "" {
		return SignupSkipped, nil
	}
	// A directory group already granted a role above user: a stored profile
	// would replace it (the RBAC resolver prefers stored profiles) and demote
	// the person. A plain user role still needs a profile for services.
	for _, role := range principal.Roles {
		if role != auth.RoleUser {
			return SignupSkipped, nil
		}
	}
	for _, existing := range repository.UserAccess() {
		if existing.Subject == principal.Subject {
			return SignupSkipped, nil
		}
	}
	// Any earlier request, pending or rejected, means the person already
	// signed up once; a rejection is not undone by signing in again.
	if len(repository.AccessRequestsFor(principal.Subject)) > 0 {
		return SignupSkipped, nil
	}
	if !p.allowedEmail(principal) {
		return SignupNotAllowed, nil
	}
	source := principal.Connector
	if source == "" {
		source = "single sign-on"
	}
	switch p.Mode {
	case SignupApproval:
		_, err := repository.CreateAccessRequest(principal.Subject, principal.Email, api.CreateAccessRequest{
			Reason:            "Signed up with " + source,
			RequestedServices: p.Services,
		})
		if err != nil {
			return SignupSkipped, err
		}
		return SignupRequested, nil
	case SignupOpen:
		_, err := repository.UpsertUserAccess(principal.Subject, api.UpsertUserAccessRequest{
			Email: principal.Email, Role: auth.RoleUser, Services: p.Services,
			Compute: api.ComputeGrant{Profile: p.Profile},
		}, "signup:"+source)
		if err != nil {
			return SignupSkipped, err
		}
		return SignupProvisioned, nil
	}
	return SignupSkipped, nil
}

// OnLogin adapts the policy to auth.SessionConfig.OnLogin.
func (p SignupPolicy) OnLogin(repository store.Repository) func(context.Context, auth.Principal) {
	return func(_ context.Context, principal auth.Principal) {
		outcome, err := p.SignUp(repository, principal)
		switch {
		case err != nil:
			log.Printf("sign-up for %s via %q failed: %v", principal.Subject, principal.Connector, err)
		case outcome != SignupSkipped:
			log.Printf("sign-up for %s via %q: %s", principal.Subject, principal.Connector, outcome)
		}
	}
}

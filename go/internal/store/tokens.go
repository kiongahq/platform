package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/kiongahq/platform/pkg/api"
)

func validateTokenRequest(req api.CreateAPITokenRequest) (api.CreateAPITokenRequest, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Services, req.ProjectIDs = unique(req.Services), unique(req.ProjectIDs)
	if len(req.Name) < 3 {
		return req, errors.New("token name must contain at least 3 characters")
	}
	if len(req.Services) == 0 {
		return req, errors.New("at least one service scope is required")
	}
	if req.ExpiresInDays < 1 || req.ExpiresInDays > 365 {
		return req, errors.New("expiry must be between 1 and 365 days")
	}
	for _, service := range req.Services {
		valid := false
		for _, candidate := range validServices {
			if service == candidate {
				valid = true
			}
		}
		if !valid || service == "workbench" || service == "ide" {
			return req, errors.New("invalid API service scope: " + service)
		}
	}
	return req, nil
}

func generateAPIToken() (idValue, prefix, secret, hash string, err error) {
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return
	}
	idRandom := make([]byte, 8)
	if _, err = rand.Read(idRandom); err != nil {
		return
	}
	idValue = "tok-" + hex.EncodeToString(idRandom)
	prefix = "nxs_" + hex.EncodeToString(idRandom[:4])
	secret = prefix + "_" + base64.RawURLEncoding.EncodeToString(random)
	sum := sha256.Sum256([]byte(secret))
	hash = hex.EncodeToString(sum[:])
	return
}

func tokenMatches(stored api.APIToken, secret string) bool {
	sum := sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare([]byte(stored.SecretHash), []byte(hex.EncodeToString(sum[:]))) == 1
}

func (s *Store) APITokensFor(subject string) []api.APIToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]api.APIToken, 0)
	for _, token := range s.data.APITokens {
		if token.Subject == subject {
			result = append(result, token)
		}
	}
	return result
}

func (s *Store) CreateAPIToken(subject string, req api.CreateAPITokenRequest) (api.CreatedAPIToken, error) {
	req, err := validateTokenRequest(req)
	if err != nil {
		return api.CreatedAPIToken{}, err
	}
	tokenID, prefix, secret, hash, err := generateAPIToken()
	if err != nil {
		return api.CreatedAPIToken{}, err
	}
	now := time.Now().UTC()
	token := api.APIToken{ID: tokenID, Subject: subject, Name: req.Name, Prefix: prefix, SecretHash: hash, Services: req.Services, ProjectIDs: req.ProjectIDs, CreatedAt: now, ExpiresAt: now.Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.APITokens = append([]api.APIToken{token}, s.data.APITokens...)
	s.record("api_token.created", "api_token", token.ID, subject, map[string]any{"services": token.Services, "expires_at": token.ExpiresAt})
	return api.CreatedAPIToken{Token: token, Secret: secret}, s.persist()
}

func (s *Store) RevokeAPIToken(subject, tokenID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.APITokens {
		if s.data.APITokens[i].ID == tokenID && s.data.APITokens[i].Subject == subject {
			if s.data.APITokens[i].RevokedAt == nil {
				now := time.Now().UTC()
				s.data.APITokens[i].RevokedAt = &now
				s.record("api_token.revoked", "api_token", tokenID, subject, nil)
			}
			return s.persist()
		}
	}
	return ErrNotFound
}

func (s *Store) ResolveAPIToken(secret string) (api.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for i := range s.data.APITokens {
		token := &s.data.APITokens[i]
		if strings.HasPrefix(secret, token.Prefix+"_") && token.RevokedAt == nil && now.Before(token.ExpiresAt) && tokenMatches(*token, secret) {
			token.LastUsedAt = &now
			_ = s.persist()
			return *token, nil
		}
	}
	return api.APIToken{}, ErrNotFound
}

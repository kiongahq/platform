// Package redact removes secrets from text before it is stored or exported.
//
// Two layers: well-known credential patterns (cloud keys, bearer tokens,
// provider API keys, private key blocks, key=value pairs whose key names a
// secret), and exact values of secrets the process itself knows (environment
// variables whose names mark them as secrets). Values shorter than 8
// characters are not matched exactly, to avoid redacting common words.
package redact

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const Mask = "[REDACTED]"

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bhf_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bsk-(ant-)?[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`(?i)\b(bearer|token)\s+[A-Za-z0-9._~+/=-]{16,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s:/@]+:[^\s@/]+@`),
}

// keyValue redacts the value in key=value / key: value pairs whose key names
// a secret, keeping the key so logs stay readable.
var keyValue = regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)[a-z0-9_.-]*)(["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)`)

var secretEnv = regexp.MustCompile(`(?i)(SECRET|PASSWORD|TOKEN|API_KEY|ACCESS_KEY|PRIVATE_KEY|CREDENTIAL)`)

var (
	knownOnce  sync.Once
	knownValue []string
)

func known() []string {
	knownOnce.Do(func() {
		seen := map[string]bool{}
		for _, item := range os.Environ() {
			name, value, ok := strings.Cut(item, "=")
			if !ok || len(value) < 8 || !secretEnv.MatchString(name) || seen[value] {
				continue
			}
			seen[value] = true
			knownValue = append(knownValue, value)
		}
		// Longest first so overlapping values redact completely.
		sort.Slice(knownValue, func(i, j int) bool { return len(knownValue[i]) > len(knownValue[j]) })
	})
	return knownValue
}

// String returns text with secrets replaced by Mask.
func String(text string) string {
	return With(text, known())
}

// With redacts patterns plus the given exact secret values.
func With(text string, secrets []string) string {
	for _, value := range secrets {
		if len(value) >= 8 {
			text = strings.ReplaceAll(text, value, Mask)
		}
	}
	for _, pattern := range patterns {
		text = pattern.ReplaceAllStringFunc(text, func(match string) string {
			if strings.Contains(match, "://") {
				scheme, _, _ := strings.Cut(match, "://")
				return scheme + "://" + Mask + "@"
			}
			if lower := strings.ToLower(match); strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "token ") {
				prefix, _, _ := strings.Cut(match, " ")
				return prefix + " " + Mask
			}
			return Mask
		})
	}
	return keyValue.ReplaceAllString(text, "${1}${3}"+Mask)
}

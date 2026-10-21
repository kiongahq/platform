package auth

import "testing"

// Every value a browser could resolve to another origin must fall back to the
// console. Browsers treat "\" like "/" and strip tabs and newlines, so
// "/\evil.test" and "/\t/evil.test" are protocol-relative URLs to them.
func TestSafeReturnToRejectsOffSiteTargets(t *testing.T) {
	for _, value := range []string{
		"https://evil.test/", "//evil.test", "///evil.test", "/\\evil.test", "\\\\evil.test",
		"/\t/evil.test", "/\n/evil.test", "https:evil.test", "javascript:alert(1)", "evil.test",
		"/%5Cevil.test", "/%2F%2Fevil.test",
	} {
		got := safeReturnTo(value)
		if !sameOriginPath(got) {
			t.Errorf("safeReturnTo(%q) = %q, which a browser may resolve off-site", value, got)
		}
	}
	for value, want := range map[string]string{
		"": "/console.html", "/console.html": "/console.html",
		"/workspace.html?tool=ide": "/workspace.html?tool=ide",
	} {
		if got := safeReturnTo(value); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", value, got, want)
		}
	}
}

// sameOriginPath mirrors how a browser resolves a Location header: after
// stripping tab/newline and mapping "\" to "/", it must start with exactly
// one "/".
func sameOriginPath(location string) bool {
	normalized := make([]byte, 0, len(location))
	for i := 0; i < len(location); i++ {
		switch c := location[i]; c {
		case '\t', '\n', '\r':
		case '\\':
			normalized = append(normalized, '/')
		default:
			normalized = append(normalized, c)
		}
	}
	s := string(normalized)
	return len(s) > 0 && s[0] == '/' && (len(s) == 1 || s[1] != '/')
}

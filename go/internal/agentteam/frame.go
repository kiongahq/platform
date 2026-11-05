package agentteam

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxToolOutput caps tool output before it is stored or sent to a model.
const MaxToolOutput = 64 << 10

// frameTag matches anything a model could read as the start of a kionga-data
// tag, open or closing, with case changes, spaces, zero-width characters or
// full-width angle brackets between the parts.
var frameTag = regexp.MustCompile(`(?i)[<＜﹤][\s\p{Cf}]*/?[\s\p{Cf}]*kionga[\s\p{Cf}]*[-_‐‑–—]?[\s\p{Cf}]*data`)

var attributeSafe = regexp.MustCompile(`[^A-Za-z0-9:_.\-]`)

// Neutralize makes body unable to close its frame or to open a forged one.
func Neutralize(body string) string {
	return frameTag.ReplaceAllStringFunc(body, func(match string) string {
		_, size := utf8.DecodeRuneInString(match)
		return "&lt;" + match[size:]
	})
}

// Frame wraps inbound content with provenance. Only the gateway calls this, so
// source and kind never come from a model, and nonce is fresh per delivery.
func Frame(source, kind, nonce, body string) string {
	return `<kionga-data source="` + attributeSafe.ReplaceAllString(source, "_") +
		`" kind="` + attributeSafe.ReplaceAllString(kind, "_") +
		`" nonce="` + attributeSafe.ReplaceAllString(nonce, "_") + `">` +
		Neutralize(body) + `</kionga-data>`
}

// Cap keeps the head and tail of output beyond limit bytes and marks the cut.
func Cap(output string, limit int) string {
	if len(output) <= limit {
		return output
	}
	const marker = "\n...[truncated]...\n"
	keep := limit - len(marker)
	if keep < 2 {
		return marker
	}
	head, tail := keep/2, keep-keep/2
	for head > 0 && !utf8.RuneStart(output[head]) {
		head--
	}
	start := len(output) - tail
	for start < len(output) && !utf8.RuneStart(output[start]) {
		start++
	}
	return strings.ToValidUTF8(output[:head], "") + marker + strings.ToValidUTF8(output[start:], "")
}

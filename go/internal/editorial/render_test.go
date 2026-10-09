package editorial

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/kiongahq/platform/pkg/api"
)

func block(kind string, data any) api.Block {
	raw, _ := json.Marshal(data)
	return api.Block{ID: "x" + kind, Type: kind, Data: raw}
}

func media(id string) MediaLookup {
	return func(got string) (api.BlogMedia, bool) {
		if got != id {
			return api.BlogMedia{}, false
		}
		return api.BlogMedia{ID: id, Alt: "library alt", Width: 2000, Height: 1000, Variants: []api.MediaVariant{
			{Name: "480", Width: 480, Height: 240}, {Name: "960", Width: 960, Height: 480}, {Name: "1600", Width: 1600, Height: 800},
		}}, true
	}
}

// renderClean normalizes like a save does, then renders.
func renderClean(t *testing.T, blocks ...api.Block) string {
	t.Helper()
	normalized, err := NormalizeBlocks(blocks)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return Render(normalized, media("med-abcdef12"))
}

var preBlock = regexp.MustCompile(`(?s)<pre class="kb-code"><code[^>]*>.*?</code></pre>`)

// The XSS corpus: every payload must render without executable markup.
func TestRendererNeutralizesXSSCorpus(t *testing.T) {
	payloads := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<a href="javascript:alert(1)">click</a>`,
		`<a href="JaVaScRiPt:alert(1)">click</a>`,
		`<a href="java&#x09;script:alert(1)">tab</a>`,
		`<a href=" javascript:alert(1)">space</a>`,
		`<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">data</a>`,
		`<a href="vbscript:msgbox(1)">vb</a>`,
		`<a href="//evil.example/x">protocol relative</a>`,
		`<svg onload=alert(1)><circle/></svg>`,
		`<svg><script>alert(1)</script></svg>`,
		`<math><mtext><img src=x onerror=alert(1)></mtext></math>`,
		`<b style="background:url(javascript:alert(1))">styled</b>`,
		`<span style="position:fixed;inset:0">overlay</span>`,
		`<style>body{display:none}</style>`,
		`<iframe src="https://evil.example"></iframe>`,
		`<object data="x.swf"></object>`,
		`<b onclick="alert(1)">bold</b>`,
		`<a href="https://ok.example" onmouseover="alert(1)">ok</a>`,
		`"><script>alert(1)</script>`,
		`<img src="data:image/svg+xml;base64,PHN2Zz4=">`,
		`<form action="https://evil.example"><input name=x></form>`,
		`<meta http-equiv="refresh" content="0;url=https://evil.example">`,
		`<base href="https://evil.example/">`,
		`<template><img src=x onerror=alert(1)></template>`,
		`<noscript><p title="</noscript><img src=x onerror=alert(1)>">`,
		`&lt;script&gt;alert(1)&lt;/script&gt;`,
	}
	for _, payload := range payloads {
		out := renderClean(t,
			block("paragraph", map[string]any{"text": payload}),
			block("header", map[string]any{"text": payload, "level": 2}),
			block("quote", map[string]any{"text": payload, "caption": payload}),
			block("list", map[string]any{"style": "unordered", "items": []any{map[string]any{"content": payload, "items": []any{}}}}),
			block("table", map[string]any{"withHeadings": true, "content": [][]string{{payload}, {payload}}}),
			block("warning", map[string]any{"title": payload, "message": payload}),
			block("code", map[string]any{"code": payload, "language": `"><script>`}),
			block("image", map[string]any{"media_id": "med-abcdef12", "alt": payload, "caption": payload}),
		)
		// Code blocks show the payload as escaped text; scan everything else.
		pre := preBlock.FindString(out)
		if pre == "" || strings.Contains(pre[strings.Index(pre, "<code"):], "<script") {
			t.Errorf("code block not escaped: %s", pre)
		}
		lower := strings.ToLower(preBlock.ReplaceAllString(out, ""))
		for _, forbidden := range []string{"<script", "onerror", "onload", "onclick", "onmouseover", "javascript:", "vbscript:", "data:", "<svg", "<math", "<style", "style=", "<iframe", "<object", "<form", "<input", "<meta", "<base", "<template", "//evil.example"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("payload %q leaked %q:\n%s", payload, forbidden, out)
			}
		}
	}
}

func TestRendererForcesLinkRelAndKeepsSafeLinks(t *testing.T) {
	out := renderClean(t, block("paragraph", map[string]any{"text": `Read <a href="https://go.dev/doc" rel="opener" target="_self">the docs</a> or <a href="/blogs.html">more</a>.`}))
	if !strings.Contains(out, `href="https://go.dev/doc"`) || !strings.Contains(out, `href="/blogs.html"`) {
		t.Fatalf("safe links dropped: %s", out)
	}
	if strings.Count(out, `rel="noopener noreferrer nofollow"`) != 2 || strings.Contains(out, "target=") || strings.Contains(out, "opener\"") {
		t.Fatalf("rel not forced on every link: %s", out)
	}
}

func TestRendererImagesOnlyFromMediaEndpoint(t *testing.T) {
	out := renderClean(t, block("image", map[string]any{"media_id": "med-abcdef12", "alt": "Cluster diagram", "caption": "<i>Fig 1</i>", "alignment": "wide"}))
	for _, want := range []string{`src="/media/blog/med-abcdef12/960"`, `/media/blog/med-abcdef12/480 480w`, `/media/blog/med-abcdef12/1600 1600w`, `alt="Cluster diagram"`, `<figcaption><i>Fig 1</i></figcaption>`, `kb-align-wide`, `width="960"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	// A foreign URL in file.url never becomes the src.
	if _, err := NormalizeBlocks([]api.Block{block("image", map[string]any{"file": map[string]any{"url": "https://evil.example/x.png"}, "alt": "x"})}); err == nil {
		t.Fatal("image without our media id accepted")
	}
	// Unknown media is dropped at render time.
	out = Render([]api.Block{block("image", map[string]any{"media_id": "med-zzzzzz99", "alt": "x"})}, media("med-abcdef12"))
	if strings.Contains(out, "<img") {
		t.Fatalf("unknown media rendered: %s", out)
	}
	// Even raw HTML that bypassed normalization cannot load other images.
	if got := Policy().Sanitize(`<img src="https://evil.example/a.png"><img src="/media/blog/med-abcdef12/960" alt="ok">`); strings.Contains(got, "evil") || !strings.Contains(got, "med-abcdef12") {
		t.Fatalf("policy image filter: %s", got)
	}
}

func TestEmbedsAllowlistedAndSandboxed(t *testing.T) {
	out := renderClean(t,
		block("embed", map[string]any{"service": "youtube", "source": "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "embed": "https://evil.example/frame"}),
		block("embed", map[string]any{"service": "vimeo", "source": "https://vimeo.com/76979871"}),
		block("embed", map[string]any{"service": "github", "source": "https://gist.github.com/octocat/6cad326836d38bd3a7ae"}),
	)
	for _, want := range []string{`src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ"`, `src="https://player.vimeo.com/video/76979871"`, `sandbox="allow-scripts allow-same-origin allow-presentation allow-popups"`, `href="https://gist.github.com/octocat/6cad326836d38bd3a7ae"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, "evil.example") {
		t.Fatalf("client-supplied embed URL used: %s", out)
	}
	for _, source := range []string{"https://evil.example/watch?v=dQw4w9WgXcQ", "javascript:alert(1)", "https://codepen.io/x/pen/y", "https://www.youtube.com/watch?v=short"} {
		if _, err := NormalizeBlocks([]api.Block{block("embed", map[string]any{"service": "youtube", "source": source})}); err == nil {
			t.Errorf("embed %s accepted", source)
		}
	}
}

func TestNormalizeRejectsUnknownBlocksAndBounds(t *testing.T) {
	if _, err := NormalizeBlocks([]api.Block{{ID: "a", Type: "raw", Data: json.RawMessage(`{"html":"<script>"}`)}}); err == nil {
		t.Fatal("raw block accepted")
	}
	huge := make([]api.Block, MaxBlocks+1)
	for i := range huge {
		huge[i] = block("delimiter", map[string]any{})
	}
	if _, err := NormalizeBlocks(huge); err == nil {
		t.Fatal("block limit not enforced")
	}
	blocks, err := NormalizeBlocks([]api.Block{block("header", map[string]any{"text": "T", "level": 99}), {ID: "../bad id", Type: "paragraph", Data: json.RawMessage(`{"text":"x"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blocks[0].Data), `"level":2`) || blocks[1].ID == "../bad id" {
		t.Fatalf("not clamped: %+v", blocks)
	}
}

func TestChecklistAndCodeRender(t *testing.T) {
	out := renderClean(t,
		block("list", map[string]any{"style": "checklist", "items": []any{map[string]any{"content": "done", "meta": map[string]any{"checked": true}, "items": []any{}}}}),
		block("code", map[string]any{"code": "if a < b { return }", "language": "go"}),
	)
	if !strings.Contains(out, `kb-check kb-checked`) || !strings.Contains(out, `<code class="language-go">if a &lt; b { return }</code>`) {
		t.Fatalf("render: %s", out)
	}
}

func TestReadingMinutes(t *testing.T) {
	text := strings.Repeat("word ", 450)
	if got := ReadingMinutes([]api.Block{block("paragraph", map[string]any{"text": text})}); got != 3 {
		t.Fatalf("reading minutes %d", got)
	}
	if got := ReadingMinutes(nil); got != 1 {
		t.Fatalf("empty reading minutes %d", got)
	}
}

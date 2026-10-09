package editorial

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kiongahq/platform/pkg/api"
)

func TestFromMarkdownCoversLegacySubset(t *testing.T) {
	source := "# Title\n\nIntro with `code` and **bold** and [link](https://go.dev) and [bad](javascript:alert(1)).\n\n~~~yaml\nkey: <value>\n~~~\n\n- one\n- two\n\n1. first\n2. second\n\n- [ ] todo\n- [x] done\n\n> quoted\n\n---\n\n<script>alert(1)</script>\n"
	blocks, err := NormalizeBlocks(FromMarkdown(source))
	if err != nil {
		t.Fatal(err)
	}
	types := []string{}
	for _, b := range blocks {
		types = append(types, b.Type)
	}
	if got := strings.Join(types, ","); got != "header,paragraph,code,list,list,list,quote,delimiter,paragraph" {
		t.Fatalf("types %s", got)
	}
	var code codeData
	_ = json.Unmarshal(blocks[2].Data, &code)
	if code.Language != "yaml" || code.Code != "key: <value>" {
		t.Fatalf("code %+v", code)
	}
	html := Render(blocks, func(string) (api.BlogMedia, bool) { return api.BlogMedia{}, false })
	for _, want := range []string{`<h2 id="title">Title</h2>`, `<code class="inline-code">code</code>`, `<b>bold</b>`, `href="https://go.dev"`, `kb-checklist`, `<ol>`, `&lt;script&gt;alert(1)&lt;/script&gt;`} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s in %s", want, html)
		}
	}
	if strings.Contains(html, "javascript:") || strings.Contains(html, "<script>") {
		t.Fatalf("unsafe markdown survived: %s", html)
	}
}

func TestMarkdownRoundTrip(t *testing.T) {
	source := "# Heading\n\nSome **bold** text with `code`.\n\n- a\n- b\n\n~~~go\nfmt.Println(1)\n~~~\n"
	blocks, err := NormalizeBlocks(FromMarkdown(source))
	if err != nil {
		t.Fatal(err)
	}
	out := ToMarkdown(blocks)
	again, err := NormalizeBlocks(FromMarkdown(out))
	if err != nil {
		t.Fatal(err)
	}
	if ToMarkdown(again) != out {
		t.Fatalf("export is not stable:\n%s\n---\n%s", out, ToMarkdown(again))
	}
	if !strings.Contains(out, "**bold**") || !strings.Contains(out, "~~~go") {
		t.Fatalf("export lost formatting: %s", out)
	}
}

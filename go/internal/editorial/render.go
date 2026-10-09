package editorial

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	"github.com/ml-ai-ops/platform/pkg/api"
)

// MediaLookup resolves a media id for rendering; ok=false drops the image.
type MediaLookup func(id string) (api.BlogMedia, bool)

// MediaURL is the public path of one media variant.
func MediaURL(id, variant string) string { return "/media/blog/" + id + "/" + variant }

// Image builds the reader projection (src, srcset, alt) of a media item.
func Image(media api.BlogMedia, alt string) api.PublicImage {
	variants := append([]api.MediaVariant(nil), media.Variants...)
	sort.Slice(variants, func(i, j int) bool { return variants[i].Width < variants[j].Width })
	image := api.PublicImage{Alt: alt, Width: media.Width, Height: media.Height}
	var srcset []string
	for _, variant := range variants {
		srcset = append(srcset, fmt.Sprintf("%s %dw", MediaURL(media.ID, variant.Name), variant.Width))
		if image.URL == "" || variant.Name == "960" {
			image.URL, image.Width, image.Height = MediaURL(media.ID, variant.Name), variant.Width, variant.Height
		}
	}
	image.SrcSet = strings.Join(srcset, ", ")
	if image.Alt == "" {
		image.Alt = media.Alt
	}
	return image
}

// Render turns blocks into HTML and sanitizes the result with Policy. Block
// text is already reduced to the inline allowlist at save time; the policy
// is the second, independent layer.
func Render(blocks []api.Block, media MediaLookup) string {
	var b strings.Builder
	for _, block := range blocks {
		renderBlock(&b, block, media)
	}
	return Policy().Sanitize(b.String())
}

func esc(value string) string { return html.EscapeString(value) }

func renderBlock(b *strings.Builder, block api.Block, media MediaLookup) {
	data, err := normalizeData(block.Type, block.Data)
	if err != nil || data == nil {
		return
	}
	switch value := data.(type) {
	case paragraphData:
		if strings.TrimSpace(value.Text) != "" {
			fmt.Fprintf(b, "<p>%s</p>\n", value.Text)
		}
	case headerData:
		// The post title is the page's h1, so body headings start at h2.
		level := min(max(value.Level, 2), 4)
		fmt.Fprintf(b, `<h%d id="%s">%s</h%d>`+"\n", level, esc(anchor(value.Text)), value.Text, level)
	case listData:
		renderList(b, value.Style, value.Items)
	case quoteData:
		fmt.Fprintf(b, `<blockquote class="kb-quote kb-align-%s"><p>%s</p>`, value.Alignment, value.Text)
		if value.Caption != "" {
			fmt.Fprintf(b, "<cite>%s</cite>", value.Caption)
		}
		b.WriteString("</blockquote>\n")
	case codeData:
		class := ""
		if value.Language != "" {
			class = ` class="language-` + esc(value.Language) + `"`
		}
		fmt.Fprintf(b, "<pre class=\"kb-code\"><code%s>%s</code></pre>\n", class, esc(value.Code))
	case struct{}:
		b.WriteString("<hr class=\"kb-delimiter\">\n")
	case imageData:
		item, ok := media(value.MediaID)
		if !ok || len(item.Variants) == 0 {
			return
		}
		image := Image(item, value.Alt)
		fmt.Fprintf(b, `<figure class="kb-image kb-align-%s"><img src="%s" srcset="%s" sizes="(max-width: 760px) 100vw, 740px" width="%d" height="%d" alt="%s" loading="lazy" decoding="async">`,
			value.Alignment, esc(image.URL), esc(image.SrcSet), image.Width, image.Height, esc(image.Alt))
		caption := value.Caption
		if item.Attribution != "" {
			if caption != "" {
				caption += " "
			}
			caption += `<span class="kb-attribution">` + esc(item.Attribution) + `</span>`
		}
		if caption != "" {
			fmt.Fprintf(b, "<figcaption>%s</figcaption>", caption)
		}
		b.WriteString("</figure>\n")
	case embedData:
		embed, ok := ResolveEmbed(value.Service, value.Source)
		if !ok {
			return
		}
		b.WriteString(`<figure class="kb-embed kb-embed-` + embed.Service + `">`)
		if embed.Frame != "" {
			fmt.Fprintf(b, `<div class="kb-embed-frame"><iframe src="%s" title="%s" loading="lazy" sandbox="allow-scripts allow-same-origin allow-presentation allow-popups" allow="fullscreen; picture-in-picture" referrerpolicy="strict-origin-when-cross-origin" allowfullscreen></iframe></div>`,
				esc(embed.Frame), esc(embedTitle(embed, value.Caption)))
		} else {
			fmt.Fprintf(b, `<a class="kb-embed-link" href="%s" rel="%s">View the gist on GitHub: %s</a>`, esc(embed.Source), LinkRel, esc(strings.TrimPrefix(embed.Source, "https://")))
		}
		if value.Caption != "" {
			fmt.Fprintf(b, "<figcaption>%s</figcaption>", value.Caption)
		}
		b.WriteString("</figure>\n")
	case tableData:
		b.WriteString(`<div class="kb-table"><table>`)
		rows := value.Content
		if value.WithHeadings && len(rows) > 0 {
			b.WriteString("<thead><tr>")
			for _, content := range rows[0] {
				fmt.Fprintf(b, "<th>%s</th>", content)
			}
			b.WriteString("</tr></thead>")
			rows = rows[1:]
		}
		if len(rows) > 0 {
			b.WriteString("<tbody>")
			for _, row := range rows {
				b.WriteString("<tr>")
				for _, content := range row {
					fmt.Fprintf(b, "<td>%s</td>", content)
				}
				b.WriteString("</tr>")
			}
			b.WriteString("</tbody>")
		}
		b.WriteString("</table></div>\n")
	case warningData:
		b.WriteString(`<aside class="kb-callout" role="note">`)
		if value.Title != "" {
			fmt.Fprintf(b, "<strong>%s</strong>", value.Title)
		}
		if value.Message != "" {
			fmt.Fprintf(b, "<p>%s</p>", value.Message)
		}
		b.WriteString("</aside>\n")
	}
}

func embedTitle(embed Embed, caption string) string {
	if text := strings.TrimSpace(PlainText(caption)); text != "" {
		return text
	}
	if embed.Service == "youtube" {
		return "YouTube video"
	}
	return "Vimeo video"
}

func renderList(b *strings.Builder, style string, items []ListItem) {
	tag, class := "ul", ""
	switch style {
	case "ordered":
		tag = "ol"
	case "checklist":
		class = ` class="kb-checklist"`
	}
	fmt.Fprintf(b, "<%s%s>", tag, class)
	for _, item := range items {
		b.WriteString("<li>")
		if style == "checklist" {
			checked, _ := item.Meta["checked"].(bool)
			mark := "kb-check"
			if checked {
				mark += " kb-checked"
			}
			fmt.Fprintf(b, `<span class="%s"></span>`, mark)
		}
		b.WriteString(item.Content)
		if len(item.Items) > 0 {
			renderList(b, style, item.Items)
		}
		b.WriteString("</li>")
	}
	fmt.Fprintf(b, "</%s>\n", tag)
}

var anchorStrip = regexp.MustCompile(`[^a-z0-9]+`)

func anchor(text string) string {
	value := strings.Trim(anchorStrip.ReplaceAllString(strings.ToLower(PlainText(text)), "-"), "-")
	if len(value) > 60 {
		value = strings.Trim(value[:60], "-")
	}
	if value == "" {
		value = "section"
	}
	return value
}

var (
	policyOnce sync.Once
	policy     *bluemonday.Policy
)

// Policy is bluemonday's UGC policy narrowed to exactly what Render emits.
func Policy() *bluemonday.Policy {
	policyOnce.Do(func() {
		p := bluemonday.NewPolicy()
		p.AllowElements("p", "br", "b", "strong", "i", "em", "s", "u", "hr", "ul", "ol", "li",
			"blockquote", "cite", "pre", "figure", "figcaption", "table", "thead", "tbody", "tr", "th", "td", "aside", "span", "div")
		p.AllowAttrs("id").Matching(regexp.MustCompile(`^[a-z0-9-]{1,60}$`)).OnElements("h2", "h3", "h4")
		p.AllowElements("h2", "h3", "h4")
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^kb-[a-z0-9-]+( kb-[a-z0-9-]+)*$`)).OnElements("figure", "blockquote", "pre", "hr", "ul", "div", "span", "aside", "a")
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^(inline-code|language-[a-z0-9+#-]{1,24})$`)).OnElements("code")
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^cdx-marker$`)).OnElements("mark")
		p.AllowElements("code", "mark")
		p.AllowAttrs("role").Matching(regexp.MustCompile(`^note$`)).OnElements("aside")
		// Links: http(s), mailto, site-relative; rel is forced.
		p.AllowAttrs("href").OnElements("a")
		p.AllowAttrs("rel").Matching(regexp.MustCompile(`^noopener noreferrer nofollow$`)).OnElements("a")
		p.AllowURLSchemes("http", "https", "mailto")
		p.AllowRelativeURLs(true)
		p.RequireParseableURLs(true)
		p.RequireNoFollowOnLinks(true)
		p.RequireNoReferrerOnLinks(true)
		// Images only from our media endpoint.
		mediaSrc := regexp.MustCompile(`^/media/blog/med-[a-z0-9]{6,40}/(480|960|1600)$`)
		p.AllowAttrs("src").Matching(mediaSrc).OnElements("img")
		p.AllowAttrs("srcset").Matching(regexp.MustCompile(`^/media/blog/med-[a-z0-9]{6,40}/(480|960|1600) \d{1,5}w(, /media/blog/med-[a-z0-9]{6,40}/(480|960|1600) \d{1,5}w)*$`)).OnElements("img")
		p.AllowAttrs("sizes").Matching(regexp.MustCompile(`^[a-z0-9 (),:-]{1,80}$`)).OnElements("img")
		p.AllowAttrs("width", "height").Matching(bluemonday.Integer).OnElements("img")
		p.AllowAttrs("alt").OnElements("img")
		p.AllowAttrs("loading").Matching(regexp.MustCompile(`^lazy$`)).OnElements("img", "iframe")
		p.AllowAttrs("decoding").Matching(regexp.MustCompile(`^async$`)).OnElements("img")
		p.AllowElements("img")
		// Embeds: sandboxed iframes from the two allowlisted video hosts.
		p.AllowAttrs("src").Matching(regexp.MustCompile(`^https://(www\.youtube-nocookie\.com/embed/[A-Za-z0-9_-]{11}|player\.vimeo\.com/video/[0-9]{1,12})$`)).OnElements("iframe")
		p.AllowAttrs("title").OnElements("iframe")
		p.AllowAttrs("allow").Matching(regexp.MustCompile(`^fullscreen; picture-in-picture$`)).OnElements("iframe")
		p.AllowAttrs("referrerpolicy").Matching(regexp.MustCompile(`^strict-origin-when-cross-origin$`)).OnElements("iframe")
		p.AllowAttrs("allowfullscreen").OnElements("iframe")
		p.AllowAttrs("sandbox").OnElements("iframe")
		p.RequireSandboxOnIFrame(bluemonday.SandboxAllowScripts, bluemonday.SandboxAllowSameOrigin, bluemonday.SandboxAllowPresentation, bluemonday.SandboxAllowPopups)
		p.AllowElements("iframe")
		policy = p
	})
	return policy
}

// TextOf returns the readable text of a post for reading time and search.
func TextOf(blocks []api.Block) string {
	var parts []string
	for _, block := range blocks {
		data, err := normalizeData(block.Type, block.Data)
		if err != nil || data == nil {
			continue
		}
		switch value := data.(type) {
		case paragraphData:
			parts = append(parts, PlainText(value.Text))
		case headerData:
			parts = append(parts, PlainText(value.Text))
		case listData:
			var walk func([]ListItem)
			walk = func(items []ListItem) {
				for _, item := range items {
					parts = append(parts, PlainText(item.Content))
					walk(item.Items)
				}
			}
			walk(value.Items)
		case quoteData:
			parts = append(parts, PlainText(value.Text))
		case codeData:
			parts = append(parts, value.Code)
		case tableData:
			for _, row := range value.Content {
				for _, cell := range row {
					parts = append(parts, PlainText(cell))
				}
			}
		case warningData:
			parts = append(parts, PlainText(value.Title), PlainText(value.Message))
		case imageData:
			parts = append(parts, PlainText(value.Caption))
		}
	}
	return strings.Join(parts, "\n")
}

// ReadingMinutes estimates reading time at 220 words per minute.
func ReadingMinutes(blocks []api.Block) int {
	words := len(strings.Fields(TextOf(blocks)))
	minutes := (words + 219) / 220
	if minutes < 1 {
		minutes = 1
	}
	return minutes
}

// ContentHash fingerprints the editable content so a no-op autosave does not
// mint a revision.
func ContentHash(fields ...any) string {
	raw, _ := json.Marshal(fields)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

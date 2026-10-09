package editorial

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/kiongahq/platform/pkg/api"
	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// FromMarkdown converts the Markdown subset the legacy blog supported (and a
// little more) into blocks: ATX headings, fenced code (``` or ~~~ with an
// optional language), unordered/ordered/task lists, block quotes, horizontal
// rules, paragraphs, and inline `code`, **bold**, *italic* and [links](url).
// The legacy renderer shifted heading levels down by one (# -> h2), so the
// block level is the Markdown depth plus one and export subtracts it again.
func FromMarkdown(source string) []api.Block {
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	var blocks []api.Block
	add := func(kind string, data any) {
		raw, _ := json.Marshal(data)
		blocks = append(blocks, api.Block{ID: fmt.Sprintf("md%d", len(blocks)+1), Type: kind, Data: raw})
	}
	var paragraph []string
	var listStyle string
	var listItems []ListItem
	flushParagraph := func() {
		if len(paragraph) > 0 {
			add("paragraph", paragraphData{Text: inlineMarkdown(strings.Join(paragraph, " "))})
			paragraph = nil
		}
	}
	flushList := func() {
		if len(listItems) > 0 {
			add("list", listData{Style: listStyle, Items: listItems})
			listItems, listStyle = nil, ""
		}
	}
	heading := regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*$`)
	task := regexp.MustCompile(`^[-*]\s+\[( |x|X)\]\s+(.+)$`)
	bullet := regexp.MustCompile(`^[-*]\s+(.+)$`)
	ordered := regexp.MustCompile(`^\d+[.)]\s+(.+)$`)
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "~~~") || strings.HasPrefix(trimmed, "```") {
			flushParagraph()
			flushList()
			fence := trimmed[:3]
			language := strings.TrimSpace(trimmed[3:])
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), fence); i++ {
				code = append(code, lines[i])
			}
			add("code", codeData{Code: strings.Join(code, "\n"), Language: strings.ToLower(language)})
			continue
		}
		if match := heading.FindStringSubmatch(trimmed); match != nil {
			flushParagraph()
			flushList()
			add("header", headerData{Text: inlineMarkdown(match[2]), Level: min(len(match[1])+1, 6)})
			continue
		}
		if trimmed == "---" || trimmed == "***" {
			flushParagraph()
			flushList()
			add("delimiter", struct{}{})
			continue
		}
		if match := task.FindStringSubmatch(trimmed); match != nil {
			flushParagraph()
			if listStyle != "checklist" {
				flushList()
				listStyle = "checklist"
			}
			listItems = append(listItems, ListItem{Content: inlineMarkdown(match[2]), Meta: map[string]any{"checked": match[1] != " "}, Items: []ListItem{}})
			continue
		}
		if match := bullet.FindStringSubmatch(trimmed); match != nil {
			flushParagraph()
			if listStyle != "unordered" {
				flushList()
				listStyle = "unordered"
			}
			listItems = append(listItems, ListItem{Content: inlineMarkdown(match[1]), Items: []ListItem{}})
			continue
		}
		if match := ordered.FindStringSubmatch(trimmed); match != nil {
			flushParagraph()
			if listStyle != "ordered" {
				flushList()
				listStyle = "ordered"
			}
			listItems = append(listItems, ListItem{Content: inlineMarkdown(match[1]), Items: []ListItem{}})
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			flushParagraph()
			flushList()
			quote := []string{strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))}
			for i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), ">") {
				i++
				quote = append(quote, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">")))
			}
			add("quote", quoteData{Text: inlineMarkdown(strings.Join(quote, " ")), Alignment: "left"})
			continue
		}
		if trimmed == "" {
			flushParagraph()
			flushList()
			continue
		}
		flushList()
		paragraph = append(paragraph, trimmed)
	}
	flushParagraph()
	flushList()
	return blocks
}

var (
	mdCode   = regexp.MustCompile("`([^`]+)`")
	mdBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdItalic = regexp.MustCompile(`(^|[^*\w])\*([^*\s][^*]*)\*`)
	mdLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
)

// inlineMarkdown escapes text then applies inline Markdown, protecting code
// spans from further formatting.
func inlineMarkdown(value string) string {
	var spans []string
	value = mdCode.ReplaceAllStringFunc(value, func(match string) string {
		spans = append(spans, `<code class="inline-code">`+html.EscapeString(match[1:len(match)-1])+`</code>`)
		return fmt.Sprintf("\x00%d\x00", len(spans)-1)
	})
	value = html.EscapeString(value)
	value = mdLink.ReplaceAllStringFunc(value, func(match string) string {
		parts := mdLink.FindStringSubmatch(match)
		href := SafeHref(html.UnescapeString(parts[2]))
		if href == "" {
			return parts[1]
		}
		return `<a href="` + html.EscapeString(href) + `">` + parts[1] + `</a>`
	})
	value = mdBold.ReplaceAllString(value, "<b>$1</b>")
	value = mdItalic.ReplaceAllString(value, "$1<i>$2</i>")
	for index, span := range spans {
		value = strings.Replace(value, fmt.Sprintf("\x00%d\x00", index), span, 1)
	}
	return CleanInline(value)
}

// ToMarkdown exports blocks as Markdown (optional export; also fills the
// legacy "content" field of the public API).
func ToMarkdown(blocks []api.Block) string {
	var out []string
	for _, block := range blocks {
		data, err := normalizeData(block.Type, block.Data)
		if err != nil || data == nil {
			continue
		}
		switch value := data.(type) {
		case paragraphData:
			out = append(out, inlineToMarkdown(value.Text))
		case headerData:
			out = append(out, strings.Repeat("#", max(value.Level-1, 1))+" "+inlineToMarkdown(value.Text))
		case listData:
			var lines []string
			var walk func([]ListItem, int)
			walk = func(items []ListItem, depth int) {
				for index, item := range items {
					marker := "-"
					switch value.Style {
					case "ordered":
						marker = fmt.Sprintf("%d.", index+1)
					case "checklist":
						if checked, _ := item.Meta["checked"].(bool); checked {
							marker = "- [x]"
						} else {
							marker = "- [ ]"
						}
					}
					lines = append(lines, strings.Repeat("  ", depth)+marker+" "+inlineToMarkdown(item.Content))
					walk(item.Items, depth+1)
				}
			}
			walk(value.Items, 0)
			out = append(out, strings.Join(lines, "\n"))
		case quoteData:
			out = append(out, "> "+inlineToMarkdown(value.Text))
		case codeData:
			out = append(out, "~~~"+value.Language+"\n"+value.Code+"\n~~~")
		case struct{}:
			out = append(out, "---")
		case imageData:
			out = append(out, fmt.Sprintf("![%s](%s)", strings.ReplaceAll(value.Alt, "]", ""), MediaURL(value.MediaID, "960")))
		case embedData:
			out = append(out, value.Source)
		case tableData:
			var lines []string
			for r, row := range value.Content {
				cells := make([]string, len(row))
				for c, cell := range row {
					cells[c] = strings.ReplaceAll(inlineToMarkdown(cell), "|", `\|`)
				}
				lines = append(lines, "| "+strings.Join(cells, " | ")+" |")
				if r == 0 {
					lines = append(lines, "|"+strings.Repeat(" --- |", len(row)))
				}
			}
			out = append(out, strings.Join(lines, "\n"))
		case warningData:
			out = append(out, "> **"+inlineToMarkdown(value.Title)+"** "+inlineToMarkdown(value.Message))
		}
	}
	return strings.Join(out, "\n\n") + "\n"
}

func inlineToMarkdown(value string) string {
	nodes, err := nethtml.ParseFragment(strings.NewReader(value), &nethtml.Node{Type: nethtml.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return PlainText(value)
	}
	var b strings.Builder
	var walk func(*nethtml.Node)
	walk = func(node *nethtml.Node) {
		if node.Type == nethtml.TextNode {
			b.WriteString(node.Data)
			return
		}
		inner := func() {
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
		}
		switch node.DataAtom {
		case atom.B, atom.Strong:
			b.WriteString("**")
			inner()
			b.WriteString("**")
		case atom.I, atom.Em:
			b.WriteString("*")
			inner()
			b.WriteString("*")
		case atom.Code:
			b.WriteString("`")
			inner()
			b.WriteString("`")
		case atom.A:
			b.WriteString("[")
			inner()
			b.WriteString("](" + attr(node, "href") + ")")
		case atom.Br:
			b.WriteString("  \n")
		default:
			inner()
		}
	}
	for _, node := range nodes {
		walk(node)
	}
	return b.String()
}

// Package editorial holds the engineering blog's deterministic core: block
// validation, the sanitized block renderer, Markdown import/export, the
// workflow rules, image processing, media storage and the scheduled
// publisher. HTTP wiring lives in internal/httpapi.
package editorial

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/kiongahq/platform/pkg/api"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Limits keep one post bounded no matter what a client sends.
const (
	MaxBlocks      = 2000
	MaxBlockBytes  = 200 << 10
	MaxTitle       = 200
	MaxSummary     = 600
	MaxTags        = 12
	MaxListDepth   = 6
	MaxTableCells  = 2000
	maxInlineBytes = 64 << 10
)

var blockIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var mediaIDPattern = regexp.MustCompile(`^med-[a-z0-9]{6,40}$`)
var languagePattern = regexp.MustCompile(`^[a-z0-9+#-]{1,24}$`)

// Supported block types. Anything else is rejected at save time.
var blockTypes = map[string]bool{
	"paragraph": true, "header": true, "list": true, "quote": true, "code": true,
	"delimiter": true, "image": true, "embed": true, "table": true, "warning": true,
}

type paragraphData struct {
	Text string `json:"text"`
}

type headerData struct {
	Text  string `json:"text"`
	Level int    `json:"level"`
}

// ListItem covers @editorjs/list 2.x nested items. Legacy flat lists (items
// as plain strings) are converted on read.
type ListItem struct {
	Content string         `json:"content"`
	Meta    map[string]any `json:"meta,omitempty"`
	Items   []ListItem     `json:"items"`
}

type listData struct {
	Style string         `json:"style"`
	Meta  map[string]any `json:"meta,omitempty"`
	Items []ListItem     `json:"items"`
}

type quoteData struct {
	Text      string `json:"text"`
	Caption   string `json:"caption"`
	Alignment string `json:"alignment,omitempty"`
}

type codeData struct {
	Code     string `json:"code"`
	Language string `json:"language,omitempty"`
}

type imageData struct {
	MediaID   string         `json:"media_id"`
	File      map[string]any `json:"file,omitempty"`
	Alt       string         `json:"alt"`
	Caption   string         `json:"caption"`
	Alignment string         `json:"alignment,omitempty"`
}

type embedData struct {
	Service string `json:"service"`
	Source  string `json:"source"`
	Embed   string `json:"embed,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Caption string `json:"caption,omitempty"`
}

type tableData struct {
	WithHeadings bool       `json:"withHeadings"`
	Stretched    bool       `json:"stretched,omitempty"`
	Content      [][]string `json:"content"`
}

type warningData struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

// ValidationError is returned for content a client must fix; handlers map it
// to 422.
type ValidationError struct{ Message string }

func (e ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return ValidationError{Message: fmt.Sprintf(format, args...)}
}

// NormalizeBlocks validates every block and returns a cleaned copy: inline
// HTML is reduced to the inline allowlist, unknown fields are dropped and
// enum values are clamped. The result is what gets stored.
func NormalizeBlocks(blocks []api.Block) ([]api.Block, error) {
	if len(blocks) > MaxBlocks {
		return nil, invalid("a post may have at most %d blocks", MaxBlocks)
	}
	out := make([]api.Block, 0, len(blocks))
	seen := map[string]bool{}
	for index, block := range blocks {
		if !blockTypes[block.Type] {
			return nil, invalid("block %d has unsupported type %q", index+1, block.Type)
		}
		if len(block.Data) > MaxBlockBytes {
			return nil, invalid("block %d is larger than %d KiB", index+1, MaxBlockBytes>>10)
		}
		if block.ID == "" || !blockIDPattern.MatchString(block.ID) || seen[block.ID] {
			block.ID = fmt.Sprintf("b%d", index+1)
			for seen[block.ID] {
				block.ID += "x"
			}
		}
		seen[block.ID] = true
		data, err := normalizeData(block.Type, block.Data)
		if err != nil {
			return nil, invalid("block %d (%s): %v", index+1, block.Type, err)
		}
		if data == nil {
			continue
		}
		raw, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		out = append(out, api.Block{ID: block.ID, Type: block.Type, Data: raw})
	}
	return out, nil
}

func decodeData(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return errors.New("malformed block data")
	}
	return nil
}

// normalizeData returns the cleaned data for one block, or nil to drop an
// empty block.
func normalizeData(kind string, raw json.RawMessage) (any, error) {
	switch kind {
	case "paragraph":
		var data paragraphData
		if err := decodeData(raw, &data); err != nil {
			return nil, err
		}
		data.Text = CleanInline(data.Text)
		return data, nil
	case "header":
		var data headerData
		if err := decodeData(raw, &data); err != nil {
			return nil, err
		}
		data.Text = CleanInline(data.Text)
		if data.Level < 1 || data.Level > 6 {
			data.Level = 2
		}
		return data, nil
	case "list":
		data, err := decodeList(raw)
		if err != nil {
			return nil, err
		}
		cells := 0
		var clean func([]ListItem, int) ([]ListItem, error)
		clean = func(items []ListItem, depth int) ([]ListItem, error) {
			if depth > MaxListDepth {
				return nil, errors.New("list is nested too deeply")
			}
			result := make([]ListItem, 0, len(items))
			for _, item := range items {
				cells++
				if cells > MaxTableCells {
					return nil, errors.New("list has too many items")
				}
				children, err := clean(item.Items, depth+1)
				if err != nil {
					return nil, err
				}
				meta := map[string]any{}
				if checked, ok := item.Meta["checked"].(bool); ok && data.Style == "checklist" {
					meta["checked"] = checked
				}
				result = append(result, ListItem{Content: CleanInline(item.Content), Meta: meta, Items: children})
			}
			return result, nil
		}
		items, err := clean(data.Items, 1)
		if err != nil {
			return nil, err
		}
		data.Items, data.Meta = items, nil
		return data, nil
	case "quote":
		var data quoteData
		if err := decodeData(raw, &data); err != nil {
			return nil, err
		}
		data.Text, data.Caption = CleanInline(data.Text), CleanInline(data.Caption)
		if data.Alignment != "center" {
			data.Alignment = "left"
		}
		return data, nil
	case "code":
		var data codeData
		if err := decodeData(raw, &data); err != nil {
			return nil, err
		}
		data.Language = strings.ToLower(strings.TrimSpace(data.Language))
		if !languagePattern.MatchString(data.Language) {
			data.Language = ""
		}
		return data, nil
	case "delimiter":
		return struct{}{}, nil
	case "image":
		var data imageData
		if err := decodeData(raw, &data); err != nil {
			return nil, err
		}
		if data.MediaID == "" {
			data.MediaID = MediaIDFromURL(fileURL(data.File))
		}
		if !mediaIDPattern.MatchString(data.MediaID) {
			return nil, errors.New("image must reference an uploaded media item")
		}
		data.Alt = strings.TrimSpace(PlainText(data.Alt))
		data.Caption = CleanInline(data.Caption)
		switch data.Alignment {
		case "wide", "full":
		default:
			data.Alignment = "center"
		}
		data.File = map[string]any{"url": "/media/blog/" + data.MediaID + "/960"}
		return data, nil
	case "embed":
		var data embedData
		if err := decodeData(raw, &data); err != nil {
			return nil, err
		}
		embed, ok := ResolveEmbed(data.Service, data.Source)
		if !ok {
			return nil, errors.New("embeds are limited to YouTube, Vimeo and GitHub gists")
		}
		data.Service, data.Source, data.Embed = embed.Service, embed.Source, embed.Frame
		data.Caption = CleanInline(data.Caption)
		return data, nil
	case "table":
		var data tableData
		if err := decodeData(raw, &data); err != nil {
			return nil, err
		}
		cells := 0
		for r := range data.Content {
			for c := range data.Content[r] {
				cells++
				data.Content[r][c] = CleanInline(data.Content[r][c])
			}
		}
		if cells > MaxTableCells {
			return nil, errors.New("table has too many cells")
		}
		return data, nil
	case "warning":
		var data warningData
		if err := decodeData(raw, &data); err != nil {
			return nil, err
		}
		data.Title, data.Message = CleanInline(data.Title), CleanInline(data.Message)
		return data, nil
	}
	return nil, errors.New("unsupported block")
}

func decodeList(raw json.RawMessage) (listData, error) {
	var data listData
	if err := json.Unmarshal(raw, &data); err == nil {
		normalizeListStyle(&data)
		return data, nil
	}
	// Legacy @editorjs/list 1.x: items are strings.
	var legacy struct {
		Style string   `json:"style"`
		Items []string `json:"items"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return data, errors.New("malformed list")
	}
	data.Style = legacy.Style
	for _, item := range legacy.Items {
		data.Items = append(data.Items, ListItem{Content: item})
	}
	normalizeListStyle(&data)
	return data, nil
}

func normalizeListStyle(data *listData) {
	switch data.Style {
	case "ordered", "checklist":
	default:
		data.Style = "unordered"
	}
}

func fileURL(file map[string]any) string {
	value, _ := file["url"].(string)
	return value
}

var mediaURLPattern = regexp.MustCompile(`^/media/blog/(med-[a-z0-9]{6,40})/(?:480|960|1600)$`)

// MediaIDFromURL extracts the media id from one of our media URLs.
func MediaIDFromURL(value string) string {
	if match := mediaURLPattern.FindStringSubmatch(value); match != nil {
		return match[1]
	}
	return ""
}

// MediaIDs lists every media item a post references (image blocks + cover).
func MediaIDs(blocks []api.Block, cover *api.PostCover) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if cover != nil {
		add(cover.MediaID)
	}
	for _, block := range blocks {
		if block.Type == "image" {
			var data imageData
			if json.Unmarshal(block.Data, &data) == nil {
				add(data.MediaID)
			}
		}
	}
	return ids
}

// MissingAltText returns the ids of image blocks without alt text.
func MissingAltText(blocks []api.Block) []string {
	var missing []string
	for _, block := range blocks {
		if block.Type != "image" {
			continue
		}
		var data imageData
		if json.Unmarshal(block.Data, &data) != nil || strings.TrimSpace(data.Alt) == "" {
			missing = append(missing, block.ID)
		}
	}
	return missing
}

// --- inline HTML ---

// inlineTags are the only elements Editor.js inline tools produce that we
// keep. Everything else is unwrapped (text kept) or, for dangerous
// containers, dropped together with its content.
var inlineTags = map[atom.Atom]bool{
	atom.B: true, atom.Strong: true, atom.I: true, atom.Em: true, atom.A: true,
	atom.Code: true, atom.Mark: true, atom.Br: true, atom.S: true, atom.U: true,
}

var droppedTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Object: true, atom.Embed: true,
	atom.Svg: true, atom.Math: true, atom.Template: true, atom.Noscript: true, atom.Textarea: true,
	atom.Select: true, atom.Title: true, atom.Xmp: true, atom.Noembed: true, atom.Noframes: true,
}

// LinkRel is set on every link the renderer emits.
const LinkRel = "noopener noreferrer nofollow"

// CleanInline rebuilds inline HTML from the allowlist. Attributes are never
// copied except a validated href (and the two class names inline tools use),
// so event handlers, styles and javascript:/data: URLs cannot survive.
func CleanInline(value string) string {
	if len(value) > maxInlineBytes {
		value = value[:maxInlineBytes]
	}
	if !strings.ContainsAny(value, "<&") {
		return html.EscapeString(value)
	}
	nodes, err := html.ParseFragment(strings.NewReader(value), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return html.EscapeString(value)
	}
	var b strings.Builder
	for _, node := range nodes {
		writeInline(&b, node)
	}
	return strings.TrimSpace(b.String())
}

func writeInline(b *strings.Builder, node *html.Node) {
	switch node.Type {
	case html.TextNode:
		b.WriteString(html.EscapeString(node.Data))
		return
	case html.ElementNode:
	default:
		return
	}
	if droppedTags[node.DataAtom] || node.DataAtom == 0 && node.Namespace != "" {
		return
	}
	if node.Namespace != "" {
		return
	}
	tag := node.DataAtom
	if !inlineTags[tag] {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			writeInline(b, child)
		}
		return
	}
	if tag == atom.Br {
		b.WriteString("<br>")
		return
	}
	name := tag.String()
	switch tag {
	case atom.A:
		href := SafeHref(attr(node, "href"))
		if href == "" {
			name = ""
		} else {
			fmt.Fprintf(b, `<a href="%s" rel="%s">`, html.EscapeString(href), LinkRel)
		}
	case atom.Code:
		b.WriteString(`<code class="inline-code">`)
	case atom.Mark:
		b.WriteString(`<mark class="cdx-marker">`)
	default:
		b.WriteString("<" + name + ">")
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		writeInline(b, child)
	}
	if name != "" {
		b.WriteString("</" + name + ">")
	}
}

func attr(node *html.Node, key string) string {
	for _, a := range node.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// SafeHref accepts http(s) and mailto URLs, site-relative paths and in-page
// anchors. Anything else (javascript:, data:, vbscript:, protocol-relative)
// returns "".
func SafeHref(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || strings.ContainsAny(value, "\x00\r\n\t") {
		return ""
	}
	if strings.HasPrefix(value, "#") {
		return value
	}
	if strings.HasPrefix(value, "/") {
		if strings.HasPrefix(value, "//") || strings.HasPrefix(value, "/\\") {
			return ""
		}
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if parsed.Host == "" {
			return ""
		}
		return parsed.String()
	case "mailto":
		return parsed.String()
	}
	return ""
}

// PlainText strips every tag and decodes entities.
func PlainText(value string) string {
	if !strings.ContainsAny(value, "<&") {
		return value
	}
	nodes, err := html.ParseFragment(strings.NewReader(value), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return value
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		if node.Type == html.ElementNode && droppedTags[node.DataAtom] {
			return
		}
		if node.Type == html.ElementNode && node.DataAtom == atom.Br {
			b.WriteString(" ")
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	for _, node := range nodes {
		walk(node)
	}
	return b.String()
}

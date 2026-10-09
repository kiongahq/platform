package editorial

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/ml-ai-ops/platform/pkg/api"
)

// BlockChange is one entry of a revision comparison.
type BlockChange struct {
	BlockID string `json:"block_id"`
	Type    string `json:"type"`
	Change  string `json:"change"` // added | removed | changed | moved | unchanged
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
}

// FieldChange is a metadata difference between two revisions.
type FieldChange struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Comparison is the deterministic diff of two revisions.
type Comparison struct {
	From    int           `json:"from"`
	To      int           `json:"to"`
	Fields  []FieldChange `json:"fields"`
	Blocks  []BlockChange `json:"blocks"`
	Summary struct {
		Added     int `json:"added"`
		Removed   int `json:"removed"`
		Changed   int `json:"changed"`
		Unchanged int `json:"unchanged"`
	} `json:"summary"`
}

// Compare matches blocks by id (Editor.js keeps ids stable across edits) and
// reports metadata changes, in the order of the newer revision with removed
// blocks listed after the block that preceded them.
func Compare(from, to api.PostRevision) Comparison {
	result := Comparison{From: from.Number, To: to.Number, Fields: []FieldChange{}, Blocks: []BlockChange{}}
	field := func(name, before, after string) {
		if before != after {
			result.Fields = append(result.Fields, FieldChange{Field: name, Before: before, After: after})
		}
	}
	field("title", from.Title, to.Title)
	field("summary", from.Summary, to.Summary)
	field("slug", from.Slug, to.Slug)
	field("tags", strings.Join(from.Tags, ", "), strings.Join(to.Tags, ", "))
	field("seo_title", from.SEO.Title, to.SEO.Title)
	field("seo_description", from.SEO.Description, to.SEO.Description)
	field("cover", coverID(from.Cover), coverID(to.Cover))

	old := map[string]api.Block{}
	oldOrder := []string{}
	for _, block := range from.Blocks {
		old[block.ID] = block
		oldOrder = append(oldOrder, block.ID)
	}
	present := map[string]bool{}
	for _, block := range to.Blocks {
		present[block.ID] = true
	}
	removedAfter := map[string][]api.Block{} // keyed by preceding surviving id ("" = start)
	previous := ""
	for _, id := range oldOrder {
		if present[id] {
			previous = id
			continue
		}
		removedAfter[previous] = append(removedAfter[previous], old[id])
	}
	flush := func(anchor string) {
		for _, block := range removedAfter[anchor] {
			result.Blocks = append(result.Blocks, BlockChange{BlockID: block.ID, Type: block.Type, Change: "removed", Before: blockText(block)})
			result.Summary.Removed++
		}
	}
	flush("")
	for _, block := range to.Blocks {
		before, existed := old[block.ID]
		change := BlockChange{BlockID: block.ID, Type: block.Type, After: blockText(block)}
		switch {
		case !existed:
			change.Change = "added"
			result.Summary.Added++
		case before.Type != block.Type || !bytes.Equal(compact(before.Data), compact(block.Data)):
			change.Change, change.Before = "changed", blockText(before)
			result.Summary.Changed++
		default:
			change.Change = "unchanged"
			result.Summary.Unchanged++
		}
		result.Blocks = append(result.Blocks, change)
		if slices.Contains(oldOrder, block.ID) {
			flush(block.ID)
		}
	}
	return result
}

func coverID(cover *api.PostCover) string {
	if cover == nil {
		return ""
	}
	return cover.MediaID
}

func compact(raw []byte) []byte {
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, raw); err != nil {
		return raw
	}
	return buffer.Bytes()
}

func blockText(block api.Block) string {
	text := TextOf([]api.Block{block})
	if block.Type == "image" {
		var data imageData
		if decodeData(block.Data, &data) == nil {
			text = "[image: " + data.Alt + "] " + text
		}
	}
	if block.Type == "delimiter" {
		text = "———"
	}
	if block.Type == "embed" {
		var data embedData
		if decodeData(block.Data, &data) == nil {
			text = "[embed] " + data.Source
		}
	}
	return strings.TrimSpace(text)
}

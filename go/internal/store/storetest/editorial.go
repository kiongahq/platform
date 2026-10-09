package storetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ml-ai-ops/platform/internal/editorial"
	"github.com/ml-ai-ops/platform/internal/store"
	"github.com/ml-ai-ops/platform/pkg/api"
)

// Editorial checks the editorial workspace's document usage on a backend:
// concurrent autosaves on one base revision produce exactly one revision,
// revisions are immutable, media documents attach, and two publishers racing
// on a due post publish it once.
func Editorial(t *testing.T, docs store.Documents) {
	t.Helper()
	now := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	svc := &editorial.Service{Docs: docs, Media: &editorial.FSStore{Dir: t.TempDir()}, Now: clock}
	if _, err := svc.Bootstrap(api.EditorialMember{Subject: "ada"}, "admin"); err != nil {
		t.Fatal(err)
	}
	admin, _ := svc.ActiveMember("ada")
	author, err := svc.PutMember(admin, api.EditorialMember{Subject: "ana", Role: api.EditorialAuthor, DisplayName: "Ana"})
	if err != nil {
		t.Fatal(err)
	}

	img := image.NewRGBA(image.Rect(0, 0, 700, 350))
	img.Set(3, 3, color.RGBA{200, 10, 10, 255})
	var buffer bytes.Buffer
	_ = jpeg.Encode(&buffer, img, nil)
	media, err := svc.Upload(context.Background(), author, buffer.Bytes(), "A chart", "", "")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := svc.MediaItem(media.ID)
	if err != nil || stored.Status != "pending" || len(stored.Variants) != 2 || stored.Uploader != "ana" {
		t.Fatalf("media doc %+v %v", stored, err)
	}

	text, _ := json.Marshal(map[string]string{"text": strings.Repeat("durable words ", 20)})
	picture, _ := json.Marshal(map[string]string{"media_id": media.ID, "alt": "A chart"})
	request := api.SavePostRequest{Title: "Postgres editorial", Summary: "Revisions and media on PostgreSQL documents.",
		Blocks: []api.Block{{ID: "p", Type: "paragraph", Data: text}, {ID: "i", Type: "image", Data: picture}}}
	post, err := svc.CreatePost(author, request)
	if err != nil {
		t.Fatal(err)
	}
	if attached, _ := svc.MediaItem(media.ID); attached.Status != "attached" || attached.PostIDs[0] != post.ID {
		t.Fatalf("media not attached: %+v", attached)
	}

	// Ten tabs autosave different edits on the same base revision.
	var wg sync.WaitGroup
	var saved, conflicts int
	var countMu sync.Mutex
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			edit := request
			edit.BaseRevision = 1
			edit.Title = "Postgres editorial " + strings.Repeat("!", i+1)
			_, _, err := svc.SavePost(author, post.ID, edit)
			var conflict editorial.ConflictError
			countMu.Lock()
			defer countMu.Unlock()
			switch {
			case err == nil:
				saved++
			case errors.As(err, &conflict):
				conflicts++
			default:
				t.Errorf("save: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if saved != 1 || conflicts != 9 {
		t.Fatalf("concurrent autosaves: %d saved, %d conflicts", saved, conflicts)
	}
	revisions := svc.Revisions(post.ID)
	if len(revisions) != 2 || revisions[0].Number != 2 || revisions[1].Title != "Postgres editorial" {
		t.Fatalf("revisions %+v", revisions)
	}
	restored, err := svc.Restore(author, post.ID, 1, 2)
	if err != nil || restored.Revision != 3 || restored.Title != "Postgres editorial" {
		t.Fatalf("restore %+v %v", restored, err)
	}
	if first, _ := svc.Revision(post.ID, 1); first.Reason != "create" || first.Title != "Postgres editorial" {
		t.Fatalf("revision 1 changed: %+v", first)
	}

	// Two replicas race to publish a due scheduled post: exactly one wins.
	editor, _ := svc.PutMember(admin, api.EditorialMember{Subject: "eve", Role: api.EditorialEditor})
	at := now.Add(time.Hour)
	if _, err := svc.Transition(editor, post.ID, api.TransitionRequest{Action: editorial.ActionSchedule, PublishAt: &at}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	now = at.Add(time.Minute)
	mu.Unlock()
	var fired int
	var fireMu sync.Mutex
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Simulated split brain: all replicas believe they hold the lease,
			// so only the post row lock prevents a double publish.
			publisher := &editorial.Publisher{Service: svc, Holder: "shared"}
			published, _ := publisher.Tick(context.Background())
			fireMu.Lock()
			fired += len(published)
			fireMu.Unlock()
		}()
	}
	wg.Wait()
	if fired != 1 {
		t.Fatalf("scheduled post published %d times", fired)
	}
	attached, _ := svc.MediaItem(media.ID)
	if final, _ := svc.Post(post.ID); final.Status != api.PostPublished || !svc.MediaPublic(attached) {
		t.Fatalf("final %s, media public %v", final.Status, svc.MediaPublic(attached))
	}
}

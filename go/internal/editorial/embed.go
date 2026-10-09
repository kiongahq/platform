package editorial

import (
	"net/url"
	"regexp"
	"strings"
)

// Embed is an allowlisted third-party embed rebuilt from its source URL. The
// iframe URL the client sends is never trusted; it is derived here.
type Embed struct {
	Service string
	Source  string
	// Frame is the iframe src; empty for services rendered as a link card.
	Frame string
}

var (
	youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	vimeoID   = regexp.MustCompile(`^[0-9]{1,12}$`)
	gistPath  = regexp.MustCompile(`^/([A-Za-z0-9-]{1,39})/([0-9a-f]{20,40})/?$`)
)

// ResolveEmbed validates an embed source against the allowlist (YouTube,
// Vimeo, GitHub gist).
func ResolveEmbed(service, source string) (Embed, bool) {
	parsed, err := url.Parse(strings.TrimSpace(source))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return Embed{}, false
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	switch host {
	case "youtube.com", "m.youtube.com", "youtu.be", "youtube-nocookie.com":
		var id string
		switch {
		case host == "youtu.be":
			id = strings.Trim(parsed.Path, "/")
		case parsed.Path == "/watch":
			id = parsed.Query().Get("v")
		case strings.HasPrefix(parsed.Path, "/embed/"), strings.HasPrefix(parsed.Path, "/shorts/"):
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(parts) == 2 {
				id = parts[1]
			}
		}
		if !youtubeID.MatchString(id) {
			return Embed{}, false
		}
		return Embed{Service: "youtube", Source: "https://www.youtube.com/watch?v=" + id, Frame: "https://www.youtube-nocookie.com/embed/" + id}, true
	case "vimeo.com", "player.vimeo.com":
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		id := parts[len(parts)-1]
		if !vimeoID.MatchString(id) {
			return Embed{}, false
		}
		return Embed{Service: "vimeo", Source: "https://vimeo.com/" + id, Frame: "https://player.vimeo.com/video/" + id}, true
	case "gist.github.com":
		match := gistPath.FindStringSubmatch(parsed.Path)
		if match == nil {
			return Embed{}, false
		}
		return Embed{Service: "github", Source: "https://gist.github.com/" + match[1] + "/" + match[2]}, true
	}
	_ = service
	return Embed{}, false
}

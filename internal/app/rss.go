package app

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const userRSSLimit = 50

type rssFeedMeta struct {
	Title       string
	Link        string
	Description string
	FeedURL     string
	Username    string
}

type rssDocument struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	AtomNS  string     `xml:"xmlns:atom,attr,omitempty"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string      `xml:"title"`
	Link          string      `xml:"link"`
	Description   string      `xml:"description"`
	Language      string      `xml:"language,omitempty"`
	LastBuildDate string      `xml:"lastBuildDate,omitempty"`
	AtomLink      rssAtomLink `xml:"atom:link"`
	Items         []rssItem   `xml:"item"`
}

type rssAtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        rssGUID  `xml:"guid"`
	PubDate     string   `xml:"pubDate,omitempty"`
	Description rssCDATA `xml:"description"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr,omitempty"`
	Value       string `xml:",chardata"`
}

type rssCDATA struct {
	Value string `xml:",cdata"`
}

func (app *App) handleUserRSS(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	profile, err := app.GetUserByUsername(username)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	posts, err := app.GetUserPosts(profile.ID, userRSSLimit, 0)
	if err != nil {
		http.Error(w, "Failed to load feed.", http.StatusInternalServerError)
		return
	}
	if err := app.HydratePosts(posts, 0); err != nil {
		http.Error(w, "Failed to load feed.", http.StatusInternalServerError)
		return
	}

	app.writeRSS(w, posts, rssFeedMeta{
		Title:       fmt.Sprintf("@%s on KarpathyTalk", profile.Username),
		Link:        app.absoluteURL(fmt.Sprintf("/user/%s", profile.Username)),
		Description: fmt.Sprintf("Recent posts by @%s on KarpathyTalk", profile.Username),
		FeedURL:     app.absoluteURL(fmt.Sprintf("/user/%s/feed.xml", profile.Username)),
		Username:    profile.Username,
	})
}

func (app *App) handleFollowingRSS(w http.ResponseWriter, r *http.Request) {
	user := GetCurrentUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	posts, err := app.GetFollowingTimelinePosts(user.ID, userRSSLimit, 0)
	if err != nil {
		http.Error(w, "Failed to load feed.", http.StatusInternalServerError)
		return
	}
	if err := app.HydratePosts(posts, user.ID); err != nil {
		http.Error(w, "Failed to load feed.", http.StatusInternalServerError)
		return
	}

	app.writeRSS(w, posts, rssFeedMeta{
		Title:       fmt.Sprintf("@%s following feed on KarpathyTalk", user.Username),
		Link:        app.absoluteURL("/?tab=following"),
		Description: fmt.Sprintf("Recent posts from @%s and followed users on KarpathyTalk", user.Username),
		FeedURL:     app.absoluteURL("/feed.xml?tab=following"),
		Username:    user.Username,
	})
}

func (app *App) writeRSS(w http.ResponseWriter, posts []*Post, meta rssFeedMeta) {
	items := make([]rssItem, 0, len(posts))
	lastBuildDate := time.Now()
	if len(posts) > 0 {
		lastBuildDate = posts[0].CreatedAt
	}
	for _, post := range posts {
		postURL := app.absoluteURL(fmt.Sprintf("/posts/%d", post.ID))
		items = append(items, rssItem{
			Title:       feedItemTitle(post.Content, meta.Username),
			Link:        postURL,
			GUID:        rssGUID{IsPermaLink: "true", Value: postURL},
			PubDate:     post.CreatedAt.Format(time.RFC1123Z),
			Description: rssCDATA{Value: post.ContentHTML},
		})

		if post.CreatedAt.After(lastBuildDate) {
			lastBuildDate = post.CreatedAt
		}
		if post.EditedAt != nil && post.EditedAt.After(lastBuildDate) {
			lastBuildDate = *post.EditedAt
		}
	}

	doc := rssDocument{
		Version: "2.0",
		AtomNS:  "http://www.w3.org/2005/Atom",
		Channel: rssChannel{
			Title:         meta.Title,
			Link:          meta.Link,
			Description:   meta.Description,
			Language:      "en-us",
			LastBuildDate: lastBuildDate.Format(time.RFC1123Z),
			AtomLink: rssAtomLink{
				Href: meta.FeedURL,
				Rel:  "self",
				Type: "application/rss+xml",
			},
			Items: items,
		},
	}

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		http.Error(w, "Failed to encode feed.", http.StatusInternalServerError)
		return
	}
}

func (app *App) absoluteURL(path string) string {
	baseURL := strings.TrimRight(app.BaseURL, "/")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	return baseURL + path
}

func feedItemTitle(content, username string) string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	for _, line := range strings.Split(content, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		return trimRunes(line, 80)
	}
	return fmt.Sprintf("Post by @%s", username)
}

func trimRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

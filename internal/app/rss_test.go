package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleUserRSS(t *testing.T) {
	app := newTestApp(t)
	app.BaseURL = "https://karpathytalk.com"

	author, err := app.UpsertUser(1, "author", "Author", "https://example.com/a.png")
	if err != nil {
		t.Fatalf("UpsertUser(author): %v", err)
	}

	postID, err := app.CreatePost(author.ID, "Top level post", markdownHTML(t, "Top level post"), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreatePost(top-level): %v", err)
	}
	if _, err := app.CreatePost(author.ID, "Reply post", markdownHTML(t, "Reply post"), &postID, nil, nil, nil); err != nil {
		t.Fatalf("CreatePost(reply): %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/user/author/feed.xml", nil)
	req.SetPathValue("username", "author")
	rr := httptest.NewRecorder()

	app.handleUserRSS(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/rss+xml") {
		t.Fatalf("Content-Type = %q, want RSS XML", got)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "<title>@author on KarpathyTalk</title>") {
		t.Fatalf("feed body missing channel title: %q", body)
	}
	if !strings.Contains(body, "<link>https://karpathytalk.com/user/author</link>") {
		t.Fatalf("feed body missing profile link: %q", body)
	}
	if !strings.Contains(body, `href="https://karpathytalk.com/user/author/feed.xml"`) {
		t.Fatalf("feed body missing self link: %q", body)
	}
	if strings.Count(body, "<item>") != 1 {
		t.Fatalf("item count = %d, want 1", strings.Count(body, "<item>"))
	}
	if !strings.Contains(body, "<title>Top level post</title>") {
		t.Fatalf("feed body missing item title: %q", body)
	}
	if !strings.Contains(body, fmt.Sprintf("<link>https://karpathytalk.com/posts/%d</link>", postID)) {
		t.Fatalf("feed body missing post link: %q", body)
	}
	if !strings.Contains(body, "<description><![CDATA[<p>Top level post</p>\n]]></description>") {
		t.Fatalf("feed body missing rendered HTML description: %q", body)
	}
}

func TestHandleFollowingRSS(t *testing.T) {
	app := newTestApp(t)
	app.BaseURL = "https://karpathytalk.com"
	app.Templates = LoadTemplates()

	me, err := app.UpsertUser(1, "me", "Me", "https://example.com/me.png")
	if err != nil {
		t.Fatalf("UpsertUser(me): %v", err)
	}
	followed, err := app.UpsertUser(2, "followed", "Followed", "https://example.com/followed.png")
	if err != nil {
		t.Fatalf("UpsertUser(followed): %v", err)
	}
	other, err := app.UpsertUser(3, "other", "Other", "https://example.com/other.png")
	if err != nil {
		t.Fatalf("UpsertUser(other): %v", err)
	}

	myPostID, err := app.CreatePost(me.ID, "my root", markdownHTML(t, "my root"), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreatePost(my root): %v", err)
	}
	followedPostID, err := app.CreatePost(followed.ID, "followed root", markdownHTML(t, "followed root"), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreatePost(followed root): %v", err)
	}
	if _, err := app.CreatePost(other.ID, "other root", markdownHTML(t, "other root"), nil, nil, nil, nil); err != nil {
		t.Fatalf("CreatePost(other root): %v", err)
	}
	if _, err := app.ToggleFollow(me.ID, followed.ID); err != nil {
		t.Fatalf("ToggleFollow: %v", err)
	}

	sessionToken, err := app.CreateSession(me.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/feed.xml?tab=following", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: sessionToken})
	rr := httptest.NewRecorder()

	app.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/rss+xml") {
		t.Fatalf("Content-Type = %q, want RSS XML", got)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "<title>@me following feed on KarpathyTalk</title>") {
		t.Fatalf("feed body missing channel title: %q", body)
	}
	if !strings.Contains(body, "<link>https://karpathytalk.com/?tab=following</link>") {
		t.Fatalf("feed body missing following timeline link: %q", body)
	}
	if !strings.Contains(body, `href="https://karpathytalk.com/feed.xml?tab=following"`) {
		t.Fatalf("feed body missing self link: %q", body)
	}
	if strings.Count(body, "<item>") != 2 {
		t.Fatalf("item count = %d, want 2", strings.Count(body, "<item>"))
	}
	if !strings.Contains(body, fmt.Sprintf("<link>https://karpathytalk.com/posts/%d</link>", myPostID)) {
		t.Fatalf("feed body missing own post link: %q", body)
	}
	if !strings.Contains(body, fmt.Sprintf("<link>https://karpathytalk.com/posts/%d</link>", followedPostID)) {
		t.Fatalf("feed body missing followed post link: %q", body)
	}
	if strings.Contains(body, "other root") {
		t.Fatalf("feed body should not include unfollowed post: %q", body)
	}
}

func TestHandleFollowingRSSRequiresAuth(t *testing.T) {
	app := newTestApp(t)
	app.Templates = LoadTemplates()

	req := httptest.NewRequest(http.MethodGet, "/feed.xml?tab=following", nil)
	rr := httptest.NewRecorder()

	app.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	if got := rr.Header().Get("Location"); got != "/login" {
		t.Fatalf("Location = %q, want /login", got)
	}
}

func TestFollowingTimelineShowsRSSLink(t *testing.T) {
	app := newTestApp(t)
	app.Templates = LoadTemplates()

	me, err := app.UpsertUser(1, "me", "Me", "https://example.com/me.png")
	if err != nil {
		t.Fatalf("UpsertUser(me): %v", err)
	}
	sessionToken, err := app.CreateSession(me.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/?tab=following", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: sessionToken})
	rr := httptest.NewRecorder()

	app.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `href="/feed.xml?tab=following"`) {
		t.Fatalf("following timeline missing RSS link: %q", body)
	}
}

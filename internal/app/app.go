package app

import (
	"database/sql"
	"html/template"
	"net/http"
)

type App struct {
	DB                 *sql.DB
	GitHubClientID     string
	GitHubClientSecret string
	BaseURL            string
	Templates          map[string]*template.Template
	RateLimiter        *IPRateLimiter
}

func New(db *sql.DB, githubClientID, githubClientSecret, baseURL string) *App {
	return &App{
		DB:                 db,
		GitHubClientID:     githubClientID,
		GitHubClientSecret: githubClientSecret,
		BaseURL:            baseURL,
		Templates:          LoadTemplates(),
		RateLimiter:        NewIPRateLimiter(),
	}
}

func (app *App) Handler() http.Handler {
	mux := http.NewServeMux()

	// Static files + uploads
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(projectPath("static")))))
	mux.Handle("GET /uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(projectPath("uploads")))))

	// Auth
	mux.HandleFunc("GET /login", app.handleLogin)
	mux.HandleFunc("GET /auth/github", app.handleGitHubAuth)
	mux.HandleFunc("GET /auth/callback", app.handleGitHubCallback)
	mux.HandleFunc("POST /auth/logout", app.handleLogout)

	// Timeline
	mux.HandleFunc("GET /", app.handleTimeline)
	mux.HandleFunc("GET /feed.xml", app.requireAuth(app.handleFollowingRSS))

	// Docs + read API
	mux.HandleFunc("GET /docs", app.handleDocs)
	mux.HandleFunc("GET /docs.md", app.handleDocsRaw)
	mux.HandleFunc("GET /user/{username}/md", app.handleUserMarkdown)
	mux.HandleFunc("GET /api/posts", app.handleAPIPosts)
	mux.HandleFunc("GET /api/users/{username}", app.handleAPIUser)
	mux.HandleFunc("GET /api/posts/{id}", app.handleAPIPost)

	// Posts
	mux.HandleFunc("GET /compose", app.requireAuth(app.handleCompose))
	mux.HandleFunc("POST /posts", app.requireAuth(app.handleCreatePost))
	mux.HandleFunc("GET /posts/{id}", app.handleViewPost)
	mux.HandleFunc("GET /posts/{id}/md", app.handlePostMarkdown)
	mux.HandleFunc("GET /posts/{id}/edit", app.requireAuth(app.handleEditPostForm))
	mux.HandleFunc("POST /posts/{id}/edit", app.requireAuth(app.handleEditPost))
	mux.HandleFunc("GET /posts/{id}/revisions", app.handlePostRevisions)
	mux.HandleFunc("GET /posts/{id}/revisions/{revision}", app.handleViewPostRevision)
	mux.HandleFunc("POST /posts/{id}/delete", app.requireAuth(app.handleDeletePost))
	mux.HandleFunc("GET /posts/{id}/raw", app.handlePostRaw)
	mux.HandleFunc("POST /posts/{id}/like", app.requireAuth(app.handleToggleLike))
	mux.HandleFunc("POST /posts/{id}/repost", app.requireAuth(app.handleToggleRepost))
	mux.HandleFunc("POST /posts/{id}/bookmark", app.requireAuth(app.handleToggleBookmark))
	mux.HandleFunc("GET /bookmarks", app.requireAuth(app.handleBookmarks))

	// Replies
	mux.HandleFunc("POST /posts/{id}/reply", app.requireAuth(app.handleCreateReply))

	// Upload + preview
	mux.HandleFunc("POST /upload", app.requireAuth(app.handleUpload))
	mux.HandleFunc("POST /preview", app.requireAuth(app.handlePreview))

	// Profile + follow
	mux.HandleFunc("GET /user/{username}/feed.xml", app.handleUserRSS)
	mux.HandleFunc("GET /user/{username}", app.handleProfile)
	mux.HandleFunc("POST /user/{username}/follow", app.requireAuth(app.handleToggleFollow))

	return app.withSecurityHeaders(app.withUser(app.withRateLimit(app.withCSRF(mux))))
}

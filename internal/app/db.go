package app

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var errPostUnchanged = errors.New("post unchanged")

const maxReplyTreeDepth = 50

type PostQuery struct {
	AuthorID     *int64
	HasParent    *bool
	ParentPostID *int64
	BeforeID     int64
	Limit        int
}

func InitDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite doesn't handle concurrent writes well

	schema, err := os.ReadFile(projectPath("schema.sql"))
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		return nil, fmt.Errorf("exec schema: %w", err)
	}

	return db, nil
}

// --- Users ---

func (app *App) UpsertUser(githubID int64, username, displayName, avatarURL string) (*User, error) {
	_, err := app.DB.Exec(`
		INSERT INTO users (github_id, username, display_name, avatar_url)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(github_id) DO UPDATE SET
			username = excluded.username,
			display_name = excluded.display_name,
			avatar_url = excluded.avatar_url
	`, githubID, username, displayName, avatarURL)
	if err != nil {
		return nil, err
	}
	return app.GetUserByGitHubID(githubID)
}

func (app *App) GetUserByGitHubID(githubID int64) (*User, error) {
	u := &User{}
	err := app.DB.QueryRow(`
		SELECT id, github_id, username, display_name, avatar_url, bio, created_at
		FROM users WHERE github_id = ?
	`, githubID).Scan(&u.ID, &u.GitHubID, &u.Username, &u.DisplayName, &u.AvatarURL, &u.Bio, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (app *App) GetUserByUsername(username string) (*User, error) {
	u := &User{}
	err := app.DB.QueryRow(`
		SELECT id, github_id, username, display_name, avatar_url, bio, created_at
		FROM users WHERE username = ?
	`, username).Scan(&u.ID, &u.GitHubID, &u.Username, &u.DisplayName, &u.AvatarURL, &u.Bio, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (app *App) GetUserStats(userID int64) (followers, following, posts int, err error) {
	err = app.DB.QueryRow(`SELECT COUNT(*) FROM follows WHERE following_id = ?`, userID).Scan(&followers)
	if err != nil {
		return
	}
	err = app.DB.QueryRow(`SELECT COUNT(*) FROM follows WHERE follower_id = ?`, userID).Scan(&following)
	if err != nil {
		return
	}
	err = app.DB.QueryRow(`SELECT COUNT(*) FROM posts WHERE author_id = ? AND parent_post_id IS NULL`, userID).Scan(&posts)
	return
}

// --- Sessions ---

func (app *App) CreateSession(userID int64) (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(bytes)

	_, err := app.DB.Exec(`
		INSERT INTO sessions (token, user_id, expires_at)
		VALUES (?, ?, ?)
	`, token, userID, time.Now().Add(30*24*time.Hour))
	if err != nil {
		return "", err
	}

	return token, nil
}

func (app *App) GetUserBySession(token string) (*User, error) {
	u := &User{}
	err := app.DB.QueryRow(`
		SELECT u.id, u.github_id, u.username, u.display_name, u.avatar_url, u.bio, u.created_at
		FROM sessions s
		JOIN users u ON s.user_id = u.id
		WHERE s.token = ? AND s.expires_at > ?
	`, token, time.Now()).Scan(&u.ID, &u.GitHubID, &u.Username, &u.DisplayName, &u.AvatarURL, &u.Bio, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (app *App) DeleteSession(token string) error {
	_, err := app.DB.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (app *App) CleanExpiredSessions() error {
	_, err := app.DB.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now())
	return err
}

// --- Posts ---

func (app *App) CreatePost(authorID int64, content, contentHTML string, parentPostID, parentPostRevisionID, quoteOfID, quoteOfRevisionID *int64) (int64, error) {
	tx, err := app.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		INSERT INTO posts (
			author_id, content, content_html, parent_post_id, parent_post_revision_id,
			quote_of_id, quote_of_revision_id, revision_count
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)
	`, authorID, content, contentHTML, parentPostID, parentPostRevisionID, quoteOfID, quoteOfRevisionID)
	if err != nil {
		return 0, err
	}
	postID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	res, err = tx.Exec(`
		INSERT INTO post_revisions (post_id, revision_number, content, content_html)
		VALUES (?, 1, ?, ?)
	`, postID, content, contentHTML)
	if err != nil {
		return 0, err
	}
	revisionID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(`
		UPDATE posts
		SET current_revision_id = ?, revision_count = 1
		WHERE id = ?
	`, revisionID, postID); err != nil {
		return 0, err
	}

	if parentPostID != nil {
		if _, err := tx.Exec(`
			UPDATE posts
			SET comment_count = comment_count + 1
			WHERE id = ?
		`, *parentPostID); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return postID, nil
}

func (app *App) GetPost(id int64) (*Post, error) {
	p := &Post{}
	var parentPostID sql.NullInt64
	var parentPostRevisionID sql.NullInt64
	var quoteOfID sql.NullInt64
	var quoteOfRevisionID sql.NullInt64
	var editedAt sql.NullTime
	err := app.DB.QueryRow(`
		SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
			   quote_of_id, quote_of_revision_id,
			   created_at, edited_at, like_count, repost_count, comment_count,
			   current_revision_id, revision_count
		FROM posts WHERE id = ?
	`, id).Scan(
		&p.ID, &p.AuthorID, &p.Content, &p.ContentHTML, &parentPostID, &parentPostRevisionID,
		&quoteOfID, &quoteOfRevisionID,
		&p.CreatedAt, &editedAt, &p.LikeCount, &p.RepostCount, &p.ReplyCount,
		&p.CurrentRevisionID, &p.RevisionCount,
	)
	if err != nil {
		return nil, err
	}
	if parentPostID.Valid {
		p.ParentPostID = &parentPostID.Int64
	}
	if parentPostRevisionID.Valid {
		p.ParentPostRevisionID = &parentPostRevisionID.Int64
	}
	if quoteOfID.Valid {
		p.QuoteOfID = &quoteOfID.Int64
	}
	if quoteOfRevisionID.Valid {
		p.QuoteOfRevisionID = &quoteOfRevisionID.Int64
	}
	if editedAt.Valid {
		p.EditedAt = &editedAt.Time
	}
	p.RevisionID = p.CurrentRevisionID
	p.RevisionNumber = p.RevisionCount
	p.RevisionCreatedAt = p.CreatedAt
	return p, nil
}

func (app *App) GetPostWithAuthor(id int64) (*Post, error) {
	p, err := app.GetPost(id)
	if err != nil {
		return nil, err
	}
	p.Author, err = app.getUserByID(p.AuthorID)
	if err != nil {
		return nil, err
	}
	if p.ParentPostID != nil {
		parentRevisionID := int64(0)
		if p.ParentPostRevisionID != nil {
			parentRevisionID = *p.ParentPostRevisionID
		}
		if parentRevisionID > 0 {
			p.ParentPost, _ = app.GetPostRevisionWithAuthor(*p.ParentPostID, parentRevisionID)
		} else {
			p.ParentPost, _ = app.GetPostWithAuthor(*p.ParentPostID)
		}
	}
	if p.QuoteOfID != nil && p.QuoteOfRevisionID != nil {
		p.QuotedPost, _ = app.GetPostRevisionWithAuthor(*p.QuoteOfID, *p.QuoteOfRevisionID) // ok if quoted post was deleted
	}
	return p, nil
}

func (app *App) GetPostRevisionWithAuthor(postID, revisionID int64) (*Post, error) {
	p, err := app.GetPostRevisionByID(postID, revisionID)
	if err != nil {
		return nil, err
	}
	p.Author, err = app.getUserByID(p.AuthorID)
	if err != nil {
		return nil, err
	}
	if p.ParentPostID != nil {
		parentRevisionID := int64(0)
		if p.ParentPostRevisionID != nil {
			parentRevisionID = *p.ParentPostRevisionID
		}
		if parentRevisionID > 0 {
			p.ParentPost, _ = app.GetPostRevisionWithAuthor(*p.ParentPostID, parentRevisionID)
		} else {
			p.ParentPost, _ = app.GetPostWithAuthor(*p.ParentPostID)
		}
	}
	if p.QuoteOfID != nil && p.QuoteOfRevisionID != nil {
		p.QuotedPost, _ = app.GetPostRevisionWithAuthor(*p.QuoteOfID, *p.QuoteOfRevisionID)
	}
	return p, nil
}

func (app *App) GetPostRevision(postID int64, revisionNumber int) (*Post, error) {
	p := &Post{}
	var parentPostID sql.NullInt64
	var parentPostRevisionID sql.NullInt64
	var quoteOfID sql.NullInt64
	var quoteOfRevisionID sql.NullInt64
	var editedAt sql.NullTime
	err := app.DB.QueryRow(`
		SELECT p.id, p.author_id, pr.content, pr.content_html, p.parent_post_id, p.parent_post_revision_id,
			   p.quote_of_id, p.quote_of_revision_id,
			   p.created_at, p.edited_at, p.like_count, p.repost_count, p.comment_count,
			   pr.id, pr.revision_number, pr.created_at, p.current_revision_id, p.revision_count
		FROM posts p
		JOIN post_revisions pr ON pr.post_id = p.id
		WHERE p.id = ? AND pr.revision_number = ?
	`, postID, revisionNumber).Scan(
		&p.ID, &p.AuthorID, &p.Content, &p.ContentHTML, &parentPostID, &parentPostRevisionID,
		&quoteOfID, &quoteOfRevisionID,
		&p.CreatedAt, &editedAt, &p.LikeCount, &p.RepostCount, &p.ReplyCount,
		&p.RevisionID, &p.RevisionNumber, &p.RevisionCreatedAt, &p.CurrentRevisionID, &p.RevisionCount,
	)
	if err != nil {
		return nil, err
	}
	if parentPostID.Valid {
		p.ParentPostID = &parentPostID.Int64
	}
	if parentPostRevisionID.Valid {
		p.ParentPostRevisionID = &parentPostRevisionID.Int64
	}
	if quoteOfID.Valid {
		p.QuoteOfID = &quoteOfID.Int64
	}
	if quoteOfRevisionID.Valid {
		p.QuoteOfRevisionID = &quoteOfRevisionID.Int64
	}
	if editedAt.Valid {
		p.EditedAt = &editedAt.Time
	}
	return p, nil
}

func (app *App) GetPostRevisionByID(postID, revisionID int64) (*Post, error) {
	p := &Post{}
	var parentPostID sql.NullInt64
	var parentPostRevisionID sql.NullInt64
	var quoteOfID sql.NullInt64
	var quoteOfRevisionID sql.NullInt64
	var editedAt sql.NullTime
	err := app.DB.QueryRow(`
		SELECT p.id, p.author_id, pr.content, pr.content_html, p.parent_post_id, p.parent_post_revision_id,
			   p.quote_of_id, p.quote_of_revision_id,
			   p.created_at, p.edited_at, p.like_count, p.repost_count, p.comment_count,
			   pr.id, pr.revision_number, pr.created_at, p.current_revision_id, p.revision_count
		FROM posts p
		JOIN post_revisions pr ON pr.post_id = p.id
		WHERE p.id = ? AND pr.id = ?
	`, postID, revisionID).Scan(
		&p.ID, &p.AuthorID, &p.Content, &p.ContentHTML, &parentPostID, &parentPostRevisionID,
		&quoteOfID, &quoteOfRevisionID,
		&p.CreatedAt, &editedAt, &p.LikeCount, &p.RepostCount, &p.ReplyCount,
		&p.RevisionID, &p.RevisionNumber, &p.RevisionCreatedAt, &p.CurrentRevisionID, &p.RevisionCount,
	)
	if err != nil {
		return nil, err
	}
	if parentPostID.Valid {
		p.ParentPostID = &parentPostID.Int64
	}
	if parentPostRevisionID.Valid {
		p.ParentPostRevisionID = &parentPostRevisionID.Int64
	}
	if quoteOfID.Valid {
		p.QuoteOfID = &quoteOfID.Int64
	}
	if quoteOfRevisionID.Valid {
		p.QuoteOfRevisionID = &quoteOfRevisionID.Int64
	}
	if editedAt.Valid {
		p.EditedAt = &editedAt.Time
	}
	return p, nil
}

func (app *App) GetPostRevisions(postID int64) ([]*PostRevision, error) {
	rows, err := app.DB.Query(`
		SELECT id, post_id, revision_number, content, content_html, created_at
		FROM post_revisions
		WHERE post_id = ?
		ORDER BY revision_number DESC
	`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var revisions []*PostRevision
	for rows.Next() {
		revision := &PostRevision{}
		if err := rows.Scan(
			&revision.ID, &revision.PostID, &revision.RevisionNumber,
			&revision.Content, &revision.ContentHTML, &revision.CreatedAt,
		); err != nil {
			return nil, err
		}
		revisions = append(revisions, revision)
	}
	return revisions, rows.Err()
}

func (app *App) EditPost(postID, authorID int64, content, contentHTML string) (*PostRevision, error) {
	tx, err := app.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var currentContent string
	var currentRevisionCount int
	err = tx.QueryRow(`
		SELECT content, revision_count
		FROM posts
		WHERE id = ? AND author_id = ?
	`, postID, authorID).Scan(&currentContent, &currentRevisionCount)
	if err != nil {
		return nil, err
	}
	if currentContent == content {
		return nil, errPostUnchanged
	}

	nextRevision := currentRevisionCount + 1
	res, err := tx.Exec(`
		INSERT INTO post_revisions (post_id, revision_number, content, content_html)
		VALUES (?, ?, ?, ?)
	`, postID, nextRevision, content, contentHTML)
	if err != nil {
		return nil, err
	}
	revisionID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	if _, err := tx.Exec(`
		UPDATE posts
		SET content = ?, content_html = ?, current_revision_id = ?, revision_count = ?, edited_at = ?
		WHERE id = ?
	`, content, contentHTML, revisionID, nextRevision, now, postID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &PostRevision{
		ID:             revisionID,
		PostID:         postID,
		RevisionNumber: nextRevision,
		Content:        content,
		ContentHTML:    contentHTML,
		CreatedAt:      now,
	}, nil
}

func (app *App) getUserByID(id int64) (*User, error) {
	u := &User{}
	err := app.DB.QueryRow(`
		SELECT id, github_id, username, display_name, avatar_url, bio, created_at
		FROM users WHERE id = ?
	`, id).Scan(&u.ID, &u.GitHubID, &u.Username, &u.DisplayName, &u.AvatarURL, &u.Bio, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (app *App) GetTimelinePosts(limit int, beforeID int64) ([]*Post, error) {
	var rows *sql.Rows
	var err error

	if beforeID > 0 {
		rows, err = app.DB.Query(`
			SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
				   quote_of_id, quote_of_revision_id,
				   created_at, edited_at, like_count, repost_count, comment_count,
				   current_revision_id, revision_count
			FROM posts WHERE parent_post_id IS NULL AND id < ?
			ORDER BY created_at DESC LIMIT ?
		`, beforeID, limit)
	} else {
		rows, err = app.DB.Query(`
			SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
				   quote_of_id, quote_of_revision_id,
				   created_at, edited_at, like_count, repost_count, comment_count,
				   current_revision_id, revision_count
			FROM posts
			WHERE parent_post_id IS NULL
			ORDER BY created_at DESC LIMIT ?
		`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return app.scanPosts(rows)
}

func (app *App) GetActivityItems(userID int64, limit int, before string) ([]*ActivityItem, string, error) {
	if limit <= 0 {
		limit = postsPerPage
	}

	query := `
		WITH events AS (
			SELECT 'reply' AS type, p.author_id AS actor_id, p.created_at AS created_at, p.id AS event_post_id, parent.id AS subject_post_id
			FROM posts p
			JOIN posts parent ON p.parent_post_id = parent.id
			WHERE parent.author_id = ? AND p.author_id != ?

			UNION ALL

			SELECT 'quote' AS type, p.author_id AS actor_id, p.created_at AS created_at, p.id AS event_post_id, quoted.id AS subject_post_id
			FROM posts p
			JOIN posts quoted ON p.quote_of_id = quoted.id
			WHERE quoted.author_id = ? AND p.author_id != ?

			UNION ALL

			SELECT 'repost' AS type, r.user_id AS actor_id, r.created_at AS created_at, NULL AS event_post_id, p.id AS subject_post_id
			FROM reposts r
			JOIN posts p ON r.post_id = p.id
			WHERE p.author_id = ? AND r.user_id != ?

			UNION ALL

			SELECT 'like' AS type, l.user_id AS actor_id, l.created_at AS created_at, NULL AS event_post_id, p.id AS subject_post_id
			FROM likes l
			JOIN posts p ON l.post_id = p.id
			WHERE p.author_id = ? AND l.user_id != ?

			UNION ALL

			SELECT 'follow' AS type, f.follower_id AS actor_id, f.created_at AS created_at, NULL AS event_post_id, NULL AS subject_post_id
			FROM follows f
			WHERE f.following_id = ? AND f.follower_id != ?
		)
		SELECT type, actor_id, created_at, event_post_id, subject_post_id
		FROM events
	`
	args := []any{userID, userID, userID, userID, userID, userID, userID, userID, userID, userID}
	if before != "" {
		query += " WHERE created_at < ?"
		args = append(args, before)
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := app.DB.Query(query, args...)
	if err != nil {
		return nil, "", err
	}

	var items []*ActivityItem
	actorIDs := map[int64]bool{}
	eventPostIDs := map[int64]bool{}
	subjectPostIDs := map[int64]bool{}
	for rows.Next() {
		item := &ActivityItem{}
		var eventPostID sql.NullInt64
		var subjectPostID sql.NullInt64
		if err := rows.Scan(&item.Type, &item.ActorID, &item.CreatedAt, &eventPostID, &subjectPostID); err != nil {
			rows.Close()
			return nil, "", err
		}
		actorIDs[item.ActorID] = true
		if eventPostID.Valid {
			item.EventPostID = &eventPostID.Int64
			eventPostIDs[eventPostID.Int64] = true
		}
		if subjectPostID.Valid {
			item.SubjectPostID = &subjectPostID.Int64
			subjectPostIDs[subjectPostID.Int64] = true
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", err
	}
	rows.Close()

	actorCache := map[int64]*User{}
	for actorID := range actorIDs {
		if actor, err := app.getUserByID(actorID); err == nil {
			actorCache[actorID] = actor
		}
	}

	postCache := map[int64]*Post{}
	for postID := range eventPostIDs {
		if post, err := app.GetPostWithAuthor(postID); err == nil {
			postCache[postID] = post
		}
	}
	for postID := range subjectPostIDs {
		if _, ok := postCache[postID]; ok {
			continue
		}
		if post, err := app.GetPostWithAuthor(postID); err == nil {
			postCache[postID] = post
		}
	}

	for _, item := range items {
		item.Actor = actorCache[item.ActorID]
		if item.EventPostID != nil {
			item.EventPost = postCache[*item.EventPostID]
		}
		if item.SubjectPostID != nil {
			item.SubjectPost = postCache[*item.SubjectPostID]
		}
	}

	var nextCursor string
	if len(items) > 0 {
		nextCursor = items[len(items)-1].CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return items, nextCursor, nil
}

func (app *App) GetFollowingTimelinePosts(userID int64, limit int, beforeID int64) ([]*Post, error) {
	var rows *sql.Rows
	var err error

	if beforeID > 0 {
		rows, err = app.DB.Query(`
			SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
				   quote_of_id, quote_of_revision_id,
				   created_at, edited_at, like_count, repost_count, comment_count,
				   current_revision_id, revision_count
			FROM posts
			WHERE parent_post_id IS NULL
			  AND id < ?
			  AND (
				author_id = ?
				OR author_id IN (
					SELECT following_id FROM follows WHERE follower_id = ?
				)
			  )
			ORDER BY created_at DESC, id DESC LIMIT ?
		`, beforeID, userID, userID, limit)
	} else {
		rows, err = app.DB.Query(`
			SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
				   quote_of_id, quote_of_revision_id,
				   created_at, edited_at, like_count, repost_count, comment_count,
				   current_revision_id, revision_count
			FROM posts
			WHERE parent_post_id IS NULL
			  AND (
				author_id = ?
				OR author_id IN (
					SELECT following_id FROM follows WHERE follower_id = ?
				)
			  )
			ORDER BY created_at DESC, id DESC LIMIT ?
		`, userID, userID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return app.scanPosts(rows)
}

func (app *App) GetPosts(query PostQuery) ([]*Post, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = postsPerPage
	}

	var conditions []string
	var args []any

	if query.AuthorID != nil {
		conditions = append(conditions, "author_id = ?")
		args = append(args, *query.AuthorID)
	}
	if query.ParentPostID != nil {
		conditions = append(conditions, "parent_post_id = ?")
		args = append(args, *query.ParentPostID)
	} else if query.HasParent != nil {
		if *query.HasParent {
			conditions = append(conditions, "parent_post_id IS NOT NULL")
		} else {
			conditions = append(conditions, "parent_post_id IS NULL")
		}
	}
	if query.BeforeID > 0 {
		conditions = append(conditions, "id < ?")
		args = append(args, query.BeforeID)
	}

	baseQuery := `
		SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
			   quote_of_id, quote_of_revision_id,
			   created_at, edited_at, like_count, repost_count, comment_count,
			   current_revision_id, revision_count
		FROM posts
	`
	if len(conditions) > 0 {
		baseQuery += " WHERE " + strings.Join(conditions, " AND ")
	}
	baseQuery += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := app.DB.Query(baseQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return app.scanPosts(rows)
}

func clampPostsQueryLimit(limitValue string, fallback int) (int, bool) {
	if limitValue == "" {
		return fallback, true
	}
	limit, err := strconv.Atoi(limitValue)
	if err != nil || limit < 1 {
		return 0, false
	}
	if limit > 100 {
		limit = 100
	}
	return limit, true
}

func (app *App) GetUserPosts(userID int64, limit int, beforeID int64) ([]*Post, error) {
	var rows *sql.Rows
	var err error

	if beforeID > 0 {
		rows, err = app.DB.Query(`
			SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
				   quote_of_id, quote_of_revision_id,
				   created_at, edited_at, like_count, repost_count, comment_count,
				   current_revision_id, revision_count
			FROM posts WHERE author_id = ? AND parent_post_id IS NULL AND id < ?
			ORDER BY created_at DESC LIMIT ?
		`, userID, beforeID, limit)
	} else {
		rows, err = app.DB.Query(`
			SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
				   quote_of_id, quote_of_revision_id,
				   created_at, edited_at, like_count, repost_count, comment_count,
				   current_revision_id, revision_count
			FROM posts WHERE author_id = ? AND parent_post_id IS NULL
			ORDER BY created_at DESC LIMIT ?
		`, userID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return app.scanPosts(rows)
}

func (app *App) GetUserReplies(userID int64, limit int, beforeID int64) ([]*Post, error) {
	var rows *sql.Rows
	var err error

	if beforeID > 0 {
		rows, err = app.DB.Query(`
			SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
				   quote_of_id, quote_of_revision_id,
				   created_at, edited_at, like_count, repost_count, comment_count,
				   current_revision_id, revision_count
			FROM posts WHERE author_id = ? AND parent_post_id IS NOT NULL AND id < ?
			ORDER BY created_at DESC LIMIT ?
		`, userID, beforeID, limit)
	} else {
		rows, err = app.DB.Query(`
			SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
				   quote_of_id, quote_of_revision_id,
				   created_at, edited_at, like_count, repost_count, comment_count,
				   current_revision_id, revision_count
			FROM posts WHERE author_id = ? AND parent_post_id IS NOT NULL
			ORDER BY created_at DESC LIMIT ?
		`, userID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return app.scanPosts(rows)
}

func (app *App) scanPosts(rows *sql.Rows) ([]*Post, error) {
	var posts []*Post
	for rows.Next() {
		p := &Post{}
		var parentPostID sql.NullInt64
		var parentPostRevisionID sql.NullInt64
		var quoteOfID sql.NullInt64
		var quoteOfRevisionID sql.NullInt64
		var editedAt sql.NullTime
		err := rows.Scan(
			&p.ID, &p.AuthorID, &p.Content, &p.ContentHTML, &parentPostID, &parentPostRevisionID,
			&quoteOfID, &quoteOfRevisionID,
			&p.CreatedAt, &editedAt, &p.LikeCount, &p.RepostCount, &p.ReplyCount,
			&p.CurrentRevisionID, &p.RevisionCount,
		)
		if err != nil {
			return nil, err
		}
		if parentPostID.Valid {
			p.ParentPostID = &parentPostID.Int64
		}
		if parentPostRevisionID.Valid {
			p.ParentPostRevisionID = &parentPostRevisionID.Int64
		}
		if quoteOfID.Valid {
			p.QuoteOfID = &quoteOfID.Int64
		}
		if quoteOfRevisionID.Valid {
			p.QuoteOfRevisionID = &quoteOfRevisionID.Int64
		}
		if editedAt.Valid {
			p.EditedAt = &editedAt.Time
		}
		p.RevisionID = p.CurrentRevisionID
		p.RevisionNumber = p.RevisionCount
		p.RevisionCreatedAt = p.CreatedAt
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

// HydratePosts fills in Author, QuotedPost, and current user state for a list of posts.
func (app *App) HydratePosts(posts []*Post, currentUserID int64) error {
	if len(posts) == 0 {
		return nil
	}

	// Collect unique author IDs and post IDs
	authorIDs := map[int64]bool{}
	postIDs := make([]int64, len(posts))
	for i, p := range posts {
		authorIDs[p.AuthorID] = true
		postIDs[i] = p.ID
	}

	// Batch load authors
	userCache := map[int64]*User{}
	for id := range authorIDs {
		u, err := app.getUserByID(id)
		if err != nil {
			return err
		}
		userCache[id] = u
	}

	// Load current user's likes, reposts, and bookmarks
	likedSet := map[int64]bool{}
	repostedSet := map[int64]bool{}
	bookmarkedSet := map[int64]bool{}
	if currentUserID > 0 {
		placeholders := make([]string, len(postIDs))
		args := make([]interface{}, len(postIDs)+1)
		args[0] = currentUserID
		for i, id := range postIDs {
			placeholders[i] = "?"
			args[i+1] = id
		}
		ph := strings.Join(placeholders, ",")

		rows, err := app.DB.Query(
			`SELECT post_id FROM likes WHERE user_id = ? AND post_id IN (`+ph+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var pid int64
			rows.Scan(&pid)
			likedSet[pid] = true
		}
		rows.Close()

		rows, err = app.DB.Query(
			`SELECT post_id FROM reposts WHERE user_id = ? AND post_id IN (`+ph+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var pid int64
			rows.Scan(&pid)
			repostedSet[pid] = true
		}
		rows.Close()

		rows, err = app.DB.Query(
			`SELECT post_id FROM bookmarks WHERE user_id = ? AND post_id IN (`+ph+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var pid int64
			rows.Scan(&pid)
			bookmarkedSet[pid] = true
		}
		rows.Close()
	}

	// Assign to posts
	for _, p := range posts {
		p.Author = userCache[p.AuthorID]
		p.IsLiked = likedSet[p.ID]
		p.IsReposted = repostedSet[p.ID]
		p.IsBookmarked = bookmarkedSet[p.ID]

		if p.ParentPostID != nil {
			if p.ParentPostRevisionID != nil {
				p.ParentPost, _ = app.GetPostRevisionWithAuthor(*p.ParentPostID, *p.ParentPostRevisionID)
			} else {
				p.ParentPost, _ = app.GetPostWithAuthor(*p.ParentPostID)
			}
		}
		if p.QuoteOfID != nil && p.QuoteOfRevisionID != nil {
			p.QuotedPost, _ = app.GetPostRevisionWithAuthor(*p.QuoteOfID, *p.QuoteOfRevisionID)
		}
	}

	return nil
}

func (app *App) DeletePost(postID, authorID int64) error {
	tx, err := app.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var parentPostID sql.NullInt64
	if err := tx.QueryRow(`SELECT parent_post_id FROM posts WHERE id = ? AND author_id = ?`, postID, authorID).Scan(&parentPostID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("post not found or not owned by user")
		}
		return err
	}

	res, err := tx.Exec(`DELETE FROM posts WHERE id = ? AND author_id = ?`, postID, authorID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("post not found or not owned by user")
	}
	if parentPostID.Valid {
		if _, err := tx.Exec(`UPDATE posts SET comment_count = MAX(0, comment_count - 1) WHERE id = ?`, parentPostID.Int64); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (app *App) GetReplies(postID int64) ([]*Post, error) {
	rows, err := app.DB.Query(`
		WITH RECURSIVE reply_tree AS (
			SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
				   quote_of_id, quote_of_revision_id, created_at, edited_at,
				   like_count, repost_count, comment_count, current_revision_id, revision_count, 0 AS depth
			FROM posts
			WHERE parent_post_id = ?
			UNION ALL
			SELECT p.id, p.author_id, p.content, p.content_html, p.parent_post_id, p.parent_post_revision_id,
				   p.quote_of_id, p.quote_of_revision_id, p.created_at, p.edited_at,
				   p.like_count, p.repost_count, p.comment_count, p.current_revision_id, p.revision_count, rt.depth + 1
			FROM posts p
			JOIN reply_tree rt ON p.parent_post_id = rt.id
			WHERE rt.depth < ?
		)
		SELECT id, author_id, content, content_html, parent_post_id, parent_post_revision_id,
			   quote_of_id, quote_of_revision_id, created_at, edited_at,
			   like_count, repost_count, comment_count, current_revision_id, revision_count, depth
		FROM reply_tree
		ORDER BY created_at ASC, id ASC
	`, postID, maxReplyTreeDepth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var replies []*Post
	for rows.Next() {
		reply := &Post{}
		var parentPostID sql.NullInt64
		var parentPostRevisionID sql.NullInt64
		var quoteOfID sql.NullInt64
		var quoteOfRevisionID sql.NullInt64
		var editedAt sql.NullTime
		if err := rows.Scan(
			&reply.ID, &reply.AuthorID, &reply.Content, &reply.ContentHTML, &parentPostID, &parentPostRevisionID,
			&quoteOfID, &quoteOfRevisionID, &reply.CreatedAt, &editedAt,
			&reply.LikeCount, &reply.RepostCount, &reply.ReplyCount, &reply.CurrentRevisionID, &reply.RevisionCount, &reply.Depth,
		); err != nil {
			return nil, err
		}
		if parentPostID.Valid {
			reply.ParentPostID = &parentPostID.Int64
		}
		if parentPostRevisionID.Valid {
			reply.ParentPostRevisionID = &parentPostRevisionID.Int64
		}
		if quoteOfID.Valid {
			reply.QuoteOfID = &quoteOfID.Int64
		}
		if quoteOfRevisionID.Valid {
			reply.QuoteOfRevisionID = &quoteOfRevisionID.Int64
		}
		if editedAt.Valid {
			reply.EditedAt = &editedAt.Time
		}
		reply.RevisionID = reply.CurrentRevisionID
		reply.RevisionNumber = reply.RevisionCount
		reply.RevisionCreatedAt = reply.CreatedAt
		replies = append(replies, reply)
	}
	return replies, rows.Err()
}

// --- Likes ---

func (app *App) ToggleLike(userID, postID int64) (liked bool, err error) {
	tx, err := app.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var count int
	err = tx.QueryRow(`SELECT COUNT(*) FROM likes WHERE user_id = ? AND post_id = ?`,
		userID, postID).Scan(&count)
	if err != nil {
		return false, err
	}

	if count > 0 {
		if _, err = tx.Exec(`DELETE FROM likes WHERE user_id = ? AND post_id = ?`, userID, postID); err != nil {
			return false, err
		}
		if _, err = tx.Exec(`UPDATE posts SET like_count = MAX(0, like_count - 1) WHERE id = ?`, postID); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}

	if _, err = tx.Exec(`INSERT INTO likes (user_id, post_id) VALUES (?, ?)`, userID, postID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE posts SET like_count = like_count + 1 WHERE id = ?`, postID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// --- Reposts ---

func (app *App) ToggleRepost(userID, postID, postRevisionID int64) (reposted bool, err error) {
	tx, err := app.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var count int
	err = tx.QueryRow(`SELECT COUNT(*) FROM reposts WHERE user_id = ? AND post_id = ?`,
		userID, postID).Scan(&count)
	if err != nil {
		return false, err
	}

	if count > 0 {
		if _, err = tx.Exec(`DELETE FROM reposts WHERE user_id = ? AND post_id = ?`, userID, postID); err != nil {
			return false, err
		}
		if _, err = tx.Exec(`UPDATE posts SET repost_count = MAX(0, repost_count - 1) WHERE id = ?`, postID); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}

	if _, err = tx.Exec(`INSERT INTO reposts (user_id, post_id, post_revision_id) VALUES (?, ?, ?)`,
		userID, postID, postRevisionID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE posts SET repost_count = repost_count + 1 WHERE id = ?`, postID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// --- Bookmarks ---

func (app *App) ToggleBookmark(userID, postID int64) (bookmarked bool, err error) {
	tx, err := app.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var count int
	err = tx.QueryRow(`SELECT COUNT(*) FROM bookmarks WHERE user_id = ? AND post_id = ?`,
		userID, postID).Scan(&count)
	if err != nil {
		return false, err
	}

	if count > 0 {
		if _, err = tx.Exec(`DELETE FROM bookmarks WHERE user_id = ? AND post_id = ?`, userID, postID); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}

	if _, err = tx.Exec(`INSERT INTO bookmarks (user_id, post_id) VALUES (?, ?)`, userID, postID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// GetBookmarkedPosts returns bookmarks ordered by bookmark time.
// Cursor format: "created_at,post_id" for stable keyset pagination.
func (app *App) GetBookmarkedPosts(userID int64, limit int, beforeCursor string) ([]*Post, string, error) {
	var rows *sql.Rows
	var err error

	if beforeCursor != "" {
		parts := strings.SplitN(beforeCursor, ",", 2)
		if len(parts) != 2 {
			return nil, "", fmt.Errorf("invalid cursor")
		}
		cursorTime, cursorPostID := parts[0], parts[1]
		rows, err = app.DB.Query(`
			SELECT p.id, p.author_id, p.content, p.content_html, p.parent_post_id, p.parent_post_revision_id,
				   p.quote_of_id, p.quote_of_revision_id,
				   p.created_at, p.edited_at, p.like_count, p.repost_count, p.comment_count,
				   p.current_revision_id, p.revision_count, b.created_at, b.post_id
			FROM bookmarks b
			JOIN posts p ON b.post_id = p.id
			WHERE b.user_id = ?
			  AND (b.created_at < ? OR (b.created_at = ? AND b.post_id < ?))
			ORDER BY b.created_at DESC, b.post_id DESC LIMIT ?
		`, userID, cursorTime, cursorTime, cursorPostID, limit)
	} else {
		rows, err = app.DB.Query(`
			SELECT p.id, p.author_id, p.content, p.content_html, p.parent_post_id, p.parent_post_revision_id,
				   p.quote_of_id, p.quote_of_revision_id,
				   p.created_at, p.edited_at, p.like_count, p.repost_count, p.comment_count,
				   p.current_revision_id, p.revision_count, b.created_at, b.post_id
			FROM bookmarks b
			JOIN posts p ON b.post_id = p.id
			WHERE b.user_id = ?
			ORDER BY b.created_at DESC, b.post_id DESC LIMIT ?
		`, userID, limit)
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	type bookmarkRow struct {
		post         *Post
		bookmarkTime string
		bookmarkPID  int64
	}
	var results []bookmarkRow
	for rows.Next() {
		p := &Post{}
		var parentPostID sql.NullInt64
		var parentPostRevisionID sql.NullInt64
		var quoteOfID sql.NullInt64
		var quoteOfRevisionID sql.NullInt64
		var editedAt sql.NullTime
		var bTime string
		var bPID int64
		err := rows.Scan(
			&p.ID, &p.AuthorID, &p.Content, &p.ContentHTML, &parentPostID, &parentPostRevisionID,
			&quoteOfID, &quoteOfRevisionID,
			&p.CreatedAt, &editedAt, &p.LikeCount, &p.RepostCount, &p.ReplyCount,
			&p.CurrentRevisionID, &p.RevisionCount, &bTime, &bPID,
		)
		if err != nil {
			return nil, "", err
		}
		if parentPostID.Valid {
			p.ParentPostID = &parentPostID.Int64
		}
		if parentPostRevisionID.Valid {
			p.ParentPostRevisionID = &parentPostRevisionID.Int64
		}
		if quoteOfID.Valid {
			p.QuoteOfID = &quoteOfID.Int64
		}
		if quoteOfRevisionID.Valid {
			p.QuoteOfRevisionID = &quoteOfRevisionID.Int64
		}
		if editedAt.Valid {
			p.EditedAt = &editedAt.Time
		}
		p.RevisionID = p.CurrentRevisionID
		p.RevisionNumber = p.RevisionCount
		p.RevisionCreatedAt = p.CreatedAt
		results = append(results, bookmarkRow{post: p, bookmarkTime: bTime, bookmarkPID: bPID})
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	posts := make([]*Post, len(results))
	for i, r := range results {
		posts[i] = r.post
	}

	// Cursor comes from the last row the caller will actually display (index limit-2),
	// not from the overflow sentinel row.
	var nextCursor string
	if len(results) >= limit && limit > 1 {
		last := results[limit-2] // last visible row
		nextCursor = fmt.Sprintf("%s,%d", last.bookmarkTime, last.bookmarkPID)
	}
	return posts, nextCursor, nil
}

// --- Follows ---

func (app *App) ToggleFollow(followerID, followingID int64) (following bool, err error) {
	if followerID == followingID {
		return false, fmt.Errorf("cannot follow yourself")
	}

	tx, err := app.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var count int
	err = tx.QueryRow(`SELECT COUNT(*) FROM follows WHERE follower_id = ? AND following_id = ?`,
		followerID, followingID).Scan(&count)
	if err != nil {
		return false, err
	}

	if count > 0 {
		if _, err = tx.Exec(`DELETE FROM follows WHERE follower_id = ? AND following_id = ?`,
			followerID, followingID); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}

	if _, err = tx.Exec(`INSERT INTO follows (follower_id, following_id) VALUES (?, ?)`,
		followerID, followingID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (app *App) IsFollowing(followerID, followingID int64) bool {
	var count int
	app.DB.QueryRow(`SELECT COUNT(*) FROM follows WHERE follower_id = ? AND following_id = ?`,
		followerID, followingID).Scan(&count)
	return count > 0
}

func (app *App) GetRepostRevisionID(userID, postID int64) (int64, error) {
	var revisionID sql.NullInt64
	err := app.DB.QueryRow(`
		SELECT post_revision_id
		FROM reposts
		WHERE user_id = ? AND post_id = ?
	`, userID, postID).Scan(&revisionID)
	if err != nil {
		return 0, err
	}
	if !revisionID.Valid {
		return 0, sql.ErrNoRows
	}
	return revisionID.Int64, nil
}

// --- Rate Limiting Queries ---

func (app *App) CountRecentPosts(userID int64, since time.Time) (int, error) {
	var count int
	err := app.DB.QueryRow(`SELECT COUNT(*) FROM posts WHERE author_id = ? AND parent_post_id IS NULL AND created_at > ?`,
		userID, since).Scan(&count)
	return count, err
}

func (app *App) CountRecentReplies(userID int64, since time.Time) (int, error) {
	var count int
	err := app.DB.QueryRow(`SELECT COUNT(*) FROM posts WHERE author_id = ? AND parent_post_id IS NOT NULL AND created_at > ?`,
		userID, since).Scan(&count)
	return count, err
}

// --- Uploads ---

func (app *App) RecordUpload(userID int64, filename string, sizeBytes int64) error {
	_, err := app.DB.Exec(`INSERT INTO uploads (user_id, filename, size_bytes) VALUES (?, ?, ?)`,
		userID, filename, sizeBytes)
	return err
}

func (app *App) CountRecentUploads(userID int64, since time.Time) (int, error) {
	var count int
	err := app.DB.QueryRow(`SELECT COUNT(*) FROM uploads WHERE user_id = ? AND created_at > ?`,
		userID, since).Scan(&count)
	return count, err
}

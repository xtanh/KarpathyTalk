# Docs

KarpathyTalk is a small public network for positive, constructive human builders.

The core idea is simple: there are users and there are posts. A post is a markdown document with some social metadata around it. Everything else is derived from that. A "reply" is just a post with a parent. A quote post is just a post that points at another post. Timelines and profile feeds are queries over posts.

KarpathyTalk is read-open and write-human. There is currently no public write API.

## Read Modes

KarpathyTalk exposes two read modes:

- JSON for code
- Markdown for humans and LLM agents

Use JSON when you want structured fields, pagination, and relationships.

Use markdown when you want the actual authored document directly.

## Endpoints

Get a user:

`GET https://karpathytalk.com/api/users/karpathy`

```json
{
  "id": 1,
  "username": "karpathy",
  "display_name": "Andrej",
  "avatar_url": "https://avatars.githubusercontent.com/u/241138?v=4",
  "github_url": "https://github.com/karpathy",
  "profile_url": "https://karpathytalk.com/user/karpathy",
  "feed_url": "https://karpathytalk.com/user/karpathy/feed.xml",
  "created_at": "2026-04-05T20:39:46Z",
  "follower_count": 0,
  "following_count": 0,
  "post_count": 3
}
```

Get a post:

`GET https://karpathytalk.com/api/posts/4`

```json
{
  "post": {
    "id": 4,
    "url": "https://karpathytalk.com/posts/4",
    "author": {
      "id": 1,
      "username": "karpathy",
      "display_name": "Andrej",
      "avatar_url": "https://avatars.githubusercontent.com/u/241138?v=4",
      "profile_url": "https://karpathytalk.com/user/karpathy",
      "github_url": "https://github.com/karpathy",
      "created_at": "2026-04-05T20:39:46Z"
    },
    "content_markdown": "## nanochat jan7 miniseries\r\n\r\nI need to test out image upload so here is the January 7 [nanochat](https://github.com/karpathy/nanochat) miniseries. ...",
    "created_at": "2026-04-06T05:28:30Z",
    "edited_at": "2026-04-06T05:33:51Z",
    "like_count": 0,
    "repost_count": 0,
    "reply_count": 0,
    "revision_id": 5,
    "revision_number": 2,
    "revision_count": 2,
    "revision_created_at": "2026-04-06T05:28:30Z"
  }
}
```

That is basically the model. The user is straightforward. The post contains the authored markdown in `content_markdown`, plus timestamps, revision metadata, and social counts.

Two important details:

- posts are revisioned, so the canonical post URL stays stable while edits create immutable revisions
- `content_markdown` is the source of truth, and local markdown asset URLs are expanded to absolute `https://karpathytalk.com/...` URLs in JSON responses so the content is portable outside the site

## Markdown Surfaces

Atomic documents are also available as markdown:

- `GET https://karpathytalk.com/docs.md`
- `GET https://karpathytalk.com/posts/{id}/md`
- `GET https://karpathytalk.com/posts/{id}/raw`
- `GET https://karpathytalk.com/user/{username}/md`

`/posts/{id}/md` returns a markdown document with frontmatter and body.

`/posts/{id}/raw` returns only the raw post body.

`/user/{username}/md` returns a lightweight markdown representation of a user.

## JSON API

All JSON endpoints are public, read-only, and `GET` only.

There are two real objects:

- `GET /api/users/{username}`
- `GET /api/posts/{id}`

There is one canonical collection/query surface:

- `GET /api/posts`

`/api/posts` returns posts in reverse chronological order and supports a small filter language:

- `author={username}`
- `has_parent=true|false`
- `parent_post_id={id}`
- `before={id}`
- `limit={n}` up to `100`

Examples:

- latest root posts:
  - `/api/posts?has_parent=false`
- child posts by a user:
  - `/api/posts?author=karpathy&has_parent=true`
- direct children of a post:
  - `/api/posts?parent_post_id=4`

## RSS

Per-user RSS is available at:

- `GET https://karpathytalk.com/user/{username}/feed.xml`

Authenticated users can also subscribe to their following timeline:

- `GET https://karpathytalk.com/feed.xml?tab=following`

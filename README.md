# loc2api

A small Go client library for the public list endpoints of [lookonchain.com](https://lookonchain.com): the news flash feeds shown on `/feeds` and the articles listed on `/articles`.

Both endpoints are undocumented; they were located by reverse-engineering the site's AJAX calls. They may change or disappear without notice.

## Install

```sh
go get github.com/fudcourt-caps/loc2api
```

Replace `<user>` with the repository's GitHub owner (update `go.mod` accordingly before tagging a release).

For local development against a checkout, add a `replace` directive to your own `go.mod`:

```go
require github.com/fudcourt-caps/loc2api v0.1.0

replace github.com/fudcourt-caps/loc2api => ../loc2api
```

## Quick start

Feeds (news flash items):

```go
client := loc2api.New()

feeds, err := client.FetchFeeds(ctx, loc2api.Query{Pages: 2, Count: 20})
if err != nil {
    return err
}
for _, f := range feeds {
    fmt.Println(f.CreatedAt, f.Title)
}
```

Articles:

```go
client := loc2api.New()

articles, err := client.FetchArticles(ctx, loc2api.Query{
    Pages:   2,
    Count:   20,
    ProType: loc2api.CategoryXDailyPicks, // or CategorySmartMoney, CategoryAll
})
if err != nil {
    return err
}
for _, a := range articles {
    fmt.Println(a.CreatedAt, a.Category, a.Title)
}
```

## API

Construct with `New` and configure with options:

```go
loc2api.New(
    loc2api.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}),
    loc2api.WithBaseURL("https://lookonchain.com"),
    loc2api.WithRetry(3, time.Second),
)
```

Methods on `*Client`:

| Method | Description |
| --- | --- |
| `FetchFeeds(ctx, Query)` | Feeds from `/ashx/index.ashx`, newest first |
| `FetchArticles(ctx, Query)` | Articles from `/ashx/article_index.ashx`, newest first |
| `Fetch(ctx, Query)` | Alias of `FetchFeeds` |
| `FetchLatestFeeds(ctx, count)` | One page of feeds, up to now |
| `FetchLatestArticles(ctx, count)` | One page of articles, up to now |
| `FetchSince(ctx, since, pages)` | Feeds newer than `since`; paginates until an item at or before the cutoff is seen |

`Query` fields:

| Field | Type | Notes |
| --- | --- | --- |
| `Pages` | `int` | Page cap; defaults to `DefaultPages` (2) when zero |
| `Count` | `int` | Items per page; defaults to `DefaultCount` (20) when zero |
| `MaxTime` | `time.Time` | Upper bound of the time window; zero means now |
| `ProType` | `string` | Category filter; see quirks below |

Constants: `CategoryAll`, `CategoryXDailyPicks`, `CategorySmartMoney`, `DefaultPages`, `DefaultCount`, `TimeLayout` (`"2006-01-02 15:04:05"`, the format the API expects for and returns timestamps).

`Feed.CreatedAtTime()` and `Article.CreatedAtTime()` parse `CreatedAt` using `TimeLayout` in the local time zone.

### Feed fields

| Field | JSON | Type | Notes |
| --- | --- | --- | --- |
| `ID` | `id` | `string` | Server-side flash ID; pagination dedup key |
| `Title` | `title` | `string` | |
| `Abstract` | `abstract` | `string` | |
| `Text` | `text` | `string` | `scontent` with HTML stripped and entities unescaped |
| `CreatedAt` | `created_at` | `string` | `dcreate_time`, in `TimeLayout` |
| `Source` | `source` | `string` | Usually empty; the upstream outlet is in `SourceURL` |
| `SourceURL` | `source_url` | `string` | Original article/flash link |
| `IsHot` | `is_hot` | `bool` | |
| `HasImage` | `has_image` | `bool` | |
| `Times` | `times` | `string` | Server-side relative label, e.g. `"19 minutes ago"` |
| `URL` | `url` | `string` | Canonical lookonchain page |

### Article fields

| Field | JSON | Type | Notes |
| --- | --- | --- | --- |
| `ID` | `id` | `string` | Server-side article ID; pagination dedup key |
| `Title` | `title` | `string` | |
| `Abstract` | `abstract` | `string` | |
| `Text` | `text` | `string` | `scontent` with HTML stripped and entities unescaped |
| `Labels` | `labels` | `[]string` | Comma-separated on the wire |
| `CreatedAt` | `created_at` | `string` | `dcreate_time`, in `TimeLayout` |
| `Author` | `author` | `string` | |
| `Category` | `category` | `string` | e.g. `"X Daily Picks"` |
| `CoverURL` | `cover_url` | `string` | |
| `SourceURL` | `source_url` | `string` | Original post link |
| `Featured` | `featured` | `bool` | `is_jingxuan` |
| `HasImage` | `has_image` | `bool` | |
| `Views` | `views` | `int` | |
| `Collects` | `collects` | `int` | |
| `Times` | `times` | `string` | Server-side date label, e.g. `"2026.03.09"` |
| `URL` | `url` | `string` | Canonical lookonchain page |

## CLI

The `cmd/loc2api-cli` binary fetches both lists.

Flags:

| Flag | Default | Description |
| --- | --- | --- |
| `-pages` | `DefaultPages` (2) | Max pages to fetch |
| `-count` | `DefaultCount` (20) | Items per page |
| `-protype` | `""` | Topic filter (feeds only, empty = All); with `-articles`, a raw category ID that overrides `-category` |
| `-maxtime` | now | Upper time bound, `YYYY-MM-DD HH:MM:SS` |
| `-json` | `false` | Emit JSON instead of text |
| `-since` | `""` | Only items newer than this time, `YYYY-MM-DD HH:MM:SS` |
| `-articles` | `false` | Fetch articles instead of feeds |
| `-category` | `all` | Article category (`-articles` only): `all`, `x-daily-picks`, `smart-money`, or a raw numeric category ID |

Run it from the module root:

```sh
go run ./cmd/loc2api-cli
go run ./cmd/loc2api-cli -pages 3 -count 50
go run ./cmd/loc2api-cli -articles -category smart-money -json
go run ./cmd/loc2api-cli -articles -category 1016 -count 10
go run ./cmd/loc2api-cli -since "2026-10-19 00:00:00"
go run ./cmd/loc2api-cli -maxtime "2026-10-01 00:00:00" -pages 5
```

## API quirks and limits

These come from the upstream service; the library works around some of them but cannot fix them.

- Both endpoints return a JSON envelope `{"success":"Y","msg":"...","content":[...]}`.
- The server emits **invalid JSON escapes**, such as `\'` inside strings (e.g. `Meta Platforms\' target price`). The library repairs these escapes before decoding.
- `protype` for feeds only accepts `""` (All). Any non-empty value returns a server 500 HTML page, not JSON.
- `protype` for articles is a category ID: `""` (all), `"1016"` (X Daily Picks), `"1015"` (Lookonchain SmartMoney).
- Pagination stops when a page comes back short (fewer items than requested) or when a page adds no new IDs. There is no explicit "total" or page-count field.
- There is no lower-bound time parameter. Time filtering such as `-since` is done client-side, after fetching.
- These are undocumented endpoints of a third-party site; the response shape, field names, and category IDs may change at any time without notice. There is no rate-limit or authentication contract either.

## Testing

```sh
go test ./...
go test -short ./...
```

Tests use `httptest` servers for the request, pagination, retry, and decoding logic. The live smoke tests (`TestFetchLive` for feeds, `TestFetchArticlesLive` for articles) hit the real API and are skipped under `-short`.

## License

MIT. See [LICENSE](LICENSE).

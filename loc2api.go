// Package loc2api fetches content from lookonchain.com: news flash feeds
// (/feeds) and articles (/articles).
//
// Both lists load via POST to their own .ashx endpoint (see
// /js/yh.scroll.store.js, which injects page/count into the request params):
//
//	/ashx/index.ashx          news flash feeds
//	/ashx/article_index.ashx  articles
//
// max_time is the upper bound of the window; protype filters by topic for feeds
// (only the empty value, "All", is usable — non-empty values return a
// server-side 500 HTML page) or by category ID for articles.
//
// The server's JSON is not strictly valid: string values contain invalid escapes
// such as \' (e.g. "Meta Platforms\' target"). Responses are parsed with a
// tolerant reader that repairs those escapes before decoding.
//
// Typical use:
//
//	client := loc2api.New()
//
//	feeds, err := client.FetchFeeds(ctx, loc2api.Query{Pages: 3, Count: 20})
//	// ...
//	articles, err := client.FetchArticles(ctx, loc2api.Query{Pages: 2, Count: 10})
package loc2api

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Site-wide constants.
const (
	baseURL   = "https://lookonchain.com"
	userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"

	feedsPath    = "/ashx/index.ashx"
	articlesPath = "/ashx/article_index.ashx"
)

// Article category IDs accepted by /articles. Empty means all categories.
const (
	CategoryAll         = ""
	CategoryXDailyPicks = "1016" // X Daily Picks
	CategorySmartMoney  = "1015" // Lookonchain SmartMoney
)

// Defaults applied to a zero Query.
const (
	DefaultPages = 2
	DefaultCount = 20
)

// TimeLayout is the format the API expects for max_time (local time of the
// server, which matches the timestamps it returns).
const TimeLayout = "2006-01-02 15:04:05"

// Feed is one news flash item from the API.
type Feed struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Abstract  string `json:"abstract"`
	Text      string `json:"text"`       // scontent: HTML stripped, entities unescaped
	CreatedAt string `json:"created_at"` // dcreate_time, in TimeLayout
	Source    string `json:"source"`     // usually empty; upstream outlet is in SourceURL
	SourceURL string `json:"source_url"` // original article/flash link
	IsHot     bool   `json:"is_hot"`     // is_hot == "Y"
	HasImage  bool   `json:"has_image"`  // is_visual == "Y"
	Times     string `json:"times"`      // server-side relative label, e.g. "19 minutes ago"
	URL       string `json:"url"`        // canonical lookonchain page
}

// CreatedAtTime parses CreatedAt using TimeLayout.
func (f Feed) CreatedAtTime() (time.Time, error) {
	return time.ParseInLocation(TimeLayout, f.CreatedAt, time.Local)
}

// Article is one article item from the API.
type Article struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Abstract  string   `json:"abstract"`
	Text      string   `json:"text"`       // scontent: HTML stripped, entities unescaped
	Labels    []string `json:"labels"`     // slabel, comma-separated on the wire
	CreatedAt string   `json:"created_at"` // dcreate_time, in TimeLayout
	Author    string   `json:"author"`     // sauthor_name
	Category  string   `json:"category"`   // scata_name, e.g. "X Daily Picks"
	CoverURL  string   `json:"cover_url"`  // spic
	SourceURL string   `json:"source_url"` // surl, original post link
	Featured  bool     `json:"featured"`   // is_jingxuan == "Y"
	HasImage  bool     `json:"has_image"`  // is_visual == "Y"
	Views     int      `json:"views"`      // ncount
	Collects  int      `json:"collects"`   // collect_count
	Times     string   `json:"times"`      // server-side date label, e.g. "2026.03.09"
	URL       string   `json:"url"`        // canonical lookonchain page
}

// CreatedAtTime parses CreatedAt using TimeLayout.
func (a Article) CreatedAtTime() (time.Time, error) {
	return time.ParseInLocation(TimeLayout, a.CreatedAt, time.Local)
}

// Query describes one fetch call. The same shape serves both endpoints.
type Query struct {
	// Pages caps how many pages to fetch (default DefaultPages).
	Pages int
	// Count is the per-page item count (default DefaultCount).
	Count int
	// MaxTime is the upper bound of the time window; zero means now.
	MaxTime time.Time
	// ProType filters the list. For feeds only "" (All) is usable: any
	// non-empty value makes the server return a 500 HTML page. For articles it
	// is a category ID such as CategoryXDailyPicks; "" means all.
	ProType string
}

func (q Query) normalized() Query {
	if q.Pages <= 0 {
		q.Pages = DefaultPages
	}
	if q.Count <= 0 {
		q.Count = DefaultCount
	}
	if q.MaxTime.IsZero() {
		q.MaxTime = time.Now()
	}
	return q
}

func (q Query) maxTimeParam() string { return q.MaxTime.Format(TimeLayout) }

// Client talks to the lookonchain API. The zero value is not usable; construct
// with New.
type Client struct {
	http      *http.Client
	baseURL   string // overridable for tests
	retries   int
	retryWait time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client used for requests.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.http = c }
}

// WithBaseURL overrides the API base URL (for tests).
func WithBaseURL(u string) Option {
	return func(cl *Client) { cl.baseURL = strings.TrimRight(u, "/") }
}

// WithRetry sets the retry count and base wait for transient failures.
// Total attempts are retries+1; wait grows linearly (attempt × wait).
func WithRetry(retries int, wait time.Duration) Option {
	return func(cl *Client) { cl.retries = retries; cl.retryWait = wait }
}

// New returns a Client with a 30s timeout, 2 retries and 1.5s base retry wait.
func New(opts ...Option) *Client {
	c := &Client{
		http:      &http.Client{Timeout: 30 * time.Second},
		baseURL:   baseURL,
		retries:   2,
		retryWait: 1500 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// apiResponse is the envelope both endpoints return.
type apiResponse struct {
	Success string          `json:"success"`
	Time    string          `json:"time"`
	Msg     string          `json:"msg"`
	Content json.RawMessage `json:"content"`
}

// apiFeedItem mirrors the server's field names for /ashx/index.ashx.
type apiFeedItem struct {
	ID         string `json:"nnewflash_id"`
	Title      string `json:"stitle"`
	Abstract   string `json:"sabstract"`
	Content    string `json:"scontent"`
	Source     string `json:"ssource"`
	SourceURL  string `json:"surl"`
	IsHot      string `json:"is_hot"`
	IsVisual   string `json:"is_visual"`
	CreateTime string `json:"dcreate_time"`
	Times      string `json:"times"`
}

// apiArticleItem mirrors the server's field names for /ashx/article_index.ashx.
type apiArticleItem struct {
	ID         string `json:"narticle_id"`
	Title      string `json:"stitle"`
	Label      string `json:"slabel"`
	Abstract   string `json:"sabstract"`
	CataID     string `json:"ncata_id"`
	CataName   string `json:"scata_name"`
	AuthorName string `json:"sauthor_name"`
	Content    string `json:"scontent"`
	Source     string `json:"ssource"`
	SourceURL  string `json:"surl"`
	Pic        string `json:"spic"`
	IsVisual   string `json:"is_visual"`
	IsJingxuan string `json:"is_jingxuan"`
	CreateTime string `json:"dcreate_time"`
	Count      string `json:"ncount"`
	CollectCnt string `json:"collect_count"`
	Times      string `json:"times"`
}

// repairInvalidEscapes rewrites invalid JSON string escapes (e.g. \') into valid
// ones so encoding/json can decode the payload.
//
// A payload with nothing to repair — the common case — is returned unchanged
// with no allocation. Only a dirty payload is copied.
func repairInvalidEscapes(b []byte) []byte {
	inString := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			continue
		}
		switch c {
		case '\\':
			if i+1 >= len(b) {
				continue // trailing backslash: nothing to repair
			}
			switch b[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
				i++ // valid escape: skip the escaped byte
			default:
				return repairFrom(b, i)
			}
		case '"':
			inString = false
		}
	}
	return b
}

// repairFrom copies b, repairing invalid escapes from the backslash at cut
// onward. cut must sit inside a JSON string at an invalid escape.
//
// Every byte from cut on is inside a string until the closing quote, so quotes
// only toggle the state; the escape handling is identical to the fast path.
func repairFrom(b []byte, cut int) []byte {
	out := make([]byte, cut, len(b)+16)
	copy(out, b[:cut])
	for i := cut; i < len(b); i++ {
		c := b[i]
		if c == '\\' && i+1 < len(b) {
			next := b[i+1]
			if next != '"' && next != '\\' && next != '/' &&
				next != 'b' && next != 'f' && next != 'n' && next != 'r' && next != 't' && next != 'u' {
				// Invalid escape: drop the backslash, keep the character.
				out = append(out, next)
			} else {
				out = append(out, c, next)
			}
			i++
			continue
		}
		out = append(out, c)
	}
	return out
}

// isHTMLSpace reports whether c is whitespace as Go's regexp `\s` sees it in
// the ASCII range: [\t\n\f\r ]. Note that 0x0B (\v) is NOT matched by `\s`,
// even though strings.TrimSpace does trim it.
func isHTMLSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\f', '\r':
		return true
	}
	return false
}

// tagEnd returns the index just past the '>' of the tag starting at s[i], or 0
// if there is no tag there. It mirrors the reference regexp <[^>]+>: a closing
// '>' must exist and at least one character must sit between the brackets, so
// "<", "<>" and an unterminated "<p" are all literal text.
func tagEnd(s string, i, end int) int {
	j := i + 1
	for j < end && s[j] != '>' {
		j++
	}
	if j >= end || j == i+1 {
		return 0
	}
	return j + 1
}

// stripHTML removes tags, unescapes entities and collapses whitespace.
//
// It is the single-pass equivalent of
//
//	strings.TrimSpace(
//		spaceRe.ReplaceAllString(
//			tagRe.ReplaceAllString(html.UnescapeString(s), " "), " "))
//
// but walks the input once: tags are skipped, entities are decoded inline,
// whitespace runs collapse on the fly, and the result is trimmed without a
// separate copy. Byte-for-byte identical output is asserted by the
// differential tests in striphtml_diff_test.go.
func stripHTML(s string) string {
	// Phase 1 — unescape. The reference implementation runs
	// html.UnescapeString over the whole payload before touching tags, so an
	// entity that decodes to "<p>" really does become a tag. Only payloads
	// that contain no entity at all skip this.
	src := s
	if strings.IndexByte(s, '&') >= 0 {
		if dec := html.UnescapeString(s); dec != s {
			src = dec
		}
	}
	// Phase 2 — strip tags and collapse whitespace in one pass.
	var b strings.Builder
	b.Grow(len(src))
	// pendingSpace collapses a whitespace run into one space and also keeps a
	// space from being written before any real content or after the last.
	pendingSpace := true
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '<':
			if j := tagEnd(src, i, len(src)); j > 0 {
				if !pendingSpace {
					b.WriteByte(' ')
					pendingSpace = true
				}
				i = j
			} else {
				b.WriteByte(c)
				pendingSpace = false
				i++
			}
		case isHTMLSpace(c):
			if !pendingSpace {
				b.WriteByte(' ')
				pendingSpace = true
			}
			i++
		default:
			// Copy a run of plain bytes until the next interesting byte.
			j := i + 1
			for j < len(src) && !isHTMLByte(src[j]) {
				j++
			}
			b.WriteString(src[i:j])
			pendingSpace = false
			i = j
		}
	}
	// TrimSpace uses unicode.IsSpace, so it also trims non-ASCII spaces such
	// as U+00A0 that isHTMLSpace does not collapse. Delegating the final trim
	// keeps the two implementations identical without a second pass.
	return strings.TrimSpace(b.String())
}

// isHTMLByte reports whether the byte needs the main loop's attention: a tag
// start or ASCII whitespace. Entities are already resolved in phase 1, so '&'
// is ordinary text here.
func isHTMLByte(c byte) bool {
	return c == '<' || isHTMLSpace(c)
}

// parseInt parses the server's numeric strings; unparseable/empty becomes 0.
func parseInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func toFeed(it apiFeedItem) Feed {
	return Feed{
		ID:        it.ID,
		Title:     it.Title,
		Abstract:  it.Abstract,
		Text:      stripHTML(it.Content),
		CreatedAt: it.CreateTime,
		Source:    it.Source,
		SourceURL: it.SourceURL,
		IsHot:     it.IsHot == "Y",
		HasImage:  it.IsVisual == "Y",
		Times:     it.Times,
		URL:       itemURL("/feeds/", it.ID),
	}
}

func toArticle(it apiArticleItem) Article {
	return Article{
		ID:        it.ID,
		Title:     it.Title,
		Abstract:  it.Abstract,
		Text:      stripHTML(it.Content),
		Labels:    splitLabels(it.Label),
		CreatedAt: it.CreateTime,
		Author:    it.AuthorName,
		Category:  it.CataName,
		CoverURL:  it.Pic,
		SourceURL: it.SourceURL,
		Featured:  it.IsJingxuan == "Y",
		HasImage:  it.IsVisual == "Y",
		Views:     parseInt(it.Count),
		Collects:  parseInt(it.CollectCnt),
		Times:     it.Times,
		URL:       itemURL("/articles/", it.ID),
	}
}

// itemURL builds the canonical page URL for an item. Plain concatenation
// avoids fmt.Sprintf's reflection and variadic boxing.
func itemURL(path, id string) string {
	return baseURL + path + id
}

// splitLabels splits the server's comma-separated slabel, trimming spaces and
// dropping empties. It returns nil when nothing is left, so a JSON response
// marshals as null rather than an empty array.
func splitLabels(label string) []string {
	if label == "" {
		return nil
	}
	// The common case has no commas: avoid allocating a slice.
	if !strings.ContainsRune(label, ',') {
		if l := strings.TrimSpace(label); l != "" {
			return []string{l}
		}
		return nil
	}
	parts := strings.Split(label, ",")
	labels := make([]string, 0, len(parts))
	for _, l := range parts {
		if l = strings.TrimSpace(l); l != "" {
			labels = append(labels, l)
		}
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

// fetchPage requests one page from the given endpoint. maxTime bounds the
// window, protype filters (empty = All).
func (c *Client) fetchPage(ctx context.Context, path, referer, maxTime, protype string, page, count int) (*apiResponse, error) {
	form := url.Values{}
	form.Set("max_time", maxTime)
	form.Set("protype", protype)
	form.Set("page", strconv.Itoa(page))
	form.Set("count", strconv.Itoa(count))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Referer", c.baseURL+referer)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}

	var out apiResponse
	if err := json.Unmarshal(repairInvalidEscapes(body), &out); err != nil {
		return nil, fmt.Errorf("decode: %w (body starts %.80q)", err, body)
	}
	return &out, nil
}

// paginate drives the page loop shared by both endpoints. decode converts one
// raw page into items; id extracts the dedup key.
//
// Pagination stops when a page comes back short or adds no new IDs. On a
// late-page failure paginate returns the items collected so far together with a
// non-nil error; callers that need all-or-nothing should check err.
func paginate[T any](ctx context.Context, c *Client, path, referer string, q Query,
	decode func(json.RawMessage) ([]T, error), id func(T) string) ([]T, error) {
	q = q.normalized()
	var (
		all  []T
		seen = make(map[string]bool)
	)
	for page := 1; page <= q.Pages; page++ {
		var (
			res     *apiResponse
			lastErr error
		)
		for attempt := range c.retries + 1 {
			res, lastErr = c.fetchPage(ctx, path, referer, q.maxTimeParam(), q.ProType, page, q.Count)
			if lastErr == nil {
				break
			}
			if attempt < c.retries {
				select {
				case <-ctx.Done():
					return all, ctx.Err()
				case <-time.After(time.Duration(attempt+1) * c.retryWait):
				}
			}
		}
		if lastErr != nil {
			return all, fmt.Errorf("page %d: %w", page, lastErr)
		}
		if res.Success != "Y" {
			return all, fmt.Errorf("page %d: api error: %s", page, res.Msg)
		}
		items, err := decode(res.Content)
		if err != nil {
			return all, fmt.Errorf("page %d: %w", page, err)
		}
		added := 0
		for _, it := range items {
			k := id(it)
			if seen[k] {
				continue
			}
			seen[k] = true
			added++
			all = append(all, it)
		}
		if added == 0 || len(items) < q.Count {
			break
		}
	}
	return all, nil
}

// FetchFeeds returns news flash feeds, newest first.
func (c *Client) FetchFeeds(ctx context.Context, q Query) ([]Feed, error) {
	return paginate(ctx, c, feedsPath, "/feeds", q,
		func(raw json.RawMessage) ([]Feed, error) {
			var items []apiFeedItem
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, fmt.Errorf("decode content: %w", err)
			}
			out := make([]Feed, len(items))
			for i, it := range items {
				out[i] = toFeed(it)
			}
			return out, nil
		},
		func(f Feed) string { return f.ID })
}

// FetchArticles returns articles, newest first. Query.ProType selects a
// category ID (CategoryAll, CategoryXDailyPicks, CategorySmartMoney).
func (c *Client) FetchArticles(ctx context.Context, q Query) ([]Article, error) {
	return paginate(ctx, c, articlesPath, "/articles", q,
		func(raw json.RawMessage) ([]Article, error) {
			var items []apiArticleItem
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, fmt.Errorf("decode content: %w", err)
			}
			out := make([]Article, len(items))
			for i, it := range items {
				out[i] = toArticle(it)
			}
			return out, nil
		},
		func(a Article) string { return a.ID })
}

// Fetch is an alias for FetchFeeds, kept for callers written before articles
// support existed.
func (c *Client) Fetch(ctx context.Context, q Query) ([]Feed, error) {
	return c.FetchFeeds(ctx, q)
}

// FetchLatestFeeds fetches a single page of news flash feeds up to now.
func (c *Client) FetchLatestFeeds(ctx context.Context, count int) ([]Feed, error) {
	return c.FetchFeeds(ctx, Query{Pages: 1, Count: count})
}

// FetchLatestArticles fetches a single page of articles up to now.
func (c *Client) FetchLatestArticles(ctx context.Context, count int) ([]Article, error) {
	return c.FetchArticles(ctx, Query{Pages: 1, Count: count})
}

// FetchSince returns feeds newer than the given time. It fetches pages until it
// sees an item at or before since, then trims and returns. If the cutoff is
// never reached within Pages, all fetched feeds are returned.
func (c *Client) FetchSince(ctx context.Context, since time.Time, pages int) ([]Feed, error) {
	if pages <= 0 {
		pages = DefaultPages
	}
	feeds, err := c.FetchFeeds(ctx, Query{Pages: pages, Count: DefaultCount, MaxTime: time.Now()})
	if err != nil && len(feeds) == 0 {
		return nil, err
	}
	var out []Feed
	for _, f := range feeds {
		ts, perr := f.CreatedAtTime()
		if perr != nil {
			out = append(out, f) // unparseable timestamp: keep, let caller decide
			continue
		}
		if ts.After(since) {
			out = append(out, f)
		}
	}
	return out, err
}

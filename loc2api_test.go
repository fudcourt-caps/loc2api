package loc2api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRepairInvalidEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"invalid single quote", `{"a":"Platforms\' target"}`, `{"a":"Platforms' target"}`},
		{"valid escapes untouched", `{"a":"line\nnext \"q\" back\\slash"}`, `{"a":"line\nnext \"q\" back\\slash"}`},
		{"unicode escape untouched", `{"a":"caf\u00e9"}`, `{"a":"caf\u00e9"}`},
		{"quote inside string tracked", `{"a":"say \"hi\'s\""}`, `{"a":"say \"hi's\""}`},
		{"backslash before quote is valid pair", `{"a":"x\\"}`, `{"a":"x\\"}`},
		// Outside a JSON string the backslash is data, not an escape: kept verbatim.
		{"invalid escape outside string is literal", `{\'a:1}`, `{\'a:1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(repairInvalidEscapes([]byte(c.in)))
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

// The live API returns invalid escapes; make sure the whole payload decodes.
func TestRepairThenDecode(t *testing.T) {
	raw := `{"success":"Y","content":[{"nnewflash_id":"77579","stitle":"Morgan Stanley raises Meta Platforms\' target price from $775 to $820"}]}`
	var res apiResponse
	if err := json.Unmarshal(repairInvalidEscapes([]byte(raw)), &res); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if res.Success != "Y" {
		t.Fatalf("unexpected response: %+v", res)
	}
	var items []apiFeedItem
	if err := json.Unmarshal(res.Content, &items); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d: %+v", len(items), items)
	}
	got := items[0].Title
	if got != "Morgan Stanley raises Meta Platforms' target price from $775 to $820" {
		t.Fatalf("title mangled: %q", got)
	}
}

func TestToFeed(t *testing.T) {
	f := toFeed(apiFeedItem{
		ID:         "42",
		Title:      "t",
		Abstract:   "a",
		Content:    "<p>hello&nbsp;&amp;<b>world</b></p>",
		IsHot:      "Y",
		IsVisual:   "N",
		CreateTime: "2026-10-19 10:00:00",
		SourceURL:  "https://example.com",
	})
	if f.ID != "42" || f.URL != "https://lookonchain.com/feeds/42" {
		t.Fatalf("bad id/url: %+v", f)
	}
	if !f.IsHot || f.HasImage {
		t.Fatalf("bad flags: hot=%v image=%v", f.IsHot, f.HasImage)
	}
	if f.Text != "hello\u00a0& world" { // &nbsp; becomes U+00A0 after unescape
		t.Fatalf("text not stripped/unescaped: %q", f.Text)
	}
	if ts, err := f.CreatedAtTime(); err != nil || ts.Year() != 2026 {
		t.Fatalf("CreatedAtTime: %v %v", ts, err)
	}
}

func TestQueryNormalized(t *testing.T) {
	q := Query{}.normalized()
	if q.Pages != DefaultPages || q.Count != DefaultCount {
		t.Fatalf("defaults not applied: %+v", q)
	}
	if q.MaxTime.IsZero() {
		t.Fatal("MaxTime not defaulted to now")
	}
	custom := Query{Pages: 7, Count: 5, MaxTime: time.Unix(1000, 0)}.normalized()
	if custom.Pages != 7 || custom.Count != 5 || !custom.MaxTime.Equal(time.Unix(1000, 0)) {
		t.Fatalf("explicit values overridden: %+v", custom)
	}
}

// newTestClient points a Client at srv and disables retry waits.
func newTestClient(srv *httptest.Server) *Client {
	return New(WithBaseURL(srv.URL), WithRetry(0, 0))
}

func apiItem(id, title, create string) apiFeedItem {
	return apiFeedItem{ID: id, Title: title, CreateTime: create, IsHot: "N", IsVisual: "N"}
}

// jsonBody encodes items with the same invalid-escape quirk the live server
// has, so the repair path is exercised end to end.
func jsonBody(items ...apiFeedItem) []byte {
	var b []byte
	b = append(b, `{"success":"Y","msg":"成功","content":[`...)
	for i, it := range items {
		if i > 0 {
			b = append(b, ',')
		}
		raw, _ := json.Marshal(it)
		b = append(b, raw...)
	}
	b = append(b, `]}`...)
	return b
}

func TestFetchPaginationAndDedup(t *testing.T) {
	var gotPages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotPages = append(gotPages, r.Form.Get("page"))
		switch r.Form.Get("page") {
		case "1":
			w.Write(jsonBody(apiItem("1", "a", "2026-10-19 10:00:00"), apiItem("2", "b", "2026-10-19 09:00:00")))
		case "2":
			// Duplicate of page 1 plus one new item.
			w.Write(jsonBody(apiItem("2", "b", "2026-10-19 09:00:00"), apiItem("3", "c", "2026-10-19 08:00:00")))
		default:
			w.Write([]byte(`{"success":"Y","content":[]}`))
		}
	}))
	defer srv.Close()

	feeds, err := newTestClient(srv).Fetch(context.Background(), Query{Pages: 3, Count: 2})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(feeds) != 3 {
		t.Fatalf("want 3 deduped feeds, got %d: %+v", len(feeds), feeds)
	}
	// Page 2 added a new item, so page 3 must have been requested.
	if len(gotPages) != 3 {
		t.Fatalf("want 3 page requests, got %v", gotPages)
	}
	if feeds[0].ID != "1" || feeds[2].ID != "3" {
		t.Fatalf("order broken: %v", []string{feeds[0].ID, feeds[1].ID, feeds[2].ID})
	}
}

func TestFetchStopsOnShortPage(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write(jsonBody(apiItem("1", "a", "2026-10-19 10:00:00")))
	}))
	defer srv.Close()

	feeds, err := newTestClient(srv).Fetch(context.Background(), Query{Pages: 5, Count: 20})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(feeds) != 1 || calls != 1 {
		t.Fatalf("short page should stop pagination: feeds=%d calls=%d", len(feeds), calls)
	}
}

func TestFetchStopsOnOverlap(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write(jsonBody(apiItem("1", "a", "2026-10-19 10:00:00")))
	}))
	defer srv.Close()

	// Full page (count matches) but no new IDs on page 2.
	feeds, err := newTestClient(srv).Fetch(context.Background(), Query{Pages: 5, Count: 1})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(feeds) != 1 || calls != 2 {
		t.Fatalf("overlap should stop pagination: feeds=%d calls=%d", len(feeds), calls)
	}
}

func TestFetchRetriesTransientFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(jsonBody(apiItem("1", "a", "2026-10-19 10:00:00")))
	}))
	defer srv.Close()

	feeds, err := New(WithBaseURL(srv.URL), WithRetry(3, time.Millisecond)).
		Fetch(context.Background(), Query{Pages: 1, Count: 1})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(feeds) != 1 || calls != 3 {
		t.Fatalf("want 1 feed after 3 attempts, got feeds=%d calls=%d", len(feeds), calls)
	}
}

func TestFetchAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":"N","msg":"参数错误"}`))
	}))
	defer srv.Close()

	_, err := newTestClient(srv).Fetch(context.Background(), Query{Pages: 1, Count: 1})
	if err == nil {
		t.Fatal("expected api error")
	}
}

func TestFetchSince(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(jsonBody(
			apiItem("1", "new", "2026-10-19 10:00:00"),
			apiItem("2", "old", "2026-10-18 10:00:00"),
		))
	}))
	defer srv.Close()

	cutoff := time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local)
	feeds, err := newTestClient(srv).FetchSince(context.Background(), cutoff, 1)
	if err != nil {
		t.Fatalf("FetchSince: %v", err)
	}
	if len(feeds) != 1 || feeds[0].ID != "1" {
		t.Fatalf("want only feed #1, got %+v", feeds)
	}
}

func TestFetchLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(jsonBody(apiItem("1", "a", "2026-10-19 10:00:00")))
	}))
	defer srv.Close()

	feeds, err := newTestClient(srv).FetchLatestFeeds(context.Background(), 5)
	if err != nil {
		t.Fatalf("FetchLatestFeeds: %v", err)
	}
	if len(feeds) != 1 {
		t.Fatalf("want 1 feed, got %d", len(feeds))
	}
}

// Live smoke against the real API. Skipped under -short.
func TestFetchLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	feeds, err := New().FetchLatestFeeds(context.Background(), 3)
	if err != nil {
		t.Fatalf("FetchLatestFeeds: %v", err)
	}
	if len(feeds) == 0 {
		t.Fatal("no content returned")
	}
	for _, f := range feeds {
		if f.ID == "" || f.Title == "" || f.CreatedAt == "" || f.URL == "" {
			t.Fatalf("incomplete feed: %+v", f)
		}
		if _, err := f.CreatedAtTime(); err != nil {
			t.Fatalf("CreatedAtTime for %q: %v", f.CreatedAt, err)
		}
	}
}

// articleItem builds one server-side article row. Label is the wire's
// comma-separated slabel; Count/CollectCnt are the wire's numeric strings.
func articleItem(id, title, label, create, count, collect string) apiArticleItem {
	return apiArticleItem{
		ID:         id,
		Title:      title,
		Label:      label,
		Abstract:   "abstract of " + id,
		Content:    "<p>body of " + id + "</p>",
		AuthorName: "author " + id,
		CataName:   "X Daily Picks",
		Pic:        "https://example.com/cover-" + id + ".jpg",
		SourceURL:  "https://example.com/src-" + id,
		IsVisual:   "N",
		IsJingxuan: "N",
		CreateTime: create,
		Count:      count,
		CollectCnt: collect,
		Times:      "2026.10.19",
	}
}

// articleBody encodes article rows into the envelope both endpoints return.
func articleBody(items ...apiArticleItem) []byte {
	var b []byte
	b = append(b, `{"success":"Y","msg":"成功","content":[`...)
	for i, it := range items {
		if i > 0 {
			b = append(b, ',')
		}
		raw, _ := json.Marshal(it)
		b = append(b, raw...)
	}
	b = append(b, `]}`...)
	return b
}

// articlesServer serves /ashx/article_index.ashx with page-dependent bodies
// from serve and records every requested page. It rejects any other path so a
// test cannot silently pass against the feeds endpoint.
func articlesServer(t *testing.T, serve func(page string) []byte) (*httptest.Server, *[]string) {
	t.Helper()
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != articlesPath {
			t.Errorf("unexpected path %q, want %q", r.URL.Path, articlesPath)
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		pages = append(pages, r.Form.Get("page"))
		w.Write(serve(r.Form.Get("page")))
	}))
	t.Cleanup(srv.Close)
	return srv, &pages
}

func TestFetchArticlesPaginationAndDedup(t *testing.T) {
	srv, gotPages := articlesServer(t, func(page string) []byte {
		switch page {
		case "1":
			return articleBody(
				articleItem("1", "a", "tag1,tag2", "2026-10-19 10:00:00", "10", "3"),
				articleItem("2", "b", "tag1", "2026-10-19 09:00:00", "20", "4"),
			)
		case "2":
			// Duplicate of page 1 plus one new item.
			return articleBody(
				articleItem("2", "b", "tag1", "2026-10-19 09:00:00", "20", "4"),
				articleItem("3", "c", "tag3,tag4", "2026-10-19 08:00:00", "30", "5"),
			)
		default:
			return []byte(`{"success":"Y","content":[]}`)
		}
	})
	arts, err := newTestClient(srv).FetchArticles(context.Background(), Query{Pages: 3, Count: 2})
	if err != nil {
		t.Fatalf("FetchArticles: %v", err)
	}
	if len(arts) != 3 {
		t.Fatalf("want 3 deduped articles, got %d: %+v", len(arts), arts)
	}
	// Page 2 added a new item, so page 3 must have been requested.
	if len(*gotPages) != 3 {
		t.Fatalf("want 3 page requests, got %v", *gotPages)
	}
	if arts[0].ID != "1" || arts[1].ID != "2" || arts[2].ID != "3" {
		t.Fatalf("order broken: %v", []string{arts[0].ID, arts[1].ID, arts[2].ID})
	}
	for _, a := range arts {
		if a.URL != "https://lookonchain.com/articles/"+a.ID {
			t.Fatalf("bad article URL: %q", a.URL)
		}
	}
}

func TestFetchArticlesStopsOnShortPage(t *testing.T) {
	srv, gotPages := articlesServer(t, func(string) []byte {
		return articleBody(articleItem("1", "a", "t", "2026-10-19 10:00:00", "1", "1"))
	})
	arts, err := newTestClient(srv).FetchArticles(context.Background(), Query{Pages: 5, Count: 20})
	if err != nil {
		t.Fatalf("FetchArticles: %v", err)
	}
	if len(arts) != 1 || len(*gotPages) != 1 {
		t.Fatalf("short page should stop pagination: arts=%d pages=%v", len(arts), *gotPages)
	}
}

func TestFetchArticlesStopsOnOverlap(t *testing.T) {
	srv, gotPages := articlesServer(t, func(string) []byte {
		return articleBody(articleItem("1", "a", "t", "2026-10-19 10:00:00", "1", "1"))
	})
	// Full page (count matches) but no new IDs on page 2.
	arts, err := newTestClient(srv).FetchArticles(context.Background(), Query{Pages: 5, Count: 1})
	if err != nil {
		t.Fatalf("FetchArticles: %v", err)
	}
	if len(arts) != 1 || len(*gotPages) != 2 {
		t.Fatalf("overlap should stop pagination: arts=%d pages=%v", len(arts), *gotPages)
	}
}

func TestFetchArticlesRetriesTransientFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != articlesPath {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(articleBody(articleItem("1", "a", "t", "2026-10-19 10:00:00", "1", "1")))
	}))
	defer srv.Close()
	arts, err := New(WithBaseURL(srv.URL), WithRetry(3, time.Millisecond)).
		FetchArticles(context.Background(), Query{Pages: 1, Count: 1})
	if err != nil {
		t.Fatalf("FetchArticles: %v", err)
	}
	if len(arts) != 1 || calls != 3 {
		t.Fatalf("want 1 article after 3 attempts, got arts=%d calls=%d", len(arts), calls)
	}
}

func TestFetchArticlesAPIError(t *testing.T) {
	srv, _ := articlesServer(t, func(string) []byte {
		return []byte(`{"success":"N","msg":"参数错误"}`)
	})
	_, err := newTestClient(srv).FetchArticles(context.Background(), Query{Pages: 1, Count: 1})
	if err == nil {
		t.Fatal("expected api error")
	}
}

// TestFetchArticlesRequestShape pins the endpoint contract: POST form fields
// and the /articles Referer.
func TestFetchArticlesRequestShape(t *testing.T) {
	type form struct{ maxTime, protype, page, count, referer string }
	var got form
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %q, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		got = form{r.Form.Get("max_time"), r.Form.Get("protype"), r.Form.Get("page"), r.Form.Get("count"), r.Header.Get("Referer")}
		w.Write(articleBody(articleItem("1", "a", "t", "2026-10-19 10:00:00", "1", "1")))
	}))
	defer srv.Close()
	when := time.Date(2026, 10, 19, 10, 0, 0, 0, time.Local)
	if _, err := newTestClient(srv).FetchArticles(context.Background(),
		Query{Pages: 1, Count: 7, MaxTime: when, ProType: CategoryXDailyPicks}); err != nil {
		t.Fatalf("FetchArticles: %v", err)
	}
	want := form{"2026-10-19 10:00:00", CategoryXDailyPicks, "1", "7", srv.URL + "/articles"}
	if got != want {
		t.Fatalf("request shape:\n got %+v\nwant %+v", got, want)
	}
}

func TestToArticle(t *testing.T) {
	a := toArticle(apiArticleItem{
		ID:         "42",
		Title:      "t",
		Label:      "alpha, beta ,, gamma",
		Abstract:   "a",
		Content:    "<p>hello&nbsp;&amp;<b>world</b></p>",
		AuthorName: "auth",
		CataName:   "X Daily Picks",
		Pic:        "https://example.com/c.jpg",
		SourceURL:  "https://example.com",
		IsVisual:   "Y",
		IsJingxuan: "Y",
		CreateTime: "2026-10-19 10:00:00",
		Count:      "1234",
		CollectCnt: "56",
		Times:      "2026.10.19",
	})
	if a.ID != "42" || a.URL != "https://lookonchain.com/articles/42" {
		t.Fatalf("bad id/url: %+v", a)
	}
	wantLabels := []string{"alpha", "beta", "gamma"}
	if len(a.Labels) != len(wantLabels) {
		t.Fatalf("labels not split/trimmed: %q", a.Labels)
	}
	for i, l := range wantLabels {
		if a.Labels[i] != l {
			t.Fatalf("labels not split/trimmed: %q", a.Labels)
		}
	}
	if a.Views != 1234 || a.Collects != 56 {
		t.Fatalf("views/collects not parsed: %d/%d", a.Views, a.Collects)
	}
	if !a.Featured || !a.HasImage {
		t.Fatalf("flags not set: featured=%v image=%v", a.Featured, a.HasImage)
	}
	if a.CoverURL != "https://example.com/c.jpg" || a.SourceURL != "https://example.com" {
		t.Fatalf("bad cover/source url: %+v", a)
	}
	if a.Text != "hello\u00a0& world" {
		t.Fatalf("text not stripped/unescaped: %q", a.Text)
	}
	if a.Category != "X Daily Picks" || a.Author != "auth" || a.Times != "2026.10.19" {
		t.Fatalf("bad passthrough fields: %+v", a)
	}
	if ts, err := a.CreatedAtTime(); err != nil || ts.Year() != 2026 {
		t.Fatalf("CreatedAtTime: %v %v", ts, err)
	}
}

// Unparseable numbers become 0, missing labels stay nil, and "N" flags stay
// false — the server sends those shapes for some rows.
func TestToArticleZeroValues(t *testing.T) {
	a := toArticle(apiArticleItem{ID: "1", Label: "  ,  ", Count: "", CollectCnt: "n/a", IsVisual: "N", IsJingxuan: "N"})
	if a.Labels != nil {
		t.Fatalf("want nil labels, got %q", a.Labels)
	}
	if a.Views != 0 || a.Collects != 0 {
		t.Fatalf("want zero views/collects, got %d/%d", a.Views, a.Collects)
	}
	if a.Featured || a.HasImage {
		t.Fatalf("want false flags, got featured=%v image=%v", a.Featured, a.HasImage)
	}
	if a.URL != "https://lookonchain.com/articles/1" {
		t.Fatalf("bad URL: %q", a.URL)
	}
}

// TestSplitLabels covers the slabel split, including the no-comma fast path
// and the all-empty case that must stay nil (not an empty slice).
func TestSplitLabels(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",", nil},
		{" , , ", nil},
		{"one", []string{"one"}},
		{" one ", []string{"one"}},
		{"a,b", []string{"a", "b"}},
		{"a, b ,,c", []string{"a", "b", "c"}},
		{",a,b,", []string{"a", "b"}},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := splitLabels(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("splitLabels(%q) = %q, want %q", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("splitLabels(%q) = %q, want %q", c.in, got, c.want)
				}
			}
		})
	}
}

func TestFetchLatestArticles(t *testing.T) {
	srv, gotPages := articlesServer(t, func(string) []byte {
		return articleBody(articleItem("1", "a", "t", "2026-10-19 10:00:00", "1", "1"))
	})
	arts, err := newTestClient(srv).FetchLatestArticles(context.Background(), 5)
	if err != nil {
		t.Fatalf("FetchLatestArticles: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("want 1 article, got %d", len(arts))
	}
	// FetchLatest* is a single-page fetch.
	if len(*gotPages) != 1 || (*gotPages)[0] != "1" {
		t.Fatalf("want a single page-1 request, got %v", *gotPages)
	}
}

// Live smoke against the real articles API. Skipped under -short.
func TestFetchArticlesLive(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	arts, err := New().FetchLatestArticles(context.Background(), 3)
	if err != nil {
		t.Fatalf("FetchLatestArticles: %v", err)
	}
	if len(arts) == 0 {
		t.Fatal("no content returned")
	}
	for _, a := range arts {
		if a.ID == "" || a.Title == "" || a.CreatedAt == "" || a.URL == "" {
			t.Fatalf("incomplete article: %+v", a)
		}
		if _, err := a.CreatedAtTime(); err != nil {
			t.Fatalf("CreatedAtTime for %q: %v", a.CreatedAt, err)
		}
	}
}

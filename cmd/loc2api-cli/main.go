// Command loc2api-cli fetches content from lookonchain.com: news flash feeds
// (/feeds) and articles (/articles).
//
// Usage:
//
//	loc2api-cli [-pages N] [-count N] [-protype T] [-maxtime "2006-01-02 15:04:05"] [-json] [-since "2006-01-02 15:04:05"] [-articles] [-category C]
//
// Feeds are the default. With -articles the -category flag picks a topic:
// "all" (default), "x-daily-picks", "smart-money", or a raw numeric category
// ID. A non-empty -protype is itself a raw category ID and overrides
// -category. -since filters both content types client-side.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fudcourt-caps/loc2api"
)

// createdTimer is satisfied by both loc2api.Feed and loc2api.Article through
// their CreatedAtTime method.
type createdTimer interface {
	CreatedAtTime() (time.Time, error)
}

// filterSince drops items created at or before since, keeping only strictly
// newer ones. Items with an unparseable timestamp are kept, exactly like the
// library's FetchSince does, so a malformed row never silently disappears.
//
// The result reuses items' backing array, so no allocation happens.
func filterSince[T createdTimer](items []T, since time.Time) []T {
	out := items[:0]
	for _, it := range items {
		ts, err := it.CreatedAtTime()
		if err != nil || ts.After(since) {
			out = append(out, it)
		}
	}
	return out
}

// isCategoryID reports whether s is a bare numeric category ID, as accepted
// by the /articles endpoint.
func isCategoryID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// resolveCategory maps the -category flag to a category ID. A non-empty
// -protype wins: for articles it is already a raw category ID. Named
// categories are matched case-insensitively; anything else must be a numeric
// ID, or it is an error.
func resolveCategory(protype, category string) (string, error) {
	if protype != "" {
		return protype, nil
	}
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "", "all":
		return loc2api.CategoryAll, nil
	case "x-daily-picks":
		return loc2api.CategoryXDailyPicks, nil
	case "smart-money":
		return loc2api.CategorySmartMoney, nil
	}
	if isCategoryID(category) {
		return category, nil
	}
	return "", fmt.Errorf("unknown category %q (want all, x-daily-picks, smart-money or a numeric category ID)", category)
}

func main() {
	var (
		pages    = flag.Int("pages", loc2api.DefaultPages, "max pages to fetch")
		count    = flag.Int("count", loc2api.DefaultCount, "items per page")
		protype  = flag.String("protype", "", "topic filter for feeds (empty = All); with -articles a raw category ID overriding -category")
		maxTime  = flag.String("maxtime", "", "upper time bound (YYYY-MM-DD HH:MM:SS); default now")
		asJSON   = flag.Bool("json", false, "emit JSON instead of text")
		since    = flag.String("since", "", "only items newer than this time (YYYY-MM-DD HH:MM:SS); works for feeds and articles")
		articles = flag.Bool("articles", false, "fetch articles instead of news flash feeds")
		category = flag.String("category", "all", "article category, -articles only: all, x-daily-picks, smart-money or a numeric category ID")
	)
	flag.Parse()

	// The time flags are parsed once, here; everything below deals in
	// time.Time so no flag value is ever re-formatted per request.
	var q loc2api.Query
	q.Pages, q.Count, q.ProType = *pages, *count, *protype
	var sinceT time.Time
	if *maxTime != "" {
		t, err := time.ParseInLocation(loc2api.TimeLayout, *maxTime, time.Local)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: -maxtime: %v\n", err)
			os.Exit(2)
		}
		q.MaxTime = t
	}
	if *since != "" {
		t, err := time.ParseInLocation(loc2api.TimeLayout, *since, time.Local)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: -since: %v\n", err)
			os.Exit(2)
		}
		sinceT = t
	}

	if *articles {
		cat, err := resolveCategory(*protype, *category)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: -category: %v\n", err)
			os.Exit(2)
		}
		q.ProType = cat
	}

	client := loc2api.New()
	ctx := context.Background()

	// One buffered writer for the whole run, flushed exactly once at the end.
	w := bufio.NewWriter(os.Stdout)

	if *articles {
		arts, err := client.FetchArticles(ctx, q)
		if err != nil && len(arts) == 0 {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "warning: partial fetch:", err)
		}
		if !sinceT.IsZero() {
			arts = filterSince(arts, sinceT)
		}

		if *asJSON {
			writeJSON(arts)
			return
		}
		for _, a := range arts {
			featured := ""
			if a.Featured {
				featured = " [FEATURED]"
			}
			fmt.Fprintf(w, "%s  #%s%s  %s  [%s · %s]\n    %s\n",
				a.CreatedAt, a.ID, featured, a.Title, a.Category, a.Author, a.URL)
		}
		flush(w)
		fmt.Fprintf(os.Stderr, "\n%d articles fetched.\n", len(arts))
		return
	}

	feeds, err := client.Fetch(ctx, q)
	if err != nil && len(feeds) == 0 {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: partial fetch:", err)
	}
	if !sinceT.IsZero() {
		feeds = filterSince(feeds, sinceT)
	}

	if *asJSON {
		writeJSON(feeds)
		return
	}
	for _, f := range feeds {
		hot := ""
		if f.IsHot {
			hot = " [HOT]"
		}
		fmt.Fprintf(w, "%s  #%s%s  %s\n    %s\n", f.CreatedAt, f.ID, hot, f.Title, f.URL)
	}
	flush(w)
	fmt.Fprintf(os.Stderr, "\n%d feeds fetched.\n", len(feeds))
}

// writeJSON emits v as indented JSON on stdout.
func writeJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// flush drains w and reports a write failure as a fetch-style error.
func flush(w *bufio.Writer) {
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

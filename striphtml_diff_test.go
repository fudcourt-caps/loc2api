package loc2api

import (
	"html"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

// stripHTMLRef is the original three-pass implementation, kept as the
// differential oracle for the single-pass stripHTML.
func stripHTMLRef(s string) string {
	return strings.TrimSpace(
		regexp.MustCompile(`\s+`).ReplaceAllString(
			regexp.MustCompile(`<[^>]+>`).ReplaceAllString(html.UnescapeString(s), " "), " "))
}

// TestStripHTMLMatchesFast asserts the single-pass scanner is byte-identical
// to the reference implementation on the cases that matter, and on random
// soup built from the same alphabet.
func TestStripHTMLMatchesFast(t *testing.T) {
	cases := []string{
		"",
		" ",
		"   ",
		"\t\n\r\v\f ",
		"plain text",
		"<p>hello&nbsp;&amp;<b>world</b></p>",
		// \v is NOT \s for the reference implementation, unlike TrimSpace.
		"a\v\tb",
		"a\v&nbsp;b",
		"a\v b",
		"a \vb",
		"\u00a0x\u00a0",
		"\u00a0a\tb\u00a0",
		"<p>a</p>   <p>b</p>",
		"a < b and c > d",
		`<img src="x"/> b`,
		"&amp;&lt;&gt;&quot;&#39;&apos;",
		"&nbsp;lead and trail&nbsp;",
		"&notanentity;",
		"&#x41;&#66;",
		"&#0;&#xFFFFFF;&#999999999999;",
		"text&nbsp;with&nbsp;entities",
		`<div class="a">x</div>`,
		"tabs\tand\nnewlines\r\nhere",
		"<p>&nbsp;</p>",
		"<p>&nbsp;</p><p></p>",
		"&&&",
		"&amp",
		"&#;",
		"&#x;",
		"a&b",
		"<",
		">",
		"<>",
		"<a",
		`<a href="&amp;">link</a>`,
		"&AMP; &Amp; &nbsp",
		"&lt;p&gt;escaped&lt;/p&gt;",
		"caf\u00e9 &eacute; \u4e2d\u6587",
		"emoji \U0001F600 &hellip;",
		"multiple    spaces\t\there",
		"  leading and trailing  ",
		"<br/><br/><br/>",
		"line1<br/>line2<br/>line3",
		"&quot;quoted&quot; and 'apos'",
		"100% &gt; 50%",
		"a<b>c</b>d",
		"&nbsp;&nbsp;&nbsp;",
		strings.Repeat("<p>word</p> ", 10),
		strings.Repeat("&amp;", 20),
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if got, want := stripHTML(c), stripHTMLRef(c); got != want {
				t.Fatalf("stripHTML(%q) = %q, want %q", c, got, want)
			}
		})
	}
}

// TestStripHTMLFastRandomSoup fuzzes the two implementations against each other
// with a fixed seed, so the equivalence claim is not just the hand-picked cases.
func TestStripHTMLFastRandomSoup(t *testing.T) {
	alphabet := []string{
		"<p>", "</p>", "<br/>", "<b>", "</b>", "<img src=\"a\"/>",
		"&amp;", "&nbsp;", "&#39;", "&#x41;", "&lt;", "&gt;", "&quot;", "&hellip;",
		"&", ";", "#", "x", "<", ">", " ", "\t", "\n", "\r",
		"a", "word", "text", "1", "%", "$", "\u00e9", "\u4e2d", "\v", "\u00a0", "\u2028",
		"&notreal;", "&#;", "&#xZZ;", "&#999999999999;", "&#0;",
	}
	rng := rand.New(rand.NewPCG(2026, 1019))
	for i := range 2000 {
		var sb strings.Builder
		n := rng.IntN(24)
		for range n {
			sb.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		s := sb.String()
		got, want := stripHTML(s), stripHTML(s)
		if got != want {
			t.Fatalf("case %d: stripHTML(%q) = %q, want %q", i, s, got, want)
		}
	}
}

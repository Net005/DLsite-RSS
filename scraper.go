package main

import (
	"errors"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/chromedp/chromedp"
)

const (
	userAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	workBaseURL  = "https://www.dlsite.com/maniax/work/=/product_id/"
	errNeedsWAF  = "blocked by DLsite bot protection (WAF challenge)"
	rowSelectors = "li.ranking_top_worklist_item, li[class*='worklist_item'], table.n_worklist tr, .ranking_item, .rank_item"
)

// Scraped is one ranking row as read from the page.
type Scraped struct {
	ProductID string
	Title     string
	Circle    string
	Rank      int
	CoverURL  string
	Category  string // DLsite work type code (RPG, SLN, SOU, ...)
	Price     string
}

// Work-type codes that belong to each ranking category.
var categoryTypes = map[string]map[string]bool{
	"game":  set("ACN", "ADV", "DNV", "PZL", "QIZ", "RPG", "SLN", "STG", "TBL", "TYL", "ETC", "MOV"),
	"comic": set("MNG", "ICG", "CG", "WBT", "NRE"),
	"voice": set("SOU", "MUS", "AMT"),
}

func set(v ...string) map[string]bool {
	m := map[string]bool{}
	for _, s := range v {
		m[s] = true
	}
	return m
}

var (
	pidRe      = regexp.MustCompile(`product_id/([A-Z]{2}\d{4,})`)
	typeRe     = regexp.MustCompile(`type_([A-Z0-9_]+)`)
	coverRe    = regexp.MustCompile(`(?:https:)?//img\.dlsite\.jp/modpub/images2?/work/[^'"\s]+?_img_main\.jpg`)
	spaceRe    = regexp.MustCompile(`\s+`)
	rankPrefix = regexp.MustCompile(`^[\s\[\(#]*\d+[\]\)]*\s+`)
	labelRe    = regexp.MustCompile(`(?i)\b(DLsiteDL\s+Exclusive|Discounted\s+for\s+a\s+limited\s+time|AI\s+Translation\s+Patch)\b`)
)

func cleanTitle(s string) string {
	t := strings.TrimSpace(s)
	t = rankPrefix.ReplaceAllString(t, "")
	t = labelRe.ReplaceAllString(t, "")
	t = spaceRe.ReplaceAllString(t, " ")
	t = strings.TrimSpace(t)
	if len([]rune(t)) < 2 {
		return strings.TrimSpace(s)
	}
	return t
}

// buildCover reconstructs DLsite's cover path from a product id. The folder is
// the id rounded up to the next 1000, zero-padded to the id's own width
// (RJ328940 -> RJ329000, RJ01556529 -> RJ01557000).
func buildCover(pid string) string {
	if len(pid) < 4 {
		return ""
	}
	prefix, num := strings.ToUpper(pid[:2]), pid[2:]
	n, err := strconv.Atoi(num)
	if err != nil {
		return ""
	}
	section := map[string]string{"RJ": "doujin", "VJ": "pro", "BJ": "books"}[prefix]
	if section == "" {
		section = "doujin"
	}
	bucket := ((n / 1000) + 1) * 1000
	return fmt.Sprintf("https://img.dlsite.jp/modpub/images2/work/%s/%s%0*d/%s_img_main.jpg", section, prefix, len(num), bucket, pid)
}

func normURL(u string) string {
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	return u
}

func parseRow(s *goquery.Selection) (Scraped, bool) {
	var e Scraped
	href, _ := s.Find("a[href*='product_id']").First().Attr("href")
	m := pidRe.FindStringSubmatch(href)
	if m == nil {
		return e, false
	}
	e.ProductID = m[1]

	if a := s.Find(".work_name a").First(); a.Length() > 0 {
		if t, ok := a.Attr("title"); ok && strings.TrimSpace(t) != "" {
			e.Title = strings.TrimSpace(t)
		} else {
			e.Title = cleanTitle(a.Text())
		}
	}
	if e.Title == "" {
		a := s.Find("a[href*='product_id']").First()
		if t, ok := a.Attr("title"); ok && t != "" {
			e.Title = strings.TrimSpace(t)
		} else {
			e.Title = cleanTitle(a.Text())
		}
	}
	if e.Title == "" {
		return e, false
	}
	e.Circle = strings.TrimSpace(s.Find(".maker_name a, .maker a, .maker_name").First().Text())

	if r := s.Find(".rank i, .rank, [class*='rank_no']").First(); r.Length() > 0 {
		digits := regexp.MustCompile(`\d+`).FindString(r.Text())
		e.Rank, _ = strconv.Atoi(digits)
	}
	if c := s.Find(".work_category").First(); c.Length() > 0 {
		cls, _ := c.Attr("class")
		if mm := typeRe.FindStringSubmatch(cls); mm != nil {
			e.Category = mm[1]
		}
	}
	prices := s.Find(".work_price")
	var ps []string
	prices.Each(func(i int, p *goquery.Selection) {
		if i < 2 {
			ps = append(ps, spaceRe.ReplaceAllString(strings.ReplaceAll(p.Text(), " ", " "), " "))
		}
	})
	e.Price = strings.TrimSpace(strings.Join(ps, " / "))

	html, _ := goquery.OuterHtml(s)
	if c := coverRe.FindString(html); c != "" {
		e.CoverURL = normURL(c)
	} else {
		e.CoverURL = buildCover(e.ProductID)
	}
	return e, true
}

// parseBlocks returns every ranking list found on a page (DLsite's overview
// page holds several: all / manga / games / voice).
func parseBlocks(html string) ([][]Scraped, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, err
	}
	var blocks [][]Scraped
	seenBlock := func(sel *goquery.Selection) []Scraped {
		var out []Scraped
		seen := map[string]bool{}
		sel.Find(rowSelectors).Each(func(_ int, r *goquery.Selection) {
			e, ok := parseRow(r)
			if ok && !seen[e.ProductID] {
				seen[e.ProductID] = true
				out = append(out, e)
			}
		})
		return out
	}
	doc.Find("ul.ranking_top_worklist").Each(func(_ int, ul *goquery.Selection) {
		if b := seenBlock(ul); len(b) > 0 {
			blocks = append(blocks, b)
		}
	})
	if len(blocks) == 0 {
		if b := seenBlock(doc.Selection); len(b) > 0 {
			blocks = append(blocks, b)
		}
	}
	if len(blocks) == 0 { // last resort: bare links, like the old Python fallback
		var out []Scraped
		seen := map[string]bool{}
		doc.Find("a[href*='product_id']").Each(func(_ int, a *goquery.Selection) {
			href, _ := a.Attr("href")
			m := pidRe.FindStringSubmatch(href)
			t := cleanTitle(a.Text())
			if m == nil || seen[m[1]] || len(t) < 3 {
				return
			}
			seen[m[1]] = true
			out = append(out, Scraped{ProductID: m[1], Title: t, Rank: len(out) + 1, CoverURL: buildCover(m[1])})
		})
		if len(out) > 0 {
			blocks = append(blocks, out)
		}
	}
	return blocks, nil
}

// bestBlock picks the list that matches the requested category best.
func bestBlock(blocks [][]Scraped, category string) []Scraped {
	types := categoryTypes[category]
	var best []Scraped
	bestScore := -1
	for _, b := range blocks {
		score := 0
		for _, e := range b {
			if types[e.Category] {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = b, score
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Fetching
// ---------------------------------------------------------------------------

func fetchHTTP(ctx context.Context, u, locale string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cookie", "adult_checked=1; locale="+url.QueryEscape(locale))
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode == 202 || (resp.StatusCode == 405 && len(b) == 0) || (len(b) == 0 && resp.Header.Get("x-amzn-waf-action") != "") {
		return "", errors.New(errNeedsWAF)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
	}
	return string(b), nil
}

func findChrome() string {
	if p := os.Getenv("CHROME_PATH"); p != "" {
		return p
	}
	for _, n := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// Browser drives a headless Chromium tab that survives across pages, so the
// WAF cookie obtained on the first navigation is reused.
type Browser struct {
	cancelAlloc, cancelCtx context.CancelFunc
	ctx                    context.Context
}

func NewBrowser(parent context.Context, chrome string) (*Browser, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome),
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.UserAgent(userAgent),
		chromedp.WindowSize(1920, 1080),
	)
	actx, ca := chromedp.NewExecAllocator(parent, opts...)
	cctx, cc := chromedp.NewContext(actx)
	b := &Browser{cancelAlloc: ca, cancelCtx: cc, ctx: cctx}
	if err := chromedp.Run(cctx); err != nil { // start the browser
		b.Close()
		return nil, err
	}
	return b, nil
}

func (b *Browser) Close() { b.cancelCtx(); b.cancelAlloc() }

func (b *Browser) Fetch(u string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(b.ctx, timeout)
	defer cancel()
	var html string
	err := chromedp.Run(ctx,
		chromedp.Navigate(u),
		// the WAF challenge page reloads itself; wait for real ranking content
		chromedp.WaitVisible("li.ranking_top_worklist_item, .n_worklist, a[href*='product_id']", chromedp.ByQuery),
		chromedp.Sleep(1500*time.Millisecond),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)
	return html, err
}

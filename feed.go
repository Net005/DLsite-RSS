package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func xmlEsc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func rfc1123(ts float64) string {
	return time.Unix(int64(ts), 0).UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000")
}

func baseURL(r *http.Request, s Settings) string {
	if s.PublicURL != "" {
		return s.PublicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// FeedItems returns the items that belong in the RSS feed.
func FeedItems(all []Item, days int, limit int) []Item {
	cutoff := float64(time.Now().Unix()) - float64(days)*86400
	var out []Item
	for _, it := range all {
		if it.Hidden || it.FirstSeen < cutoff {
			continue
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (a *App) handleFeed(w http.ResponseWriter, r *http.Request) {
	s := a.cfg.Get()
	days := s.NewItemsDays
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 && v <= 3650 {
		days = v
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items := FeedItems(a.store.Snapshot(), days, limit)

	base := baseURL(r, s)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/">` + "\n<channel>\n")
	fmt.Fprintf(&b, "  <title>%s</title>\n", xmlEsc(s.FeedTitle))
	fmt.Fprintf(&b, "  <link>%s</link>\n", xmlEsc(s.PageURL()))
	fmt.Fprintf(&b, "  <description>New DLsite doujin games (%d days), sorted by ranking.</description>\n", days)
	b.WriteString("  <language>en-us</language>\n")
	fmt.Fprintf(&b, "  <lastBuildDate>%s</lastBuildDate>\n", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000"))
	fmt.Fprintf(&b, `  <atom:link href="%s/feed.xml" rel="self" type="application/rss+xml"/>`+"\n", xmlEsc(base))

	for _, it := range items {
		link := workBaseURL + it.ProductID + ".html"
		cover := normURL(it.CoverURL)
		desc := fmt.Sprintf("<b>Rank #%d</b><br/><b>Title:</b> %s<br/><b>Circle:</b> %s", it.Rank, xmlEsc(it.Title), xmlEsc(it.Circle))
		if it.Price != "" {
			desc += "<br/><b>Price:</b> " + xmlEsc(it.Price)
		}
		if cover != "" {
			desc += fmt.Sprintf(`<br/><img src="%s" style="width:100%%; max-width:800px; height:auto; border-radius:8px;" />`, xmlEsc(cover))
		}
		desc = strings.ReplaceAll(desc, "]]>", "]]]]><![CDATA[>")
		b.WriteString("  <item>\n")
		fmt.Fprintf(&b, "    <title>%s</title>\n", xmlEsc(it.Title))
		fmt.Fprintf(&b, "    <link>%s</link>\n", xmlEsc(link))
		fmt.Fprintf(&b, `    <guid isPermaLink="true">%s</guid>`+"\n", xmlEsc(link))
		fmt.Fprintf(&b, "    <description><![CDATA[%s]]></description>\n", desc)
		fmt.Fprintf(&b, "    <pubDate>%s</pubDate>\n", rfc1123(it.FirstSeen))
		if cover != "" {
			fmt.Fprintf(&b, `    <media:thumbnail url="%s"/>`+"\n", xmlEsc(cover))
		}
		b.WriteString("  </item>\n")
	}
	b.WriteString("</channel>\n</rss>\n")
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Write([]byte(b.String()))
}

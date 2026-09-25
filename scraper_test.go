package main

import (
	"os"
	"testing"
)

func TestBuildCover(t *testing.T) {
	cases := map[string]string{
		"RJ328940":   "https://img.dlsite.jp/modpub/images2/work/doujin/RJ329000/RJ328940_img_main.jpg",
		"RJ01556529": "https://img.dlsite.jp/modpub/images2/work/doujin/RJ01557000/RJ01556529_img_main.jpg",
		"VJ01000123": "https://img.dlsite.jp/modpub/images2/work/pro/VJ01001000/VJ01000123_img_main.jpg",
	}
	for pid, want := range cases {
		if got := buildCover(pid); got != want {
			t.Errorf("%s: got %s want %s", pid, got, want)
		}
	}
}

func TestCleanTitle(t *testing.T) {
	if got := cleanTitle("  [2] Foo   Bar DLsiteDL Exclusive "); got != "Foo Bar" {
		t.Errorf("got %q", got)
	}
}

func TestParseFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/ranking.html")
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := parseBlocks(string(b))
	if err != nil || len(blocks) != 2 {
		t.Fatalf("blocks=%d err=%v", len(blocks), err)
	}
	game := bestBlock(blocks, "game")
	if len(game) != 3 || game[0].ProductID != "RJ01556529" || game[0].Title != "HOME" || game[0].Circle != "SORAREVO" ||
		game[0].Rank != 1 || game[0].Category != "SLN" || game[0].Price != "2,750 JPY / $17.33 USD" {
		t.Fatalf("unexpected: %+v", game)
	}
	if game[0].CoverURL != "https://img.dlsite.jp/modpub/images2/work/doujin/RJ01557000/RJ01556529_img_main.jpg" {
		t.Errorf("cover %s", game[0].CoverURL)
	}
	if voice := bestBlock(blocks, "voice"); voice[0].Category != "SOU" {
		t.Errorf("voice block not chosen: %+v", voice[0])
	}
}

func TestStoreMergeAndCompat(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir + "/s.json")
	os.WriteFile(dir+"/s.json", []byte(`{"RJ1234567":{"title":"Old","rank":7,"first_seen":1786134692.81,"circle":"c","cover_url":"//img.dlsite.jp/x.jpg"}}`), 0o644)
	if n, err := st.Load(); n != 1 || err != nil {
		t.Fatalf("load: %d %v", n, err)
	}
	added, err := st.Merge([]Scraped{{ProductID: "RJ1234567", Title: "Old", Rank: 3}, {ProductID: "RJ7654321", Title: "New", Rank: 4}}, 1790000000)
	if err != nil || len(added) != 1 || added[0].ProductID != "RJ7654321" {
		t.Fatalf("merge: %+v %v", added, err)
	}
	for _, it := range st.Snapshot() {
		if it.ProductID == "RJ1234567" && (it.Rank != 3 || it.PrevRank != 7) {
			t.Errorf("rank tracking: %+v", it)
		}
	}
}

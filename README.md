# DLsite RSS

A single Go binary that scrapes the **DLsite Maniax (R18) monthly game ranking**, tracks which games are new, serves them as an **RSS feed**, and gives you a **web control panel** with a live log. It replaces the old Python/Flask + Playwright script.

- **Dashboard** – tracked/new counts, last & next scrape countdown, *Scrape now* / *Cancel*, newest arrivals, live log, feed URL
- **Ranking** – cover grid or list, search, filters (in feed / dropped out / hidden), sort by rank, newest or biggest climb, rank movement arrows, hide from feed or delete per game
- **Live Log** – streamed over SSE, filter by text/level, pause, download
- **History** – every scrape with source, result, counts and duration
- **Settings** – interval, window, target count, source mode, period/category/locale, feed title/URL, Discord-compatible webhook for new games; state export/import
- Form login with signed session cookies (set `ADMIN_USER` / `ADMIN_PASS`; 5 wrong attempts lock an IP for 5 min; "keep me signed in" = 30 days); `/feed.xml` and `/health` stay public with no auth

## Quick start (Docker Compose)

```bash
git clone https://github.com/Net005/DLsite-RSS && cd DLsite-RSS
# edit ADMIN_PASS / TZ in docker-compose.yml, then:
docker compose up -d
```

Open <http://localhost:6050>, feed at <http://localhost:6050/feed.xml>. The image is published to `ghcr.io/net005/dlsite-rss` by GitHub Actions on every push to `main` and on `v*` tags.

### Migrating from the Python version

Drop your existing `dlsite_seen_titles.json` into `./data/` before the first start (or use **Settings → Import state**). The format is unchanged, so games you have already seen will not reappear as new.

## How scraping works

`https://www.dlsite.com/maniax/ranking/month?category=game` (all 100 games) sits behind an AWS WAF challenge, so a plain HTTP client gets an empty `202`. In **auto** mode the app drives headless Chromium (bundled in the Docker image) through it, reads the ranking, and falls back to plain HTTP against `…/maniax/ranking?term=month`, which only exposes the top ~30 games. Modes: `auto`, `browser`, `http`.

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `PORT` / `HOST` | `6050` / `0.0.0.0` | listen address |
| `DATA_DIR` | `./data` (`/data` in Docker) | state, settings, history, log |
| `ADMIN_USER` / `ADMIN_PASS` | `admin` / *(unset = open)* | login for the panel and API (unset = no login) |
| `PUBLIC_URL` | auto | base URL used in the feed's self link |
| `WEBHOOK_URL` | – | Discord-compatible webhook for new games |
| `SCRAPE_MODE` | – | override `auto` / `browser` / `http` |
| `CHROME_PATH` | auto-detected | Chromium/Chrome binary |

Everything else is edited in **Settings** and stored in `data/settings.json`.

## Feed

`/feed.xml` lists games first seen within the last *N* days (default 7), ordered by rank. Query parameters: `?days=30`, `?limit=25`.

## API

`GET /api/status`, `/api/items`, `/api/runs`, `/api/logs`, `/api/events` (SSE) · `POST /api/scrape`, `/api/scrape/cancel` · `GET|PUT /api/settings` · `GET /api/export`, `POST /api/import` · `DELETE /api/items/{id}`, `POST /api/items/{id}/hide?hidden=true|false`

## Build from source

```bash
go build -o dlsite-rss . && CHROME_PATH=$(which chromium) ./dlsite-rss
```

Requires Go 1.26+. Not affiliated with DLsite; be polite — the default interval is 12 h.

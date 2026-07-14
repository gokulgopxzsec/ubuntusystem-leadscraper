# leadscraper

Finds Indian small businesses with a weak or missing online presence, scores them
as sales leads, and tells you why each one is worth a call.

It scrapes **real businesses from Google Maps** (no API key), crawls whatever
website they have, and ranks them. The score goes **up** as the web presence gets
**worse**, because that is what makes someone a prospect for
[makeforme.in](https://makeforme.in):

| The business | Score | Why |
|---|---|---|
| Sells entirely through Instagram | highest | Trading, but every order lands in a DM. No storefront at all. |
| No website | high | Nothing to migrate, everything to gain. |
| Website is down | high | Paying for something that does not load. |
| Real site, but no way to buy | medium | Orders still go through phone calls. |
| Modern site with checkout | low | Not your customer. |

## Run it

One command:

```bash
./scripts/setup.sh    # only if Go or Docker are missing; safe to re-run
./run.sh
```

Then open **http://localhost:8080**, click **Find leads**, and try `bakery` in
`Kochi`.

`run.sh` starts Postgres and Redis, pulls the Maps scraper image, builds, and runs
the API, the worker, and the dashboard in a single process. Ctrl-C stops it.

If port 8080 is taken: `PORT=8099 ./run.sh`.

## What you get

A real run against `bakery in Kochi` returns things like:

```
100  NO-WEB   Jaya Bakery              no website, 88 reviews
 95  NO-WEB   Society Bakery           no website
 84  SOCIAL   Beurre De Vanille        instagram.com/beurredevanille
 62           Kunjus JamRolls          jamrolls.com, not mobile friendly
 44           Supreme Bakers           supremebakers.in, no way to order
```

Each lead comes with the gaps that fired and a pitch line, e.g.

> Beurre De Vanille is a top lead. They sell through instagram with no real
> storefront, so every order goes through DMs. Show them a makeforme.in link they
> can put in their bio today and take payments straight away.

## How it works

A scrape job fans out into a chain of queued jobs, one per business:

```
collect_business  →  website_crawl  →  ai_audit  →  rule_scoring  →  gen_recommendation
                          │                              ↑
                    (no website, social-only,            │
                     or site unreachable) ───────────────┘
```

- **collect_business** runs a source adapter and bulk-upserts what it finds,
  deduping on `(source, source_key)` — Google's `place_id` for Maps results.
- **website_crawl** fetches the landing page plus up to four pages whose URLs
  suggest contact details, and extracts emails, phone numbers, WhatsApp links,
  social profiles, and a technology fingerprint in one pass, while the HTML is
  still in memory.
- **ai_audit** is optional. With no AI provider the chain skips to scoring.
- **rule_scoring** assembles everything known about the business and scores it.
- **gen_recommendation** folds the AI's critique into the sales pitch.

`extract_contacts`, `extract_technology`, and `find_socials` also exist as
standalone job types for reprocessing an existing crawl. They read stored HTML,
so they need `CRAWLER_STORE_HTML=true`.

## Sources

### google_maps (default, no API key)

Drives [gosom/google-maps-scraper](https://github.com/gosom/google-maps-scraper)
as a sibling container. It automates a headless Chromium — there is no API key
and no billing.

```bash
curl -X POST localhost:8080/api/v1/scrape \
  -H 'Content-Type: application/json' \
  -d '{"source":"google_maps","category":"bakery","location":"Kochi","limit":20}'
```

**Two things worth knowing:**

The image is pinned to the **`-rod` tag**, not `:latest`. Every Playwright-based
tag of that image is currently broken upstream: they pin a driver version that
only ever existed on `playwright.azureedge.net`, a CDN Microsoft has retired, so
the container dies on startup with `could not install driver ... 404`. The `-rod`
build drives Chromium over CDP with go-rod and downloads no driver.

The headless browser is **the heaviest thing this project runs** — gosom's own
Kubernetes example asks for 512Mi per instance. `GMAPS_CONCURRENCY` defaults to
`1` for that reason. On a small machine, close your browser while a scrape runs.

### csv

Always available. Drop a file in `data/` and point at it. Header names are matched
loosely, so `Business Name`, `business_name`, and `Company` all land on the same
field.

```bash
curl -X POST localhost:8080/api/v1/scrape \
  -H 'Content-Type: application/json' \
  -d '{"source":"csv","file":"my-leads.csv","category":"bakery"}'
```

### google_places (optional)

The official, paid Places API, registered as a separate source. Not needed if you
are using `google_maps`. Set `GOOGLE_PLACES_API_KEY` to enable it.

## Scoring

Eleven weighted rules. A rule firing means the gap is present, which is a reason
to call. Rules count toward the maximum **only when they could apply** — a business
with no website cannot have a broken one — so the percentage is always a share of
what was actually achievable.

| Rule | Weight |
|---|---|
| `social_only` | 32 |
| `no_website` | 30 |
| `broken_website` | 25 |
| `not_mobile_friendly` | 15 |
| `no_booking` | 12 |
| `ssl_missing` | 10 |
| `no_contact_form` | 8 |
| `email_missing` | 6 |
| `no_social_links` | 5 |
| `meta_missing` | 4 |
| `phone_missing` | 2 |

`>=60%` is high priority, `>=30%` medium, below that low.

## API

The dashboard is a client of this API and nothing more. Data endpoints sit behind
`X-API-Key` when `API_KEY` is set; leave it unset and the API is unauthenticated,
which is fine on localhost and nowhere else.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/` | the dashboard |
| `GET` | `/api/v1/health` | liveness |
| `GET` | `/api/v1/ready` | pings Postgres and Redis, reports queue depth |
| `POST` | `/api/v1/scrape` | queue a scrape |
| `GET` | `/api/v1/scrape` | list scrape jobs |
| `GET` | `/api/v1/scrape/{id}` | one job's progress |
| `GET` | `/api/v1/scrape/sources` | which sources are configured |
| `GET` | `/api/v1/leads` | **scored leads, ranked** |
| `GET` | `/api/v1/businesses` | filter and paginate |
| `GET` | `/api/v1/businesses/{id}` | one business, fully enriched |

On the detail response, `website` is the business's URL string and `site` is what
the crawler found. They are separate fields on purpose: naming both `website` made
the nested object shadow the URL and silently drop it.

## AI audit (optional)

Leads are fully scored by rules without this. The AI pass only adds a written
critique and a services-to-pitch list.

```bash
# Google AI Studio (aistudio.google.com/apikey)
AI_PROVIDER=gemini
AI_API_KEY=AIza...
AI_MODEL=gemini-2.0-flash
```

Any OpenAI-compatible endpoint works too — OpenRouter, vLLM, Ollama:

```bash
AI_PROVIDER=openai
AI_BASE_URL=https://openrouter.ai/api/v1
AI_API_KEY=sk-or-v1-...
AI_MODEL=google/gemini-2.0-flash-001
```

The model must support JSON mode; the audit relies on it.

Only the page's visible text is sent, truncated to `AI_MAX_HTML_CHARS`. Markup is
most of a page's bytes and almost none of its meaning.

**Do not point this at a local Ollama on a small machine.** Two cores with no
usable GPU offload means minutes per lead on a prompt this size.

## Running the pieces separately

`run.sh` is one process doing everything, which is what you want on a small box.
To split it:

```bash
make dev-deps      # postgres + redis only
make run           # API + dashboard only   (--api)
make run-worker    # worker only            (--worker)
```

Or the full containerised stack:

```bash
make docker-up
```

Note the worker container launches the Maps scraper as a *sibling* container, so
it needs `/var/run/docker.sock` mounted (compose does this) and the container user
must be able to read it. If the daemon is unreachable the worker says so at
startup rather than failing every job. Running the worker natively avoids this
entirely.

## Notes for a small machine

Sized for a 2-core, ~7 GB box that is also running a desktop:

- The crawl and scoring work is **network-bound, not CPU-bound**. `WORKER_CONCURRENCY=4`
  is fine on two cores; the ceiling is the politeness delay between requests.
- The **Maps scrape is the exception** — it is a real browser. Keep
  `GMAPS_CONCURRENCY=1` and close other apps.
- `./run.sh` compiles natively. `make docker-up` recompiles Go *inside* the image
  every time, which is painful on two cores.
- `docker-compose.yml` sets memory limits on every service deliberately.
- `CRAWLER_STORE_HTML` defaults to off. Raw HTML is by far the largest column in
  the schema.

## Configuration

Everything is environment variables; see [.env.example](.env.example) for the full
list with comments.

## Development

```bash
make test          # go test ./...
make lint          # golangci-lint
make build         # build/server
```

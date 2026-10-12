# The admin UI

meta serves a small web admin at its root: open `http://localhost:8901/` (or wherever meta listens). It is built into the binary and the image; nothing else needs to run.

## Signing in

The admin signs in with the **admin token**: the one in `META_ADMIN_TOKEN`, or the one meta printed on its first start (see [configuration](configuration.md#admin-token)). The page checks the token with `GET /v1/whoami` and only accepts the admin token: client API keys and keys from the deprecated `api_keys` setting are refused.

The token is kept in the browser tab's session storage only (no cookies) and sent as `Authorization: Bearer …` with every API call. Closing the tab signs out. If meta stops accepting the token (it was changed, or `meta admin-token reset` ran), the page returns to the sign-in form.

## Pages

| Page | What |
|---|---|
| **Catalogue** | Browse every entry with filters (type, status, content rating, genres, tags with or without, years, original and title language) and sort orders, or search titles in every language and the people credited. With a search engine, the filters show how many matches each value has. Each entry shows who its data comes from |
| **Entry** | Everything meta knows about one entry: titles, descriptions, people and companies, tags (spoiler tags hidden until asked for), relations, links, external IDs, the provider records it's built from with their attribution, which provider each field came from, and each provider's stats. "Refresh from providers" asks lookup-capable plugins for fresh data |
| **Providers** | The configured metadata plugins: health, ingestion types, schedules, record counts and last runs, with **Run now** per sync type (optionally forced). Below, every sync run with its counts, duration and live progress while it runs; **Details** shows notices, errors, cursors and the snapshot |
| **Jobs** | Every background job, grouped: provider syncs (one per provider and sync type), the entry rebuild and the stale-entry refresh. Each shows its schedule and where it is configured, the last run with its outcome, and the next run. **Run now** asks for the job's parameters (e.g. `force`, `limit`) first; a job that is already running answers with a link to that run. **History** lists a job's runs, filterable by status; a run's details show its trigger and who started it, parameters, counts, notes and live progress, with **Cancel run** while it runs |
| **Duplicates** | Pairs of entries that are probably the same work, side by side with what the score is made of and the decision model's rating; **Confirm** (merge, choosing the entry that stays), **Dismiss**, **Reopen**; **Find duplicates now**; the decision model's settings (see [duplicates](duplicates.md)) |
| **Clients** | API clients and their scopes. A key is shown once, when the client is created or its key rotated |
| **Integrations** | Challenge solvers, network profiles (proxy, solver, limits, user agent, headers, DNS over HTTPS, timeouts), the default profile and the profile of each provider, with where its settings come from (see [integrations](integrations.md)). A profile pushed by a host is shown with its proxy, solver, resolver and when it was pushed, and **Remove pushed profile** drops it when meta runs on its own again. Solvers no profile uses are flagged, with a one-click "Use for all providers". **Search engine**: the backend in force and where its settings come from, what it can do, its index (documents, last build), changes waiting and errors; **Change** (with **Test connection**; the key is stored encrypted and never shown), **Rebuild index**, **Use the configuration file** (see [search](search.md)) |
| **Health** | Every part of meta checked now: the database, the search backend and each provider's plugin, with status, message, latency and details. The top bar shows a badge when meta is degraded or down (see [observability](observability.md)) |

## Security

The UI is a static single-page app served under a strict Content-Security-Policy:

```
default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob: https:;
connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'
```

Scripts, styles and API calls stay on meta's origin. Images may also come from `https:` URLs because entry covers are hosted by the metadata providers. Every response also carries `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and `X-Frame-Options: DENY`. The page itself loads without credentials; everything it shows comes from API calls that need the admin token.

## Developing

The UI lives in [`admin/`](../admin): React with TanStack Router and Query, built by Vite. Its API types are generated from meta's OpenAPI document (`make gen`, or `npm run gen:api` in `admin/`).

```sh
make admin-setup      # npm ci in admin/ (Node 22+)
make admin-dev        # dev server on :5173, proxying the API to META_BACKEND (default http://localhost:8901)
make admin            # build admin/dist, which `make build` embeds into the binary
make admin-check      # lint, type-check, API types up to date, tests
```

Without a running meta, `npm run dev:mock` in `admin/` answers the API from made-up data in the browser: sign in with any token (`bad` is refused, `mtk_…` is treated as a client key, `config-key` as an `api_keys` key).

A binary built without the UI (`go build` without `make admin`) still runs; its root page says how to build the UI.

# The meta API

meta serves one API on one port (default `:8901`) in several protocols:

| Protocol | For | Example |
|---|---|---|
| **REST/JSON** | browsers, scripts, `curl` | `GET /v1/entries?sort=SORT_FIELD_RATING` |
| **gRPC** | services (the canonical protocol between Panelarr services) | `panelarr.meta.v1.MetaService/Browse` |
| gRPC-Web, Connect | web clients | `POST /panelarr.meta.v1.MetaService/Browse` |

The protos are the contract: [`proto/panelarr/meta/v1`](../proto/panelarr/meta/v1). The OpenAPI 3.1 document is generated from them, committed at [`gen/openapi/meta.openapi.yaml`](../gen/openapi/meta.openapi.yaml), and served at `GET /openapi.yaml`. gRPC server reflection is on, so `grpcurl` can list the services.

## Authentication

Every call sends a bearer token: `Authorization: Bearer <token>` (gRPC: `authorization` metadata). `/healthz`, `/readyz` and `/openapi.yaml` need none.

| Credential | Looks like | Access |
|---|---|---|
| **Admin token** | `mta_…` or your own | Everything. From `META_ADMIN_TOKEN`, or generated on first start and printed once (see [configuration](configuration.md#admin-token)) |
| **Client API key** | `mtk_…` | The client's scopes |
| Key from the `api_keys` setting | anything | Everything. **Deprecated**: create clients instead |

### Clients and scopes (`MetaClientsService`)

Each caller (a Panelarr server, a script) gets its own **client** with an API key and scopes. Keys are shown once, when created or rotated, and stored only as hashes.

| Scope | Allows |
|---|---|
| `catalog:read` | `MetaService`: browse, search, entry details, lookups, tags |
| `metrics:read` | `/metrics` when it's served on the API port |
| `admin` | Everything, including `MetaAdminService`, `MetaClientsService` and gRPC reflection |

Anything not listed needs `admin`. A missing or wrong token gets `unauthenticated` (HTTP 401), a missing scope `permission_denied` (403).

| Call | REST | What |
|---|---|---|
| `WhoAmI` | `GET /v1/whoami` | Who the token belongs to (any valid token) |
| `ListClients` | `GET /v1/admin/clients` | Clients with key prefix, created, rotated and last-used times; never keys |
| `CreateClient` | `POST /v1/admin/clients` `{"name", "scopes"}` | Returns the key, once |
| `UpdateClient` | `PATCH /v1/admin/clients/{client_id}` | Rename or change scopes; the key stays |
| `RotateClientKey` | `POST /v1/admin/clients/{client_id}/rotate` | New key, once; the old one stops working at once |
| `DeleteClient` | `DELETE /v1/admin/clients/{client_id}` | The key stops working at once |

```sh
curl -X POST -H "Authorization: Bearer $META_ADMIN_TOKEN" \
  -d '{"name": "library", "scopes": ["catalog:read"]}' http://localhost:8901/v1/admin/clients
```

## Catalogue (`MetaService`)

| Call | REST | What |
|---|---|---|
| `Browse` | `GET /v1/entries` | List entries with filters and a sort order, for discovery pages |
| `Search` | `GET /v1/search?query=…` | Find entries by title (every language, alternative titles) and by people |
| `GetEntry` | `GET /v1/entries/{id}` | Full details with provenance and attribution |
| `LookupExternal` | `GET /v1/lookup/{provider}/{external_id}` | The entry for another site's ID, e.g. `/v1/lookup/anilist/30013` |
| `ListTags` | `GET /v1/tags?genres_only=true` | Tags and genres with entry counts, for filter menus |

### Filters and sorting

Browse and Search take the same filter. In REST, nested fields use dots and repeated fields repeat the key:

```sh
curl -H "Authorization: Bearer $KEY" \
  'http://localhost:8901/v1/entries?filter.types=ENTRY_TYPE_MANHWA&filter.genres=Romance&filter.statuses=ENTRY_STATUS_COMPLETED&filter.yearFrom=2015&sort=SORT_FIELD_RATING&pageSize=24'
```

| Filter | Matches |
|---|---|
| `genres` | any of the genres |
| `tags` | **all** of the tags |
| `exclude_tags` | none of the tags or genres |
| `types`, `statuses`, `content_ratings` | any of the values |
| `languages` | original language, any of them (`ja`, `ko`, …) |
| `year_from`, `year_to` | start year, inclusive |
| `title_languages` | has a title in one of the languages |
| `include_inactive` | also merged and removed entries |

Sorts: `SORT_FIELD_POPULARITY` (Browse default), `RATING`, `UPDATED`, `YEAR`, `TITLE`, and `RELEVANCE` (Search default). `reverse` flips the order. Paging: `page_size` (1–100, default 24) and the opaque `next_page_token`. `total_size` counts every match. Search considers the best 1,000 title matches and 1,000 name matches.

`language` (BCP 47) picks display titles: an entry with a primary title in that language shows it, others keep their default title.

`no_providers` is true when meta has no metadata plugins configured, so an empty page means "nothing set up yet", not "nothing matched".

### Entries

An entry merges what every provider knows about one work. Alongside the data it carries:

- `sources`: the provider records behind it, each with its **attribution** (shown wherever the data is used), when meta fetched it and when the provider last changed it;
- `field_sources`: which provider each field came from (or `manual` for curator locks);
- `stats`: each provider's numbers, kept apart because they aren't comparable.

IDs of entries that were merged into another keep working: `GetEntry` returns the surviving entry and says so in `redirected_from`. When an entry's provider records are older than `ingest.stale_after`, reading it triggers a background refresh through providers that support lookups.

`LookupExternal` first checks the IDs meta knows. If none matches (or `refresh=true`), it asks the provider's plugin, then any plugin that can resolve other sites' IDs, stores what comes back and returns the entry.

## Administration (`MetaAdminService`)

| Call | REST | What |
|---|---|---|
| `ListProviders` | `GET /v1/admin/providers` | Plugins, what they support, health, last runs, record counts |
| `RunSync` | `POST /v1/admin/providers/{provider}/sync` with `{"kind": "dump", "force": false}` | Start a sync now; returns the run at once (409 if one is running) |
| `ListSyncRuns` | `GET /v1/admin/sync-runs?provider=…` | Runs, newest first |
| `GetSyncRun` | `GET /v1/admin/sync-runs/{id}` | One run: status, counts, progress, notices, error |
| `GetHealth` | `GET /v1/admin/health` | Database, search backend and every plugin, with status, latency and details ([observability](observability.md)) |
| `RefreshEntry` | `POST /v1/admin/entries/{id}/refresh` | Fetch fresh records for one entry from providers that support lookups |

## Errors

Errors use gRPC/Connect codes; REST maps them to HTTP statuses (`invalid_argument` → 400, `unauthenticated` → 401, `not_found` → 404, `already_exists` → 409, `resource_exhausted` → 429, `unavailable` → 503).

# Integrations: solvers and network profiles

Metadata plugins fetch from the internet: dumps, APIs, web pages. **Integrations** decide how they connect: through a proxy or a VPN container, with a challenge solver for anti-bot pages, at what rate and with which user agent. Everything is optional: with no integrations, every plugin connects directly with its own defaults.

All of it is managed with `MetaIntegrationsService` (the `admin` scope) or on the admin's Integrations page.

## The chain

1. A **challenge solver** is a FlareSolverr or Byparr endpoint. It only does something through a profile.
2. A **network profile** bundles a proxy, a solver, a rate limit, concurrency, user agent, extra headers and timeouts.
3. Each **provider** uses one profile:

   | Order | Origin | Set with |
   |---|---|---|
   | 1 | **Pushed** by a host (a Panelarr server that owns network settings) | `SetPushedProfile` |
   | 2 | **Assigned** to the provider | `AssignProfile` |
   | 3 | The **default** profile | `SetDefaultProfile` |
   | 4 | **None**: a direct connection | |

4. meta hands the profile in effect to each plugin that declares the `network_profile` common service, whenever it changes and when the plugin connects. `ListAssignments` shows, per provider, the profile in effect, where it comes from, and whether the plugin supports profiles (`supported`).

**A solver alone does nothing.** To make that hard to miss:
- Adding the first solver while there are no profiles at all also creates a profile `default` that uses it (user agent "from the solver") and makes it the default, so every provider uses it at once. `PutSolver` says so in `created_default_profile_id`.
- Each solver lists the profiles that use it (`used_by`). meta logs a warning at startup for solvers no profile uses, and the admin flags them.

## Solvers

| Field | What |
|---|---|
| `id`, `name` | ID: lowercase letters, digits and dashes |
| `type` | FlareSolverr or Byparr (they speak the same API) |
| `url` | Base URL, e.g. `http://flaresolverr:8191` |
| `session_ttl` | How long clearance cookies from one solve are reused |
| `max_timeout` | Upper bound for one solve |
| `as_response_fallback` | When a fresh request still fails after solving, use the solver's own page |

A solver can't be deleted while a profile uses it.

## Network profiles

| Field | What |
|---|---|
| `proxy` | `http://`, `https://` or `socks5://` URL, with username and password in their own fields (not in the URL). A VPN is a container such as gluetun that exposes a proxy |
| `solver_id` | The solver to use, if any |
| `rate_limit` | Requests per period, optionally counted per target host |
| `concurrency` | Maximum parallel requests per provider |
| `user_agent_mode` | Plugin default, a fixed value, a random desktop browser, or the solver's user agent (matches its cookies) |
| `headers` | Extra headers on every request |
| `connect_timeout`, `read_timeout`, `call_timeout` | Timeouts |

**Proxy passwords** are write-only: they are encrypted at rest (AES-256-GCM, with `META_SECRET_KEY` or the generated key in `data_dir`, see [configuration](configuration.md)) and never returned; `has_password` says whether one is stored. Leaving the password empty on an update keeps the stored one; removing the username or the proxy drops it.

Deleting a profile removes its assignments, and clears the default when it was the default.

## API

| Call | REST |
|---|---|
| `ListSolvers`, `PutSolver`, `DeleteSolver` | `GET /v1/admin/solvers`, `PUT /v1/admin/solvers/{solver.id}`, `DELETE /v1/admin/solvers/{solver_id}` |
| `ListProfiles`, `PutProfile`, `DeleteProfile` | `GET /v1/admin/profiles`, `PUT /v1/admin/profiles/{profile.id}`, `DELETE /v1/admin/profiles/{profile_id}` |
| `ListAssignments`, `AssignProfile` | `GET /v1/admin/assignments`, `PUT /v1/admin/assignments/{provider_id}` |
| `SetDefaultProfile` | `PUT /v1/admin/default-profile` |
| `SetPushedProfile` | `PUT /v1/admin/pushed-profiles/{provider_id}` |

```sh
T="Authorization: Bearer $META_ADMIN_TOKEN"
curl -X PUT -H "$T" -d '{"name": "FlareSolverr", "url": "http://flaresolverr:8191"}' localhost:8901/v1/admin/solvers/flaresolverr
curl -X PUT -H "$T" -d '{"name": "VPN NL", "proxy": {"url": "http://gluetun:8888"}, "solverId": "flaresolverr"}' localhost:8901/v1/admin/profiles/vpn-nl
curl -X PUT -H "$T" -d '{"profileId": "vpn-nl"}' localhost:8901/v1/admin/assignments/mangabaka
```

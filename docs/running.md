# Running meta

## The image

`panelarr/meta` is a small static image (distroless, non-root, about 27 MB) with the [admin UI](admin.md) built in. It runs `meta serve` by default and listens on port 8901: open `http://<host>:8901/` and sign in with the admin token.

```sh
docker run -d --name meta -p 8901:8901 \
  -e META_DATABASE_URL='postgres://meta:secret@db:5432/meta?sslmode=disable' \
  -e META_ADMIN_TOKEN=mta_change-me \
  -e META_CONFIG=/etc/meta/meta.yaml -v ./meta.yaml:/etc/meta/meta.yaml:ro \
  panelarr/meta
```

Build it yourself with `make docker`. The image's health check runs `meta healthcheck`, which probes `/healthz` on the port in `META_LISTEN`.

Other commands run in the same image: `docker exec meta /meta sync mangabaka`, `docker exec meta /meta migrate status`.

## The compose example

[`compose.yaml`](../compose.yaml) runs PostgreSQL, meta and the MangaBaka plugin (standalone, reached over gRPC on port 8903 inside the compose network). Its meta configuration is [`compose/meta.yaml`](../compose/meta.yaml). The quick start in the [README](../README.md#quick-start) walks through it.

| Variable | Default | What |
|---|---|---|
| `META_ADMIN_TOKEN` | required | meta's admin token |
| `MANGABAKA_PLUGIN_KEY` | required | Key meta uses to call the plugin |
| `POSTGRES_PASSWORD` | `meta` | Database password |
| `META_PORT` | `8901` | Published port of meta |
| `META_TAG`, `MANGABAKA_TAG` | `dev` | Image tags |

## In production

- **Database:** give meta its own database on your PostgreSQL server. Its role needs to own that database and nothing more: no superuser, no server-wide settings. Point `META_DATABASE_URL` at it. See [configuration](configuration.md#database).
- **Data directory:** mount `/data` (it holds the generated `secret.key`), or set `META_SECRET_KEY`. Back the key up with the database: stored proxy passwords can't be read without it.
- **Keys:** keep the admin token for yourself and give every caller a client with only the scopes it needs. The deprecated `api_keys` setting still works but grants full access.
- **Plugins:** run them as their own containers (standalone, ports 8903 and up) or let meta start them (managed). Give standalone plugins an API key.
- **Several replicas** can share one database: migrations take a lock, and so does each provider's sync, so a provider never syncs twice at the same time.
- **Health:** `/healthz` (process up) and `/readyz` (database reachable) need no key.
- **Upgrades:** meta applies new migrations at startup. `meta migrate status` shows where a database stands. Some upgrades also queue a rebuild of every entry from the stored provider records; `meta serve` runs it in the background after starting (a large catalogue takes a while, and the API keeps answering), or run `meta rebuild` beforehand.
- **Shutdown:** on SIGTERM meta stops scheduling, cancels running syncs (they are recorded as cancelled; what was committed stays) and drains requests.

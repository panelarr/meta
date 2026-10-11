# meta

**meta** is the metadata service of [Panelarr](https://github.com/panelarr), a self-hosted library for manga, manhwa, manhua and comics. It collects series metadata from **metadata plugins**, stores it in PostgreSQL, merges what different providers say into one record per series, and serves **browse, search and details** over the whole catalogue, not only what is already in someone's library.

meta is optional and works on its own. Any project that wants a searchable manga metadata catalogue can run it without the rest of Panelarr.

> **Status:** early development. Nothing is released yet. See [PROGRESS.md](PROGRESS.md).

## What is in this repo

| Path | What |
|---|---|
| `proto/panelarr/metadata/v1` | The **metadata plugin protocol**: what a plugin implements so meta can ingest from it |
| `proto/panelarr/meta/v1` | The **meta API**: browse, search, get, lookup by external ID, admin |
| `gen/` | Go, gRPC and Connect code generated from the protos, and the OpenAPI document |
| `sdk/` | The Go SDK for writing metadata plugins (`plugin.Main`), a host client, settings helpers and a conformance harness |
| `cmd/meta` | The meta service |
| `admin/` | The admin web UI (React), embedded into the binary |

Plugins live in their own repositories. A fresh meta has no plugins and an empty catalogue; you add the providers you want.

## Quick start

The fastest way to try meta is the compose example: PostgreSQL, meta and the [MangaBaka plugin](https://github.com/panelarr/meta-mangabaka). You need Docker with Compose, about 5 GB of free disk, and both repositories next to each other (the images are built locally until they are published).

```sh
git clone https://github.com/panelarr/meta
git clone https://github.com/panelarr/meta-mangabaka
cd meta

export META_ADMIN_TOKEN=mta_$(openssl rand -hex 24) MANGABAKA_PLUGIN_KEY=$(openssl rand -hex 16)
docker compose up -d --build

# Fill the catalogue from MangaBaka's nightly dump: downloads about 400 MB,
# then reads about half a million series (expect 10 to 15 minutes).
docker compose exec meta /meta sync mangabaka

# Browse and search.
curl -H "Authorization: Bearer $META_ADMIN_TOKEN" 'http://localhost:8901/v1/entries?sort=SORT_FIELD_POPULARITY&pageSize=5'
curl -H "Authorization: Bearer $META_ADMIN_TOKEN" 'http://localhost:8901/v1/search?query=one%20piece'
```

Or open `http://localhost:8901/` and sign in with the admin token: the [admin UI](docs/admin.md) browses the catalogue, runs syncs and manages clients and integrations.

Give other callers their own key with only the scopes they need (`POST /v1/admin/clients`, see [docs/api.md](docs/api.md#clients-and-scopes-metaclientsservice)). After that, the dump is synced every night at 03:00 UTC. A sync can also be started over the API (`POST /v1/admin/providers/mangabaka/sync`). MangaBaka's data is CC BY-NC-SA 4.0: every entry carries its attribution, and the data must not be redistributed.

### Without Docker

```sh
make build
export META_DATABASE_URL='postgres://meta:secret@localhost:5432/meta?sslmode=disable'
bin/meta serve --config meta.example.yaml
```

meta listens on `:8901`. With no providers configured it runs with an empty catalogue and says so in its responses (`no_providers`). See [docs/running.md](docs/running.md) and [docs/configuration.md](docs/configuration.md).

## Documentation

- [The admin UI](docs/admin.md)
- [Running meta](docs/running.md)

- [The API](docs/api.md)
- [Configuration](docs/configuration.md)
- [The plugin protocol](docs/protocol.md)
- [Writing a plugin](docs/writing-a-plugin.md)
- [The entry schema](docs/schema.md)

## Licence

meta is free software under the **GNU General Public License v3.0 or later** ([LICENSE](LICENSE)).

The parts other programs build on are under the **Apache License 2.0**, so plugins and clients can use any licence they like:

- `proto/`: the protocol definitions;
- `gen/`: the code generated from them;
- `sdk/`: the plugin SDK and the conformance harness.

Each of these directories has its own `LICENSE`, and hand-written source files carry an SPDX header saying which licence applies.

Metadata that meta ingests keeps the licence of the provider it came from. meta stores and shows attribution for every record, and it never ships provider data.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: [SECURITY.md](SECURITY.md).

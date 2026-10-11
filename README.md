# meta

**meta** is the metadata service of [Panelarr](https://github.com/panelarr), a self-hosted library for manga, manhwa, manhua and comics. It collects series metadata from **metadata plugins**, stores it in PostgreSQL, merges what different providers say into one record per series, and serves **browse, search and details** over the whole catalogue, not only what is already in someone's library.

meta is optional and works on its own. Any project that wants a searchable manga metadata catalogue can run it without the rest of Panelarr.

> **Status:** early development. Nothing is released yet. See [PROGRESS.md](PROGRESS.md).

## What is in this repo

| Path | What |
|---|---|
| `proto/panelarr/metadata/v1` | The **metadata plugin protocol**: what a plugin implements so meta can ingest from it |
| `proto/panelarr/meta/v1` | The **meta API**: browse, search, get, lookup by external ID, admin |
| `sdk/` | The Go SDK for writing metadata plugins (`plugin.Serve`) and a conformance harness |
| `cmd/meta` | The meta service |

Plugins live in their own repositories. A fresh meta has no plugins and an empty catalogue; you add the providers you want.

## Licence

meta is free software under the **GNU General Public License v3.0 or later** ([LICENSE](LICENSE)).

The parts other programs build on are under the **Apache License 2.0**, so plugins and clients can use any licence they like:

- `proto/`: the protocol definitions;
- `gen/`: the code generated from them;
- `sdk/`: the plugin SDK and the conformance harness.

Each of these directories has its own `LICENSE`, and every file carries an SPDX header saying which licence applies.

Metadata that meta ingests keeps the licence of the provider it came from. meta stores and shows attribution for every record, and it never ships provider data.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: [SECURITY.md](SECURITY.md).

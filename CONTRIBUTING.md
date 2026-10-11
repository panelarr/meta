# Contributing

Thanks for helping. This page covers the development setup and how changes get in.

## Development setup

You need Go (the version in `go.mod`; the `go` command downloads it if yours is older), Docker (tests start PostgreSQL in a container) and `make`.

```sh
make setup   # installs pinned tools (buf, sqlc, golangci-lint, govulncheck, protoc plugins) into ./bin
make check   # format check, vet, lint, govulncheck and tests: run this before every PR
make help    # lists every target
```

### Tests and PostgreSQL

Tests run against a real PostgreSQL, never a substitute. By default they start a `postgres` container with testcontainers. To use a server you already run, point `META_TEST_DATABASE_URL` at it. The role needs `CREATEDB` (and nothing more): each test creates and drops its own database.

```sh
export META_TEST_DATABASE_URL='postgres://meta:meta@localhost:5432/postgres?sslmode=disable'
make test
```

`make test-short` skips everything that needs PostgreSQL or Docker.

### Generated code

Protobuf and SQL code is generated and committed. After changing `proto/`, `internal/store/migrations/` or `internal/store/queries/`, run `make gen` and commit the result. CI fails when the generated code is stale.

### No provider data

Never commit metadata dumps, extracts or real records from any provider, in code, tests, fixtures or docs. Tests use made-up data.

## Working conventions

- **Tasks:** everything runs through `make`.
- **Branches:** `<type>/<scope>-<slug>`, e.g. `feat/api-browse-filters`.
- **Commits and PR titles:** [Conventional Commits](https://www.conventionalcommits.org/): `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `perf`, `build`, `ci`. Add `!` for breaking changes. PRs are squash-merged, so the PR title becomes the commit message.
- **Reviews:** at least one approval and green checks before merge.
- **AI agents:** agents may open PRs and post reviews. Anything an agent posts from a person's account starts with a **⚠️ AI agent** line. Humans stay responsible for what they merge.

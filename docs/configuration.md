# Configuration

> This page grows with each part of meta. Today it covers the database.

## Database

meta stores everything in **PostgreSQL** (version 14 or newer; CI runs 18). Point it at a database with a URL:

```sh
export META_DATABASE_URL='postgres://meta:secret@db:5432/meta?sslmode=disable'
meta migrate        # apply migrations (meta also applies them at startup)
meta migrate status # list migrations
meta migrate down   # roll back the latest one
```

meta keeps everything **inside its own database**, so it can share a PostgreSQL server with other services:

- The role needs to own the database, or have `CREATE` on it. It needs **no superuser** rights and changes no server-wide settings.
- The only extension is `pg_trgm` (for search). It is a *trusted* extension, so the database owner can create it. If your server disallows it, a superuser can run `CREATE EXTENSION pg_trgm` in meta's database once.
- To keep meta's tables in their own schema, create the schema and add it to the URL: `...?search_path=meta`.
- Migrations take a PostgreSQL advisory lock, so several meta instances can start at once safely.

Setting up a database on a shared server, as a superuser:

```sql
CREATE ROLE meta LOGIN PASSWORD 'secret';
CREATE DATABASE meta OWNER meta;
```

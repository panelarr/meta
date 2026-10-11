# meta admin UI

The web admin meta serves at `/`: React 19 with TanStack Router and Query, built by Vite, and embedded into the meta binary (`embed.go`). See [docs/admin.md](../docs/admin.md) for what it does.

```sh
npm ci                 # dependencies (Node 22+); or `make admin-setup` from the repo root
npm run dev            # dev server on :5173, API proxied to META_BACKEND (default http://localhost:8901)
npm run dev:mock       # no backend: the API is answered from made-up data in src/mocks
npm run build          # dist/, checked for anything the CSP would block (`make admin`)
npm run lint           # Biome
npm run typecheck      # tsc
npm test               # Vitest + Testing Library, API mocked with MSW
npm run gen:api        # src/api/schema.d.ts from ../gen/openapi/meta.openapi.yaml (`make gen`)
```

## How it works

- **Auth:** the admin token is kept in `sessionStorage` (`meta.admin-token`), never a cookie, and sent as a bearer token. `GET /v1/whoami` checks it at sign-in; a 401 later signs the tab out.
- **API:** `openapi-fetch` with types generated from meta's OpenAPI document. Errors are meta's `google.rpc.Status` bodies (`{"code": 5, "message": …}`) and are shown as their message.
- **CSP:** no inline scripts or styles and no `eval`; `scripts/check-dist.mjs` fails the build otherwise. Covers load from `https:` provider URLs.
- **Layout:** pages under `src/pages`, shared pieces in `src/components` (dialogs, tabs, badges, fields), pure helpers with tests in `src/lib`.

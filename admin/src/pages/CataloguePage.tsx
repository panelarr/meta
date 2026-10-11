// SPDX-License-Identifier: GPL-3.0-or-later

import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { type FormEvent, useEffect, useState } from "react";
import { api, unwrap } from "../api/client";
import type { EntrySummary } from "../api/types";
import { Badge, Empty, ErrorBox, Field, Loading, PageHeader } from "../components/ui";
import {
  type CatalogueSearch,
  catalogueQuery,
  contentRatings,
  coverUrl,
  entryStatuses,
  entryTypes,
  sortFields,
} from "../lib/catalogue";
import { enumLabel } from "../lib/format";

const route = getRouteApi("/_app/catalogue");

type Page = {
  entries: EntrySummary[];
  matched: Map<string, string>;
  next?: string;
  total: number;
  noProviders: boolean;
};

async function fetchPage(search: CatalogueSearch, pageToken?: string): Promise<Page> {
  const query = catalogueQuery(search, pageToken);
  if (search.q) {
    const res = await unwrap(api.GET("/v1/search", { params: { query } }));
    const hits = res.hits ?? [];
    return {
      entries: hits.flatMap((hit) => (hit.entry ? [hit.entry] : [])),
      matched: new Map(
        hits.flatMap((hit) =>
          hit.entry?.id && hit.matched && hit.matched !== hit.entry.title ? [[hit.entry.id, hit.matched] as const] : [],
        ),
      ),
      next: res.nextPageToken || undefined,
      total: Number(res.totalSize ?? 0),
      noProviders: res.noProviders ?? false,
    };
  }
  const res = await unwrap(api.GET("/v1/entries", { params: { query } }));
  return {
    entries: res.entries ?? [],
    matched: new Map(),
    next: res.nextPageToken || undefined,
    total: Number(res.totalSize ?? 0),
    noProviders: res.noProviders ?? false,
  };
}

function useGenres() {
  return useQuery({
    queryKey: ["genres"],
    queryFn: async () =>
      (await unwrap(api.GET("/v1/tags", { params: { query: { genresOnly: true, pageSize: 1000 } } }))).tags ?? [],
    staleTime: 5 * 60_000,
  });
}

export function CataloguePage() {
  const search = route.useSearch();
  const navigate = useNavigate();
  const genres = useGenres();
  const [q, setQ] = useState(search.q ?? "");
  const [from, setFrom] = useState(search.from ? String(search.from) : "");
  const [to, setTo] = useState(search.to ? String(search.to) : "");
  const [lang, setLang] = useState(search.lang ?? "");

  // Keep the inputs in step when the URL changes (back button, links).
  useEffect(() => {
    setQ(search.q ?? "");
    setFrom(search.from ? String(search.from) : "");
    setTo(search.to ? String(search.to) : "");
    setLang(search.lang ?? "");
  }, [search.q, search.from, search.to, search.lang]);

  const results = useInfiniteQuery({
    queryKey: ["catalogue", search],
    queryFn: ({ pageParam }) => fetchPage(search, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
  });

  const go = (patch: Partial<CatalogueSearch>) =>
    void navigate({ to: "/catalogue", search: (prev: CatalogueSearch) => clean({ ...prev, ...patch }) });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    go({
      q: q.trim() || undefined,
      from: from ? Number(from) : undefined,
      to: to ? Number(to) : undefined,
      lang: lang.trim() || undefined,
    });
  };

  const pages = results.data?.pages ?? [];
  const entries = pages.flatMap((p) => p.entries);
  const matched = new Map(pages.flatMap((p) => [...p.matched]));
  const first = pages[0];

  return (
    <>
      <PageHeader title="Catalogue" />
      <p className="muted">
        Every entry meta knows, merged from its providers. Search looks through titles in every language and the people
        credited.
      </p>

      <form className="catalogue-filters" onSubmit={submit} aria-label="Catalogue filters">
        <Field label="Search">
          <input
            type="search"
            value={q}
            placeholder="Titles in any language, or people"
            onChange={(event) => setQ(event.target.value)}
          />
        </Field>
        <Field label="Type">
          <select value={search.type ?? ""} onChange={(e) => go({ type: (e.target.value || undefined) as never })}>
            <option value="">Any</option>
            {entryTypes.map((t) => (
              <option key={t} value={t}>
                {enumLabel(t, "ENTRY_TYPE_")}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Status">
          <select value={search.status ?? ""} onChange={(e) => go({ status: (e.target.value || undefined) as never })}>
            <option value="">Any</option>
            {entryStatuses.map((s) => (
              <option key={s} value={s}>
                {enumLabel(s, "ENTRY_STATUS_")}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Content rating">
          <select value={search.rating ?? ""} onChange={(e) => go({ rating: (e.target.value || undefined) as never })}>
            <option value="">Any</option>
            {contentRatings.map((r) => (
              <option key={r} value={r}>
                {enumLabel(r, "CONTENT_RATING_")}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Genre">
          <select value={search.genre ?? ""} onChange={(e) => go({ genre: e.target.value || undefined })}>
            <option value="">Any</option>
            {(genres.data ?? []).map((g) => (
              <option key={g.id ?? g.name} value={g.name}>
                {g.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="From year">
          <input type="number" min="1" max="9999" value={from} onChange={(e) => setFrom(e.target.value)} />
        </Field>
        <Field label="To year">
          <input type="number" min="1" max="9999" value={to} onChange={(e) => setTo(e.target.value)} />
        </Field>
        <Field label="Original language" hint='e.g. "ja", "ko", "zh"'>
          <input type="text" value={lang} onChange={(e) => setLang(e.target.value)} />
        </Field>
        <Field label="Sort">
          <select value={search.sort ?? ""} onChange={(e) => go({ sort: (e.target.value || undefined) as never })}>
            <option value="">{search.q ? "Relevance" : "Popularity"}</option>
            {sortFields.map((s) => (
              <option key={s.value} value={s.value}>
                {s.label}
              </option>
            ))}
          </select>
        </Field>
        <label className="filter-check">
          <input
            type="checkbox"
            checked={search.reverse ?? false}
            onChange={(e) => go({ reverse: e.target.checked || undefined })}
          />
          <span>Reverse order</span>
        </label>
        <div className="actions">
          <button type="submit" className="primary">
            {q.trim() ? "Search" : "Apply"}
          </button>
          <button type="button" onClick={() => void navigate({ to: "/catalogue", search: {} })}>
            Reset
          </button>
        </div>
      </form>

      {results.isPending ? <Loading /> : null}
      <ErrorBox error={results.error} onRetry={() => results.refetch()} />
      {first?.noProviders ? (
        <Empty>
          No providers configured, so the catalogue is empty. Add a metadata plugin to meta's configuration, then sync
          it on the <Link to="/providers">Providers</Link> page.
        </Empty>
      ) : null}
      {first && !first.noProviders && entries.length === 0 ? <Empty>Nothing matches.</Empty> : null}
      {first && entries.length > 0 ? (
        <p className="muted small" role="status">
          {first.total === 1 ? "1 entry" : `${first.total.toLocaleString()} entries`}
        </p>
      ) : null}
      {entries.length > 0 ? (
        <ul className="entry-grid">
          {entries.map((entry) => (
            <EntryCard key={entry.id} entry={entry} matched={matched.get(entry.id ?? "")} />
          ))}
        </ul>
      ) : null}
      {results.hasNextPage ? (
        <div className="pager">
          <button type="button" disabled={results.isFetchingNextPage} onClick={() => results.fetchNextPage()}>
            {results.isFetchingNextPage ? "Loading…" : "Load more"}
          </button>
        </div>
      ) : null}
    </>
  );
}

function clean(search: CatalogueSearch): CatalogueSearch {
  return Object.fromEntries(Object.entries(search).filter(([, v]) => v !== undefined && v !== "")) as CatalogueSearch;
}

export function Cover({
  entry,
  large = false,
}: {
  entry: { cover?: EntrySummary["cover"]; title?: string };
  large?: boolean;
}) {
  const url = coverUrl(entry.cover, large ? 350 : 250);
  return (
    <div className={large ? "cover cover-large" : "cover"}>
      {url ? (
        <img src={url} alt="" loading="lazy" referrerPolicy="no-referrer" />
      ) : (
        <span className="cover-placeholder">No cover</span>
      )}
    </div>
  );
}

function EntryCard({ entry, matched }: { entry: EntrySummary; matched?: string }) {
  const facts = [
    entry.type ? enumLabel(entry.type, "ENTRY_TYPE_") : undefined,
    entry.status && entry.status !== "ENTRY_STATUS_UNSPECIFIED" ? enumLabel(entry.status, "ENTRY_STATUS_") : undefined,
    entry.year ? String(entry.year) : undefined,
  ].filter(Boolean);
  const attribution = (entry.attributions ?? []).map((a) => a.providerName).filter(Boolean);
  return (
    <li>
      <Link to="/entries/$entryId" params={{ entryId: entry.id ?? "" }} className="entry-card">
        <Cover entry={entry} />
        <span className="entry-title">{entry.title || entry.id}</span>
      </Link>
      {matched ? <div className="muted small">Matched: {matched}</div> : null}
      <div className="muted small">{facts.join(" · ")}</div>
      {entry.genres && entry.genres.length > 0 ? (
        <div className="badges">
          {entry.genres.slice(0, 3).map((g) => (
            <Badge key={g}>{g}</Badge>
          ))}
        </div>
      ) : null}
      {attribution.length > 0 ? <div className="attribution small">Data: {attribution.join(", ")}</div> : null}
    </li>
  );
}

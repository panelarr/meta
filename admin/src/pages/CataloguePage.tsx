// SPDX-License-Identifier: GPL-3.0-or-later

import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { api, unwrap } from "../api/client";
import type { EntrySummary, Facet, Tag } from "../api/types";
import { MultiSelect, type Option } from "../components/MultiSelect";
import { Badge, Empty, ErrorBox, Field, Loading, PageHeader, SearchInput } from "../components/ui";
import {
  type CatalogueSearch,
  catalogueQuery,
  contentRatings,
  coverUrl,
  entryStatuses,
  entryTypes,
  facetCounts,
  labelFromKey,
  languageName,
  shortName,
  sortFields,
  tagLabel,
} from "../lib/catalogue";
import { enumLabel } from "../lib/format";

const route = getRouteApi("/_app/catalogue");

type Page = {
  entries: EntrySummary[];
  matched: Map<string, string>;
  next?: string;
  total: number;
  noProviders: boolean;
  facets?: Facet[];
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
      facets: res.facets,
    };
  }
  const res = await unwrap(api.GET("/v1/entries", { params: { query } }));
  return {
    entries: res.entries ?? [],
    matched: new Map(),
    next: res.nextPageToken || undefined,
    total: Number(res.totalSize ?? 0),
    noProviders: res.noProviders ?? false,
    facets: res.facets,
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

function useLanguages() {
  return useQuery({
    queryKey: ["languages"],
    queryFn: async () => unwrap(api.GET("/v1/languages")),
    staleTime: 5 * 60_000,
  });
}

/** Tag suggestions for typed text, fetched from the server (there can be thousands of tags). */
function useTagSuggestions(text: string, tagsOnly: boolean) {
  const [debounced, setDebounced] = useState(text);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(text), 250);
    return () => clearTimeout(t);
  }, [text]);
  return useQuery({
    queryKey: ["tag-suggestions", debounced, tagsOnly],
    queryFn: async () =>
      (await unwrap(api.GET("/v1/tags", { params: { query: { query: debounced.trim(), tagsOnly, pageSize: 20 } } })))
        .tags ?? [],
    placeholderData: (prev) => prev,
    staleTime: 60_000,
  });
}

function tagOptions(tags: Tag[]): Option[] {
  return tags.map((t) => ({
    value: t.key ?? t.name ?? "",
    label: t.isGenre ? `${t.name} (genre)` : tagLabel(t),
    detail: Number(t.entryCount ?? 0).toLocaleString(),
  }));
}

export function CataloguePage() {
  const search = route.useSearch();
  const navigate = useNavigate();
  const genres = useGenres();
  const languages = useLanguages();
  const [q, setQ] = useState(search.q ?? "");
  const [from, setFrom] = useState(search.from ? String(search.from) : "");
  const [to, setTo] = useState(search.to ? String(search.to) : "");
  const [tagText, setTagText] = useState("");
  const [xtagText, setXtagText] = useState("");
  const tagSuggestions = useTagSuggestions(tagText, true);
  const xtagSuggestions = useTagSuggestions(xtagText, false);
  // Labels of tags seen in suggestions, so chips keep them after the list changes.
  const labels = useRef(new Map<string, string>());
  for (const o of [...tagOptions(tagSuggestions.data ?? []), ...tagOptions(xtagSuggestions.data ?? [])]) {
    labels.current.set(o.value, o.label);
  }
  const labelOf = (key: string) => labels.current.get(key) ?? labelFromKey(key);

  // Keep the inputs in step when the URL changes (back button, links).
  useEffect(() => {
    setQ(search.q ?? "");
    setFrom(search.from ? String(search.from) : "");
    setTo(search.to ? String(search.to) : "");
  }, [search.q, search.from, search.to]);

  const results = useInfiniteQuery({
    queryKey: ["catalogue", search],
    queryFn: ({ pageParam }) => fetchPage(search, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
  });
  // Counts per filter value among the matches, when the search engine has them.
  const facets = results.data?.pages[0]?.facets;
  const counted = (field: string, value: string, prefix = "") => {
    const counts = facetCounts(facets, field);
    if (counts.size === 0) return "";
    return ` (${(counts.get(prefix ? shortName(value, prefix) : value) ?? 0).toLocaleString()})`;
  };

  const go = (patch: Partial<CatalogueSearch>) =>
    void navigate({ to: "/catalogue", search: (prev: CatalogueSearch) => clean({ ...prev, ...patch }) });

  const typed = (): Partial<CatalogueSearch> => ({
    q: q.trim() || undefined,
    from: from ? Number(from) : undefined,
    to: to ? Number(to) : undefined,
  });

  // Typed fields apply on their own after a pause; Enter applies at once.
  useEffect(() => {
    const patch = typed();
    if (patch.q === search.q && patch.from === search.from && patch.to === search.to) return;
    const t = setTimeout(() => go(patch), 500);
    return () => clearTimeout(t);
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    go(typed());
  };

  const pages = results.data?.pages ?? [];
  const entries = pages.flatMap((p) => p.entries);
  const matched = new Map(pages.flatMap((p) => [...p.matched]));
  const first = pages[0];
  const list = (values: string[]) => (values.length > 0 ? values : undefined);

  return (
    <>
      <PageHeader title="Catalogue" />
      <p className="muted">
        Every entry meta knows, merged from its providers. Search looks through titles in every language and the people
        credited.
      </p>

      <form className="catalogue-filters" onSubmit={submit} aria-label="Catalogue filters">
        <Field label="Search" className="span-all">
          <SearchInput
            value={q}
            placeholder="Titles in any language, or people"
            onChange={setQ}
            onClear={() => go({ q: undefined })}
          />
        </Field>
        <MultiSelect
          className="span-2"
          label="Genres"
          hint="Entries with any of them"
          placeholder="Any genre"
          values={search.genre ?? []}
          onChange={(v) => go({ genre: list(v) })}
          filterLocally
          options={(genres.data ?? []).map((g) => {
            const inResults = facetCounts(facets, "genres");
            return {
              value: g.name ?? "",
              label: g.name ?? "",
              detail:
                inResults.size > 0
                  ? `${(inResults.get(g.key ?? "") ?? 0).toLocaleString()} here`
                  : Number(g.entryCount ?? 0).toLocaleString(),
            };
          })}
        />
        <MultiSelect
          className="span-2"
          label="With tags"
          hint="Entries with all of them"
          placeholder="Type to find tags"
          values={search.tag ?? []}
          onChange={(v) => go({ tag: list(v) })}
          onQuery={setTagText}
          options={tagOptions(tagSuggestions.data ?? [])}
          labelFor={labelOf}
        />
        <MultiSelect
          className="span-2"
          label="Without tags"
          hint="Leave out entries with any of them"
          placeholder="Type to find tags or genres"
          values={search.xtag ?? []}
          onChange={(v) => go({ xtag: list(v) })}
          onQuery={setXtagText}
          options={tagOptions(xtagSuggestions.data ?? [])}
          labelFor={labelOf}
        />
        <Field label="Type">
          <select value={search.type ?? ""} onChange={(e) => go({ type: (e.target.value || undefined) as never })}>
            <option value="">Any</option>
            {entryTypes.map((t) => (
              <option key={t} value={t}>
                {enumLabel(t, "ENTRY_TYPE_")}
                {counted("type", t, "ENTRY_TYPE_")}
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
                {counted("status", s, "ENTRY_STATUS_")}
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
                {counted("content_rating", r, "CONTENT_RATING_")}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Original language">
          <select value={search.lang ?? ""} onChange={(e) => go({ lang: e.target.value || undefined })}>
            <option value="">Any</option>
            {languageOptions(languages.data?.original, search.lang)}
          </select>
        </Field>
        <Field label="Title language">
          <select value={search.tlang ?? ""} onChange={(e) => go({ tlang: e.target.value || undefined })}>
            <option value="">Any</option>
            {languageOptions(languages.data?.titles, search.tlang)}
          </select>
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
        <Field label="From year">
          <input type="number" min="1" max="9999" value={from} onChange={(e) => setFrom(e.target.value)} />
        </Field>
        <Field label="To year">
          <input type="number" min="1" max="9999" value={to} onChange={(e) => setTo(e.target.value)} />
        </Field>
        <div className="field">
          <span className="field-label">Order</span>
          <label className="check-control">
            <input
              type="checkbox"
              checked={search.reverse ?? false}
              onChange={(e) => go({ reverse: e.target.checked || undefined })}
            />
            <span>Reverse order</span>
          </label>
        </div>
        <div className="filter-actions">
          <button type="button" onClick={() => void navigate({ to: "/catalogue", search: {} })}>
            Reset
          </button>
          <button type="submit" className="primary">
            Search
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
            <EntryCard
              key={entry.id}
              entry={entry}
              matched={matched.get(entry.id ?? "")}
              onGenre={(g) => go({ genre: [...new Set([...(search.genre ?? []), g])] })}
            />
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

function languageOptions(list: { code?: string; entries?: string }[] | undefined, current?: string) {
  const items = [...(list ?? [])];
  // Keep a language from the URL selectable even before the list loads.
  if (current && !items.some((l) => l.code === current)) items.unshift({ code: current });
  return items.map((l) => (
    <option key={l.code} value={l.code}>
      {languageName(l.code ?? "")}
      {l.entries ? ` (${Number(l.entries).toLocaleString()})` : ""}
    </option>
  ));
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

const cardGenres = 3;

function EntryCard({
  entry,
  matched,
  onGenre,
}: {
  entry: EntrySummary;
  matched?: string;
  onGenre: (genre: string) => void;
}) {
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
          {entry.genres.slice(0, cardGenres).map((g) => (
            <button
              key={g}
              type="button"
              className="badge badge-neutral chip-button"
              title={`Show ${g}`}
              onClick={() => onGenre(g)}
            >
              {g}
            </button>
          ))}
          {entry.genres.length > cardGenres ? (
            <Badge title={entry.genres.slice(cardGenres).join(", ")}>+{entry.genres.length - cardGenres}</Badge>
          ) : null}
        </div>
      ) : null}
      {attribution.length > 0 ? <div className="attribution small">Data: {attribution.join(", ")}</div> : null}
    </li>
  );
}

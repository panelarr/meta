// SPDX-License-Identifier: GPL-3.0-or-later

// The catalogue page's filters live in the URL (so a view can be shared and the back
// button works) and turn into the query of GET /v1/entries or GET /v1/search.
import type { ContentRating, EntryStatus, EntryType, Facet, Image, SortField } from "../api/types";

export type CatalogueSearch = {
  q?: string;
  type?: EntryType;
  status?: EntryStatus;
  rating?: ContentRating;
  /** Genre names: an entry needs any of them. */
  genre?: string[];
  /** Tag keys ("tag/settings/school"): an entry needs all of them. */
  tag?: string[];
  /** Tag or genre keys or names to leave out. */
  xtag?: string[];
  from?: number;
  to?: number;
  /** Original language (BCP 47). */
  lang?: string;
  /** Has a title in this language. */
  tlang?: string;
  sort?: SortField;
  reverse?: boolean;
};

export const entryTypes: EntryType[] = [
  "ENTRY_TYPE_MANGA",
  "ENTRY_TYPE_MANHWA",
  "ENTRY_TYPE_MANHUA",
  "ENTRY_TYPE_NOVEL",
  "ENTRY_TYPE_LIGHT_NOVEL",
  "ENTRY_TYPE_WEB_NOVEL",
  "ENTRY_TYPE_OEL",
  "ENTRY_TYPE_COMIC",
  "ENTRY_TYPE_WESTERN_COMIC",
  "ENTRY_TYPE_GRAPHIC_NOVEL",
  "ENTRY_TYPE_WEBCOMIC",
  "ENTRY_TYPE_WEBTOON",
  "ENTRY_TYPE_DOUJINSHI",
  "ENTRY_TYPE_ONE_SHOT",
  "ENTRY_TYPE_ANTHOLOGY",
  "ENTRY_TYPE_OTHER",
];

export const entryStatuses: EntryStatus[] = [
  "ENTRY_STATUS_RELEASING",
  "ENTRY_STATUS_COMPLETED",
  "ENTRY_STATUS_HIATUS",
  "ENTRY_STATUS_CANCELLED",
  "ENTRY_STATUS_UPCOMING",
  "ENTRY_STATUS_UNKNOWN",
];

export const contentRatings: ContentRating[] = [
  "CONTENT_RATING_SAFE",
  "CONTENT_RATING_SUGGESTIVE",
  "CONTENT_RATING_EROTIC",
  "CONTENT_RATING_PORNOGRAPHIC",
];

export const sortFields: { value: SortField; label: string }[] = [
  { value: "SORT_FIELD_POPULARITY", label: "Popularity" },
  { value: "SORT_FIELD_RATING", label: "Rating" },
  { value: "SORT_FIELD_UPDATED", label: "Recently updated" },
  { value: "SORT_FIELD_YEAR", label: "Year" },
  { value: "SORT_FIELD_TITLE", label: "Title" },
  { value: "SORT_FIELD_RELEVANCE", label: "Relevance (search)" },
];

function oneOf<T extends string>(values: readonly T[], value: unknown): T | undefined {
  return values.find((v) => v === value);
}

function year(value: unknown): number | undefined {
  const n = typeof value === "number" ? value : typeof value === "string" ? Number(value) : Number.NaN;
  return Number.isInteger(n) && n > 0 && n < 10000 ? n : undefined;
}

function text(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() !== "" ? value : undefined;
}

/** A list from the URL: an array, or a single value from an older link. */
function list(value: unknown): string[] | undefined {
  const values = (Array.isArray(value) ? value : [value]).flatMap((v) => (text(v) ? [String(v)] : []));
  return values.length > 0 ? [...new Set(values)] : undefined;
}

export function parseCatalogueSearch(search: Record<string, unknown>): CatalogueSearch {
  return {
    q: text(search.q),
    type: oneOf(entryTypes, search.type),
    status: oneOf(entryStatuses, search.status),
    rating: oneOf(contentRatings, search.rating),
    genre: list(search.genre),
    tag: list(search.tag),
    xtag: list(search.xtag),
    from: year(search.from),
    to: year(search.to),
    lang: text(search.lang),
    tlang: text(search.tlang),
    sort: oneOf(
      sortFields.map((s) => s.value),
      search.sort,
    ),
    reverse: search.reverse === true || search.reverse === "true" ? true : undefined,
  };
}

/** The query parameters of GET /v1/entries and GET /v1/search for a catalogue view. */
export function catalogueQuery(search: CatalogueSearch, pageToken?: string) {
  const query: {
    query?: string;
    "filter.types"?: EntryType[];
    "filter.statuses"?: EntryStatus[];
    "filter.contentRatings"?: ContentRating[];
    "filter.genres"?: string[];
    "filter.tags"?: string[];
    "filter.excludeTags"?: string[];
    "filter.yearFrom"?: number;
    "filter.yearTo"?: number;
    "filter.languages"?: string[];
    "filter.titleLanguages"?: string[];
    sort?: SortField;
    reverse?: boolean;
    pageSize: number;
    pageToken?: string;
    facets?: boolean;
  } = { pageSize: 24 };
  if (search.q) query.query = search.q;
  if (search.type) query["filter.types"] = [search.type];
  if (search.status) query["filter.statuses"] = [search.status];
  if (search.rating) query["filter.contentRatings"] = [search.rating];
  if (search.genre?.length) query["filter.genres"] = search.genre;
  if (search.tag?.length) query["filter.tags"] = search.tag;
  if (search.xtag?.length) query["filter.excludeTags"] = search.xtag;
  if (search.from) query["filter.yearFrom"] = search.from;
  if (search.to) query["filter.yearTo"] = search.to;
  if (search.lang) query["filter.languages"] = [search.lang];
  if (search.tlang) query["filter.titleLanguages"] = [search.tlang];
  if (search.sort) query.sort = search.sort;
  if (search.reverse) query.reverse = true;
  if (pageToken) query.pageToken = pageToken;
  // Counts per filter value (a search engine has them), once per query.
  else query.facets = true;
  return query;
}

/** A cover URL sized for a card: the 250px variant at the screen's density when there is one. */
export function coverUrl(image: Image | undefined, width = 250): string | undefined {
  if (!image) return undefined;
  const density = typeof window !== "undefined" && window.devicePixelRatio >= 2 ? 2 : 1;
  const variants = image.variants ?? [];
  const exact = variants.find((v) => v.width === width && v.density === density && v.url);
  const sized = exact ?? variants.find((v) => v.width === width && v.url);
  return sized?.url || image.url || undefined;
}

/** "Settings › Fantasy": a tag's path and name. */
export function tagLabel(tag: { name?: string; path?: string[] }): string {
  return [...(tag.path ?? []), tag.name ?? ""].filter(Boolean).join(" › ");
}

/** A readable label for a tag key when the tag itself isn't loaded: "tag/settings/school" → "Settings › School". */
export function labelFromKey(key: string): string {
  const parts = key.split("/");
  if (parts[0] !== "tag" && parts[0] !== "genre") return key;
  return parts
    .slice(1)
    .map((p) => p.replace(/-/g, " ").replace(/\b\w/g, (c) => c.toUpperCase()))
    .join(" › ");
}

/** A language's English name ("ja" → "Japanese"), or the code when the browser doesn't know it. */
export function languageName(code: string): string {
  try {
    const name = new Intl.DisplayNames(["en"], { type: "language" }).of(code);
    return name && name !== code ? name : code;
  } catch {
    return code;
  }
}

/** Counts per value of one facet field ("type", "genres", ...); empty without facets. */
export function facetCounts(facets: Facet[] | undefined, field: string): Map<string, number> {
  const f = (facets ?? []).find((x) => x.field === field);
  return new Map((f?.values ?? []).map((v) => [v.value ?? "", Number(v.count ?? 0)]));
}

/** "ENTRY_TYPE_MANGA" → "manga", the short name facets use. */
export function shortName(value: string, prefix: string): string {
  return value.replace(prefix, "").toLowerCase();
}

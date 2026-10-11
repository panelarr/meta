// SPDX-License-Identifier: GPL-3.0-or-later

// The catalogue page's filters live in the URL (so a view can be shared and the back
// button works) and turn into the query of GET /v1/entries or GET /v1/search.
import type { ContentRating, EntryStatus, EntryType, Image, SortField } from "../api/types";

export type CatalogueSearch = {
  q?: string;
  type?: EntryType;
  status?: EntryStatus;
  rating?: ContentRating;
  genre?: string;
  from?: number;
  to?: number;
  lang?: string;
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

export function parseCatalogueSearch(search: Record<string, unknown>): CatalogueSearch {
  return {
    q: text(search.q),
    type: oneOf(entryTypes, search.type),
    status: oneOf(entryStatuses, search.status),
    rating: oneOf(contentRatings, search.rating),
    genre: text(search.genre),
    from: year(search.from),
    to: year(search.to),
    lang: text(search.lang),
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
    "filter.yearFrom"?: number;
    "filter.yearTo"?: number;
    "filter.languages"?: string[];
    sort?: SortField;
    reverse?: boolean;
    pageSize: number;
    pageToken?: string;
  } = { pageSize: 24 };
  if (search.q) query.query = search.q;
  if (search.type) query["filter.types"] = [search.type];
  if (search.status) query["filter.statuses"] = [search.status];
  if (search.rating) query["filter.contentRatings"] = [search.rating];
  if (search.genre) query["filter.genres"] = [search.genre];
  if (search.from) query["filter.yearFrom"] = search.from;
  if (search.to) query["filter.yearTo"] = search.to;
  if (search.lang) query["filter.languages"] = [search.lang];
  if (search.sort) query.sort = search.sort;
  if (search.reverse) query.reverse = true;
  if (pageToken) query.pageToken = pageToken;
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

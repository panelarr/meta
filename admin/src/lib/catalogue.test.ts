// SPDX-License-Identifier: GPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { catalogueQuery, coverUrl, parseCatalogueSearch } from "./catalogue";

describe("catalogue search", () => {
  it("keeps only valid values from the URL", () => {
    expect(
      parseCatalogueSearch({
        q: "garden",
        type: "ENTRY_TYPE_MANHWA",
        status: "NOPE",
        from: "2010",
        to: "abc",
        reverse: "true",
        sort: "SORT_FIELD_RATING",
      }),
    ).toEqual({
      q: "garden",
      type: "ENTRY_TYPE_MANHWA",
      status: undefined,
      rating: undefined,
      genre: undefined,
      from: 2010,
      to: undefined,
      lang: undefined,
      sort: "SORT_FIELD_RATING",
      reverse: true,
    });
  });

  it("builds the API query", () => {
    expect(
      catalogueQuery({ type: "ENTRY_TYPE_MANGA", genre: "Fantasy", from: 2000, lang: "ja", reverse: true }, "24"),
    ).toEqual({
      pageSize: 24,
      pageToken: "24",
      "filter.types": ["ENTRY_TYPE_MANGA"],
      "filter.genres": ["Fantasy"],
      "filter.yearFrom": 2000,
      "filter.languages": ["ja"],
      reverse: true,
    });
  });

  it("picks a sized cover", () => {
    const image = {
      url: "https://img.example/raw.jpg",
      variants: [
        { width: 150, density: 1, url: "https://img.example/150.jpg" },
        { width: 250, density: 1, url: "https://img.example/250.jpg" },
      ],
    };
    expect(coverUrl(image)).toBe("https://img.example/250.jpg");
    expect(coverUrl({ url: "https://img.example/raw.jpg" })).toBe("https://img.example/raw.jpg");
    expect(coverUrl(undefined)).toBeUndefined();
  });
});

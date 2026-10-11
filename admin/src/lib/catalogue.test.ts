// SPDX-License-Identifier: GPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { catalogueQuery, coverUrl, labelFromKey, languageName, parseCatalogueSearch, tagLabel } from "./catalogue";

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
      tag: undefined,
      xtag: undefined,
      from: 2010,
      to: undefined,
      lang: undefined,
      tlang: undefined,
      sort: "SORT_FIELD_RATING",
      reverse: true,
    });
  });

  it("builds the API query", () => {
    expect(
      catalogueQuery(
        {
          type: "ENTRY_TYPE_MANGA",
          genre: ["Fantasy", "Drama"],
          tag: ["tag/settings/school"],
          xtag: ["genre/horror"],
          from: 2000,
          lang: "ja",
          tlang: "en",
          reverse: true,
        },
        "24",
      ),
    ).toEqual({
      pageSize: 24,
      pageToken: "24",
      "filter.types": ["ENTRY_TYPE_MANGA"],
      "filter.genres": ["Fantasy", "Drama"],
      "filter.tags": ["tag/settings/school"],
      "filter.excludeTags": ["genre/horror"],
      "filter.yearFrom": 2000,
      "filter.languages": ["ja"],
      "filter.titleLanguages": ["en"],
      reverse: true,
    });
  });

  it("reads lists from the URL, also single values from older links", () => {
    expect(parseCatalogueSearch({ genre: "Fantasy", tag: ["tag/a", "tag/a", "tag/b"] })).toMatchObject({
      genre: ["Fantasy"],
      tag: ["tag/a", "tag/b"],
    });
  });

  it("labels tags and languages", () => {
    expect(tagLabel({ name: "Fantasy", path: ["Settings"] })).toBe("Settings › Fantasy");
    expect(labelFromKey("tag/settings/school-life")).toBe("Settings › School Life");
    expect(languageName("ja")).toBe("Japanese");
    expect(languageName("not-a-language-xx")).toBe("not-a-language-xx");
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

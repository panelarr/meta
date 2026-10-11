// SPDX-License-Identifier: GPL-3.0-or-later

// Fake data for mock mode and the tests. Every series, person and provider here is made
// up; nothing comes from a real metadata provider.
import type {
  Client,
  ContentRating,
  Entry,
  EntryStatus,
  EntryType,
  NetworkProfile,
  Provider,
  Solver,
  SyncRun,
  Tag,
} from "../api/types";

export const MOCK_ADMIN_TOKEN = "mock-admin";
/** Tokens starting with this are treated as client API keys. */
export const CLIENT_KEY_PREFIX = "mtk_";
/** A key from the deprecated api_keys setting. */
export const MOCK_CONFIG_KEY = "config-key";

/** An SVG cover as a data URL, so mock mode needs no network. */
function cover(title: string, hue: number): string {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="250" height="375" viewBox="0 0 250 375"><rect width="250" height="375" fill="hsl(${hue} 45% 50%)"/><text x="125" y="190" font-family="sans-serif" font-size="18" fill="#fff" text-anchor="middle">${title.replace(/[<>&"]/g, "")}</text></svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

const attribution = {
  providerName: "Example Data",
  url: "https://data.example.org",
  license: "CC BY 4.0",
  licenseUrl: "https://creativecommons.org/licenses/by/4.0/",
};

type Seed = {
  id: string;
  title: string;
  native?: string;
  type: EntryType;
  status: EntryStatus;
  rating: ContentRating;
  year: number;
  lang: string;
  genres: string[];
  tags?: { name: string; path?: string[]; spoiler?: boolean }[];
  score: number;
  rank: number;
  author: string;
};

const seeds: Seed[] = [
  {
    id: "0192aa00-0000-7000-8000-000000000001",
    title: "The Clockwork Garden",
    native: "時計仕掛けの庭",
    type: "ENTRY_TYPE_MANGA",
    status: "ENTRY_STATUS_COMPLETED",
    rating: "CONTENT_RATING_SAFE",
    year: 2011,
    lang: "ja",
    genres: ["Fantasy", "Slice of Life"],
    tags: [{ name: "Gardens" }, { name: "Time Loop", spoiler: true }],
    score: 81.5,
    rank: 3,
    author: "Aiko Fictional",
  },
  {
    id: "0192aa00-0000-7000-8000-000000000002",
    title: "Lantern Keeper",
    native: "등불지기",
    type: "ENTRY_TYPE_MANHWA",
    status: "ENTRY_STATUS_RELEASING",
    rating: "CONTENT_RATING_SAFE",
    year: 2019,
    lang: "ko",
    genres: ["Fantasy", "Action"],
    tags: [{ name: "Lighthouses" }],
    score: 88,
    rank: 1,
    author: "Min Madeup",
  },
  {
    id: "0192aa00-0000-7000-8000-000000000003",
    title: "Quiet Orbit",
    type: "ENTRY_TYPE_MANGA",
    status: "ENTRY_STATUS_HIATUS",
    rating: "CONTENT_RATING_SUGGESTIVE",
    year: 2004,
    lang: "ja",
    genres: ["Sci-Fi"],
    // A tag that shares its name with a genre: it must not count as the genre.
    tags: [
      { name: "Fantasy", path: ["Settings"] },
      { name: "Space", path: ["Settings"] },
    ],
    score: 64,
    rank: 9,
    author: "Ren Invented",
  },
  {
    id: "0192aa00-0000-7000-8000-000000000004",
    title: "Salt and Iron",
    native: "盐与铁",
    type: "ENTRY_TYPE_MANHUA",
    status: "ENTRY_STATUS_RELEASING",
    rating: "CONTENT_RATING_SAFE",
    year: 2021,
    lang: "zh",
    genres: ["Action", "Historical"],
    score: 72,
    rank: 5,
    author: "Wei Notreal",
  },
];

/** An old entry ID that was merged into the first entry. */
export const MERGED_ENTRY_ID = "0192aa00-0000-7000-8000-0000000000ff";

export function mockEntries(): Entry[] {
  return seeds.map((s, i) => ({
    id: s.id,
    title: s.title,
    type: s.type,
    status: s.status,
    contentRating: s.rating,
    originalLanguage: s.lang,
    state: "ENTRY_STATE_ACTIVE",
    titles: [
      { text: s.title, language: "en", kind: "TITLE_KIND_OFFICIAL", isPrimary: true },
      ...(s.native ? [{ text: s.native, language: s.lang, kind: "TITLE_KIND_NATIVE" as const, isPrimary: true }] : []),
    ],
    descriptions: [
      { language: "en", text: `A made-up story called ${s.title}.`, format: "DESCRIPTION_FORMAT_MARKDOWN" },
    ],
    credits: [{ person: { id: `p${i}`, name: s.author }, role: "PERSON_ROLE_AUTHOR", rawRole: "author" }],
    companies: [{ company: { id: `c${i}`, name: "Imaginary Press" }, role: "COMPANY_ROLE_PUBLISHER", language: "en" }],
    tags: [
      ...s.genres.map((g) => ({ tag: { id: `g-${g}`, key: genreKey(g), name: g, isGenre: true } })),
      ...(s.tags ?? []).map((t) => ({
        tag: { id: `t-${tagKey(t.name, t.path)}`, key: tagKey(t.name, t.path), name: t.name, path: t.path ?? [] },
        isSpoiler: t.spoiler ?? false,
      })),
    ],
    relations:
      i === 0
        ? [
            {
              type: "RELATION_TYPE_SEQUEL",
              target: { provider: "example", id: "2" },
              entryId: seeds[1]?.id,
              title: "Lantern Keeper",
            },
            {
              type: "RELATION_TYPE_SPIN_OFF",
              target: { provider: "example", id: "999" },
              title: "An unknown spin-off",
            },
          ]
        : [],
    links: [{ type: "LINK_TYPE_OFFICIAL_WEBSITE", url: `https://publisher.example/${i + 1}`, title: "Publisher page" }],
    images: [
      {
        kind: "IMAGE_KIND_COVER",
        url: cover(s.title, i * 70),
        variants: [{ width: 250, density: 1, url: cover(s.title, i * 70) }],
      },
    ],
    externalIds: [
      { provider: "example", id: String(i + 1), url: `https://data.example.org/series/${i + 1}` },
      { provider: "anilist", id: String(1000 + i) },
    ],
    stats: [{ provider: "example", stats: { rating: s.score, popularityRank: String(s.rank) } }],
    dates: { start: { year: s.year, month: 4 } },
    sources: [
      {
        ref: { provider: "example", id: String(i + 1), url: `https://data.example.org/series/${i + 1}` },
        attribution,
        state: "ENTRY_STATE_ACTIVE",
        fetchedAt: "2026-10-10T03:00:00Z",
        providerUpdatedAt: "2026-10-01T12:00:00Z",
      },
    ],
    fieldSources: { titles: "example", tags: "example", credits: "example" },
    createdAt: "2026-10-01T03:00:00Z",
    updatedAt: "2026-10-10T03:00:00Z",
  }));
}

const slug = (s: string) =>
  s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");
export const genreKey = (name: string) => `genre/${slug(name)}`;
export const tagKey = (name: string, path: string[] = []) => ["tag", ...path.map(slug), slug(name)].join("/");

export function mockGenres(): Tag[] {
  const names = [...new Set(seeds.flatMap((s) => s.genres))].sort();
  return names.map((name) => ({
    id: `g-${name}`,
    key: genreKey(name),
    name,
    isGenre: true,
    entryCount: String(seeds.filter((s) => s.genres.includes(name)).length),
  }));
}

export function mockTags(): Tag[] {
  const byKey = new Map<string, Tag>();
  for (const s of seeds) {
    for (const t of s.tags ?? []) {
      const key = tagKey(t.name, t.path);
      const cur = byKey.get(key);
      byKey.set(key, {
        id: `t-${key}`,
        key,
        name: t.name,
        path: t.path ?? [],
        isGenre: false,
        entryCount: String(Number(cur?.entryCount ?? 0) + 1),
      });
    }
  }
  return [...byKey.values()];
}

export function mockProviders(): Provider[] {
  return [
    {
      id: "example",
      mode: "remote",
      health: "HEALTH_STATUS_OK",
      info: {
        id: "example",
        name: "Example Data",
        version: "1.2.0",
        provider: "example",
        ingestions: [
          { kind: "dump", description: "Nightly snapshot", fullSnapshot: true },
          { kind: "lookup", description: "API lookups" },
        ],
        attribution,
      },
      schedules: { dump: "0 3 * * *" },
      recordCount: "4",
      lastRuns: [],
    },
    {
      id: "sample",
      mode: "managed",
      health: "HEALTH_STATUS_DOWN",
      healthMessage: "plugin process exited",
      schedules: {},
      recordCount: "0",
      lastRuns: [],
    },
  ];
}

export function mockRuns(): SyncRun[] {
  return [
    {
      id: "0192bb00-0000-7000-8000-000000000001",
      provider: "example",
      kind: "dump",
      status: "SYNC_RUN_STATUS_SUCCEEDED",
      trigger: "schedule",
      startedAt: "2026-10-10T03:00:00Z",
      finishedAt: "2026-10-10T03:09:12Z",
      snapshotId: "4f2c9a",
      cursorStart: "",
      cursorEnd: "4f2c9a",
      upserts: "564040",
      changed: "1203",
      removals: "0",
      merges: "258029",
      skipped: "2",
      markedRemoved: "3",
      notices: [
        {
          level: "NOTICE_LEVEL_WARNING",
          message: "row has no title; skipped",
          ref: "example:777",
          at: "2026-10-10T03:05:00Z",
        },
      ],
    },
    {
      id: "0192bb00-0000-7000-8000-000000000002",
      provider: "example",
      kind: "dump",
      status: "SYNC_RUN_STATUS_FAILED",
      trigger: "manual",
      startedAt: "2026-10-09T10:00:00Z",
      finishedAt: "2026-10-09T10:00:05Z",
      upserts: "0",
      changed: "0",
      error: "plugin: unavailable: the dump's checksum doesn't match",
      notices: [],
    },
  ];
}

export function mockClients(): Client[] {
  return [
    {
      id: "a1b2c3d4e5f60718",
      name: "library-server",
      scopes: ["catalog:read"],
      keyPrefix: "mtk_AbCdEf",
      createdAt: "2026-10-01T10:00:00Z",
      lastUsedAt: "2026-10-11T08:30:00Z",
    },
  ];
}

export function mockSolvers(): Solver[] {
  return [
    {
      id: "flaresolverr",
      name: "FlareSolverr",
      type: "SOLVER_TYPE_FLARESOLVERR",
      url: "http://flaresolverr:8191",
      maxTimeout: "60s",
      usedBy: ["via-proxy"],
    },
    { id: "spare", name: "Spare solver", type: "SOLVER_TYPE_BYPARR", url: "http://byparr:8191", usedBy: [] },
  ];
}

export function mockProfiles(): NetworkProfile[] {
  return [
    {
      id: "polite",
      name: "Polite",
      rateLimit: { requests: 30, period: "60s" },
      userAgentMode: "USER_AGENT_MODE_UNSPECIFIED",
    },
    {
      id: "via-proxy",
      name: "Via proxy",
      proxy: { url: "socks5://gluetun:1080", username: "vpn", hasPassword: true },
      solverId: "flaresolverr",
      userAgentMode: "USER_AGENT_MODE_FROM_SOLVER",
    },
  ];
}

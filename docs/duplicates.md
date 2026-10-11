# Duplicates

meta links a provider record to an entry when they share an ID, and never joins two active records of the **same** provider: that provider says they are different works. Providers do list some works twice, though, and works without IDs in common can't be linked automatically. The **duplicates** job finds entries that are probably the same work but aren't linked, and keeps them for a curator under **Duplicates** in the admin. Nothing is merged without a curator.

## Finding pairs

The job compares the active entries that changed since its last successful run (every entry with `full`) with their likely partners:

- **Title keys.** A title's key is its normalized words without articles and particles ("the", "a", "no", "de", …), with a plural "s" and doubled vowels of romanizations folded, in sorted order: "The Lantern Keepers" and "lantern keeper", or "Shingeki no Kyoujin" and "Kyojin Shingeki", share a key. An index keeps keys up to date; a key shared by more than 50 titles ("love", "untitled") is too common to pair on.
- **Similar titles.** Runs that look at no more than 5,000 entries (the usual daily run) also compare primary titles by trigram similarity. That costs tens of milliseconds per title in a large catalogue, so large runs (the first one, or `full`) leave it out.
- **IDs at other sites.** Two entries that report the same AniList or MyAnimeList ID, for example.

Each pair is scored from 0 to 1:

| Evidence | Effect |
|---|---|
| Best title similarity (1 for the same normalized title) | up to 0.5 |
| People in common (half of the smaller side is enough) | up to 0.2; none in common when both credit people: −0.15; unknown: neutral |
| Year (same, ±1, ±3) | up to 0.12 |
| Type, original language | up to 0.1 and 0.08; unknown: neutral |
| The same ID at another site | +0.3 |
| Both from the same provider | −0.05, and flagged |

Pairs scoring at least `duplicates.min_score` (0.6) are kept, at most `duplicates.max_per_entry` (5) per entry. Pairs are never kept when the two are known to be different works: they report **different** IDs at another site, or one is a sequel, prequel, adaptation or other relation of the other. With `duplicates.skip_same_provider`, pairs of entries from the same provider only are skipped too (off by default: a provider's own duplicates are what this finds most).

A pair a curator confirmed or dismissed is never changed by later runs. Open pairs are updated as the entries change, and dropped when they no longer qualify or an entry is no longer active.

## Reviewing

**Duplicates** in the admin lists open pairs side by side: covers, titles, type, year, language, genres and sources, with the score and what it is made of. **Confirm** merges the two into one entry: choose the one that stays; every provider record of the other moves over (even of a provider the first already has), the other is retired, and its ID redirects, so links and lookups keep working. Titles of both records stay on the entry. **Dismiss** keeps them apart for good; **Reopen** takes a dismissal back.

## Decision model (optional)

After each run, a decision model can rate the best open pairs (score at least `duplicates.judge.min_score`, 0.7): the probability that the two are the same work, and whether they are the same work, related works (sequel, spin-off, adaptation) or different. Decision models answer with probabilities, not written reasons. Three APIs are supported:

| Kind | API | Default model |
|---|---|---|
| `openai` | OpenAI Decisions, `POST /v1/decisions` | `gpt-6-luna` |
| `typesafe` | TypeSafe, `POST /v1/systemone` | `jev-latest` |
| `clef` | Cloudflare Workers AI, the model's run URL | `clef` |

It is **off by default**: nothing is sent anywhere until it is turned on, in the configuration (`duplicates.judge`) or under **Duplicates → Decision model** in the admin (with **Test**, which asks about a made-up pair). Each pair sent includes both entries' titles, people, type, status, year, original language and a short description; with `covers`, also their cover images (OpenAI and Clef). The key is stored encrypted and never shown again.

Limits per run: `max_pairs` (200) and `max_tokens` (500,000 input tokens). Requests back off on 429, 503 and 529. A verdict is cached by the pair's content and the model, so an unchanged pair is never sent twice; when either entry changes, the pair is rated again.

## Settings

| Key | Default | What |
|---|---|---|
| `duplicates.schedule` | none (manual only) | Cron expression (UTC) for the duplicates job |
| `duplicates.min_score` | `0.6` | Pairs scoring at least this are kept |
| `duplicates.max_per_entry` | `5` | Best pairs kept per entry |
| `duplicates.skip_same_provider` | `false` | Never pair entries of the same provider only |
| `duplicates.judge.enabled`, `.kind`, `.url`, `.model`, `.api_key` (`META_JUDGE_API_KEY`), `.covers` | off | The decision model; settings saved in the admin win |
| `duplicates.judge.min_score`, `.max_pairs`, `.max_tokens` | `0.7`, `200`, `500000` | Which pairs are rated, and caps per run |

## Scale

On a synthetic catalogue of 300,000 entries with 3,000 injected near-duplicates (a laptop, PostgreSQL in Docker), a full run took 17 seconds and an incremental run after 1% of the entries changed about 15 ms per entry. Full runs found the near-duplicates whose titles differ by an article or a plural, and missed those with an added subtitle ("…: Special Edition"), which incremental runs find by similarity.

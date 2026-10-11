// SPDX-License-Identifier: GPL-3.0-or-later

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { useState } from "react";
import { api, unwrap } from "../api/client";
import type { Attribution, Entry, PartialDate } from "../api/types";
import { Badge, Empty, ErrorBox, Loading, Mono, PageHeader } from "../components/ui";
import { enumLabel, formatDate, languageLabel } from "../lib/format";
import { Cover } from "./CataloguePage";

const route = getRouteApi("/_app/entries/$entryId");

function partialDate(d: PartialDate | undefined): string | undefined {
  if (!d?.year) return undefined;
  const parts = [String(d.year)];
  if (d.month) parts.push(String(d.month).padStart(2, "0"));
  if (d.month && d.day) parts.push(String(d.day).padStart(2, "0"));
  return parts.join("-") + (d.estimated ? " (estimated)" : "");
}

export function EntryPage() {
  const { entryId } = route.useParams();
  const queryClient = useQueryClient();
  const entry = useQuery({
    queryKey: ["entry", entryId],
    queryFn: () => unwrap(api.GET("/v1/entries/{id}", { params: { path: { id: entryId } } })),
  });
  const refresh = useMutation({
    mutationFn: () => unwrap(api.POST("/v1/admin/entries/{id}/refresh", { params: { path: { id: entryId } } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["entry", entryId] }),
  });

  if (entry.isPending) return <Loading />;
  if (entry.error) return <ErrorBox error={entry.error} onRetry={() => entry.refetch()} />;
  const e = entry.data.entry;
  if (!e) return <Empty>This entry has no data.</Empty>;

  return (
    <>
      <p className="crumbs">
        <Link to="/catalogue">Catalogue</Link> /
      </p>
      <PageHeader title={e.title || e.id}>
        <button type="button" disabled={refresh.isPending} onClick={() => refresh.mutate()}>
          {refresh.isPending ? "Refreshing…" : "Refresh from providers"}
        </button>
      </PageHeader>
      {entry.data.redirectedFrom ? (
        <p className="notice" role="status">
          The entry <Mono>{entry.data.redirectedFrom}</Mono> was merged into this one.
        </p>
      ) : null}
      <ErrorBox error={refresh.error} title="Refresh" />
      {refresh.data ? <RefreshResult result={refresh.data} /> : null}
      <EntryDetails entry={e} />
    </>
  );
}

function RefreshResult({ result }: { result: { refreshed?: string[]; failed?: Record<string, string> } }) {
  const failed = Object.entries(result.failed ?? {});
  return (
    <div className={failed.length ? "error-box" : "notice"} role="status">
      {result.refreshed?.length ? <div>Refreshed from: {result.refreshed.join(", ")}.</div> : null}
      {!result.refreshed?.length && !failed.length ? <div>No provider can look this entry up.</div> : null}
      {failed.map(([provider, error]) => (
        <div key={provider}>
          {provider}: {error}
        </div>
      ))}
    </div>
  );
}

function EntryDetails({ entry: e }: { entry: Entry }) {
  const [showSpoilers, setShowSpoilers] = useState(false);
  const cover = (e.images ?? []).find((i) => i.kind === "IMAGE_KIND_COVER") ?? e.images?.[0];
  const tags = e.tags ?? [];
  const spoilers = tags.filter((t) => t.isSpoiler || t.tag?.isSpoiler);
  const visibleTags = showSpoilers ? tags : tags.filter((t) => !(t.isSpoiler || t.tag?.isSpoiler));
  const dates = [partialDate(e.dates?.start), partialDate(e.dates?.end)];

  return (
    <>
      <div className="entry-header">
        <Cover entry={{ cover, title: e.title }} large />
        <div className="entry-facts">
          <div className="badges">
            {e.type ? <Badge tone="info">{enumLabel(e.type, "ENTRY_TYPE_")}</Badge> : null}
            {e.status ? <Badge>{enumLabel(e.status, "ENTRY_STATUS_")}</Badge> : null}
            {e.contentRating && e.contentRating !== "CONTENT_RATING_UNSPECIFIED" ? (
              <Badge tone={e.contentRating === "CONTENT_RATING_SAFE" ? "good" : "warn"}>
                {enumLabel(e.contentRating, "CONTENT_RATING_")}
              </Badge>
            ) : null}
            {e.state && e.state !== "ENTRY_STATE_ACTIVE" ? (
              <Badge tone="bad">{enumLabel(e.state, "ENTRY_STATE_")}</Badge>
            ) : null}
          </div>
          <dl className="details">
            <dt>ID</dt>
            <dd>
              <Mono>{e.id}</Mono>
            </dd>
            <dt>Original language</dt>
            <dd>{languageLabel(e.originalLanguage)}</dd>
            <dt>Published</dt>
            <dd>{dates[0] ? `${dates[0]}${dates[1] ? ` – ${dates[1]}` : ""}` : "—"}</dd>
            {e.progress?.final?.value || e.progress?.total?.value || e.progress?.latest?.value ? (
              <>
                <dt>Progress</dt>
                <dd>
                  {[
                    e.progress?.total?.value
                      ? `${e.progress.total.value} ${enumLabel(e.progress.total.unit, "REF_UNIT_").toLowerCase()}s in total`
                      : undefined,
                    e.progress?.latest?.value
                      ? `latest ${enumLabel(e.progress.latest.unit, "REF_UNIT_").toLowerCase()} ${e.progress.latest.value}`
                      : undefined,
                    e.progress?.final?.value
                      ? `final ${enumLabel(e.progress.final.unit, "REF_UNIT_").toLowerCase()} ${e.progress.final.value}`
                      : undefined,
                  ]
                    .filter(Boolean)
                    .join(", ")}
                </dd>
              </>
            ) : null}
            <dt>Updated</dt>
            <dd>{formatDate(e.updatedAt)}</dd>
          </dl>
          <Attributions sources={e.sources ?? []} />
        </div>
      </div>

      {(e.descriptions ?? []).length > 0 ? (
        <section className="section">
          <h2>Description</h2>
          {(e.descriptions ?? []).map((d, i) => (
            <div key={i} className="description">
              {d.language ? <span className="muted small">{d.language} · </span> : null}
              {d.text}
            </div>
          ))}
        </section>
      ) : null}

      <section className="section">
        <h2>Titles</h2>
        <table className="table">
          <thead>
            <tr>
              <th>Title</th>
              <th>Language</th>
              <th>Kind</th>
            </tr>
          </thead>
          <tbody>
            {(e.titles ?? []).map((t, i) => (
              <tr key={i}>
                <td>
                  {t.text} {t.isPrimary ? <Badge tone="info">Primary</Badge> : null}
                  {t.note ? <div className="muted small">{t.note}</div> : null}
                </td>
                <td>{t.language || <span className="muted">unknown</span>}</td>
                <td>{enumLabel(t.kind, "TITLE_KIND_")}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      {(e.credits ?? []).length > 0 || (e.companies ?? []).length > 0 ? (
        <section className="section">
          <h2>People and companies</h2>
          <ul className="plain-list">
            {(e.credits ?? []).map((c, i) => (
              <li key={`p${i}`}>
                {c.person?.name} <span className="muted small">{c.rawRole || enumLabel(c.role, "PERSON_ROLE_")}</span>
              </li>
            ))}
            {(e.companies ?? []).map((c, i) => (
              <li key={`c${i}`}>
                {c.company?.name}{" "}
                <span className="muted small">
                  {c.rawRole || enumLabel(c.role, "COMPANY_ROLE_")}
                  {c.language ? ` · ${c.language}` : ""}
                </span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {tags.length > 0 ? (
        <section className="section">
          <header className="section-header">
            <h2>Tags</h2>
            {spoilers.length > 0 ? (
              <label className="filter-check">
                <input type="checkbox" checked={showSpoilers} onChange={(ev) => setShowSpoilers(ev.target.checked)} />
                <span>Show spoiler tags ({spoilers.length})</span>
              </label>
            ) : null}
          </header>
          <div className="badges">
            {visibleTags.map((t, i) => (
              <Badge
                key={i}
                tone={t.tag?.isGenre ? "info" : t.isSpoiler || t.tag?.isSpoiler ? "warn" : "neutral"}
                title={t.tag?.description}
              >
                {t.tag?.name}
              </Badge>
            ))}
          </div>
        </section>
      ) : null}

      {(e.relations ?? []).length > 0 ? (
        <section className="section">
          <h2>Related</h2>
          <ul className="plain-list">
            {(e.relations ?? []).map((r, i) => (
              <li key={i}>
                <span className="muted small">{r.rawType || enumLabel(r.type, "RELATION_TYPE_")}: </span>
                {r.entryId ? (
                  <Link to="/entries/$entryId" params={{ entryId: r.entryId }}>
                    {r.title || r.entryId}
                  </Link>
                ) : (
                  <span>
                    {r.title || "Not in meta"}{" "}
                    <Mono>
                      {r.target?.provider}:{r.target?.id}
                    </Mono>
                  </span>
                )}
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {(e.links ?? []).length > 0 ? (
        <section className="section">
          <h2>Links</h2>
          <ul className="plain-list">
            {(e.links ?? []).map((l, i) => (
              <li key={i}>
                <a href={l.url} rel="noreferrer noopener" target="_blank">
                  {l.title || l.url}
                </a>{" "}
                <span className="muted small">{l.rawType || enumLabel(l.type, "LINK_TYPE_")}</span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      <section className="section">
        <h2>External IDs</h2>
        {(e.externalIds ?? []).length === 0 ? <Empty>None.</Empty> : null}
        <ul className="plain-list">
          {(e.externalIds ?? []).map((x, i) => (
            <li key={i}>
              <Mono>{x.provider}</Mono>{" "}
              {x.url ? (
                <a href={x.url} rel="noreferrer noopener" target="_blank">
                  {x.id}
                </a>
              ) : (
                x.id
              )}
            </li>
          ))}
        </ul>
      </section>

      <section className="section">
        <h2>Sources</h2>
        <table className="table">
          <thead>
            <tr>
              <th>Record</th>
              <th>State</th>
              <th>Fetched</th>
              <th>Changed at the provider</th>
              <th>Attribution</th>
            </tr>
          </thead>
          <tbody>
            {(e.sources ?? []).map((s, i) => (
              <tr key={i}>
                <td>
                  <Mono>
                    {s.ref?.provider}:{s.ref?.id}
                  </Mono>
                  {s.ref?.url ? (
                    <div>
                      <a href={s.ref.url} rel="noreferrer noopener" target="_blank" className="small">
                        Open at the provider
                      </a>
                    </div>
                  ) : null}
                </td>
                <td>{enumLabel(s.state, "ENTRY_STATE_")}</td>
                <td>{formatDate(s.fetchedAt)}</td>
                <td>{formatDate(s.providerUpdatedAt)}</td>
                <td>{s.attribution ? <AttributionText a={s.attribution} /> : <span className="muted">—</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      {Object.keys(e.fieldSources ?? {}).length > 0 ? (
        <section className="section">
          <h2>Where each field comes from</h2>
          <dl className="details">
            {Object.entries(e.fieldSources ?? {})
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([field, provider]) => (
                <div key={field} className="fact-row">
                  <dt>{field.replaceAll("_", " ")}</dt>
                  <dd>{provider === "manual" ? <Badge tone="warn">set by hand</Badge> : <Mono>{provider}</Mono>}</dd>
                </div>
              ))}
          </dl>
        </section>
      ) : null}

      {(e.stats ?? []).length > 0 ? (
        <section className="section">
          <h2>Stats by provider</h2>
          <table className="table">
            <thead>
              <tr>
                <th>Provider</th>
                <th>Rating (0–100)</th>
                <th>Ratings</th>
                <th>Popularity rank</th>
                <th>Popularity</th>
              </tr>
            </thead>
            <tbody>
              {(e.stats ?? []).map((s) => (
                <tr key={s.provider}>
                  <td>
                    <Mono>{s.provider}</Mono>
                  </td>
                  <td>{s.stats?.rating ?? "—"}</td>
                  <td>{s.stats?.ratingCount ?? "—"}</td>
                  <td>{s.stats?.popularityRank ?? "—"}</td>
                  <td>{s.stats?.popularity ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      ) : null}
    </>
  );
}

/** Who the data comes from, shown next to it as providers' licences ask. */
function Attributions({ sources }: { sources: NonNullable<Entry["sources"]> }) {
  const seen = new Set<string>();
  const list = sources
    .map((s) => s.attribution)
    .filter((a): a is Attribution => !!a?.providerName && !seen.has(a.providerName) && !!seen.add(a.providerName));
  if (list.length === 0) return null;
  return (
    <div className="attribution">
      Data from{" "}
      {list.map((a, i) => (
        <span key={a.providerName}>
          {i > 0 ? ", " : null}
          <AttributionText a={a} />
        </span>
      ))}
      .
    </div>
  );
}

function AttributionText({ a }: { a: Attribution }) {
  return (
    <span>
      {a.url ? (
        <a href={a.url} rel="noreferrer noopener" target="_blank">
          {a.providerName}
        </a>
      ) : (
        a.providerName
      )}
      {a.license ? (
        <>
          {" "}
          (
          {a.licenseUrl ? (
            <a href={a.licenseUrl} rel="noreferrer noopener" target="_blank">
              {a.license}
            </a>
          ) : (
            a.license
          )}
          )
        </>
      ) : null}
      {a.notice ? <div className="muted small">{a.notice}</div> : null}
    </span>
  );
}

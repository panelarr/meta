// SPDX-License-Identifier: GPL-3.0-or-later

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { type FormEvent, useState } from "react";
import { api, unwrap } from "../api/client";
import type {
  DecisionJudgeSettings,
  DuplicatePair,
  DuplicateStatus,
  DuplicateVerdict,
  EntrySummary,
} from "../api/types";
import { Dialog } from "../components/Dialog";
import { TabList, tabProps } from "../components/Tabs";
import { Badge, Empty, ErrorBox, Field, Loading, Mono, PageHeader, type Tone } from "../components/ui";
import { enumLabel, formatDate, languageLabel } from "../lib/format";
import { Cover } from "./CataloguePage";

const route = getRouteApi("/_app/duplicates");

const views: { show?: "confirmed" | "dismissed"; status: DuplicateStatus; label: string }[] = [
  { status: "DUPLICATE_STATUS_OPEN", label: "To review" },
  { show: "confirmed", status: "DUPLICATE_STATUS_CONFIRMED", label: "Confirmed" },
  { show: "dismissed", status: "DUPLICATE_STATUS_DISMISSED", label: "Dismissed" },
];

const choiceLabels: Record<string, string> = {
  same_work: "same work",
  related_work: "related works",
  different: "different works",
};

const pct = (x: number | undefined) => `${Math.round((x ?? 0) * 100)}%`;

function scoreTone(score: number): Tone {
  if (score >= 0.85) return "good";
  if (score >= 0.7) return "info";
  return "neutral";
}

export function DuplicatesPage() {
  const search = route.useSearch();
  const navigate = useNavigate();
  const status: DuplicateStatus = views.find((v) => v.show === search.show)?.status ?? "DUPLICATE_STATUS_OPEN";
  const pairs = useInfiniteQuery({
    queryKey: ["duplicates", status, search.min ?? 0, search.judged ?? false],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/v1/admin/duplicates", {
          params: {
            query: {
              status,
              minScore: search.min,
              judgedOnly: search.judged || undefined,
              pageSize: 20,
              pageToken: pageParam,
            },
          },
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextPageToken || undefined,
  });
  const list = (pairs.data?.pages ?? []).flatMap((p) => p.pairs ?? []);
  const total = Number(pairs.data?.pages[0]?.totalSize ?? 0);
  const findNow = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/v1/admin/jobs/{jobId}:run", { params: { path: { jobId: "duplicates" } }, body: {} })),
    onSuccess: (res) => void navigate({ to: "/jobs", search: { job: "duplicates", run: res.run?.id } }),
  });
  const [confirming, setConfirming] = useState<DuplicatePair | null>(null);
  const [dismissing, setDismissing] = useState<DuplicatePair | null>(null);
  const go = (patch: Record<string, unknown>) =>
    void navigate({ to: "/duplicates", search: { ...search, ...patch } as never });

  return (
    <>
      <PageHeader title="Duplicates">
        <button type="button" disabled={findNow.isPending} onClick={() => findNow.mutate()}>
          {findNow.isPending ? "Starting…" : "Find duplicates now"}
        </button>
      </PageHeader>
      <p className="muted">
        Entries that are probably the same work but aren't linked: often a provider listing one series twice. Confirm to
        merge them into one entry (the other's ID keeps working), or dismiss to keep them apart for good. Nothing is
        merged on its own.
      </p>
      <ErrorBox error={findNow.error} />
      <TabList label="Pairs">
        {views.map((v) => (
          <Link key={v.status} to="/duplicates" search={{ ...search, show: v.show }} {...tabProps(status === v.status)}>
            {v.label}
          </Link>
        ))}
      </TabList>
      <div className="dup-filters">
        <label className="inline-field">
          <span>Score at least</span>
          <select value={String(search.min ?? 0)} onChange={(e) => go({ min: Number(e.target.value) || undefined })}>
            {[0, 0.7, 0.8, 0.9].map((v) => (
              <option key={v} value={v}>
                {v ? pct(v) : "any"}
              </option>
            ))}
          </select>
        </label>
        <label className="filter-check">
          <input
            type="checkbox"
            checked={search.judged ?? false}
            onChange={(e) => go({ judged: e.target.checked || undefined })}
          />
          <span>Rated by the decision model</span>
        </label>
        <span className="muted small">{pairs.data ? `${total.toLocaleString()} pairs` : ""}</span>
      </div>
      {pairs.isPending ? <Loading /> : null}
      <ErrorBox error={pairs.error} onRetry={() => pairs.refetch()} />
      {pairs.data && list.length === 0 ? (
        <Empty>
          {status === "DUPLICATE_STATUS_OPEN"
            ? "Nothing to review. The duplicates job adds pairs as entries change."
            : "No pairs here."}
        </Empty>
      ) : null}
      <div className="dup-list">
        {list.map((p) => (
          <PairCard key={p.id} pair={p} onConfirm={() => setConfirming(p)} onDismiss={() => setDismissing(p)} />
        ))}
      </div>
      {pairs.hasNextPage ? (
        <div className="pager">
          <button type="button" disabled={pairs.isFetchingNextPage} onClick={() => pairs.fetchNextPage()}>
            {pairs.isFetchingNextPage ? "Loading…" : "Load more"}
          </button>
        </div>
      ) : null}
      <JudgeSection />
      {confirming ? <ConfirmDialog pair={confirming} onClose={() => setConfirming(null)} /> : null}
      {dismissing ? <DismissDialog pair={dismissing} onClose={() => setDismissing(null)} /> : null}
    </>
  );
}

function useInvalidatePairs() {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: ["duplicates"] });
}

function Side({ entry, label }: { entry: EntrySummary | undefined; label: string }) {
  if (!entry) return null;
  return (
    <div className="dup-side">
      <Cover entry={entry} />
      <div>
        <span className="muted small">{label}</span>
        <h3>
          <Link to="/entries/$entryId" params={{ entryId: entry.id ?? "" }}>
            {entry.title || entry.id}
          </Link>
        </h3>
        <div className="muted small">
          {[
            enumLabel(entry.type, "ENTRY_TYPE_"),
            entry.year || "",
            entry.originalLanguage ? languageLabel(entry.originalLanguage) : "",
          ]
            .filter(Boolean)
            .join(" · ")}
        </div>
        {(entry.genres ?? []).length > 0 ? (
          <div className="badges">
            {(entry.genres ?? []).slice(0, 4).map((g) => (
              <Badge key={g}>{g}</Badge>
            ))}
          </div>
        ) : null}
        <div className="muted small">{(entry.attributions ?? []).map((a) => a.providerName).join(", ")}</div>
      </div>
    </div>
  );
}

function Verdict({ v }: { v: DuplicateVerdict }) {
  const same = v.same ?? 0;
  return (
    <div className="dup-verdict">
      <Badge tone={same >= 0.8 ? "good" : same >= 0.5 ? "warn" : "bad"} title={`${v.judge} ${v.model}`}>
        Model: {pct(same)} same
      </Badge>
      {v.choice ? (
        <span className="muted small">
          most likely {choiceLabels[v.choice] ?? v.choice}
          {v.confidence ? ` (confidence ${pct(v.confidence)})` : ""}
        </span>
      ) : null}
    </div>
  );
}

function PairCard({
  pair: p,
  onConfirm,
  onDismiss,
}: {
  pair: DuplicatePair;
  onConfirm: () => void;
  onDismiss: () => void;
}) {
  const invalidate = useInvalidatePairs();
  const reopen = useMutation({
    mutationFn: () => unwrap(api.POST("/v1/admin/duplicates/{id}:reopen", { params: { path: { id: p.id ?? "" } } })),
    onSuccess: invalidate,
  });
  const s = p.signals ?? {};
  const score = p.score ?? 0;
  return (
    <article className="card dup-card" aria-label={`Pair ${p.entryA?.title} and ${p.entryB?.title}`}>
      <header className="card-header">
        <div className="badges">
          <Badge tone={scoreTone(score)}>Score {pct(score)}</Badge>
          {s.sameProvider ? (
            <Badge tone="warn" title="Both come from the same provider, which lists them as two works">
              same provider
            </Badge>
          ) : null}
          {(s.sharedIds ?? []).length > 0 ? <Badge tone="good">shared IDs</Badge> : null}
        </div>
        {p.verdict ? <Verdict v={p.verdict} /> : null}
      </header>
      <div className="dup-sides">
        <Side entry={p.entryA} label="A" />
        <Side entry={p.entryB} label="B" />
      </div>
      <ul className="dup-signals">
        <li>
          Titles {pct(s.title)} alike: <Mono>{s.titleA}</Mono> / <Mono>{s.titleB}</Mono>
        </li>
        <li>
          People:{" "}
          {(s.people ?? -1) < 0
            ? "not known on both sides"
            : (s.sharedPeople ?? []).length > 0
              ? `in common: ${(s.sharedPeople ?? []).join(", ")}`
              : "none in common"}
        </li>
        <li>
          Year {s.yearA || "?"} / {s.yearB || "?"} · type {s.typeA || "?"} / {s.typeB || "?"} · language{" "}
          {s.languageA || "?"} / {s.languageB || "?"}
        </li>
        {(s.sharedIds ?? []).length > 0 ? (
          <li>
            Same IDs at: <Mono>{(s.sharedIds ?? []).join(", ")}</Mono>
          </li>
        ) : null}
      </ul>
      {p.status === "DUPLICATE_STATUS_OPEN" ? (
        <div className="actions">
          <button type="button" className="primary" onClick={onConfirm}>
            Confirm…
          </button>
          <button type="button" onClick={onDismiss}>
            Dismiss…
          </button>
        </div>
      ) : (
        <div className="muted small">
          {p.status === "DUPLICATE_STATUS_CONFIRMED" ? "Merged" : "Dismissed"} {formatDate(p.decidedAt)}
          {p.decidedBy ? ` by ${p.decidedBy}` : ""}
          {p.note ? ` — ${p.note}` : ""}
          {p.status === "DUPLICATE_STATUS_DISMISSED" ? (
            <>
              {" "}
              <button type="button" className="small" disabled={reopen.isPending} onClick={() => reopen.mutate()}>
                Reopen
              </button>
            </>
          ) : null}
        </div>
      )}
      <ErrorBox error={reopen.error} />
    </article>
  );
}

function ConfirmDialog({ pair: p, onClose }: { pair: DuplicatePair; onClose: () => void }) {
  const invalidate = useInvalidatePairs();
  const [keep, setKeep] = useState<"a" | "b">("a");
  const [note, setNote] = useState("");
  const confirm = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/v1/admin/duplicates/{id}:confirm", {
          params: { path: { id: p.id ?? "" } },
          body: { keep, note: note.trim() },
        }),
      ),
    onSuccess: async () => {
      await invalidate();
      onClose();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    confirm.mutate();
  };
  const other = keep === "a" ? p.entryB : p.entryA;
  return (
    <Dialog title="Merge these entries?" onClose={onClose}>
      <form onSubmit={submit}>
        <p>
          The two become one entry. Every provider record of the other moves over, and its ID redirects, so links keep
          working.
        </p>
        <fieldset className="choices">
          <legend>Keep</legend>
          {(["a", "b"] as const).map((side) => (
            <label key={side} className="filter-check">
              <input type="radio" name="keep" checked={keep === side} onChange={() => setKeep(side)} />
              <span>{(side === "a" ? p.entryA : p.entryB)?.title}</span>
            </label>
          ))}
        </fieldset>
        <p className="muted small">"{other?.title}" is merged into it.</p>
        <Field label="Note (optional)">
          <input type="text" value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>
        <ErrorBox error={confirm.error} />
        <div className="actions">
          <button type="submit" className="primary" disabled={confirm.isPending}>
            {confirm.isPending ? "Merging…" : "Merge"}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function DismissDialog({ pair: p, onClose }: { pair: DuplicatePair; onClose: () => void }) {
  const invalidate = useInvalidatePairs();
  const [note, setNote] = useState("");
  const dismiss = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/v1/admin/duplicates/{id}:dismiss", {
          params: { path: { id: p.id ?? "" } },
          body: { note: note.trim() },
        }),
      ),
    onSuccess: async () => {
      await invalidate();
      onClose();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    dismiss.mutate();
  };
  return (
    <Dialog title="Different works?" onClose={onClose}>
      <form onSubmit={submit}>
        <p>
          "{p.entryA?.title}" and "{p.entryB?.title}" stay apart, and this pair won't be suggested again.
        </p>
        <Field label="Note (optional)" hint="Why they differ, for whoever looks next.">
          <input type="text" value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>
        <ErrorBox error={dismiss.error} />
        <div className="actions">
          <button type="submit" className="primary" disabled={dismiss.isPending}>
            {dismiss.isPending ? "Saving…" : "Dismiss"}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Decision model
// ---------------------------------------------------------------------------

const judgeKinds: Record<string, string> = {
  openai: "OpenAI Decisions",
  typesafe: "TypeSafe",
  clef: "Cloudflare Workers AI (Clef)",
};

function JudgeSection() {
  const judge = useQuery({
    queryKey: ["decision-judge"],
    queryFn: async () => (await unwrap(api.GET("/v1/admin/decision-judge"))).settings ?? {},
  });
  const queryClient = useQueryClient();
  const reset = useMutation({
    mutationFn: () => unwrap(api.POST("/v1/admin/decision-judge:reset", { body: {} })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["decision-judge"] }),
  });
  const [editing, setEditing] = useState(false);
  const s = judge.data;
  return (
    <section className="section" aria-label="Decision model">
      <header className="section-header">
        <h2>Decision model</h2>
        <button type="button" disabled={!s} onClick={() => setEditing(true)}>
          Change
        </button>
      </header>
      <p className="muted">
        Optional: after each run, a decision model rates the best pairs with the probability that they are the same
        work. It answers with numbers, not reasons. Entries' titles, people, years and (if allowed) covers are sent to
        the model's provider; nothing is sent while it is off.
      </p>
      {judge.isPending ? <Loading /> : null}
      <ErrorBox error={judge.error ?? reset.error} />
      {s ? (
        <dl className="details">
          <dt>Status</dt>
          <dd>
            <Badge tone={s.enabled ? "good" : "neutral"}>{s.enabled ? "On" : "Off"}</Badge>{" "}
            <span className="muted small">{s.origin === "admin" ? "set here" : "from the configuration file"}</span>
          </dd>
          {s.kind ? (
            <>
              <dt>Model</dt>
              <dd>
                {judgeKinds[s.kind] ?? s.kind} {s.model ? <Mono>{s.model}</Mono> : null}{" "}
                {s.hasApiKey ? <span className="muted small">key stored</span> : null}
                {s.covers ? <span className="muted small"> · sends covers</span> : null}
              </dd>
            </>
          ) : null}
        </dl>
      ) : null}
      {s?.origin === "admin" ? (
        <div className="actions">
          <button type="button" disabled={reset.isPending} onClick={() => reset.mutate()}>
            Use the configuration file
          </button>
        </div>
      ) : null}
      {editing && s ? <JudgeDialog current={s} onClose={() => setEditing(false)} /> : null}
    </section>
  );
}

function JudgeDialog({ current, onClose }: { current: DecisionJudgeSettings; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [enabled, setEnabled] = useState(current.enabled ?? false);
  const [kind, setKind] = useState(current.kind || "openai");
  const [url, setUrl] = useState(current.url ?? "");
  const [model, setModel] = useState(current.model ?? "");
  const [apiKey, setApiKey] = useState("");
  const [covers, setCovers] = useState(current.covers ?? false);
  const [tested, setTested] = useState<{ ok: boolean; error?: string; same?: number } | null>(null);
  const settings = (): DecisionJudgeSettings => ({
    enabled,
    kind,
    url: url.trim(),
    model: model.trim(),
    apiKey,
    covers,
  });
  const keyStored = current.hasApiKey && kind === current.kind && url.trim() === (current.url ?? "");
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/v1/admin/decision-judge:test", { body: { settings: settings() } })),
    onSuccess: (res) => setTested({ ok: !!res.ok, error: res.error, same: res.verdict?.same }),
  });
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/v1/admin/decision-judge", { body: { settings: settings() } })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["decision-judge"] });
      onClose();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate();
  };
  return (
    <Dialog title="Decision model" onClose={onClose}>
      <form className="form-grid two" onSubmit={submit}>
        <label className="filter-check span-2">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          <span>Rate the best pairs after each run</span>
        </label>
        <Field label="Provider">
          <select value={kind} onChange={(e) => setKind(e.target.value)}>
            {Object.entries(judgeKinds).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Model" hint="Empty: the provider's default.">
          <input type="text" value={model} onChange={(e) => setModel(e.target.value)} />
        </Field>
        <Field
          label="URL"
          className="span-2"
          hint={
            kind === "clef"
              ? "The model's run URL: …/accounts/<account>/ai/run/@cf/cloudflare/clef"
              : "Empty: the provider's API."
          }
        >
          <input type="url" required={kind === "clef"} value={url} onChange={(e) => setUrl(e.target.value)} />
        </Field>
        <Field
          label="API key"
          className="span-2"
          hint={
            keyStored
              ? "Leave empty to keep the stored key. Stored encrypted; never shown again."
              : "Stored encrypted; never shown again."
          }
        >
          <input
            type="password"
            autoComplete="new-password"
            value={apiKey}
            placeholder={keyStored ? "(stored)" : ""}
            onChange={(e) => setApiKey(e.target.value)}
          />
        </Field>
        {kind !== "typesafe" ? (
          <label className="filter-check span-2">
            <input type="checkbox" checked={covers} onChange={(e) => setCovers(e.target.checked)} />
            <span>Send cover images too (they count as input tokens)</span>
          </label>
        ) : null}
        <div className="span-2">
          {tested ? (
            tested.ok ? (
              <p className="notice-box" role="status">
                Answered: {pct(tested.same)} that two listings of one made-up work are the same.
              </p>
            ) : (
              <div className="error-box" role="alert">
                {tested.error}
              </div>
            )
          ) : null}
          <ErrorBox error={test.error ?? save.error} />
        </div>
        <div className="actions span-2">
          <button type="submit" className="primary" disabled={save.isPending}>
            {save.isPending ? "Saving…" : "Save"}
          </button>
          <button type="button" disabled={test.isPending} onClick={() => test.mutate()}>
            {test.isPending ? "Asking…" : "Test"}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Dialog>
  );
}

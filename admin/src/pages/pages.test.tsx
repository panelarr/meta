// SPDX-License-Identifier: GPL-3.0-or-later

import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";
import { MERGED_ENTRY_ID } from "../mocks/data";
import { renderApp } from "../test/render";
import { server } from "../test/setup";

const FIRST = "0192aa00-0000-7000-8000-000000000001";

describe("catalogue", () => {
  it("browses by popularity with attribution and paging", async () => {
    let lastQuery = "";
    server.events.on("request:start", ({ request }) => {
      if (request.url.includes("/v1/entries?")) lastQuery = new URL(request.url).search;
    });
    const app = renderApp("/catalogue");
    const cards = await screen.findAllByRole("link", { name: /Lantern Keeper|Clockwork|Orbit|Salt/ });
    // Popularity: rank 1 first.
    expect(cards[0]).toHaveTextContent("Lantern Keeper");
    expect(screen.getByText("4 entries")).toBeInTheDocument();
    expect(screen.getAllByText("Data: Example Data").length).toBe(4);
    expect(lastQuery).toContain("pageSize=24");
    server.events.removeAllListeners();
    app.dispose();
  });

  it("filters by type, genres and year, and sorts", async () => {
    const app = renderApp("/catalogue");
    await screen.findByText("4 entries");

    await userEvent.selectOptions(screen.getByLabelText("Type"), "ENTRY_TYPE_MANGA");
    expect(await screen.findByText("2 entries")).toBeInTheDocument();
    expect(app.router.state.location.search).toMatchObject({ type: "ENTRY_TYPE_MANGA" });

    // "Quiet Orbit" has a tag "Settings › Fantasy" but not the genre: it must not match.
    await userEvent.type(screen.getByRole("combobox", { name: "Genres" }), "Fan");
    await userEvent.click(await screen.findByRole("option", { name: /Fantasy/ }));
    expect(await screen.findByText("1 entry")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /The Clockwork Garden/ })).toBeInTheDocument();
    expect(app.router.state.location.search).toMatchObject({ genre: ["Fantasy"] });

    await userEvent.click(screen.getByRole("button", { name: "Reset" }));
    expect(await screen.findByText("4 entries")).toBeInTheDocument();

    // Typed fields apply on their own after a pause.
    await userEvent.type(screen.getByLabelText("From year"), "2015");
    expect(await screen.findByText("2 entries", {}, { timeout: 3000 })).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Sort"), "SORT_FIELD_TITLE");
    await waitFor(() => {
      const titles = screen.getAllByRole("link", { name: /Lantern|Salt/ }).map((a) => a.textContent);
      expect(titles).toEqual(["Lantern Keeper", "Salt and Iron"]);
    });
    app.dispose();
  });

  it("includes and excludes tags found by typing", async () => {
    const app = renderApp("/catalogue");
    await screen.findByText("4 entries");

    await userEvent.type(screen.getByRole("combobox", { name: "With tags" }), "spa");
    await userEvent.click(await screen.findByRole("option", { name: /Settings › Space/ }));
    expect(await screen.findByText("1 entry")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Quiet Orbit/ })).toBeInTheDocument();
    expect(app.router.state.location.search).toMatchObject({ tag: ["tag/settings/space"] });

    await userEvent.click(screen.getByRole("button", { name: "Remove Settings › Space" }));
    expect(await screen.findByText("4 entries")).toBeInTheDocument();

    // Leaving out the tag "Settings › Fantasy" keeps the entries with the genre Fantasy.
    await userEvent.type(screen.getByRole("combobox", { name: "Without tags" }), "fantasy");
    expect(await screen.findByRole("option", { name: /Fantasy \(genre\)/ })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("option", { name: /Settings › Fantasy/ }));
    expect(await screen.findByText("3 entries")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Quiet Orbit/ })).not.toBeInTheDocument();
    app.dispose();
  });

  it("filters by original and title language, by name", async () => {
    const app = renderApp("/catalogue");
    await screen.findByText("4 entries");
    const original = screen.getByLabelText("Original language");
    await within(original).findByRole("option", { name: "Korean (1)" });
    await userEvent.selectOptions(original, "ko");
    expect(await screen.findByText("1 entry")).toBeInTheDocument();
    expect(app.router.state.location.search).toMatchObject({ lang: "ko" });

    await userEvent.selectOptions(original, "");
    await userEvent.selectOptions(screen.getByLabelText("Title language"), "zh");
    expect(await screen.findByText("1 entry")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Salt and Iron/ })).toBeInTheDocument();
    app.dispose();
  });

  it("applies a genre from a card and shows how many more genres there are", async () => {
    const app = renderApp("/catalogue");
    await screen.findByText("4 entries");
    await userEvent.click(screen.getAllByRole("button", { name: "Action" })[0] as HTMLElement);
    expect(await screen.findByText("2 entries")).toBeInTheDocument();
    expect(app.router.state.location.search).toMatchObject({ genre: ["Action"] });
    app.dispose();

    server.use(
      http.get("*/v1/entries", () =>
        HttpResponse.json({
          totalSize: "1",
          entries: [{ id: "x", title: "Many Genres", genres: ["A", "B", "C", "D", "E"] }],
        }),
      ),
    );
    const many = renderApp("/catalogue");
    expect(await screen.findByText("+2")).toHaveAttribute("title", "D, E");
    many.dispose();
  });

  it("searches titles in any language and people", async () => {
    const app = renderApp("/catalogue");
    await screen.findByText("4 entries");
    await userEvent.type(screen.getByLabelText("Search"), "時計");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByText("1 entry")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /The Clockwork Garden/ })).toBeInTheDocument();
    expect(screen.getByText("Matched: 時計仕掛けの庭")).toBeInTheDocument();

    await userEvent.clear(screen.getByLabelText("Search"));
    await userEvent.type(screen.getByLabelText("Search"), "nothing like it");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByText("Nothing matches.")).toBeInTheDocument();
    app.dispose();
  });

  it("says when no providers are configured", async () => {
    server.use(http.get("*/v1/entries", () => HttpResponse.json({ entries: [], totalSize: "0", noProviders: true })));
    const app = renderApp("/catalogue");
    expect(await screen.findByText(/No providers configured/)).toBeInTheDocument();
    app.dispose();
  });
});

describe("entry", () => {
  it("shows the details, provenance and attribution, with spoilers hidden", async () => {
    const app = renderApp(`/entries/${FIRST}`);
    expect(await screen.findByRole("heading", { name: "The Clockwork Garden" })).toBeInTheDocument();
    expect(screen.getByText("時計仕掛けの庭")).toBeInTheDocument();
    expect(screen.getByText("Aiko Fictional")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Lantern Keeper" })).toHaveAttribute(
      "href",
      "/entries/0192aa00-0000-7000-8000-000000000002",
    );
    expect(screen.getByText("An unknown spin-off")).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "Example Data" }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("link", { name: "CC BY 4.0" }).length).toBeGreaterThan(0);

    expect(screen.getByText("Gardens")).toBeInTheDocument();
    expect(screen.queryByText("Time Loop")).not.toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("Show spoiler tags (1)"));
    expect(screen.getByText("Time Loop")).toBeInTheDocument();
    app.dispose();
  });

  it("links tags and genres to the catalogue, with the tag's path", async () => {
    const app = renderApp("/entries/0192aa00-0000-7000-8000-000000000003");
    const tag = await screen.findByRole("link", { name: "Settings › Fantasy" });
    expect(tag.getAttribute("href")).toContain("tag=");
    await userEvent.click(tag);
    expect(await screen.findByText("1 entry")).toBeInTheDocument();
    expect(app.router.state.location.search).toMatchObject({ tag: ["tag/settings/fantasy"] });
    app.dispose();
  });

  it("follows a merged entry's ID and says so", async () => {
    const app = renderApp(`/entries/${MERGED_ENTRY_ID}`);
    expect(await screen.findByRole("heading", { name: "The Clockwork Garden" })).toBeInTheDocument();
    expect(screen.getByText(/was merged into this one/)).toBeInTheDocument();
    app.dispose();
  });

  it("refreshes from providers", async () => {
    const app = renderApp(`/entries/${FIRST}`);
    await userEvent.click(await screen.findByRole("button", { name: "Refresh from providers" }));
    expect(await screen.findByText("Refreshed from: example.")).toBeInTheDocument();
    app.dispose();
  });

  it("shows a missing entry's error", async () => {
    const app = renderApp("/entries/nope");
    expect(await screen.findByRole("alert")).toHaveTextContent("no entry nope");
    app.dispose();
  });
});

describe("providers", () => {
  it("lists providers with health, ingestion and schedules", async () => {
    const app = renderApp("/providers");
    const card = await screen.findByRole("article", { name: "Provider example" });
    expect(within(card).getByText("Healthy")).toBeInTheDocument();
    expect(within(card).getByTitle("Nightly snapshot")).toHaveTextContent("dump");
    expect(within(card).getByText("0 3 * * *")).toBeInTheDocument();
    const down = screen.getByRole("article", { name: "Provider sample" });
    expect(within(down).getByText("Down")).toBeInTheDocument();
    expect(within(down).queryByRole("button", { name: /Run/ })).not.toBeInTheDocument();
    // The top bar flags that meta isn't fully healthy.
    expect(await screen.findByRole("link", { name: "Degraded: 1 part" })).toHaveAttribute("title", "provider:sample");
    app.dispose();
  });

  it("runs a sync now, then refuses a second one while it runs", async () => {
    const app = renderApp("/providers");
    const card = await screen.findByRole("article", { name: "Provider example" });
    await userEvent.click(within(card).getByRole("button", { name: "Run dump now" }));
    await userEvent.click(screen.getByLabelText(/Force/));
    await userEvent.click(screen.getByRole("button", { name: "Run now" }));
    // The new run opens in the details dialog.
    const dialog = await screen.findByRole("dialog", { name: "Sync run" });
    expect(within(dialog).getByText("Running")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Close" }));

    await userEvent.click(within(card).getByRole("button", { name: "Run dump now" }));
    await userEvent.click(screen.getByRole("button", { name: "Run now" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("A sync of example is already running");
    app.dispose();
  });

  it("lists sync runs with their outcome and details", async () => {
    const app = renderApp("/providers");
    const table = await screen.findByRole("table");
    expect(within(table).getByText("Succeeded")).toBeInTheDocument();
    expect(within(table).getByText("Failed")).toBeInTheDocument();
    expect(within(table).getByText("564,040")).toBeInTheDocument();
    await userEvent.click(within(table).getAllByRole("button", { name: "Details" })[1] as HTMLElement);
    const dialog = await screen.findByRole("dialog", { name: "Sync run" });
    expect(within(dialog).getByText(/checksum doesn't match/)).toBeInTheDocument();
    app.dispose();
  });

  it("filters runs by provider with one selected tab", async () => {
    const app = renderApp("/providers");
    await screen.findByRole("table");
    await userEvent.click(screen.getByRole("tab", { name: "sample" }));
    expect(await screen.findByText("No sync runs yet.")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "sample" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "All" })).toHaveAttribute("aria-selected", "false");
    app.dispose();
  });

  it("explains an empty provider list", async () => {
    server.use(http.get("*/v1/admin/providers", () => HttpResponse.json({ providers: [] })));
    const app = renderApp("/providers");
    expect(await screen.findByText(/No providers configured/)).toBeInTheDocument();
    app.dispose();
  });
});

describe("jobs", () => {
  it("lists jobs by group with schedule, last run and next run", async () => {
    const app = renderApp("/jobs");
    const providers = await screen.findByRole("region", { name: "Providers" });
    const sync = within(providers).getByRole("row", { name: "Job Sync example (dump)" });
    expect(within(sync).getByText("0 3 * * * UTC")).toBeInTheDocument();
    expect(within(sync).getByText("providers[example].schedules.dump")).toBeInTheDocument();
    expect(within(sync).getByText("Succeeded")).toBeInTheDocument();
    expect(within(sync).getByText(/564040 records/)).toBeInTheDocument();
    const maintenance = screen.getByRole("region", { name: "Maintenance" });
    const stale = within(maintenance).getByRole("row", { name: "Job Refresh stale entries" });
    expect(within(stale).getByText("manual only")).toBeInTheDocument();
    expect(within(stale).getByText("Failed")).toBeInTheDocument();
    expect(within(stale).getByText(/database out of space/)).toBeInTheDocument();
    app.dispose();
  });

  it("runs a job with its params, follows the run and cancels it", async () => {
    let sent: unknown;
    server.events.on("request:start", async ({ request }) => {
      if (request.url.endsWith(":run")) sent = await request.clone().json();
    });
    const app = renderApp("/jobs");
    const row = await screen.findByRole("row", { name: "Job Refresh stale entries" });
    await userEvent.click(within(row).getByRole("button", { name: "Run now" }));
    const form = await screen.findByRole("dialog", { name: "Run Refresh stale entries?" });
    const limit = within(form).getByLabelText("limit");
    expect(limit).toHaveValue(100);
    await userEvent.clear(limit);
    await userEvent.type(limit, "5");
    await userEvent.click(within(form).getByRole("checkbox", { name: "sample" }));
    await userEvent.click(within(form).getByRole("button", { name: "Run now" }));

    const dialog = await screen.findByRole("dialog", { name: "Run of Refresh stale entries" });
    expect(within(dialog).getByText("Running")).toBeInTheDocument();
    expect(within(dialog).getByText(/250 \/ 1,000 items \(25%\)/)).toBeInTheDocument();
    expect(within(dialog).getByText("by admin", { exact: false })).toBeInTheDocument();
    expect(sent).toEqual({ params: { limit: "5", providers: "sample" } });
    expect(app.router.state.location.search).toMatchObject({ job: "stale-refresh" });

    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel run" }));
    expect(await within(dialog).findByText("Cancelled")).toBeInTheDocument();
    server.events.removeAllListeners();
    app.dispose();
  });

  it("says when a job started elsewhere is already running", async () => {
    // Another admin or the schedule started it after the list loaded: the API answers 409.
    server.use(
      http.post(/\/v1\/admin\/jobs\/rebuild-entries:run$/, () =>
        HttpResponse.json(
          { code: 6, message: "job rebuild-entries is already running (run r-1)", details: [] },
          { status: 409 },
        ),
      ),
      http.get("*/v1/admin/jobs/rebuild-entries", () =>
        HttpResponse.json({ job: { id: "rebuild-entries", running: true, currentRunId: "r-1" } }),
      ),
    );
    const app = renderApp("/jobs");
    const row = await screen.findByRole("row", { name: "Job Rebuild entries" });
    await userEvent.click(within(row).getByRole("button", { name: "Run now" }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Run now" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Rebuild entries is already running");
    await userEvent.click(await screen.findByRole("button", { name: "Open the current run" }));
    await waitFor(() => expect(app.router.state.location.search).toMatchObject({ job: "rebuild-entries", run: "r-1" }));
    app.dispose();
  });

  it("shows a job's run history with a status filter and run details", async () => {
    const app = renderApp("/jobs?job=sync.example.dump");
    const runs = await screen.findByRole("region", { name: "Runs of Sync example (dump)" });
    expect(await within(runs).findByText(/1,203 changed/)).toBeInTheDocument();
    await userEvent.selectOptions(within(runs).getByLabelText("Status"), "JOB_RUN_STATUS_FAILED");
    expect(await within(runs).findByText("No runs.")).toBeInTheDocument();
    await userEvent.selectOptions(within(runs).getByLabelText("Status"), "");
    await userEvent.click(await within(runs).findByRole("button", { name: "Details" }));
    const dialog = await screen.findByRole("dialog", { name: "Run of Sync example (dump)" });
    expect(within(dialog).getByText("row has no title; skipped")).toBeInTheDocument();
    expect(within(dialog).getByText("force")).toBeInTheDocument();
    app.dispose();
  });
});

describe("clients", () => {
  it("creates a client and shows its key once", async () => {
    const app = renderApp("/clients");
    await screen.findByText("library-server");
    await userEvent.click(screen.getByRole("button", { name: "New client" }));
    await userEvent.type(screen.getByLabelText("Name"), "my script");
    expect(screen.getByRole("checkbox", { name: /catalog:read/ })).toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: "Create" }));

    const key = await screen.findByTestId("api-key");
    expect(key.textContent).toMatch(/^mtk_/);
    await userEvent.click(screen.getByRole("button", { name: "I've copied it" }));
    expect(screen.queryByTestId("api-key")).not.toBeInTheDocument();
    expect(await screen.findByText("my script")).toBeInTheDocument();
    app.dispose();
  });

  it("rotates a key", async () => {
    const app = renderApp("/clients");
    await screen.findByText("library-server");
    await userEvent.click(screen.getByRole("button", { name: "Rotate key" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Rotate key" }));
    expect(await screen.findByRole("heading", { name: "New key for library-server" })).toBeInTheDocument();
    expect((await screen.findByTestId("api-key")).textContent).toMatch(/^mtk_/);
    app.dispose();
  });
});

describe("integrations", () => {
  it("shows solvers, profiles and where each provider's settings come from", async () => {
    const app = renderApp("/integrations");
    expect(await screen.findByText("FlareSolverr")).toBeInTheDocument();
    expect(screen.getByText("password stored")).toBeInTheDocument();
    const rows = await screen.findAllByRole("row");
    const example = rows.find((r) => within(r).queryByText("example"));
    expect(example && within(example).getByText("Assigned", { selector: ".badge" })).toBeInTheDocument();
    const sample = rows.find((r) => within(r).queryByText("sample"));
    expect(sample && within(sample).getByText("Default", { selector: ".badge" })).toBeInTheDocument();
    expect(sample && within(sample).getByText(/doesn't use network profiles/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Search engine" })).toBeInTheDocument();
    app.dispose();
  });

  it("keeps the stored proxy password when the field is left empty", async () => {
    let body: { proxy?: { password?: string } } | null = null;
    server.events.on("request:start", async ({ request }) => {
      if (request.method === "PUT" && request.url.includes("/v1/admin/profiles/via-proxy")) {
        body = await request.clone().json();
      }
    });
    const app = renderApp("/integrations");
    const row = (await screen.findAllByRole("row")).find((r) => within(r).queryByText("Via proxy"));
    await userEvent.click(within(row as HTMLElement).getByRole("button", { name: "Edit" }));
    expect(screen.getByPlaceholderText("Leave empty to keep")).toHaveValue("");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(body).not.toBeNull());
    expect((body as unknown as { proxy: { password?: string } }).proxy.password).toBeUndefined();
    expect(await screen.findByText("password stored")).toBeInTheDocument();
    server.events.removeAllListeners();
    app.dispose();
  });

  it("flags an unused solver and puts it on the default profile", async () => {
    const app = renderApp("/integrations");
    const row = (await screen.findAllByRole("row")).find((r) => within(r).queryByText("Spare solver"));
    expect(within(row as HTMLElement).getByText("Not used by any profile")).toBeInTheDocument();
    await userEvent.click(within(row as HTMLElement).getByRole("button", { name: "Use for all providers" }));
    await waitFor(() => {
      const updated = screen.getAllByRole("row").find((r) => within(r).queryByText("Spare solver"));
      expect(within(updated as HTMLElement).queryByText("Not used by any profile")).not.toBeInTheDocument();
    });
    app.dispose();
  });

  it("shows the API's error when a solver is still in use", async () => {
    const app = renderApp("/integrations");
    const row = (await screen.findAllByRole("row")).find((r) => within(r).queryByText("FlareSolverr"));
    await userEvent.click(within(row as HTMLElement).getByRole("button", { name: "Delete" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete solver" }));
    expect(await within(screen.getByRole("dialog")).findByRole("alert")).toHaveTextContent(
      'solver "flaresolverr" is used by profiles: via-proxy',
    );
    app.dispose();
  });

  it("assigns a profile to a provider", async () => {
    const app = renderApp("/integrations");
    await userEvent.selectOptions(await screen.findByLabelText("Profile for sample"), "polite");
    await waitFor(() => {
      const row = screen.getAllByRole("row").find((r) => within(r).queryByText("sample"));
      expect(row && within(row).getByText("Assigned", { selector: ".badge" })).toBeInTheDocument();
    });
    app.dispose();
  });
});

describe("search engine", () => {
  it("shows PostgreSQL by default, then switches to an engine with a stored key and back", async () => {
    let sent: unknown;
    server.events.on("request:start", async ({ request }) => {
      if (request.method === "PUT" && request.url.endsWith("/v1/admin/search-engine"))
        sent = await request.clone().json();
    });
    const app = renderApp("/integrations");
    const section = await screen.findByRole("region", { name: "Search engine" });
    const card = await within(section).findByRole("article", { name: "Search backend PostgreSQL" });
    expect(within(card).getByText("From the configuration file")).toBeInTheDocument();
    expect(within(card).getByText("no facet counts")).toBeInTheDocument();

    await userEvent.click(within(section).getByRole("button", { name: "Change" }));
    const dialog = await screen.findByRole("dialog", { name: "Search engine" });
    await userEvent.selectOptions(within(dialog).getByLabelText("Backend"), "typesense");
    expect(within(dialog).getByLabelText("URL")).toHaveValue("http://typesense:8108");
    await userEvent.clear(within(dialog).getByLabelText("URL"));
    await userEvent.type(within(dialog).getByLabelText("URL"), "http://nowhere:8108");
    await userEvent.click(within(dialog).getByRole("button", { name: "Test connection" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("connection refused");
    await userEvent.clear(within(dialog).getByLabelText("URL"));
    await userEvent.type(within(dialog).getByLabelText("URL"), "http://typesense:8108");
    await userEvent.type(within(dialog).getByLabelText("API key"), "secret-key");
    await userEvent.click(within(dialog).getByRole("button", { name: "Test connection" }));
    expect(await within(dialog).findByText("Connected.")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    expect(sent).toMatchObject({
      settings: { backend: "typesense", url: "http://typesense:8108", apiKey: "secret-key" },
    });

    const engine = await within(section).findByRole("article", { name: "Search backend Typesense" });
    expect(within(engine).getByText("Set here")).toBeInTheDocument();
    expect(within(engine).getByText("key stored")).toBeInTheDocument();
    expect(within(engine).getByText("Index ready")).toBeInTheDocument();
    expect(within(engine).getByText("facet counts")).toBeInTheDocument();
    expect(section).not.toHaveTextContent("secret-key");

    await userEvent.click(within(engine).getByRole("button", { name: "Use the configuration file" }));
    expect(await within(section).findByRole("article", { name: "Search backend PostgreSQL" })).toBeInTheDocument();
    server.events.removeAllListeners();
    app.dispose();
  });

  it("starts a rebuild and opens it on the Jobs page", async () => {
    server.use(
      http.get("*/v1/admin/search-engine", () =>
        HttpResponse.json({
          settings: { backend: "meilisearch", url: "http://meilisearch:7700", hasApiKey: true, origin: "config" },
          status: { backend: "meilisearch", facets: true, reachable: false, ready: false, error: "dial tcp: refused" },
        }),
      ),
    );
    const app = renderApp("/integrations");
    const engine = await screen.findByRole("article", { name: "Search backend Meilisearch" });
    expect(within(engine).getByText("Unreachable")).toBeInTheDocument();
    expect(within(engine).getByText("No index yet")).toBeInTheDocument();
    expect(within(engine).getByRole("alert")).toHaveTextContent("dial tcp: refused");
    expect(within(engine).queryByRole("button", { name: "Use the configuration file" })).toBeNull();
    server.use(
      http.post(/\/v1\/admin\/jobs\/search-rebuild:run$/, () =>
        HttpResponse.json({ run: { id: "rb-1", jobId: "search-rebuild" } }),
      ),
    );
    await userEvent.click(within(engine).getByRole("button", { name: "Rebuild index" }));
    await waitFor(() => expect(app.router.state.location.pathname).toBe("/jobs"));
    expect(app.router.state.location.search).toMatchObject({ job: "search-rebuild", run: "rb-1" });
    app.dispose();
  });

  it("shows facet counts in the catalogue's filters when an engine has them", async () => {
    const app = renderApp("/integrations");
    const section = await screen.findByRole("region", { name: "Search engine" });
    await userEvent.click(await within(section).findByRole("button", { name: "Change" }));
    const dialog = await screen.findByRole("dialog", { name: "Search engine" });
    await userEvent.selectOptions(within(dialog).getByLabelText("Backend"), "typesense");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await within(section).findByRole("article", { name: "Search backend Typesense" });
    app.dispose();
    app.unmount();

    const cat = renderApp("/catalogue");
    await screen.findByText("4 entries");
    expect(await screen.findByRole("option", { name: "Manga (2)" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Manhwa (1)" })).toBeInTheDocument();
    cat.dispose();
  });
});

describe("health", () => {
  it("shows every part with its status and details", async () => {
    const app = renderApp("/health");
    const rows = await screen.findAllByRole("row");
    expect(rows).toHaveLength(5); // header, database, search, two providers
    const db = rows.find((r) => within(r).queryByText("Database"));
    expect(db && within(db).getByText("Healthy")).toBeInTheDocument();
    const search = rows.find((r) => within(r).queryByText("Search"));
    expect(search && within(search).getByText("postgres")).toBeInTheDocument();
    const sample = rows.find((r) => within(r).queryByRole("link", { name: "sample" }));
    expect(sample && within(sample).getByText("Down")).toBeInTheDocument();
    expect(sample && within(sample).getByText("plugin process exited")).toBeInTheDocument();
    expect(screen.getByText("Degraded", { selector: ".badge" })).toBeInTheDocument();
    app.dispose();
  });
});

describe("clients scopes", () => {
  it("offers the metrics scope", async () => {
    const app = renderApp("/clients");
    await userEvent.click(await screen.findByRole("button", { name: "New client" }));
    expect(screen.getByRole("checkbox", { name: /metrics:read/ })).toBeInTheDocument();
    app.dispose();
  });
});

describe("layout", () => {
  it("shows exactly one selected tab in every tab list", async () => {
    for (const path of ["/providers", "/providers?provider=example", "/catalogue", "/clients", "/integrations"]) {
      const app = renderApp(path);
      await screen.findByRole("heading", { level: 1 });
      await waitFor(() => expect(screen.queryByRole("status")).toBeNull(), { timeout: 2000 }).catch(() => {});
      for (const list of screen.queryAllByRole("tablist")) {
        const selected = within(list)
          .getAllByRole("tab")
          .filter((tab) => tab.getAttribute("aria-selected") === "true");
        expect(selected, `${path}: ${list.getAttribute("aria-label")}`).toHaveLength(1);
      }
      app.dispose();
      app.unmount();
    }
  });

  it("shows a not-found page", async () => {
    const app = renderApp("/no/such/page");
    expect(await screen.findByRole("heading", { name: "Not found" })).toBeInTheDocument();
    app.dispose();
  });
});

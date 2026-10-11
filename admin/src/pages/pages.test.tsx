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

  it("filters by type, genre and year, and sorts", async () => {
    const app = renderApp("/catalogue");
    await screen.findByText("4 entries");

    await userEvent.selectOptions(screen.getByLabelText("Type"), "ENTRY_TYPE_MANGA");
    expect(await screen.findByText("2 entries")).toBeInTheDocument();
    expect(app.router.state.location.search).toMatchObject({ type: "ENTRY_TYPE_MANGA" });

    await userEvent.selectOptions(await screen.findByLabelText("Genre"), "Fantasy");
    expect(await screen.findByText("1 entry")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /The Clockwork Garden/ })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Reset" }));
    expect(await screen.findByText("4 entries")).toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("From year"), "2015");
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText("2 entries")).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Sort"), "SORT_FIELD_TITLE");
    await waitFor(() => {
      const titles = screen.getAllByRole("link", { name: /Lantern|Salt/ }).map((a) => a.textContent);
      expect(titles).toEqual(["Lantern Keeper", "Salt and Iron"]);
    });
    app.dispose();
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
    // The top bar flags the unhealthy provider.
    expect(screen.getByRole("link", { name: "1 provider unhealthy" })).toBeInTheDocument();
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

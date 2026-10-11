// SPDX-License-Identifier: GPL-3.0-or-later

import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";
import { renderApp } from "../test/render";
import { server } from "../test/setup";
import { safeRedirect } from "./login";
import { session } from "./session";

describe("auth flow", () => {
  it("sends a signed-out visitor to the login page and back after signing in", async () => {
    const app = renderApp("/clients", null);
    await screen.findByLabelText("Admin token");
    expect(app.router.state.location.pathname).toBe("/login");

    await userEvent.type(screen.getByLabelText("Admin token"), "  the-admin-token ");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));

    await screen.findByRole("heading", { name: "API clients" });
    expect(app.router.state.location.pathname).toBe("/clients");
    expect(sessionStorage.getItem("meta.admin-token")).toBe("the-admin-token");
    expect(document.cookie).toBe("");
    app.dispose();
  });

  it("shows the server's message for a rejected token", async () => {
    const app = renderApp("/login", null);
    await userEvent.type(await screen.findByLabelText("Admin token"), "bad");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "The server rejected this token: missing or wrong API key",
    );
    expect(session.token()).toBeNull();
    app.dispose();
  });

  it("refuses a client API key", async () => {
    const app = renderApp("/login", null);
    await userEvent.type(await screen.findByLabelText("Admin token"), "mtk_client_key");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent('API key of client "library-server"');
    expect(session.token()).toBeNull();
    app.dispose();
  });

  it("refuses a key from the api_keys setting", async () => {
    const app = renderApp("/login", null);
    await userEvent.type(await screen.findByLabelText("Admin token"), "config-key");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("a key from the api_keys setting");
    expect(session.token()).toBeNull();
    app.dispose();
  });

  it("returns to the login page when the token stops working", async () => {
    server.use(
      http.get("*/v1/admin/clients", () =>
        HttpResponse.json({ code: "unauthenticated", message: "missing or wrong API key" }, { status: 401 }),
      ),
    );
    const app = renderApp("/clients");
    expect(await screen.findByText(/Your session ended/)).toBeInTheDocument();
    expect(app.router.state.location.pathname).toBe("/login");
    expect(session.token()).toBeNull();
    app.dispose();
  });

  it("logs out", async () => {
    const app = renderApp("/catalogue");
    await userEvent.click(await screen.findByRole("button", { name: "Log out" }));
    await screen.findByLabelText("Admin token");
    expect(session.token()).toBeNull();
    expect(screen.queryByText(/Your session ended/)).not.toBeInTheDocument();
    app.dispose();
  });

  it("keeps a signed-in user away from the login page", async () => {
    const app = renderApp("/login");
    await waitFor(() => expect(app.router.state.location.pathname).toBe("/catalogue"));
    app.dispose();
  });
});

describe("safeRedirect", () => {
  it("only follows paths of this app", () => {
    expect(safeRedirect("/clients?x=1")).toBe("/clients?x=1");
    expect(safeRedirect(undefined)).toBe("/catalogue");
    expect(safeRedirect("https://example.com")).toBe("/catalogue");
    expect(safeRedirect("//example.com")).toBe("/catalogue");
    expect(safeRedirect("/\\example.com")).toBe("/catalogue");
    expect(safeRedirect("/login")).toBe("/catalogue");
  });
});

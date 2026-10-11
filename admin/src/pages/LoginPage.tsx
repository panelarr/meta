// SPDX-License-Identifier: GPL-3.0-or-later

import { useMutation } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { type FormEvent, useState } from "react";
import icon from "../assets/panelarr-icon.svg";
import { safeRedirect, verifyAdminToken } from "../auth/login";
import { session } from "../auth/session";
import { ErrorBox, Field } from "../components/ui";

const route = getRouteApi("/login");

export function LoginPage() {
  const search = route.useSearch();
  const navigate = useNavigate();
  const [token, setToken] = useState("");

  const login = useMutation({
    mutationFn: (candidate: string) => verifyAdminToken(candidate),
    onSuccess: () => {
      session.signIn(token.trim());
      void navigate({ to: safeRedirect(search.redirect) });
    },
  });

  const submit = (event: FormEvent) => {
    event.preventDefault();
    login.mutate(token);
  };

  return (
    <main className="login">
      <form className="card login-card" onSubmit={submit}>
        <div className="login-brand">
          <img src={icon} alt="" width={56} height={56} />
          <h1>
            <span className="brand-name">Panelarr</span> <span className="brand-product">Meta</span>
          </h1>
        </div>
        {search.expired ? (
          <p className="notice" role="status">
            Your session ended: the server no longer accepts the token. Sign in again.
          </p>
        ) : null}
        <Field
          label="Admin token"
          hint="Printed once when meta first starts, or set with META_ADMIN_TOKEN. Kept only in this tab."
        >
          <input
            type="password"
            name="token"
            autoComplete="current-password"
            value={token}
            onChange={(event) => setToken(event.target.value)}
            required
          />
        </Field>
        <ErrorBox error={login.error} />
        <button type="submit" className="primary" disabled={login.isPending}>
          {login.isPending ? "Checking…" : "Sign in"}
        </button>
      </form>
    </main>
  );
}

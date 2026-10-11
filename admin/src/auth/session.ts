// SPDX-License-Identifier: GPL-3.0-or-later

/**
 * The admin token for this browser tab. It lives in sessionStorage (never a cookie), so
 * it's gone when the tab closes, and is sent as `Authorization: Bearer <token>`.
 */
const storageKey = "meta.admin-token";

export type SignOutReason = "logout" | "expired";

type Listener = (reason: SignOutReason | null) => void;

const listeners = new Set<Listener>();
// Kept in memory too, so a browser that blocks storage still works for the session.
let memoryToken: string | null = null;

function read(): string | null {
  try {
    // Storage works: it alone decides, so clearing it signs the tab out.
    return sessionStorage.getItem(storageKey);
  } catch {
    return memoryToken;
  }
}

function write(token: string | null): void {
  memoryToken = token;
  try {
    if (token === null) sessionStorage.removeItem(storageKey);
    else sessionStorage.setItem(storageKey, token);
  } catch {
    // Storage blocked: the in-memory copy is enough for this page load.
  }
}

export const session = {
  token(): string | null {
    return read();
  },

  signedIn(): boolean {
    return read() !== null;
  },

  signIn(token: string): void {
    write(token);
    for (const listener of listeners) listener(null);
  },

  /** Forgets the token. `expired` means the server rejected it (HTTP 401). */
  signOut(reason: SignOutReason): void {
    if (read() === null) return;
    write(null);
    for (const listener of listeners) listener(reason);
  },

  /** Called after every sign-in (with null) and sign-out (with the reason). */
  subscribe(listener: Listener): () => void {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
};

// SPDX-License-Identifier: GPL-3.0-or-later

import createClient, { type Middleware } from "openapi-fetch";
import { session } from "../auth/session";
import { ApiError, networkError, toApiError } from "./errors";
import type { paths } from "./schema";

/** API calls go to the origin that served the SPA (the service, or the dev server's proxy). */
function baseUrl(): string {
  return typeof window === "undefined" ? "http://localhost" : window.location.origin;
}

function bearer(token: string): string {
  return `Bearer ${token}`;
}

/**
 * Sends the session token, unless the caller set its own header (the login page checks a
 * token before keeping it). A 401 for the session token signs the user out.
 */
const authMiddleware: Middleware = {
  onRequest({ request }) {
    const token = session.token();
    if (token && !request.headers.has("Authorization")) request.headers.set("Authorization", bearer(token));
    return request;
  },
  onResponse({ request, response }) {
    expireOn401(request.headers.get("Authorization"), response.status);
    return response;
  },
};

function expireOn401(sentAuthorization: string | null, status: number): void {
  const token = session.token();
  if (status === 401 && token && sentAuthorization === bearer(token)) session.signOut("expired");
}

// fetch is looked up per call, so anything that wraps it later (mock mode, tests) applies.
export const api = createClient<paths>({ baseUrl: baseUrl(), fetch: (request) => globalThis.fetch(request) });
api.use(authMiddleware);

type FetchResult<T> = { data?: T; error?: unknown; response: Response };

/** Awaits an openapi-fetch call and returns its data, or throws an ApiError. */
export async function unwrap<T>(call: Promise<FetchResult<T>>): Promise<T> {
  let result: FetchResult<T>;
  try {
    result = await call;
  } catch (cause) {
    throw networkError(cause);
  }
  if (!result.response.ok) throw toApiError(result.response.status, result.error);
  // Empty messages may come back as an empty body.
  return (result.data ?? {}) as T;
}

/**
 * fetch() for the endpoints outside the OpenAPI document (images, uploads), with the
 * same auth and error handling as `api`. Resolves only for 2xx responses.
 */
export async function authFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  const token = session.token();
  if (token && !headers.has("Authorization")) headers.set("Authorization", bearer(token));
  let response: Response;
  try {
    response = await fetch(new URL(path, baseUrl()), { ...init, headers });
  } catch (cause) {
    throw networkError(cause);
  }
  expireOn401(headers.get("Authorization"), response.status);
  if (!response.ok) throw toApiError(response.status, await response.text().catch(() => ""));
  return response;
}

/** Reads a JSON response of authFetch. */
export async function json<T>(response: Response): Promise<T> {
  try {
    return (await response.json()) as T;
  } catch {
    throw new ApiError(response.status, "The server sent a response that isn't JSON");
  }
}

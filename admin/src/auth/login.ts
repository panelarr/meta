// SPDX-License-Identifier: GPL-3.0-or-later

import { api, unwrap } from "../api/client";
import { ApiError } from "../api/errors";
import type { WhoAmI } from "../api/types";

/**
 * Checks a token with GET /v1/whoami before the UI keeps it. Only the admin token is
 * accepted: client API keys don't have the admin scope the UI needs (or shouldn't be
 * typed into a browser), and keys from the deprecated api_keys setting are on their way
 * out.
 */
export async function verifyAdminToken(token: string): Promise<WhoAmI> {
  const trimmed = token.trim();
  if (trimmed === "") throw new Error("Enter the admin token.");
  let who: WhoAmI;
  try {
    who = await unwrap(api.GET("/v1/whoami", { headers: { Authorization: `Bearer ${trimmed}` } }));
  } catch (error) {
    if (error instanceof ApiError && error.unauthenticated) {
      throw new ApiError(error.status, `The server rejected this token: ${error.message}`, error.grpcCode);
    }
    throw error;
  }
  if (who.kind === "PRINCIPAL_KIND_CONFIG_KEY") {
    throw new Error(
      "This is a key from the api_keys setting, not the admin token. Sign in with the admin token (META_ADMIN_TOKEN, or the one meta printed on its first start).",
    );
  }
  if (who.kind !== "PRINCIPAL_KIND_ADMIN") {
    throw new Error(
      `This is the API key of client "${who.name || who.clientId || "unknown"}", not the admin token. Sign in with the admin token.`,
    );
  }
  return who;
}

/** Only same-app paths are followed after login, never another origin. */
export function safeRedirect(target: string | undefined): string {
  if (!target?.startsWith("/") || target.startsWith("//") || target.includes("\\") || target.startsWith("/login")) {
    return "/catalogue";
  }
  return target;
}

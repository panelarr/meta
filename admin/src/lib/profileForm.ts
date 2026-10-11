// SPDX-License-Identifier: GPL-3.0-or-later

// The network profile form: NetworkProfile ↔ flat form fields. The proxy password is
// write-only: the API never returns it, and sending it empty keeps the stored one.
import type { NetworkProfile, UserAgentMode } from "../api/types";
import { durationInput, secondsToDuration } from "./duration";

export type ProfileForm = {
  id: string;
  name: string;
  proxyUrl: string;
  proxyUsername: string;
  proxyPassword: string;
  /** Whether the server has a password stored (from hasPassword). */
  hasPassword: boolean;
  solverId: string;
  rateRequests: string;
  ratePeriod: string;
  ratePerHost: boolean;
  concurrency: string;
  userAgentMode: UserAgentMode;
  userAgent: string;
  headers: string;
  connectTimeout: string;
  readTimeout: string;
  callTimeout: string;
};

export const idPattern = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

/** "Name: value" per line ↔ a header map. */
export function formatHeaders(headers: Record<string, string> | undefined): string {
  return Object.entries(headers ?? {})
    .map(([name, value]) => `${name}: ${value}`)
    .join("\n");
}

export function parseHeaders(text: string): Record<string, string> {
  const headers: Record<string, string> = {};
  text.split("\n").forEach((line, index) => {
    if (line.trim() === "") return;
    const colon = line.indexOf(":");
    const name = colon > 0 ? line.slice(0, colon).trim() : "";
    if (!name || /\s/.test(name)) throw new Error(`Header line ${index + 1} should look like "Name: value".`);
    headers[name] = line.slice(colon + 1).trim();
  });
  return headers;
}

export function profileToForm(profile?: NetworkProfile): ProfileForm {
  return {
    id: profile?.id ?? "",
    name: profile?.name ?? "",
    proxyUrl: profile?.proxy?.url ?? "",
    proxyUsername: profile?.proxy?.username ?? "",
    proxyPassword: "",
    hasPassword: profile?.proxy?.hasPassword ?? false,
    solverId: profile?.solverId ?? "",
    rateRequests: profile?.rateLimit?.requests ? String(profile.rateLimit.requests) : "",
    ratePeriod: durationInput(profile?.rateLimit?.period),
    ratePerHost: profile?.rateLimit?.perHost ?? false,
    concurrency: profile?.concurrency ? String(profile.concurrency) : "",
    userAgentMode: profile?.userAgentMode ?? "USER_AGENT_MODE_UNSPECIFIED",
    userAgent: profile?.userAgent ?? "",
    headers: formatHeaders(profile?.headers),
    connectTimeout: durationInput(profile?.connectTimeout),
    readTimeout: durationInput(profile?.readTimeout),
    callTimeout: durationInput(profile?.callTimeout),
  };
}

function wholeNumber(text: string, label: string): number | undefined {
  const trimmed = text.trim();
  if (trimmed === "") return undefined;
  const value = Number(trimmed);
  if (!Number.isInteger(value) || value < 0) throw new Error(`${label} must be a whole number.`);
  return value;
}

/** The body of PUT /v1/admin/profiles/{id}. Throws with a message on invalid input. */
export function formToProfile(form: ProfileForm): NetworkProfile {
  const id = form.id.trim();
  if (!idPattern.test(id)) throw new Error("The ID may only use lowercase letters, digits and dashes.");

  const profile: NetworkProfile = { id, name: form.name.trim() };

  if (form.proxyUrl.trim()) {
    profile.proxy = { url: form.proxyUrl.trim(), username: form.proxyUsername.trim() };
    // Empty keeps the stored password.
    if (form.proxyPassword) profile.proxy.password = form.proxyPassword;
  }
  if (form.solverId) profile.solverId = form.solverId;

  const requests = wholeNumber(form.rateRequests, "Requests");
  if (requests) {
    const period = secondsToDuration(form.ratePeriod, "Rate limit period");
    if (!period) throw new Error("A rate limit needs a period.");
    profile.rateLimit = { requests, period, perHost: form.ratePerHost };
  }

  const concurrency = wholeNumber(form.concurrency, "Concurrency");
  if (concurrency) profile.concurrency = concurrency;

  profile.userAgentMode = form.userAgentMode;
  if (form.userAgentMode === "USER_AGENT_MODE_FIXED") {
    if (!form.userAgent.trim()) throw new Error("A fixed user agent needs a value.");
    profile.userAgent = form.userAgent.trim();
  }

  const headers = parseHeaders(form.headers);
  if (Object.keys(headers).length > 0) profile.headers = headers;

  const connect = secondsToDuration(form.connectTimeout, "Connect timeout");
  const read = secondsToDuration(form.readTimeout, "Read timeout");
  const call = secondsToDuration(form.callTimeout, "Call timeout");
  if (connect) profile.connectTimeout = connect;
  if (read) profile.readTimeout = read;
  if (call) profile.callTimeout = call;
  return profile;
}

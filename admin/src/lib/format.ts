// SPDX-License-Identifier: GPL-3.0-or-later

/** "EXTENSION_STATUS_LOADED" with prefix "EXTENSION_STATUS_" → "Loaded". */
export function enumLabel(value: string | undefined, prefix: string): string {
  if (!value || value.endsWith("_UNSPECIFIED")) return "Unspecified";
  const words = value.replace(prefix, "").toLowerCase().split("_");
  const text = words.join(" ");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** An RFC 3339 timestamp as local date and time, or a dash when unset. */
export function formatDate(value: string | undefined): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

/** A language tag for display: "en" → "en (English)" when the browser knows it. */
export function languageLabel(lang: string | undefined): string {
  if (!lang) return "—";
  if (lang === "all" || lang === "multi") return `${lang} (multiple)`;
  try {
    const name = new Intl.DisplayNames(undefined, { type: "language" }).of(lang);
    return name && name !== lang ? `${lang} (${name})` : lang;
  } catch {
    return lang;
  }
}

/** First and last characters of a long hex string such as a SHA-256. */
export function shortHash(value: string | undefined, keep = 8): string {
  if (!value) return "—";
  return value.length <= keep * 2 + 1 ? value : `${value.slice(0, keep)}…${value.slice(-keep)}`;
}

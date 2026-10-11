// SPDX-License-Identifier: GPL-3.0-or-later

// google.protobuf.Duration in JSON is a string of seconds with an "s" suffix: "30s", "1.5s".

/** "1.5s" → 1.5; empty or malformed → undefined. */
export function durationToSeconds(value: string | undefined): number | undefined {
  if (!value) return undefined;
  const match = /^(-?\d+(?:\.\d+)?)s$/.exec(value.trim());
  return match?.[1] ? Number(match[1]) : undefined;
}

/** Form text in seconds ("30", "1.5") → "30s"; empty → undefined. Throws on bad input. */
export function secondsToDuration(text: string, label = "Duration"): string | undefined {
  const trimmed = text.trim();
  if (trimmed === "") return undefined;
  const seconds = Number(trimmed);
  if (!Number.isFinite(seconds) || seconds < 0) throw new Error(`${label} must be a number of seconds`);
  return `${seconds}s`;
}

/** "90s" → "1m 30s" for display; unset → dash. */
export function formatDuration(value: string | undefined): string {
  const seconds = durationToSeconds(value);
  if (seconds === undefined) return "—";
  if (seconds < 60 || !Number.isInteger(seconds)) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  if (minutes < 60) return rest ? `${minutes}m ${rest}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return minutes % 60 ? `${hours}h ${minutes % 60}m` : `${hours}h`;
}

/** For a seconds input: "30s" → "30". */
export function durationInput(value: string | undefined): string {
  const seconds = durationToSeconds(value);
  return seconds === undefined ? "" : String(seconds);
}

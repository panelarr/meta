// SPDX-License-Identifier: GPL-3.0-or-later

/** gRPC status names by number. */
const grpcNames: Record<number, string> = {
  1: "CANCELED",
  2: "UNKNOWN",
  3: "INVALID_ARGUMENT",
  4: "DEADLINE_EXCEEDED",
  5: "NOT_FOUND",
  6: "ALREADY_EXISTS",
  7: "PERMISSION_DENIED",
  8: "RESOURCE_EXHAUSTED",
  9: "FAILED_PRECONDITION",
  10: "ABORTED",
  11: "OUT_OF_RANGE",
  12: "UNIMPLEMENTED",
  13: "INTERNAL",
  14: "UNAVAILABLE",
  15: "DATA_LOSS",
  16: "UNAUTHENTICATED",
};

/**
 * An API failure. Over REST, meta answers errors with a google.rpc.Status,
 * `{"code": 5, "message": "...", "details": []}`, and a matching HTTP status. Requests
 * without a valid token get `{"code": "unauthenticated", "message": "..."}` (HTTP 401).
 * Both forms are understood.
 */
export class ApiError extends Error {
  /** HTTP status, or 0 when the server couldn't be reached. */
  readonly status: number;
  /** gRPC status name, e.g. "NOT_FOUND", when the body carried one. */
  readonly grpcCode: string | undefined;

  constructor(status: number, message: string, grpcCode?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.grpcCode = grpcCode;
  }

  get unauthenticated(): boolean {
    return this.status === 401 || this.grpcCode === "UNAUTHENTICATED";
  }
}

const statusText: Record<number, string> = {
  400: "Bad request",
  401: "Not signed in",
  403: "Not allowed",
  404: "Not found",
  409: "Conflict",
  413: "Too large",
  429: "Too many requests",
  500: "Server error",
  501: "Not implemented yet",
  502: "Bad gateway",
  503: "Service unavailable",
  504: "Gateway timeout",
};

/** The gRPC status name from an error body's `code`: a number or a lower-case name. */
export function grpcCodeName(code: unknown): string | undefined {
  if (typeof code === "number") return grpcNames[code];
  if (typeof code === "string" && code.trim() !== "") return code.trim().toUpperCase();
  return undefined;
}

/** Builds an ApiError from a status and an error body (parsed JSON, text or nothing). */
export function toApiError(status: number, body: unknown): ApiError {
  let parsed = body;
  if (typeof body === "string") {
    try {
      parsed = JSON.parse(body);
    } catch {
      parsed = body;
    }
  }
  if (parsed && typeof parsed === "object") {
    const record = parsed as Record<string, unknown>;
    const grpcCode = grpcCodeName(record.code);
    const message = typeof record.message === "string" && record.message.trim() !== "" ? record.message : undefined;
    return new ApiError(status, message ?? fallbackMessage(status, grpcCode), grpcCode);
  }
  if (typeof parsed === "string" && isReadableText(parsed)) return new ApiError(status, parsed.trim());
  return new ApiError(status, fallbackMessage(status));
}

/** Short plain text is shown as is; HTML pages are not. */
function isReadableText(text: string): boolean {
  const trimmed = text.trim();
  return trimmed !== "" && trimmed.length <= 300 && !trimmed.startsWith("<");
}

function fallbackMessage(status: number, grpcCode?: string): string {
  const text = statusText[status] ?? "Request failed";
  return grpcCode ? `${text} (${grpcCode})` : `${text} (HTTP ${status})`;
}

/** For fetch() rejections: the network failed before any response. */
export function networkError(cause: unknown): ApiError {
  if (cause instanceof ApiError) return cause;
  if (cause instanceof DOMException && cause.name === "AbortError") throw cause;
  const detail = cause instanceof Error && cause.message ? `: ${cause.message}` : "";
  return new ApiError(0, `Can't reach the server${detail}`);
}

/** A message to show for any thrown value. */
export function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message || error.name;
  if (typeof error === "string") return error;
  return "Something went wrong";
}

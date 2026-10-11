// SPDX-License-Identifier: GPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { grpcCodeName, toApiError } from "./errors";

describe("toApiError", () => {
  it("reads meta's google.rpc.Status errors", () => {
    const e = toApiError(404, { code: 5, message: "no entry x", details: [] });
    expect(e.message).toBe("no entry x");
    expect(e.grpcCode).toBe("NOT_FOUND");
    expect(e.status).toBe(404);
  });

  it("reads the string codes of the auth layer", () => {
    const e = toApiError(401, '{"code":"unauthenticated","message":"missing or wrong API key"}');
    expect(e.grpcCode).toBe("UNAUTHENTICATED");
    expect(e.unauthenticated).toBe(true);
  });

  it("falls back to the status for bodies without a message", () => {
    expect(toApiError(503, {}).message).toBe("Service unavailable (HTTP 503)");
    expect(toApiError(409, { code: 6 }).message).toBe("Conflict (ALREADY_EXISTS)");
    expect(toApiError(500, "<html>oops</html>").message).toBe("Server error (HTTP 500)");
    expect(toApiError(403, "this call needs the 'admin' scope").message).toBe("this call needs the 'admin' scope");
  });

  it("names codes", () => {
    expect(grpcCodeName(9)).toBe("FAILED_PRECONDITION");
    expect(grpcCodeName("permission_denied")).toBe("PERMISSION_DENIED");
    expect(grpcCodeName(undefined)).toBeUndefined();
  });
});

import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import csrf from "../../../tests/fixtures/api/auth_csrf.json";
import authenticated from "../../../tests/fixtures/api/auth_me_edit.json";
import unauthenticated from "../../../tests/fixtures/api/auth_error_unauthenticated.json";
import { onUnauthorized, refreshCsrf, request, setCsrfToken } from "../api/client";
import type { CurrentUser } from "../api/types";
import { createAuthHandlers } from "./auth";
import { handlers } from "./handlers";

const server = setupServer(...handlers);
beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => {
  server.resetHandlers();
  setCsrfToken(null);
});
afterAll(() => server.close());

describe("auth mocks", () => {
  it("returns the shared current user without session credentials", async () => {
    const response = await request<CurrentUser>("/auth/me");
    expect(response).toEqual(authenticated);
    expect(Object.keys(response!.data).sort()).toEqual(["id", "is_platform_admin", "username"]);
  });

  it("provides CSRF before login and attaches it to a mutation", async () => {
    server.use(...createAuthHandlers(null));
    expect(await refreshCsrf()).toBe(csrf.data.csrf_token);
    setCsrfToken(null);
    server.use(http.post("*/api/v1/admin/connections", ({ request }) =>
      HttpResponse.json({ data: { token: request.headers.get("X-CSRF-Token") } }),
    ));
    const response = await request<{ token: string }>("/admin/connections", { method: "POST", body: {} });
    expect(response?.data.token).toBe(csrf.data.csrf_token);
  });

  it("returns the anonymous fixture with the matching request ID", async () => {
    server.use(...createAuthHandlers(null));
    const response = await fetch("http://localhost/api/v1/auth/me");
    expect(response.status).toBe(401);
    expect(response.headers.get("X-Request-ID")).toBe(unauthenticated.error.request_id);
    expect(await response.json()).toEqual(unauthenticated);
  });

  it("notifies the client when the mock session is absent", async () => {
    server.use(...createAuthHandlers(null));
    const listener = vi.fn();
    const unsubscribe = onUnauthorized(listener);
    try {
      await expect(request("/auth/me")).rejects.toMatchObject({
        status: 401,
        code: "UNAUTHENTICATED",
        request_id: unauthenticated.error.request_id,
      });
      expect(listener).toHaveBeenCalledOnce();
    } finally {
      unsubscribe();
    }
  });
});

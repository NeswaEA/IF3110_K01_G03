import { http, HttpResponse } from "msw";
import { describe, expect, it, vi } from "vitest";
import csrf from "../../../tests/fixtures/api/auth_csrf.json";
import { server } from "../test-utils";
import { api, ApiError, onUnauthorized, unwrap } from "./client";

const API = "*/api/v1";

describe("api client", () => {
  it("adds the in-memory CSRF token to mutations only", async () => {
    const seen: Record<string, string | null> = {};
    server.use(
      http.get(`${API}/databases`, ({ request }) => {
        seen.get = request.headers.get("X-CSRF-Token");
        return HttpResponse.json({ data: [], page: { limit: 50, offset: 0, has_more: false } });
      }),
      http.post(`${API}/admin/connections/:id/test`, ({ request }) => {
        seen.post = request.headers.get("X-CSRF-Token");
        return HttpResponse.json({ data: { reachable: true, latency_ms: 1, capabilities: {} } });
      }),
    );
    await unwrap(api.GET("/databases"));
    await unwrap(api.POST("/admin/connections/{id}/test", { params: { path: { id: "x" } } }));
    expect(seen).toEqual({ get: null, post: csrf.data.csrf_token });
  });

  it("returns undefined for 204", async () => {
    server.use(http.delete(`${API}/admin/connections/:id`, () => new HttpResponse(null, { status: 204 })));
    await expect(unwrap(api.DELETE("/admin/connections/{id}", { params: { path: { id: "x" } } }))).resolves.toBeUndefined();
  });

  it("turns the error envelope into ApiError", async () => {
    server.use(
      http.get(`${API}/databases`, () =>
        HttpResponse.json(
          { error: { code: "INVALID_REQUEST", message: "Parameter tidak valid.", fields: { limit: "terlalu besar" }, request_id: "req-1" } },
          { status: 400 },
        ),
      ),
    );
    const err = await unwrap(api.GET("/databases")).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ code: "INVALID_REQUEST", status: 400, fields: { limit: "terlalu besar" }, requestId: "req-1" });
  });

  it("reports NETWORK_ERROR when the server cannot be reached, but lets aborts through", async () => {
    server.use(http.get(`${API}/databases`, () => HttpResponse.error()));
    await expect(unwrap(api.GET("/databases"))).rejects.toMatchObject({ code: "NETWORK_ERROR", status: 0 });

    server.use(http.get(`${API}/databases`, () => new Promise(() => {})));
    const ctl = new AbortController();
    const pending = unwrap(api.GET("/databases", { signal: ctl.signal }));
    ctl.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });

  it("signals 401 from feature endpoints, not from the session probes", async () => {
    const listener = vi.fn();
    const off = onUnauthorized(listener);
    const unauthenticated = () =>
      HttpResponse.json({ error: { code: "UNAUTHENTICATED", message: "Silakan login.", fields: {}, request_id: "r" } }, { status: 401 });
    server.use(http.get(`${API}/auth/me`, unauthenticated), http.get(`${API}/databases`, unauthenticated));
    await unwrap(api.GET("/auth/me")).catch(() => {});
    expect(listener).not.toHaveBeenCalled();
    await unwrap(api.GET("/databases")).catch(() => {});
    expect(listener).toHaveBeenCalledOnce();
    off();
  });

  it("fetches a new CSRF token after a rejected one", async () => {
    let calls = 0;
    server.use(
      http.get(`${API}/auth/csrf`, () => HttpResponse.json({ data: { csrf_token: `t${++calls}` } })),
      http.post(`${API}/admin/connections/:id/test`, ({ request }) =>
        request.headers.get("X-CSRF-Token") === "t1"
          ? HttpResponse.json({ error: { code: "CSRF_REJECTED", message: "x", fields: {}, request_id: "r" } }, { status: 403 })
          : HttpResponse.json({ data: { reachable: true, latency_ms: 1, capabilities: {} } }),
      ),
    );
    const test = () => unwrap(api.POST("/admin/connections/{id}/test", { params: { path: { id: "x" } } }));
    await expect(test()).rejects.toMatchObject({ code: "CSRF_REJECTED" });
    await expect(test()).resolves.toBeDefined();
    expect(calls).toBe(2);
  });
});

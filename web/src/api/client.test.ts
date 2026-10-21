import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiRequestError, onUnauthorized, request, setCsrfToken } from "./client";

const B = "http://localhost/api/v1";
const server = setupServer();

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());
beforeEach(() => setCsrfToken(null));

const unauthenticated = () =>
  HttpResponse.json(
    { error: { code: "UNAUTHENTICATED", message: "x", fields: {}, request_id: "r" } },
    { status: 401 },
  );

describe("request", () => {
  it("returns the untouched envelope for GET JSON", async () => {
    server.use(
      http.get(`${B}/databases`, () =>
        HttpResponse.json({ data: [{ id: "a" }], page: { limit: 50, offset: 0, has_more: false } }),
      ),
    );
    const res = await request<{ id: string }[]>("/databases");
    expect(res).toEqual({ data: [{ id: "a" }], page: { limit: 50, offset: 0, has_more: false } });
  });

  it("returns undefined for 204", async () => {
    setCsrfToken("t");
    server.use(http.delete(`${B}/x`, () => new HttpResponse(null, { status: 204 })));
    expect(await request("/x", { method: "DELETE" })).toBeUndefined();
  });

  it("throws the ApiError shape, including run_id", async () => {
    server.use(
      http.get(`${B}/boom`, () =>
        HttpResponse.json(
          {
            error: {
              code: "SQL_EXECUTION_FAILED",
              message: "gagal",
              fields: { sql: "x" },
              request_id: "req-1",
              run_id: "r1",
            },
          },
          { status: 422 },
        ),
      ),
    );
    const err = await request("/boom").catch((e) => e);
    expect(err).toBeInstanceOf(ApiRequestError);
    expect(err).toMatchObject({
      code: "SQL_EXECUTION_FAILED",
      fields: { sql: "x" },
      request_id: "req-1",
      run_id: "r1",
      status: 422,
    });
  });

  it("maps a non-JSON error body to INTERNAL_ERROR with the request id header", async () => {
    server.use(
      http.get(
        `${B}/html`,
        () => new HttpResponse("<html>", { status: 502, headers: { "X-Request-ID": "req-2" } }),
      ),
    );
    await expect(request("/html")).rejects.toMatchObject({
      code: "INTERNAL_ERROR",
      request_id: "req-2",
      status: 502,
    });
  });

  it("uses NETWORK_ERROR only for transport failures", async () => {
    server.use(http.get(`${B}/down`, () => HttpResponse.error()));
    await expect(request("/down")).rejects.toMatchObject({ code: "NETWORK_ERROR", status: 0 });
  });

  it("rethrows an aborted request as AbortError", async () => {
    server.use(
      http.get(`${B}/slow`, async () => {
        await new Promise((r) => setTimeout(r, 200));
        return HttpResponse.json({ data: 1 });
      }),
    );
    const ctl = new AbortController();
    const p = request("/slow", { signal: ctl.signal });
    ctl.abort();
    await expect(p).rejects.toMatchObject({ name: "AbortError" });
  });

  it("fetches a CSRF token lazily and sends it, plus caller headers, on mutations", async () => {
    let seen: Headers | undefined;
    server.use(
      http.get(`${B}/auth/csrf`, () => HttpResponse.json({ data: { csrf_token: "tok" } })),
      http.post(`${B}/databases/1/migration-drafts/2/execute`, ({ request: r }) => {
        seen = r.headers;
        return HttpResponse.json({ data: { ok: true } });
      }),
    );
    await request("/databases/1/migration-drafts/2/execute", {
      method: "POST",
      body: { revision: 1 },
      headers: { "Idempotency-Key": "k-1" },
    });
    expect(seen?.get("x-csrf-token")).toBe("tok");
    expect(seen?.get("idempotency-key")).toBe("k-1");
    expect(seen?.get("content-type")).toBe("application/json");
  });

  it("does not send a CSRF token on GET", async () => {
    let token: string | null = "unset";
    server.use(
      http.get(`${B}/g`, ({ request: r }) => {
        token = r.headers.get("x-csrf-token");
        return HttpResponse.json({ data: 1 });
      }),
    );
    setCsrfToken("tok");
    await request("/g");
    expect(token).toBeNull();
  });

  it("drops the CSRF token and notifies listeners on 401", async () => {
    const listener = vi.fn();
    const off = onUnauthorized(listener);
    setCsrfToken("old");
    server.use(http.get(`${B}/auth/me`, unauthenticated));
    await expect(request("/auth/me")).rejects.toMatchObject({ code: "UNAUTHENTICATED" });
    expect(listener).toHaveBeenCalledTimes(1);
    off();

    // the token was dropped: the next mutation must fetch a new one
    let fetched = 0;
    server.use(
      http.get(`${B}/auth/csrf`, () => {
        fetched++;
        return HttpResponse.json({ data: { csrf_token: "new" } });
      }),
      http.post(`${B}/p`, () => HttpResponse.json({ data: 1 })),
    );
    await request("/p", { method: "POST", body: {} });
    expect(fetched).toBe(1);
  });

  it("clears the CSRF token after CSRF_REJECTED so the next call refetches", async () => {
    setCsrfToken("stale");
    server.use(
      http.post(`${B}/p`, () =>
        HttpResponse.json(
          { error: { code: "CSRF_REJECTED", message: "x", fields: {}, request_id: "r" } },
          { status: 403 },
        ),
      ),
    );
    await expect(request("/p", { method: "POST", body: {} })).rejects.toMatchObject({
      code: "CSRF_REJECTED",
    });

    let fetched = 0;
    server.use(
      http.get(`${B}/auth/csrf`, () => {
        fetched++;
        return HttpResponse.json({ data: { csrf_token: "fresh" } });
      }),
      http.post(`${B}/p`, () => HttpResponse.json({ data: 1 })),
    );
    await request("/p", { method: "POST", body: {} });
    expect(fetched).toBe(1);
  });
});

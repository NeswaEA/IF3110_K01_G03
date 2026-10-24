import { act, renderHook, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, expect, it } from "vitest";
import { useApi } from "./useApi";

const server = setupServer();
beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());

it("uses the initial response in StrictMode and fetches again on explicit reload", async () => {
  let reads = 0;
  server.use(http.get("http://localhost/api/v1/query", () => {
    reads++;
    return HttpResponse.json({ data: "updated" });
  }));
  const { result } = renderHook(() => useApi<string>("/query", { status: "success", response: { data: "server" } }), { wrapper: StrictMode });
  expect(result.current[0]).toEqual({ status: "success", response: { data: "server" } });
  expect(reads).toBe(0);
  act(() => result.current[1]());
  await waitFor(() => expect(result.current[0]).toEqual({ status: "success", response: { data: "updated" } }));
  expect(reads).toBe(1);
});

it("fetches changed paths and does not restore a stale seed when returning to the original path", async () => {
  const reads: string[] = [];
  server.use(http.get("http://localhost/api/v1/query/:id", ({ params }) => {
    reads.push(String(params.id));
    return HttpResponse.json({ data: `loaded ${params.id}` });
  }));
  const { result, rerender } = renderHook(({ path }) => useApi<string>(path, { status: "success", response: { data: "server" } }), { initialProps: { path: "/query/a" } });
  expect(reads).toHaveLength(0);
  rerender({ path: "/query/b" });
  await waitFor(() => expect(result.current[0]).toEqual({ status: "success", response: { data: "loaded b" } }));
  rerender({ path: "/query/a" });
  await waitFor(() => expect(result.current[0]).toEqual({ status: "success", response: { data: "loaded a" } }));
  expect(reads).toEqual(["b", "a"]);
});

it("retains an initial error until explicitly reloaded", async () => {
  let reads = 0;
  server.use(http.get("http://localhost/api/v1/query", () => {
    reads++;
    return HttpResponse.json({ data: "recovered" });
  }));
  const error = new Error("target unavailable");
  const { result } = renderHook(() => useApi<string>("/query", { status: "error", error }));
  expect(result.current[0]).toEqual({ status: "error", error });
  expect(reads).toBe(0);
  act(() => result.current[1]());
  await waitFor(() => expect(result.current[0]).toEqual({ status: "success", response: { data: "recovered" } }));
  expect(reads).toBe(1);
});

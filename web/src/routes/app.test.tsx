import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import forbidden from "../../../tests/fixtures/api/viewer_error_forbidden.json";
import { safeNext } from "@/lib/safe-next";
import { createAuthHandlers } from "@/mocks/auth";
import { FakeEventSource, renderApp, server } from "@/test-utils";

const PG = "30000000-0000-4000-8000-000000000001";
const API = "*/api/v1";

describe("guard and routing", () => {
  it("sends an anonymous visitor to login with a local next", async () => {
    server.use(...createAuthHandlers(null));
    const { router } = renderApp(`/databases/${PG}?tab=data`);
    await waitFor(() => expect(router.state.location.pathname).toBe("/login"));
    expect(router.state.location.search).toEqual({ next: `/databases/${PG}?tab=data` });
    expect(await screen.findByRole("heading", { name: "Masuk" })).toBeInTheDocument();
  });

  it("drops a next that leaves the site", () => {
    for (const bad of ["https://evil.example", "//evil.example", "/\\evil.example", "javascript:alert(1)", 42]) {
      expect(safeNext(bad)).toBeUndefined();
    }
    expect(safeNext("/databases/x?tab=data#t")).toBe("/databases/x?tab=data#t");
  });

  it("logs in and returns to next", async () => {
    server.use(
      ...createAuthHandlers(null),
      http.post(`${API}/auth/login`, () => HttpResponse.json({ data: { user: { id: "10000000-0000-4000-8000-000000000003", username: "editor", is_platform_admin: false } } })),
    );
    const { router } = renderApp(`/login?next=${encodeURIComponent("/databases")}`);
    await userEvent.type(await screen.findByLabelText("Username"), "editor");
    await userEvent.type(screen.getByLabelText("Password"), "secret-password");
    server.use(...createAuthHandlers());
    await userEvent.click(screen.getByRole("button", { name: "Masuk" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/databases"));
  });

  it("redirects / to the database list and renders the 404 page for unknown routes", async () => {
    const { router } = renderApp("/");
    await waitFor(() => expect(router.state.location.pathname).toBe("/databases"));
    expect(await screen.findByRole("link", { name: /PostgreSQL latihan/ })).toBeInTheDocument();

    await router.navigate({ to: "/tidak-ada" as "/" });
    expect(await screen.findByText("Halaman tidak ditemukan")).toBeInTheDocument();
  });

  it("filters the database list by name and access", async () => {
    const { router } = renderApp("/databases");
    const list = await screen.findByRole("list", { name: "Daftar database" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    await userEvent.type(screen.getByLabelText("Cari nama"), "sqlite");
    await waitFor(() => expect(within(list).getAllByRole("listitem")).toHaveLength(1));
    expect(router.state.location.search).toEqual({ q: "sqlite" });
  });

  it("keeps non-admins out of the admin pages", async () => {
    const { router } = renderApp("/admin/connections");
    await waitFor(() => expect(router.state.location.pathname).toBe("/databases"));
    expect(screen.queryByRole("link", { name: "Koneksi database" })).not.toBeInTheDocument();
  });
});

describe("viewer", () => {
  it("shows the schema and keeps the open table in the URL", async () => {
    const { router } = renderApp(`/databases/${PG}`);
    expect(await screen.findByRole("heading", { name: /Schema public/ })).toBeInTheDocument();
    await userEvent.click(screen.getByText("items"));
    await userEvent.click(screen.getAllByRole("button", { name: "Lihat data" })[0]);
    await waitFor(() => expect(router.state.location.search).toMatchObject({ tab: "data", schema: "public", table: "items" }));
    const table = await screen.findByRole("table");
    expect(within(table).getAllByText("NULL").length).toBeGreaterThan(0);
    expect(within(table).getByTitle("Data biner (base64)")).toHaveTextContent("3q2+7w==");
    expect(within(table).getByText("9007199254740993")).toBeInTheDocument(); // bigint kept exact
  });

  it("warns about unstable order without a primary key", async () => {
    renderApp(`/databases/${PG}?tab=data&schema=public&table=audit_log`);
    expect(await screen.findByText(/tidak memiliki primary key/)).toBeInTheDocument();
  });

  it("explains a 403 instead of showing an empty table", async () => {
    server.use(http.get(`${API}/databases/:id/data`, () => HttpResponse.json(forbidden, { status: 403 })));
    renderApp(`/databases/${PG}?tab=data&schema=public&table=items`);
    expect(await screen.findByText("Akses ditolak")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("marks the database's queries stale when the server signals a change", async () => {
    const { queryClient } = renderApp(`/databases/${PG}`);
    await screen.findByRole("heading", { name: /Schema public/ });
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    const source = FakeEventSource.instances[0];
    expect(source.url).toBe(`http://localhost/api/v1/databases/${PG}/events`);
    queryClient.setQueryData(["executions", PG], []);
    queryClient.setQueryData(["executions", "another-db"], []);
    source.emit("execution.updated");
    await waitFor(() => expect(queryClient.getQueryState(["executions", PG])?.isInvalidated).toBe(true));
    expect(queryClient.getQueryState(["executions", "another-db"])?.isInvalidated).toBe(false);
  });
});

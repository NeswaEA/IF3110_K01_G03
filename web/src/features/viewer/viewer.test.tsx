import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { handlers } from "../../mocks/handlers";
import forbidden from "../../../../tests/fixtures/api/viewer_error_forbidden.json";
import emptyList from "../../../../tests/fixtures/api/databases_list_empty.json";
import { DatabaseList } from "./DatabaseList";
import { DatabaseViewer } from "./DatabaseViewer";

const PG = "30000000-0000-4000-8000-000000000001";
const server = setupServer(...handlers);
beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => server.resetHandlers(...handlers));
afterAll(() => server.close());

function at(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/databases" element={<DatabaseList />} />
        <Route path="/databases/:id" element={<DatabaseViewer />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("DatabaseList", () => {
  it("lists accessible databases with their access level", async () => {
    at("/databases");
    expect(await screen.findByRole("link", { name: "PostgreSQL latihan" })).toHaveAttribute("href", `/databases/${PG}`);
    expect(screen.getByText(/akses lihat dan edit/)).toBeInTheDocument();
  });

  it("distinguishes empty from forbidden", async () => {
    server.use(http.get("*/api/v1/databases", () => HttpResponse.json(emptyList)));
    const { unmount } = at("/databases");
    expect(await screen.findByText(/Belum ada database/)).toBeInTheDocument();
    unmount();

    server.use(http.get("*/api/v1/databases", () => HttpResponse.json(forbidden, { status: 403 })));
    at("/databases");
    expect(await screen.findByRole("alert")).toHaveTextContent("Akses ditolak");
    expect(screen.queryByText(/Belum ada database/)).not.toBeInTheDocument();
  });
});

describe("DatabaseViewer", () => {
  it("shows schema objects and table data without losing precision", async () => {
    at(`/databases/${PG}`);
    await userEvent.click(await screen.findByRole("button", { name: "items" }));
    expect(screen.getByText("public.items")).toBeInTheDocument();
    expect(screen.getByText("PRIMARY KEY (id)")).toBeInTheDocument();
    const grid = (await screen.findAllByRole("table"))[1];
    expect(within(grid).getByText("9007199254740993")).toBeInTheDocument();
    expect(within(grid).getAllByText("NULL").length).toBeGreaterThan(0);
  });

  it("keeps composite column order", async () => {
    at(`/databases/${PG}`);
    await userEvent.click(await screen.findByRole("button", { name: "shipments" }));
    expect(screen.getByText(/shipments_carrier_order_idx \(carrier, order_id\)/)).toBeInTheDocument();
  });

  it("explains unstable ordering for tables without a primary key", async () => {
    server.use(
      http.get("*/api/v1/databases/:id/schema", () =>
        HttpResponse.json({
          data: { database_id: PG, driver: "postgres", fingerprint: "sha256:x",
            schemas: [{ name: "public", tables: [{ name: "audit_log", columns: [], constraints: [], indexes: [] }] }] },
        }),
      ),
    );
    at(`/databases/${PG}`);
    await userEvent.click(await screen.findByRole("button", { name: "audit_log" }));
    expect(await screen.findByText(/urutan baris antarhalaman tidak dijamin/)).toBeInTheDocument();
  });

  it("shows a not-found error for unknown databases", async () => {
    at("/databases/00000000-0000-4000-8000-000000000000");
    expect(await screen.findByRole("alert")).toHaveTextContent("Tidak ditemukan");
  });
});

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { setCsrfToken } from "../../api/client";
import { handlers } from "../../mocks/handlers";
import { ExplainPanel } from "./ExplainPanel";

const PG = "30000000-0000-4000-8000-000000000001";
const LITE = "30000000-0000-4000-8000-000000000002";
const server = setupServer(...handlers);
let calls = 0;
server.events.on("request:start", ({ request }) => {
  if (request.url.endsWith("/explain")) calls++;
});

beforeAll(() => {
  server.listen({ onUnhandledFrame: "error" });
  setCsrfToken("t");
});
afterEach(() => {
  server.resetHandlers(...handlers);
  calls = 0;
});
afterAll(() => server.close());

const button = () => screen.getByRole("button", { name: /EXPLAIN/ });

describe("ExplainPanel", () => {
  it("requests a plan only on click and renders the tree and raw output as text", async () => {
    render(<ExplainPanel databaseId={PG} sql="SELECT * FROM items WHERE id = 1" canExplain />);
    expect(calls).toBe(0);
    await userEvent.click(button());
    expect(await screen.findByText("Index Scan")).toBeInTheDocument();
    expect(screen.getByText(/memakai indeks items_pkey/)).toBeInTheDocument();
    expect(screen.getByText(/"Node Type": "Index Scan"/)).toBeInTheDocument();
    expect(calls).toBe(1);
  });

  it("shows unavailable SQLite metrics as text, not zero", async () => {
    render(<ExplainPanel databaseId={LITE} sql="SELECT * FROM items WHERE id = 2" canExplain />);
    await userEvent.click(button());
    expect(await screen.findByText(/Estimasi baris: tidak tersedia/)).toBeInTheDocument();
    expect(screen.getByLabelText("Peringatan")).toHaveTextContent("SQLite tidak menyediakan");
  });

  it("is disabled without edit access or SQL", () => {
    const { rerender } = render(<ExplainPanel databaseId={PG} sql="SELECT 1" canExplain={false} />);
    expect(button()).toBeDisabled();
    rerender(<ExplainPanel databaseId={PG} sql="   " canExplain />);
    expect(button()).toBeDisabled();
  });

  it("shows PLAN_UNSUPPORTED for DDL without hiding the message", async () => {
    render(<ExplainPanel databaseId={PG} sql="CREATE TABLE t (a int)" canExplain />);
    await userEvent.click(button());
    expect(await screen.findByRole("alert")).toHaveTextContent("tidak mendukung rencana eksekusi");
  });

  it("clears the result when the SQL changes", async () => {
    const { rerender } = render(<ExplainPanel databaseId={PG} sql="SELECT 1" canExplain />);
    await userEvent.click(button());
    await screen.findByText("Index Scan");
    rerender(<ExplainPanel databaseId={PG} sql="SELECT 2" canExplain />);
    expect(screen.queryByText("Index Scan")).not.toBeInTheDocument();
  });

  it("ignores a late response for SQL that has since changed", async () => {
    let release!: () => void;
    server.use(
      http.post("*/api/v1/databases/:id/explain", async () => {
        await new Promise<void>((r) => (release = r));
        return HttpResponse.json({
          data: { driver: "postgres", mode: "estimate", nodes: [{ id: "0", parent_id: null, operation: "Stale Scan",
            relation: null, index: null, estimated_rows: 1, estimated_cost: 1, actual_time_ms: null, actual_rows: null }],
            raw: [], warnings: [] },
        });
      }),
    );
    const { rerender } = render(<ExplainPanel databaseId={PG} sql="SELECT 1" canExplain />);
    await userEvent.click(button());
    await waitFor(() => expect(calls).toBe(1));
    rerender(<ExplainPanel databaseId={PG} sql="SELECT 2" canExplain />);
    release();
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText("Stale Scan")).not.toBeInTheDocument();
  });
});

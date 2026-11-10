import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { setCsrfToken } from "../../api/client";
import type { DatabaseSummary, DiffResult } from "../../api/types";
import { handlers } from "../../mocks/handlers";
import { SchemaDiffPage, type DiffInitialState } from "./SchemaDiffPage";
import databases from "../../../../tests/fixtures/api/databases_list.json";
import changed from "../../../../tests/fixtures/api/schema_diff_changed.json";
import empty from "../../../../tests/fixtures/api/schema_diff_empty.json";
import unsupported from "../../../../tests/fixtures/api/schema_diff_unsupported.json";

const db = databases.data[0] as DatabaseSummary;
const desired = { ...db, id: "30000000-0000-4000-8000-000000000003", name: "Pembanding view", effective_access: "view", capabilities: { ...db.capabilities, transactional_migrations: false } } as DatabaseSummary;
const initial: DiffInitialState = { database: db, comparisons: [desired], schemas: ["public", "audit"], desired_database_id: desired.id, schema: "public", problem: "" };
const base = `http://localhost/api/v1/databases/${db.id}`;
const server = setupServer(...handlers);

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
beforeEach(() => { server.resetHandlers(...handlers); setCsrfToken("test"); });
afterEach(() => vi.restoreAllMocks());
afterAll(() => server.close());

function open(overrides: Partial<DiffInitialState> = {}) {
  render(<StrictMode><SchemaDiffPage initial={{ ...initial, ...overrides }} /></StrictMode>);
}

async function generate() {
  await userEvent.click(screen.getByRole("button", { name: "Bandingkan schema" }));
  return screen.findByRole("region", { name: "Hasil perbandingan" });
}

it("uses server choices without fetching or generating on mount", async () => {
  let calls = 0;
  server.use(http.all("*", () => { calls++; return HttpResponse.error(); }));
  open();
  expect(screen.getByRole("combobox", { name: "Database pembanding" })).toHaveValue(desired.id);
  expect(screen.getByRole("combobox", { name: "Schema" })).toHaveValue("public");
  await act(async () => {});
  expect(calls).toBe(0);
});

it("generates once and shows the candidate read-only without other requests", async () => {
  let other = 0;
  server.use(http.post(`${base}/schema-diff`, async ({ request }) => {
    expect(await request.json()).toEqual({ desired_database_id: desired.id, schema: "public" });
    return HttpResponse.json(changed);
  }), http.all("*", () => { other++; return HttpResponse.error(); }));
  open();
  await generate();
  const sql = screen.getByRole("textbox", { name: "SQL migrasi" });
  expect(sql).toHaveValue(changed.data.candidate_sql);
  expect(sql).toHaveAttribute("readonly");
  expect(other).toBe(0);
});

it.each([empty.data, unsupported.data])("does not offer a candidate for empty or unsupported output", async (result) => {
  server.use(http.post(`${base}/schema-diff`, () => HttpResponse.json({ data: result })));
  open();
  await generate();
  expect(screen.queryByRole("textbox", { name: "SQL migrasi" })).not.toBeInTheDocument();
  expect(screen.getByText(result.executable ? "Schema identik. Tidak ada SQL kandidat." : /Kandidat tidak dapat dibuat/)).toBeInTheDocument();
});

it("shows destructive changes and escapes engine text", async () => {
  const attack = '<img src=x onerror="bad()">';
  const result: DiffResult = { ...(changed.data as DiffResult), changes: [{ kind: "drop_table", object: attack, destructive: true }], warnings: [attack] };
  server.use(http.post(`${base}/schema-diff`, () => HttpResponse.json({ data: result })));
  open();
  await generate();
  expect(screen.getByText("Ya, tinjau dampak pada data")).toBeInTheDocument();
  expect(screen.getAllByText(attack)).toHaveLength(2);
  expect(document.querySelector("img")).toBeNull();
});

it("flags a review whose choices changed", async () => {
  open();
  await generate();
  await userEvent.selectOptions(screen.getByRole("combobox", { name: "Schema" }), "audit");
  expect(screen.getByText(/Pilihan berubah/)).toBeInTheDocument();
});

it("offers a new-tab login after a 401", async () => {
  server.use(http.post(`${base}/schema-diff`, () => HttpResponse.json({ error: { code: "UNAUTHENTICATED", message: "Sesi berakhir.", fields: {}, request_id: "req" } }, { status: 401 })));
  open();
  await userEvent.click(screen.getByRole("button", { name: "Bandingkan schema" }));
  expect(await screen.findByRole("link", { name: "Masuk lagi di tab baru" })).toHaveAttribute("target", "_blank");
});

it("blocks duplicate generate requests while pending", async () => {
  let generated = 0;
  let finish!: () => void;
  const pending = new Promise<void>((resolve) => { finish = resolve; });
  server.use(http.post(`${base}/schema-diff`, async () => { generated++; await pending; return HttpResponse.json(changed); }));
  open();
  const button = screen.getByRole("button", { name: "Bandingkan schema" });
  fireEvent.click(button); fireEvent.click(button);
  await waitFor(() => expect(generated).toBe(1));
  expect(screen.getByRole("combobox", { name: "Schema" })).toBeDisabled();
  finish();
  await screen.findByRole("textbox", { name: "SQL migrasi" });
  expect(generated).toBe(1);
});

it.each(["comparisons", "schemas", "problem", "capability", "readiness", "view"])("disables unavailable %s", (mode) => {
  const state = structuredClone(initial);
  if (mode === "comparisons") { state.comparisons = []; state.desired_database_id = ""; }
  if (mode === "schemas") { state.schemas = []; state.schema = ""; }
  if (mode === "problem") state.problem = "Target tidak dapat dihubungi.";
  if (mode === "capability") state.database.capabilities.schema_diff = false;
  if (mode === "readiness") state.database.capabilities.transactional_migrations = false;
  if (mode === "view") state.database.effective_access = "view";
  open(state);
  expect(screen.getByRole("button", { name: "Bandingkan schema" })).toBeDisabled();
});

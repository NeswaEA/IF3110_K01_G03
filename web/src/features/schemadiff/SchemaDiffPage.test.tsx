import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { setCsrfToken } from "../../api/client";
import type { DatabaseSummary, DiffResult } from "../../api/types";
import { handlers } from "../../mocks/handlers";
import { createMigrationHandlers } from "../../mocks/migrations";
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
beforeEach(() => { server.resetHandlers(...createMigrationHandlers(), ...handlers); setCsrfToken("test"); });
afterEach(() => vi.restoreAllMocks());
afterAll(() => server.close());

function open(overrides: Partial<DiffInitialState> = {}, confirmDiscard = vi.fn(() => true)) {
  const navigate = vi.fn();
  render(<StrictMode><SchemaDiffPage initial={{ ...initial, ...overrides }} navigate={navigate} confirmDiscard={confirmDiscard} /></StrictMode>);
  return { navigate, confirmDiscard };
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

it("generates without creating or executing and saves edited SQL with the original head and fingerprint", async () => {
  let mutations = 0;
  server.use(http.post(`${base}/schema-diff`, async ({ request }) => {
    expect(await request.json()).toEqual({ desired_database_id: desired.id, schema: "public" });
    return HttpResponse.json(changed);
  }), http.post(`${base}/migration-drafts`, async ({ request }) => {
    mutations++;
    expect(await request.json()).toEqual({ title: "SQL yang ditinjau", sql: "ALTER TABLE public.items ADD COLUMN edited text;", expected_version: changed.data.expected_version, source_fingerprint: changed.data.source_fingerprint });
    return HttpResponse.json({ data: { id: "40000000-0000-4000-8000-000000000009" } }, { status: 201 });
  }), http.post(`${base}/migration-drafts/:id/execute`, () => { throw new Error("diff must not execute"); }));
  const { navigate } = open();
  await generate();
  expect(mutations).toBe(0);
  fireEvent.change(screen.getByRole("textbox", { name: "SQL migrasi" }), { target: { value: "ALTER TABLE public.items ADD COLUMN edited text;" } });
  fireEvent.change(screen.getByRole("textbox", { name: "Judul draft" }), { target: { value: "SQL yang ditinjau" } });
  await userEvent.click(screen.getByRole("button", { name: "Simpan sebagai draft" }));
  await waitFor(() => expect(navigate).toHaveBeenCalledWith(`/databases/${db.id}/migrations?draft=40000000-0000-4000-8000-000000000009#migration-draft`));
  expect(mutations).toBe(1);
  expect(screen.getByRole("button", { name: "Simpan sebagai draft" })).toBeDisabled();
});

it.each([empty.data, unsupported.data])("does not offer a draft for empty or unsupported output", async (result) => {
  server.use(http.post(`${base}/schema-diff`, () => HttpResponse.json({ data: result })));
  open();
  await generate();
  expect(screen.queryByRole("button", { name: "Simpan sebagai draft" })).not.toBeInTheDocument();
  expect(screen.queryByRole("textbox", { name: "SQL migrasi" })).not.toBeInTheDocument();
  expect(screen.getByText(result.executable ? "Schema identik. Tidak ada SQL untuk disimpan." : /Kandidat tidak dapat dibuat/)).toBeInTheDocument();
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

it("invalidates the review when choices change and asks before replacing edited SQL", async () => {
  const confirm = vi.fn(() => false);
  open({}, confirm);
  await generate();
  const sql = screen.getByRole("textbox", { name: "SQL migrasi" });
  fireEvent.change(sql, { target: { value: "SELECT 'keep my edit';" } });
  await userEvent.selectOptions(screen.getByRole("combobox", { name: "Schema" }), "audit");
  expect(screen.getByRole("button", { name: "Simpan sebagai draft" })).toBeDisabled();
  expect(screen.getByText(/Pilihan berubah/)).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Bandingkan schema" }));
  expect(confirm).toHaveBeenCalledTimes(1);
  expect(sql).toHaveValue("SELECT 'keep my edit';");
});

it.each([401, 403, 409, 503])("preserves input when saving returns %i and never resends automatically", async (status) => {
  let attempts = 0;
  const code = status === 401 ? "UNAUTHENTICATED" : status === 409 ? "SCHEMA_CONFLICT" : status === 403 ? "FORBIDDEN" : "TARGET_UNAVAILABLE";
  server.use(http.post(`${base}/migration-drafts`, () => {
    attempts++;
    return HttpResponse.json({ error: { code, message: "Kirim kembali secara manual.", fields: {}, request_id: "req-failure" } }, { status });
  }));
  const { navigate } = open();
  await generate();
  fireEvent.change(screen.getByRole("textbox", { name: "SQL migrasi" }), { target: { value: "SELECT 'SQL terbaru';" } });
  await userEvent.click(screen.getByRole("button", { name: "Simpan sebagai draft" }));
  await screen.findByText("Kirim kembali secara manual.");
  expect(screen.getByRole("textbox", { name: "SQL migrasi" })).toHaveValue("SELECT 'SQL terbaru';");
  expect(navigate).not.toHaveBeenCalled();
  if (status === 401) expect(screen.getByRole("link", { name: "Masuk lagi di tab baru" })).toHaveAttribute("target", "_blank");
  await act(async () => {});
  expect(attempts).toBe(1);
});

it("blocks duplicate generate and save requests while pending", async () => {
  let generated = 0, saved = 0;
  let finishGenerate!: () => void, finishSave!: () => void;
  const generating = new Promise<void>((resolve) => { finishGenerate = resolve; });
  const saving = new Promise<void>((resolve) => { finishSave = resolve; });
  server.use(http.post(`${base}/schema-diff`, async () => { generated++; await generating; return HttpResponse.json(changed); }),
    http.post(`${base}/migration-drafts`, async () => { saved++; await saving; return HttpResponse.json({ data: { id: "draft" } }, { status: 201 }); }));
  open();
  const generateButton = screen.getByRole("button", { name: "Bandingkan schema" });
  fireEvent.click(generateButton); fireEvent.click(generateButton);
  await waitFor(() => expect(generated).toBe(1));
  expect(screen.getByRole("combobox", { name: "Schema" })).toBeDisabled();
  finishGenerate();
  await screen.findByRole("textbox", { name: "SQL migrasi" });
  const save = screen.getByRole("button", { name: "Simpan sebagai draft" });
  fireEvent.click(save); fireEvent.click(save);
  await waitFor(() => expect(saved).toBe(1));
  finishSave();
  await screen.findByRole("link", { name: "Buka draft" });
  expect(generated).toBe(1); expect(saved).toBe(1);
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

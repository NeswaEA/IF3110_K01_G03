import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { EditorView } from "@codemirror/view";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { onUnauthorized, setCsrfToken } from "../../api/client";
import { handlers } from "../../mocks/handlers";
import { createMigrationHandlers } from "../../mocks/migrations";
import { MigrationsPage } from "./MigrationsPage";
import draftFixture from "../../../../tests/fixtures/api/migrations_draft.json";
import success from "../../../../tests/fixtures/api/migrations_run_succeeded.json";
import unknown from "../../../../tests/fixtures/api/migrations_run_unknown.json";
import conflict from "../../../../tests/fixtures/api/migrations_error_conflict.json";
import databases from "../../../../tests/fixtures/api/databases_list.json";

const db = databases.data[0];
const base = `http://localhost/api/v1/databases/${db.id}`;
const server = setupServer(...handlers);

beforeAll(() => {
  server.listen({ onUnhandledFrame: "error" });
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  Object.defineProperty(Range.prototype, "getClientRects", { configurable: true, value: () => [] });
  Object.defineProperty(Range.prototype, "getBoundingClientRect", { configurable: true, value: () => new DOMRect() });
});
beforeEach(() => { server.resetHandlers(...createMigrationHandlers(), ...handlers); setCsrfToken("test"); });
afterEach(() => { vi.restoreAllMocks(); });
afterAll(() => { server.close(); vi.unstubAllGlobals(); });

function open() {
  return render(<MemoryRouter initialEntries={[`/databases/${db.id}/migrations`]}><Routes><Route path="/databases/:id/migrations" element={<MigrationsPage />} /></Routes></MemoryRouter>);
}

async function editSQL(value: string, selection?: { anchor: number; head: number }) {
  const textbox = await screen.findByRole("textbox", { name: "SQL migrasi" });
  const editor = EditorView.findFromDOM(textbox)!;
  act(() => { editor.dispatch({ changes: { from: 0, to: editor.state.doc.length, insert: value }, selection }); });
  return editor;
}

async function savedDraft() {
  await userEvent.type(await screen.findByRole("textbox", { name: "Judul" }), "Perubahan item");
  await editSQL("ALTER TABLE items ADD COLUMN local_value TEXT;");
  await userEvent.click(screen.getByRole("button", { name: "Simpan draft" }));
  await screen.findByText("Draft tersimpan.");
}

describe("migration editor", () => {
  it("saves the current editor text and executes once on a double click", async () => {
    let saved: Record<string, unknown> | undefined;
    let calls = 0;
    let key = "";
    server.use(http.post(`${base}/migration-drafts`, async ({ request }) => {
      saved = await request.json() as Record<string, unknown>;
      return HttpResponse.json({ data: { ...draftFixture.data, ...saved } }, { status: 201 });
    }), http.post(`${base}/migration-drafts/:draftId/execute`, async ({ request }) => {
      calls++; key = request.headers.get("Idempotency-Key") ?? "";
      await new Promise((resolve) => setTimeout(resolve, 50));
      return HttpResponse.json(success);
    }));
    open(); await savedDraft();
    expect(saved?.sql).toBe("ALTER TABLE items ADD COLUMN local_value TEXT;");
    await userEvent.dblClick(screen.getByRole("button", { name: "Jalankan migrasi" }));
    expect(await screen.findByText("Status: Berhasil")).toBeInTheDocument();
    expect(calls).toBe(1);
    expect(key).toMatch(/^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/);
    expect(screen.getByRole("button", { name: "Jalankan migrasi" })).toBeDisabled();
  });

  it("keeps local SQL on a revision conflict", async () => {
    server.use(
      http.get(`${base}/migration-drafts`, () => HttpResponse.json({ data: [draftFixture.data] })),
      http.get(`${base}/migration-drafts/:draftId`, () => HttpResponse.json(draftFixture)),
      http.patch(`${base}/migration-drafts/:draftId`, () => HttpResponse.json(conflict, { status: 409 })),
    );
    open();
    await userEvent.click(await screen.findByRole("button", { name: draftFixture.data.title }));
    await screen.findByRole("heading", { name: "Draft revision 1" });
    const current = "ALTER TABLE items ADD COLUMN still_local TEXT;";
    await editSQL(current);
    await userEvent.click(screen.getByRole("button", { name: "Simpan draft" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Draft berubah sejak dibaca");
    expect(EditorView.findFromDOM(screen.getByRole("textbox", { name: "SQL migrasi" }))!.state.doc.toString()).toBe(current);
  });

  it("passes the exact selection to the existing explain panel", async () => {
    let sent = "";
    server.use(http.post(`${base}/explain`, async ({ request }) => {
      sent = ((await request.json()) as { sql: string }).sql;
      return HttpResponse.json({ data: { driver: "postgres", mode: "estimate", nodes: [], raw: {}, warnings: [] } });
    }));
    open();
    const sql = "SELECT 1;\nSELECT 'a;b';";
    await editSQL(sql, { anchor: 10, head: sql.length });
    await userEvent.click(screen.getByRole("button", { name: /EXPLAIN/ }));
    await waitFor(() => expect(sent).toBe("SELECT 'a;b';"));
    await editSQL("SELECT 'whole;statement';");
    await userEvent.click(screen.getByRole("button", { name: /EXPLAIN/ }));
    await waitFor(() => expect(sent).toBe("SELECT 'whole;statement';"));
  });

  it("reuses the idempotency key after a network failure", async () => {
    const keys: string[] = [];
    server.use(http.post(`${base}/migration-drafts/:draftId/execute`, ({ request }) => {
      keys.push(request.headers.get("Idempotency-Key")!);
      return keys.length === 1 ? HttpResponse.error() : HttpResponse.json(success);
    }));
    open(); await savedDraft();
    await userEvent.click(screen.getByRole("button", { name: "Jalankan migrasi" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Server tidak dapat dihubungi");
    await userEvent.click(screen.getByRole("button", { name: "Jalankan migrasi" }));
    await screen.findByText("Status: Berhasil");
    expect(keys).toHaveLength(2); expect(keys[0]).toBe(keys[1]);
  });

  it("polls an unknown outcome without repeating execute, then reconciles", async () => {
    let executions = 0;
    let polls = 0;
    server.use(
      http.post(`${base}/migration-drafts/:draftId/execute`, () => { executions++; return HttpResponse.json(unknown, { status: 202 }); }),
      http.get(`${base}/migration-runs/:runId`, () => { polls++; return HttpResponse.json(unknown); }),
      http.post(`${base}/migration-runs/:runId/reconcile`, () => HttpResponse.json(success)),
    );
    open(); await savedDraft();
    await userEvent.click(screen.getByRole("button", { name: "Jalankan migrasi" }));
    const detail = await screen.findByRole("region", { name: "Detail eksekusi" });
    expect(within(detail).getByText("Status: Hasil belum pasti")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Jalankan migrasi" })).toBeDisabled();
    await waitFor(() => expect(polls).toBe(1), { timeout: 3000 });
    expect(executions).toBe(1);
    await userEvent.click(screen.getByRole("button", { name: "Rekonsiliasi" }));
    expect(await screen.findByText("Status: Berhasil")).toBeInTheDocument();
    expect(executions).toBe(1);
  });

  it("shows history to a viewer without requesting drafts", async () => {
    let draftReads = 0;
    server.use(
      http.get(base, () => HttpResponse.json({ data: { ...db, effective_access: "view" } })),
      http.get(`${base}/migration-drafts`, () => { draftReads++; return new HttpResponse(null, { status: 403 }); }),
      http.get(`${base}/migration-runs`, () => HttpResponse.json({ data: [success.data] })),
    );
    open();
    expect(await screen.findByText("Akses lihat hanya dapat membaca riwayat migrasi.")).toBeInTheDocument();
    expect(await screen.findByText("Berhasil")).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "SQL migrasi" })).not.toBeInTheDocument();
    expect(draftReads).toBe(0);
  });

  it("respects engine capabilities and distinguishes denied history", async () => {
    server.use(
      http.get(base, () => HttpResponse.json({ data: { ...db, capabilities: { ...db.capabilities, explain: false, transactional_migrations: false } } })),
      http.get(`${base}/migration-runs`, () => HttpResponse.json({ error: { code: "FORBIDDEN", message: "Riwayat ditolak.", fields: {}, request_id: "req-denied" } }, { status: 403 })),
    );
    open();
    expect(await screen.findByRole("button", { name: /EXPLAIN/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Jalankan migrasi" })).toBeDisabled();
    expect(await screen.findByRole("alert")).toHaveTextContent("Akses ditolak");
    expect(screen.queryByText("Belum ada eksekusi migrasi.")).not.toBeInTheDocument();
  });

  it("does not resend local SQL when the session expires", async () => {
    let executions=0;
    server.use(http.post(`${base}/migration-drafts/:draftId/execute`,()=>{
      executions++;
      return HttpResponse.json({error:{code:"UNAUTHENTICATED",message:"Silakan login kembali.",fields:{},request_id:"req-expired"}},{status:401});
    }));
    const expired=vi.fn();const off=onUnauthorized(expired);
    try {
      open();await savedDraft();
      await userEvent.click(screen.getByRole("button",{name:"Jalankan migrasi"}));
      expect(await screen.findByRole("alert")).toHaveTextContent("Silakan login kembali");
      expect(expired).toHaveBeenCalledTimes(1);
      expect(executions).toBe(1);
      expect(EditorView.findFromDOM(screen.getByRole("textbox",{name:"SQL migrasi"}))!.state.doc.toString()).toBe("ALTER TABLE items ADD COLUMN local_value TEXT;");
    } finally {off();}
  });
});

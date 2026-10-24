import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { onUnauthorized, setCsrfToken } from "../../api/client";
import type { ApiResponse, DatabaseSummary, MigrationRun } from "../../api/types";
import { handlers } from "../../mocks/handlers";
import { createMigrationHandlers } from "../../mocks/migrations";
import { MigrationsPage, type MigrationInitialState } from "./MigrationsPage";
import draftFixture from "../../../../tests/fixtures/api/migrations_draft.json";
import success from "../../../../tests/fixtures/api/migrations_run_succeeded.json";
import unknown from "../../../../tests/fixtures/api/migrations_run_unknown.json";
import conflict from "../../../../tests/fixtures/api/migrations_error_conflict.json";
import databases from "../../../../tests/fixtures/api/databases_list.json";

const db = databases.data[0] as DatabaseSummary;
const succeededRun = success.data as MigrationRun;
const base = `http://localhost/api/v1/databases/${db.id}`;
const server = setupServer(...handlers);

beforeAll(() => {
  server.listen({ onUnhandledFrame: "error" });
});
beforeEach(() => { server.resetHandlers(...createMigrationHandlers(), ...handlers); setCsrfToken("test"); });
afterEach(() => { vi.restoreAllMocks(); });
afterAll(() => server.close());

function open() {
  return render(<MigrationsPage databaseId={db.id} />);
}

function initialState(overrides: Partial<MigrationInitialState> = {}): MigrationInitialState {
  return {
    database: db, drafts: { data: [draftFixture.data] }, history: { data: [succeededRun] },
    head: { version: 1, last_run_id: success.data.id }, head_error: "",
    draft: draftFixture.data, run: succeededRun, draft_offset: 0, history_offset: 0, ...overrides,
  };
}

async function editSQL(value: string, selection?: { anchor: number; head: number }) {
  const textbox = await screen.findByRole("textbox", { name: "SQL migrasi" }) as HTMLTextAreaElement;
  fireEvent.change(textbox, { target: { value } });
  act(() => {
    textbox.focus();
    const anchor = selection?.anchor ?? value.length;
    const head = selection?.head ?? value.length;
    textbox.setSelectionRange(Math.min(anchor, head), Math.max(anchor, head), anchor > head ? "backward" : "forward");
    fireEvent.select(textbox);
  });
  return textbox;
}

async function savedDraft() {
  await userEvent.type(await screen.findByRole("textbox", { name: "Judul" }), "Perubahan item");
  await editSQL("ALTER TABLE items ADD COLUMN local_value TEXT;");
  await userEvent.click(screen.getByRole("button", { name: "Simpan draft" }));
  await screen.findByText("Draft tersimpan.");
}

describe("migration editor", () => {
  it("opens the server-selected draft and run without fetching the initial data again", async () => {
    let reads = 0;
    server.use(http.get("*", () => { reads++; return HttpResponse.error(); }));
    render(<StrictMode><MigrationsPage databaseId={db.id} initial={initialState()} /></StrictMode>);
    expect(screen.getByRole("textbox", { name: "Judul" })).toHaveValue(draftFixture.data.title);
    expect(screen.getByRole("textbox", { name: "SQL migrasi" })).toHaveValue(draftFixture.data.sql);
    expect(screen.getByRole("region", { name: "Detail eksekusi" })).toHaveTextContent("Status: Berhasil");
    await act(async () => {});
    expect(reads).toBe(0);
  });

  it("keeps the server history and selected SQL when the target head is unavailable", () => {
    render(<MigrationsPage databaseId={db.id} initial={initialState({ head: null, head_error: "Riwayat tersimpan tetap tersedia." })} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Riwayat tersimpan tetap tersedia.");
    expect(screen.getByRole("region", { name: "Detail eksekusi" })).toHaveTextContent("Status: Berhasil");
    expect(screen.getByRole("textbox", { name: "Judul" })).toHaveValue(draftFixture.data.title);
  });

  it("continues pagination from the server-provided offset", async () => {
    let offset = "";
    server.use(http.get(`${base}/migration-runs`, ({ request }) => {
      offset = new URL(request.url).searchParams.get("offset") ?? "";
      return HttpResponse.json({ data: [], page: { limit: 50, offset: 100, has_more: false } });
    }));
    const history: ApiResponse<MigrationRun[]> = { data: [succeededRun], page: { limit: 50, offset: 50, has_more: true } };
    render(<MigrationsPage databaseId={db.id} initial={initialState({ history, history_offset: 50 })} />);
    await userEvent.click(screen.getByRole("button", { name: "Riwayat berikutnya" }));
    expect(await screen.findByText("Belum ada eksekusi migrasi.")).toBeInTheDocument();
    expect(offset).toBe("100");
  });

  it("shows server-provided history to viewers without reading drafts", async () => {
    let reads = 0;
    server.use(http.get("*", () => { reads++; return HttpResponse.error(); }));
    const initial = initialState({ database: { ...db, effective_access: "view" }, drafts: null, draft: null });
    render(<MigrationsPage databaseId={db.id} initial={initial} />);
    expect(screen.getByText("Akses lihat hanya dapat membaca riwayat migrasi.")).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "SQL migrasi" })).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Detail eksekusi" })).toHaveTextContent("Status: Berhasil");
    await act(async () => {});
    expect(reads).toBe(0);
  });

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
    expect(screen.getByRole("textbox", { name: "SQL migrasi" })).toHaveValue(current);
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
      expect(screen.getByRole("textbox", { name: "SQL migrasi" })).toHaveValue("ALTER TABLE items ADD COLUMN local_value TEXT;");
    } finally {off();}
  });
});

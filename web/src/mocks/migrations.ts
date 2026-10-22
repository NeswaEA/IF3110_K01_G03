import { http, HttpResponse, type RequestHandler } from "msw";
import type { MigrationDraft, MigrationRun } from "../api/types";
import draftFixture from "../../../tests/fixtures/api/migrations_draft.json";
import runFixture from "../../../tests/fixtures/api/migrations_run_succeeded.json";
import conflict from "../../../tests/fixtures/api/migrations_error_conflict.json";

const api = "*/api/v1/databases/:id";

export function createMigrationHandlers(): RequestHandler[] {
  const drafts = new Map<string, MigrationDraft>();
  const runs = new Map<string, MigrationRun>();
  const keys = new Map<string, { input: string; run: MigrationRun }>();
  const heads = new Map<string, number>();
  const notFound = () => HttpResponse.json({ error: { code: "NOT_FOUND", message: "Migrasi tidak ditemukan.", fields: {}, request_id: "req-migration-missing" } }, { status: 404 });
  const error = (code: string, message: string, status: number) => HttpResponse.json({ error: { code, message, fields: {}, request_id: "req-migration" } }, { status });
  const list = <T>(items: T[], request: Request) => {
    const query = new URL(request.url).searchParams;
    const limit = Number(query.get("limit") ?? 50);
    const offset = Number(query.get("offset") ?? 0);
    if (!Number.isInteger(limit) || limit < 1 || limit > 200 || !Number.isInteger(offset) || offset < 0 || offset > 100000) return error("INVALID_REQUEST", "Pagination tidak valid.", 400);
    return HttpResponse.json({ data: items.slice(offset, offset + limit), page: { limit, offset, has_more: offset + limit < items.length } });
  };
  return [
    http.get(`${api}/migration-head`, ({ params }) => HttpResponse.json({ data: { version: heads.get(String(params.id)) ?? 0, last_run_id: [...runs.values()].filter((r) => r.database_id === params.id).at(-1)?.id ?? null } })),
    http.get(`${api}/migration-drafts`, ({ params, request }) => list([...drafts.values()].filter((d) => d.database_id === params.id).sort((a, b) => b.updated_at.localeCompare(a.updated_at) || b.id.localeCompare(a.id)), request)),
    http.get(`${api}/migration-runs`, ({ params, request }) => list([...runs.values()].filter((r) => r.database_id === params.id).sort((a, b) => b.started_at.localeCompare(a.started_at) || b.id.localeCompare(a.id)), request)),
    http.post(`${api}/migration-drafts`, async ({ params, request }) => {
      const body = await request.json() as Pick<MigrationDraft, "title" | "sql" | "expected_version" | "source_fingerprint">;
      if (!body.title?.trim() || !body.sql?.trim() || !Number.isSafeInteger(body.expected_version)) return error("INVALID_REQUEST", "Judul, SQL, dan versi wajib diisi.", 400);
      const now = new Date().toISOString();
      const draft: MigrationDraft = { ...draftFixture.data, ...body, id: crypto.randomUUID(), database_id: String(params.id), source_fingerprint: body.source_fingerprint ?? null, created_at: now, updated_at: now };
      drafts.set(draft.id, draft);
      return HttpResponse.json({ data: draft }, { status: 201 });
    }),
    http.get(`${api}/migration-drafts/:draftId`, ({ params }) => {
      const draft = drafts.get(String(params.draftId));
      return draft?.database_id === params.id ? HttpResponse.json({ data: draft }) : notFound();
    }),
    http.patch(`${api}/migration-drafts/:draftId`, async ({ params, request }) => {
      const draft = drafts.get(String(params.draftId));
      if (!draft || draft.database_id !== params.id) return notFound();
      const body = await request.json() as Partial<MigrationDraft>;
      if (body.revision !== draft.revision) return HttpResponse.json(conflict, { status: 409 });
      if (draft.applied_at) return error("DRAFT_APPLIED", "Draft sudah diterapkan.", 409);
      const updated = { ...draft, ...body, id: draft.id, database_id: draft.database_id, revision: draft.revision + 1, updated_at: new Date().toISOString() };
      drafts.set(draft.id, updated);
      return HttpResponse.json({ data: updated });
    }),
    http.post(`${api}/migration-drafts/:draftId/execute`, async ({ params, request }) => {
      const key = `${params.id}:${request.headers.get("Idempotency-Key")}`;
      const body = await request.json() as { revision: number; expected_version: number };
      const input = JSON.stringify([params.id, params.draftId, body.revision, body.expected_version]);
      const old = keys.get(key);
      if (old) return old.input === input ? HttpResponse.json({ data: old.run }) : error("IDEMPOTENCY_CONFLICT", "Key sudah digunakan untuk input berbeda.", 409);
      const draft = drafts.get(String(params.draftId));
      if (!draft || draft.database_id !== params.id) return notFound();
      if (draft.applied_at) return error("DRAFT_APPLIED", "Draft sudah diterapkan.", 409);
      if (draft.revision !== body.revision) return HttpResponse.json(conflict, { status: 409 });
      if (body.expected_version !== (heads.get(String(params.id)) ?? 0)) return error("VERSION_CONFLICT", "Head database berubah.", 409);
      const now = new Date().toISOString();
      const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(draft.sql));
      const sql_hash = `sha256:${Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("")}`;
      const run: MigrationRun = { ...runFixture.data, id: crypto.randomUUID(), database_id: draft.database_id, draft_id: draft.id, draft_revision: draft.revision, sql: draft.sql, sql_hash, candidate_version: body.expected_version + 1, version: body.expected_version + 1, status: "succeeded", started_at: now, finished_at: now };
      runs.set(run.id, run); keys.set(key, { input, run });
      heads.set(draft.database_id, run.version!); drafts.set(draft.id, { ...draft, applied_at: now });
      return HttpResponse.json({ data: run });
    }),
    http.get(`${api}/migration-runs/:runId`, ({ params }) => {
      const run = runs.get(String(params.runId));
      return run?.database_id === params.id ? HttpResponse.json({ data: run }) : notFound();
    }),
    http.post(`${api}/migration-runs/:runId/reconcile`, ({ params }) => {
      const run = runs.get(String(params.runId));
      return run?.database_id === params.id ? HttpResponse.json({ data: run }) : notFound();
    }),
  ];
}

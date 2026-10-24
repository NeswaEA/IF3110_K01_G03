import { http, HttpResponse, type RequestHandler } from "msw";
import type { DiffResult, Driver } from "../api/types";
import changed from "../../../tests/fixtures/api/schema_diff_changed.json";
import empty from "../../../tests/fixtures/api/schema_diff_empty.json";

const drivers: Record<string, Driver> = {
  "30000000-0000-4000-8000-000000000001": "postgres",
  "30000000-0000-4000-8000-000000000002": "sqlite",
  "30000000-0000-4000-8000-000000000003": "postgres",
  "30000000-0000-4000-8000-000000000004": "sqlite",
};

export function createDiffHandlers(): RequestHandler[] {
  return [http.post("*/api/v1/databases/:id/schema-diff", async ({ params, request }) => {
    const body = await request.json() as { desired_database_id: string; schema: string };
    const driver = drivers[String(params.id)];
    const error = (code: string, message: string, status: number) => HttpResponse.json({ error: { code, message, fields: {}, request_id: "req-diff" } }, { status });
    if (!driver || !drivers[body.desired_database_id]) return error("NOT_FOUND", "Database tidak ditemukan.", 404);
    if (!body.schema) return error("INVALID_REQUEST", "Schema wajib diisi.", 400);
    if (drivers[body.desired_database_id] !== driver || driver === "sqlite" && body.schema !== "main") return error("DIFF_UNSUPPORTED", "Engine atau schema tidak didukung.", 422);
    const result = structuredClone(params.id === body.desired_database_id ? empty.data : changed.data) as DiffResult;
    result.driver = driver;
    result.changes = result.changes.map((change) => ({ ...change, object: change.object.replace("public.", body.schema + ".") }));
    if (driver === "sqlite") result.candidate_sql = result.candidate_sql.replace("public.", "");
    return HttpResponse.json({ data: result });
  })];
}

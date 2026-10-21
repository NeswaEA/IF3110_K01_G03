import { http, HttpResponse, type RequestHandler } from "msw";
import { createAuthHandlers } from "./auth";

import connectionsList from "../../../tests/fixtures/api/connections_list.json";
import connectionsTestOk from "../../../tests/fixtures/api/connections_test_ok.json";
import connectionsErrorValidation from "../../../tests/fixtures/api/connections_error_validation.json";
import databasesList from "../../../tests/fixtures/api/databases_list.json";
import explainErrorMultiple from "../../../tests/fixtures/api/explain_error_multiple_statements.json";
import explainErrorUnsupported from "../../../tests/fixtures/api/explain_error_unsupported.json";
import explainPg from "../../../tests/fixtures/api/explain_pg.json";
import explainSqlite from "../../../tests/fixtures/api/explain_sqlite.json";
import viewerDataEmpty from "../../../tests/fixtures/api/viewer_data_empty.json";
import viewerDataPopulated from "../../../tests/fixtures/api/viewer_data_populated.json";
import viewerDataUnordered from "../../../tests/fixtures/api/viewer_data_unordered.json";
import viewerErrorNotFound from "../../../tests/fixtures/api/viewer_error_not_found.json";
import viewerSchemaPopulated from "../../../tests/fixtures/api/viewer_schema_populated.json";

// "*" prefix lets the same handlers match relative URLs in the browser and
// absolute ones under Node (tests).
const API = "*/api/v1";

const knownIds = new Set(databasesList.data.map((d) => d.id));
const driverOf = (id: string) => databasesList.data.find((d) => d.id === id)?.driver;

const notFound = () => HttpResponse.json(viewerErrorNotFound, { status: 404 });

export const handlers: RequestHandler[] = [
  ...createAuthHandlers(),
  http.get(`${API}/admin/connections`, () => HttpResponse.json(connectionsList)),

  http.post(`${API}/admin/connections`, async ({ request }) => {
    const body = (await request.json()) as { config?: { port?: number } };
    const port = body.config?.port;
    if (port !== undefined && (port < 1 || port > 65535)) {
      return HttpResponse.json(connectionsErrorValidation, { status: 400 });
    }
    return HttpResponse.json({ data: connectionsList.data[0] }, { status: 201 });
  }),

  http.post(`${API}/admin/connections/:id/test`, () => HttpResponse.json(connectionsTestOk)),

  http.delete(`${API}/admin/connections/:id`, ({ params }) =>
    connectionsList.data.some((c) => c.id === params.id) ? new HttpResponse(null, { status: 204 }) : notFound(),
  ),

  http.get(`${API}/databases`, () => HttpResponse.json(databasesList)),

  http.get(`${API}/databases/:id`, ({ params }) => {
    const found = databasesList.data.find((d) => d.id === params.id);
    return found ? HttpResponse.json({ data: found }) : notFound();
  }),

  http.get(`${API}/databases/:id/schema`, ({ params }) =>
    knownIds.has(String(params.id)) ? HttpResponse.json(viewerSchemaPopulated) : notFound(),
  ),

  http.get(`${API}/databases/:id/data`, ({ params, request }) => {
    if (!knownIds.has(String(params.id))) return notFound();
    const url = new URL(request.url);
    const table = url.searchParams.get("table");
    if (!url.searchParams.get("schema") || !table) {
      return HttpResponse.json(
        { error: { code: "INVALID_REQUEST", message: "schema dan table wajib.", fields: { table: "wajib" }, request_id: "req-demo-011" } },
        { status: 400 },
      );
    }
    if (table === "empty_table") return HttpResponse.json(viewerDataEmpty);
    if (table === "audit_log") return HttpResponse.json(viewerDataUnordered);
    return HttpResponse.json(viewerDataPopulated);
  }),

  http.post(`${API}/databases/:id/explain`, async ({ params, request }) => {
    const id = String(params.id);
    if (!knownIds.has(id)) return notFound();
    const { sql } = (await request.json()) as { sql: string };
    const statements = sql.split(";").filter((s) => s.trim() !== "");
    if (statements.length > 1) return HttpResponse.json(explainErrorMultiple, { status: 422 });
    if (/^\s*(create|alter|drop)\b/i.test(sql)) {
      return HttpResponse.json(explainErrorUnsupported, { status: 422 });
    }
    return HttpResponse.json(driverOf(id) === "sqlite" ? explainSqlite : explainPg);
  }),
];

import { http, HttpResponse } from "msw";
import type { Schemas } from "@/api/client";

import headFixture from "../../../tests/fixtures/api/database_head.json";
import executionFixture from "../../../tests/fixtures/api/execution_queued.json";
import migrationFixture from "../../../tests/fixtures/api/migrations_pending.json";
import requestFixture from "../../../tests/fixtures/api/reviews_change_request_pending.json";
import timelineFixture from "../../../tests/fixtures/api/timeline_queued.json";
import notFound from "../../../tests/fixtures/api/viewer_error_not_found.json";

const pendingRequest = {
  ...requestFixture.data,
  action: "apply",
  status: "pending_review",
  approvals: requestFixture.data.approvals.map((approval) => ({
    ...approval, decision: approval.decided_at ? "approved" : "pending",
  })),
} satisfies Schemas["ChangeRequest"];

const pendingMigration = {
  ...migrationFixture.data,
  status: "pending",
  current: { ...migrationFixture.data.current, mode: "normal" },
  open_request: pendingRequest,
} satisfies Schemas["Migration"];

const execution = {
  ...executionFixture.data,
  action: "apply", direction: "up", mode: "normal", status: "queued",
} satisfies Schemas["Execution"];

const executedRequest = {
  ...pendingRequest,
  id: execution.change_request_id,
  migration_id: execution.migration_id,
  migration_title: execution.migration_title,
  status: "executed",
  approvals: pendingRequest.approvals.map((approval) => ({
    ...approval,
    decision: "approved",
    decided_at: approval.decided_at ?? execution.enqueued_at,
  })),
  execution_id: execution.id,
  updated_at: execution.enqueued_at,
} satisfies Schemas["ChangeRequest"];

const queuedMigration = {
  ...pendingMigration,
  id: execution.migration_id,
  title: execution.migration_title,
  open_request: null,
} satisfies Schemas["Migration"];

const migrations = [pendingMigration, queuedMigration];
const requests = [pendingRequest, executedRequest];
const timeline = { ...timelineFixture.data, execution } satisfies Schemas["TimelineEntry"];
const head = headFixture.data satisfies Schemas["DatabaseHead"];

export function migrationHandlers(api: string, knownIds: ReadonlySet<string>) {
  const base = `${api}/databases/:id`;
  const hasDatabase = (id: unknown) => knownIds.has(String(id));
  const example = (id: unknown) => id === pendingMigration.database_id;
  const findMigration = (id: unknown, mid: unknown) =>
    migrations.find((migration) => migration.database_id === id && migration.id === mid);
  const missing = () => HttpResponse.json(notFound, { status: 404 });
  const one = (data: object) => HttpResponse.json({ data });
  const list = (rows: object[]) => HttpResponse.json({ data: rows, page: { limit: 50, offset: 0, has_more: false } });
  return [
    http.get(`${base}/migrations`, ({ params, request: req }) => {
      if (!hasDatabase(params.id)) return missing();
      const status = new URL(req.url).searchParams.get("status");
      return list(migrations.filter((migration) => migration.database_id === params.id && (!status || status === migration.status)));
    }),
    http.get(`${base}/migrations/:mid`, ({ params }) => {
      const migration = findMigration(params.id, params.mid);
      return migration ? one(migration) : missing();
    }),
    http.get(`${base}/migrations/:mid/revisions`, ({ params }) => {
      const migration = findMigration(params.id, params.mid);
      return migration ? HttpResponse.json({ data: [migration.current] }) : missing();
    }),
    http.get(`${base}/change-requests`, ({ params, request: req }) => {
      if (!hasDatabase(params.id)) return missing();
      const query = new URL(req.url).searchParams;
      const mid = query.get("migration_id");
      const status = query.get("status");
      return list(requests.filter((request) =>
        request.database_id === params.id && (!mid || mid === request.migration_id) && (!status || status === request.status)));
    }),
    http.get(`${base}/change-requests/:rid`, ({ params }) => {
      const request = requests.find((item) => item.database_id === params.id && item.id === params.rid);
      return request ? one(request) : missing();
    }),
    http.get(`${base}/executions`, ({ params, request: req }) => {
      if (!hasDatabase(params.id)) return missing();
      const mid = new URL(req.url).searchParams.get("migration_id");
      return list(example(params.id) && (!mid || mid === execution.migration_id) ? [execution] : []);
    }),
    http.get(`${base}/executions/:eid`, ({ params }) =>
      example(params.id) && params.eid === execution.id ? one(execution) : missing()),
    http.get(`${base}/history`, ({ params }) =>
      hasDatabase(params.id) ? list(example(params.id) ? [timeline] : []) : missing()),
    http.get(`${base}/head`, ({ params }) =>
      hasDatabase(params.id) ? one(example(params.id) ? head : { ...head, version: 0, migration_id: null, history_seq: 0 }) : missing()),
  ];
}

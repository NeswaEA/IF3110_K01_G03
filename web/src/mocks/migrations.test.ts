import { expect, it } from "vitest";
import { api, unwrap } from "@/api/client";
import pendingFixture from "../../../tests/fixtures/api/reviews_change_request_pending.json";
import queuedFixture from "../../../tests/fixtures/api/execution_queued.json";

it("serves consistent pending review and queued migration scenarios", async () => {
  const { database_id: id, migration_id: queuedId, id: executionId, change_request_id: requestId } = queuedFixture.data;
  const pendingId = pendingFixture.data.migration_id;

  const [pendingMigration, queuedMigration, pendingExecutions, executedRequest, timeline] = await Promise.all([
    unwrap(api.GET("/databases/{id}/migrations/{mid}", { params: { path: { id, mid: pendingId } } })),
    unwrap(api.GET("/databases/{id}/migrations/{mid}", { params: { path: { id, mid: queuedId } } })),
    unwrap(api.GET("/databases/{id}/executions", { params: { path: { id }, query: { migration_id: pendingId } } })),
    unwrap(api.GET("/databases/{id}/change-requests/{rid}", { params: { path: { id, rid: requestId } } })),
    unwrap(api.GET("/databases/{id}/history", { params: { path: { id } } })),
  ]);

  expect(pendingMigration.data.open_request).toMatchObject({ status: "pending_review", execution_id: null });
  expect(pendingExecutions.data).toEqual([]);
  expect(queuedMigration.data.open_request).toBeNull();
  expect(executedRequest.data).toMatchObject({ status: "executed", migration_id: queuedId, execution_id: executionId });
  expect(executedRequest.data.approvals.every((approval) => approval.decision === "approved")).toBe(true);
  expect(timeline.data[0]?.execution).toMatchObject({ id: executionId, change_request_id: requestId, status: "queued" });
});

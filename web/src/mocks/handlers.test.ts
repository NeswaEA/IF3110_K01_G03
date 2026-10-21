import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { request, setCsrfToken } from "../api/client";
import type { AdminConnection, DatabaseSummary, PlanResult, SchemaSnapshot, TableData } from "../api/types";
import { handlers } from "./handlers";

const PG = "30000000-0000-4000-8000-000000000001";
const LITE = "30000000-0000-4000-8000-000000000002";

const server = setupServer(...handlers);
beforeAll(() => {
  server.listen({ onUnhandledFrame: "error" });
  setCsrfToken("t");
});
afterEach(() => server.resetHandlers(...handlers));
afterAll(() => server.close());

// Fixture drift check: key sets must equal the shared DTO field lists.
const keys = (o: object) => Object.keys(o).sort();

describe("fixtures match DTO field names", () => {
  it("DatabaseSummary and Capabilities", async () => {
    const res = await request<DatabaseSummary[]>("/databases");
    expect(keys(res!.data[0])).toEqual(["capabilities", "driver", "effective_access", "id", "name", "revision"]);
    expect(keys(res!.data[0].capabilities)).toEqual(["explain", "explain_analyze", "schema_diff", "transactional_migrations"]);
    expect(res!.page).toEqual({ limit: 50, offset: 0, has_more: false });
  });

  it("AdminConnection has no secret fields", async () => {
    const res = await request<AdminConnection[]>("/admin/connections");
    expect(keys(res!.data[0])).toEqual([
      "active", "config", "driver", "id", "last_checked_at", "last_error_code", "migration_ready", "name", "revision",
    ]);
    expect(JSON.stringify(res)).not.toMatch(/password"|dsn|ciphertext/i);
  });

  it("SchemaSnapshot", async () => {
    const res = await request<SchemaSnapshot>(`/databases/${PG}/schema`);
    expect(keys(res!.data)).toEqual(["database_id", "driver", "fingerprint", "schemas"]);
    const t = res!.data.schemas[0].tables[1];
    expect(keys(t)).toEqual(["columns", "constraints", "indexes", "name"]);
    expect(keys(t.constraints[0])).toEqual(["columns", "definition", "kind", "name", "references"]);
    expect(keys(t.constraints[0].references!)).toEqual(["columns", "on_delete", "on_update", "schema", "table"]);
    expect(t.indexes[0].columns).toEqual(["carrier", "order_id"]); // composite order kept
  });

  it("PlanResult for both engines, metrics null when unavailable", async () => {
    for (const [id, driver] of [[PG, "postgres"], [LITE, "sqlite"]] as const) {
      const res = await request<PlanResult>(`/databases/${id}/explain`, {
        method: "POST", body: { sql: "SELECT 1", mode: "estimate" },
      });
      expect(res!.data.driver).toBe(driver);
      expect(res!.data.mode).toBe("estimate");
      expect(keys(res!.data.nodes[0])).toEqual([
        "actual_rows", "actual_time_ms", "estimated_cost", "estimated_rows", "id", "index", "operation", "parent_id", "relation",
      ]);
      expect(res!.data.nodes[0].actual_time_ms).toBeNull();
    }
  });
});

describe("handlers behaviour", () => {
  it("keeps bigint as string and NULL as null", async () => {
    const res = await request<TableData>(`/databases/${PG}/data?schema=public&table=items`);
    expect(res!.data.rows[1][0]).toBe("9007199254740993");
    expect(res!.data.rows[0][2]).toBeNull();
  });

  it("empty table keeps columns", async () => {
    const res = await request<TableData>(`/databases/${PG}/data?schema=public&table=empty_table`);
    expect(res!.data.columns.length).toBeGreaterThan(0);
    expect(res!.data.rows).toEqual([]);
  });

  it("table without PK reports order_stable false", async () => {
    const res = await request<TableData>(`/databases/${PG}/data?schema=public&table=audit_log`);
    expect(res!.data.order_stable).toBe(false);
  });

  it("validation, not-found, and plan errors surface as ApiError", async () => {
    await expect(request(`/databases/${PG}/data`)).rejects.toMatchObject({ code: "INVALID_REQUEST", status: 400 });
    await expect(request("/databases/00000000-0000-4000-8000-000000000000")).rejects.toMatchObject({ code: "NOT_FOUND" });
    await expect(
      request(`/databases/${PG}/explain`, { method: "POST", body: { sql: "SELECT 1; SELECT 2", mode: "estimate" } }),
    ).rejects.toMatchObject({ code: "UNSUPPORTED_SQL" });
    await expect(
      request(`/databases/${PG}/explain`, { method: "POST", body: { sql: "CREATE TABLE t(a int)", mode: "estimate" } }),
    ).rejects.toMatchObject({ code: "PLAN_UNSUPPORTED" });
    await expect(
      request("/admin/connections", { method: "POST", body: { config: { port: 70000 } } }),
    ).rejects.toMatchObject({ code: "INVALID_REQUEST", fields: { "config.port": expect.any(String) } });
  });

  it("an unknown route fails loudly", async () => {
    await expect(request("/databases/typo-route/schemaa")).rejects.toBeDefined();
  });
});

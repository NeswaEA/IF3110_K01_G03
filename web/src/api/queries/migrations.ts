import { queryOptions } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "../client";
export type CreateMigrationBody = Omit<Schemas["CreateMigrationInput"], "down_sql" | "irreversible_reason"> &
  ({ down_sql: string; irreversible_reason?: never } | { irreversible_reason: string; down_sql?: never });

export const migrationListQuery = (id: string, status?: Schemas["MigrationStatus"], offset = 0) => queryOptions({
  queryKey: ["migrations", id, "list", status, offset],
  queryFn: ({ signal }) => unwrap(api.GET("/databases/{id}/migrations", { params: { path: { id }, query: { status, offset } }, signal })),
});

export const migrationQuery = (id: string, mid: string) => queryOptions({
  queryKey: ["migrations", id, mid],
  queryFn: async ({ signal }) => (await unwrap(api.GET("/databases/{id}/migrations/{mid}", { params: { path: { id, mid } }, signal }))).data,
});

export const revisionsQuery = (id: string, mid: string) => queryOptions({
  queryKey: ["migrations", id, mid, "revisions"],
  queryFn: async ({ signal }) => (await unwrap(api.GET("/databases/{id}/migrations/{mid}/revisions", { params: { path: { id, mid } }, signal }))).data,
});

export const requestsQuery = (id: string, mid: string) => queryOptions({
  queryKey: ["requests", id, mid],
  queryFn: async ({ signal }) => (await unwrap(api.GET("/databases/{id}/change-requests", { params: { path: { id }, query: { migration_id: mid } }, signal }))).data,
});

export const executionsQuery = (id: string, mid: string) => queryOptions({
  queryKey: ["executions", id, "migration", mid],
  queryFn: async ({ signal }) => (await unwrap(api.GET("/databases/{id}/executions", { params: { path: { id }, query: { migration_id: mid } }, signal }))).data,
});

export const executionQuery = (id: string, eid: string) => queryOptions({
  queryKey: ["executions", id, eid],
  queryFn: async ({ signal }) => (await unwrap(api.GET("/databases/{id}/executions/{eid}", { params: { path: { id, eid } }, signal }))).data,
  refetchInterval: ({ state }) => ["queued", "held", "running"].includes(state.data?.status ?? "") ? 5000 : false,
});

export const batchesQuery = (id: string, eid: string, offset: number, active: boolean) => queryOptions({
  queryKey: ["executions", id, eid, "batches", offset],
  queryFn: ({ signal }) => unwrap(api.GET("/databases/{id}/executions/{eid}/batches", { params: { path: { id, eid }, query: { offset } }, signal })),
  refetchInterval: active ? 5000 : false,
});

export const headQuery = (id: string) => queryOptions({
  queryKey: ["head", id],
  queryFn: async ({ signal }) => (await unwrap(api.GET("/databases/{id}/head", { params: { path: { id } }, signal }))).data,
});

export const historyQuery = (id: string, hide_rollback: boolean, offset: number) => queryOptions({
  queryKey: ["history", id, hide_rollback, offset],
  queryFn: ({ signal }) => unwrap(api.GET("/databases/{id}/history", { params: { path: { id }, query: { hide_rollback, offset } }, signal })),
});

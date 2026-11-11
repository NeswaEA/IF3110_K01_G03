import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "../client";

export const databasesQuery = queryOptions({
  queryKey: ["databases"],
  queryFn: async ({ signal }) => (await unwrap(api.GET("/databases", { params: { query: { limit: 200 } }, signal }))).data,
});

export const databaseQuery = (id: string) =>
  queryOptions({
    queryKey: ["databases", id],
    queryFn: async ({ signal }) => (await unwrap(api.GET("/databases/{id}", { params: { path: { id } }, signal }))).data,
  });

export const schemaQuery = (id: string) =>
  queryOptions({
    queryKey: ["schema", id],
    queryFn: async ({ signal }) => (await unwrap(api.GET("/databases/{id}/schema", { params: { path: { id } }, signal }))).data,
  });

export const PAGE_SIZE = 50;

export const tableDataQuery = (id: string, schema: string, table: string, offset: number) =>
  queryOptions({
    queryKey: ["data", id, schema, table, offset],
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/databases/{id}/data", {
          params: { path: { id }, query: { schema, table, offset, limit: PAGE_SIZE } },
          signal,
        }),
      ),
    placeholderData: keepPreviousData, // keep the old page on screen while the next one loads
  });

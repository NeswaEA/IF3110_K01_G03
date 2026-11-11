import { queryOptions } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "../client";

export const connectionsQuery = queryOptions({
  queryKey: ["connections"],
  queryFn: async ({ signal }) =>
    (await unwrap(api.GET("/admin/connections", { params: { query: { limit: 200 } }, signal }))).data,
});

export const createConnection = async (body: Schemas["ConnectionCreate"]) =>
  (await unwrap(api.POST("/admin/connections", { body }))).data;

export const updateConnection = async (id: string, body: Schemas["ConnectionUpdate"]) =>
  (await unwrap(api.PATCH("/admin/connections/{id}", { params: { path: { id } }, body }))).data;

export const testConnection = async (id: string) =>
  (await unwrap(api.POST("/admin/connections/{id}/test", { params: { path: { id } } }))).data;

export const deactivateConnection = (id: string) =>
  unwrap(api.DELETE("/admin/connections/{id}", { params: { path: { id } } }));

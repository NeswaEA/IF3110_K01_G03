import { queryOptions } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "../client";

export const PAGE_SIZE = 50;

export const usersQuery = (offset: number) =>
  queryOptions({
    queryKey: ["users", offset],
    queryFn: async ({ signal }) => unwrap(api.GET("/admin/users", { params: { query: { limit: PAGE_SIZE, offset } }, signal })),
  });

export const createUser = async (body: { username: string; password: string; is_platform_admin: boolean }) =>
  (await unwrap(api.POST("/admin/users", { body }))).data;

export const updateUser = async (userId: string, body: { active?: boolean; is_platform_admin?: boolean }) =>
  (await unwrap(api.PATCH("/admin/users/{userId}", { params: { path: { userId } }, body }))).data;

export const resetPassword = (userId: string, password: string) =>
  unwrap(api.PUT("/admin/users/{userId}/password", { params: { path: { userId } }, body: { password } }));

export type User = Schemas["User"];

import { queryOptions } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "../client";

export type Group = Schemas["Group"];
export type Grant = Schemas["Grant"];

// shortcut: one page of 200 covers the course deployment; page the lists when a deployment outgrows it.
const ALL = { limit: 200 } as const;

export const groupsQuery = queryOptions({
  queryKey: ["groups"],
  queryFn: async ({ signal }) => (await unwrap(api.GET("/admin/groups", { params: { query: ALL }, signal }))).data,
});

export const groupMembersQuery = (groupId: string) =>
  queryOptions({
    queryKey: ["groups", groupId, "members"],
    queryFn: async ({ signal }) =>
      (await unwrap(api.GET("/admin/groups/{groupId}/members", { params: { path: { groupId }, query: ALL }, signal }))).data,
  });

export const groupGrantsQuery = (groupId: string) =>
  queryOptions({
    queryKey: ["groups", groupId, "grants"],
    queryFn: async ({ signal }) =>
      (await unwrap(api.GET("/admin/groups/{groupId}/grants", { params: { path: { groupId }, query: ALL }, signal }))).data,
  });

/** Every account, for choosing new members. */
export const allUsersQuery = queryOptions({
  queryKey: ["users", "all"],
  queryFn: async ({ signal }) => (await unwrap(api.GET("/admin/users", { params: { query: ALL }, signal }))).data,
});

export const createGroup = async (name: string) => (await unwrap(api.POST("/admin/groups", { body: { name } }))).data;

export const renameGroup = async (groupId: string, name: string) =>
  (await unwrap(api.PATCH("/admin/groups/{groupId}", { params: { path: { groupId } }, body: { name } }))).data;

export const deleteGroup = (groupId: string) => unwrap(api.DELETE("/admin/groups/{groupId}", { params: { path: { groupId } } }));

export const addMember = (groupId: string, userId: string) =>
  unwrap(api.PUT("/admin/groups/{groupId}/members/{userId}", { params: { path: { groupId, userId } }, body: {} }));

export const removeMember = (groupId: string, userId: string) =>
  unwrap(api.DELETE("/admin/groups/{groupId}/members/{userId}", { params: { path: { groupId, userId } } }));

export const setLeader = async (groupId: string, userId: string) =>
  (await unwrap(api.PUT("/admin/groups/{groupId}/leader", { params: { path: { groupId } }, body: { user_id: userId } }))).data;

export const setGrant = async (groupId: string, databaseId: string, level: Grant["level"]) =>
  (await unwrap(api.PUT("/admin/groups/{groupId}/grants/{databaseId}", { params: { path: { groupId, databaseId } }, body: { level } }))).data;

export const removeGrant = (groupId: string, databaseId: string) =>
  unwrap(api.DELETE("/admin/groups/{groupId}/grants/{databaseId}", { params: { path: { groupId, databaseId } } }));

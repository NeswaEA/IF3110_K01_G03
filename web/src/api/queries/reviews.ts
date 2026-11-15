import { queryOptions } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "../client";

export type ChangeRequest = Schemas["ChangeRequest"];

export const REVIEWS_PAGE_SIZE = 20;

/** The caller's inbox across databases; ["reviews"] is invalidated by any request.updated event. */
export const reviewsQuery = (offset: number) =>
  queryOptions({
    queryKey: ["reviews", offset],
    queryFn: async ({ signal }) => unwrap(api.GET("/reviews", { params: { query: { limit: REVIEWS_PAGE_SIZE, offset } }, signal })),
  });

const path = (request: ChangeRequest) => ({ path: { id: request.database_id, rid: request.id } });

export const approveRequest = async (request: ChangeRequest, comment?: string) =>
  (await unwrap(api.POST("/databases/{id}/change-requests/{rid}/approve", { params: path(request), body: comment ? { comment } : {} }))).data;

export const rejectRequest = async (request: ChangeRequest, comment: string) =>
  (await unwrap(api.POST("/databases/{id}/change-requests/{rid}/reject", { params: path(request), body: { comment } }))).data;

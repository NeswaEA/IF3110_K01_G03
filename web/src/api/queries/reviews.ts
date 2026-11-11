import { api, unwrap, type Schemas } from "../client";

export type ChangeRequest = Schemas["ChangeRequest"];

const path = (request: ChangeRequest) => ({ path: { id: request.database_id, rid: request.id } });

export const approveRequest = async (request: ChangeRequest, comment?: string) =>
  (await unwrap(api.POST("/databases/{id}/change-requests/{rid}/approve", { params: path(request), body: comment ? { comment } : {} }))).data;

export const rejectRequest = async (request: ChangeRequest, comment: string) =>
  (await unwrap(api.POST("/databases/{id}/change-requests/{rid}/reject", { params: path(request), body: { comment } }))).data;

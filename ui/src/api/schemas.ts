import { api } from "./client";
import type { Schema } from "./types";

export function listSchemas(): Promise<Schema[]> {
  return api.get("/v1/schemas");
}

export function getSchema(id: string): Promise<Schema> {
  return api.get(`/v1/schemas/${id}`);
}

export function induceSchemas(): Promise<{ started: boolean; reason?: string }> {
  return api.post("/v1/schemas/induce", {});
}

export function deleteSchema(id: string): Promise<void> {
  return api.delete(`/v1/schemas/${id}`);
}

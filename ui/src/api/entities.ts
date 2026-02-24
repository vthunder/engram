import { api } from "./client";
import type { Entity, EntityCard, EntityType, Engram, EngramCard } from "./types";

export function listEntities(params?: {
  type?: EntityType;
  limit?: number;
  detail?: boolean;
}): Promise<Entity[] | EntityCard[]> {
  const q = new URLSearchParams();
  if (params?.type) q.set("type", params.type);
  if (params?.limit !== undefined) q.set("limit", String(params.limit));
  if (params?.detail) q.set("detail", "full");
  const qs = q.toString();
  return api.get(`/v1/entities${qs ? `?${qs}` : ""}`);
}

export function getEntity(id: string, detail = true): Promise<Entity> {
  return api.get(`/v1/entities/${id}${detail ? "?detail=full" : ""}`);
}

export function getEntityEngrams(
  id: string,
  detail = false,
): Promise<Engram[] | EngramCard[]> {
  return api.get(
    `/v1/entities/${id}/engrams${detail ? "?detail=full" : ""}`,
  );
}

export function searchEntities(
  query: string,
  limit = 10,
  detail = false,
): Promise<Entity[] | EntityCard[]> {
  return api.post("/v1/entities/search", {
    query,
    limit,
    detail: detail ? "full" : undefined,
  });
}

import { api } from "./client";
import type { Engram, EngramCard, EngramContext } from "./types";

export function listEngrams(params?: {
  threshold?: number;
  limit?: number;
  detail?: boolean;
  level?: number;
}): Promise<Engram[] | EngramCard[]> {
  const q = new URLSearchParams();
  if (params?.threshold !== undefined)
    q.set("threshold", String(params.threshold));
  if (params?.limit !== undefined) q.set("limit", String(params.limit));
  if (params?.detail) q.set("detail", "full");
  if (params?.level) q.set("level", String(params.level));
  const qs = q.toString();
  return api.get(`/v1/engrams${qs ? `?${qs}` : ""}`);
}

export function getEngram(id: string, detail = true, level = 0): Promise<Engram> {
  const q = new URLSearchParams();
  if (detail) q.set("detail", "full");
  if (level > 0) q.set("level", String(level));
  const qs = q.toString();
  return api.get(`/v1/engrams/${id}${qs ? `?${qs}` : ""}`);
}

export function getEngramContext(
  id: string,
  detail = true,
): Promise<EngramContext> {
  return api.get(
    `/v1/engrams/${id}/context${detail ? "?detail=full" : ""}`,
  );
}

export function searchEngrams(
  query: string,
  limit = 10,
  detail = false,
  level = 0,
): Promise<Engram[] | EngramCard[]> {
  return api.post("/v1/engrams/search", {
    query,
    limit,
    detail: detail ? "full" : undefined,
    level: level > 0 ? level : undefined,
  });
}

export function deleteEngram(id: string): Promise<void> {
  return api.delete(`/v1/engrams/${id}`);
}

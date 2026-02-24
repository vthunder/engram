import { api } from "./client";
import type { Episode, EpisodeCard, EpisodeCountResponse } from "./types";

export function listEpisodes(params?: {
  channel?: string;
  before?: string;
  unconsolidated?: boolean;
  limit?: number;
  detail?: boolean;
  level?: number;
}): Promise<Episode[] | EpisodeCard[]> {
  const q = new URLSearchParams();
  if (params?.channel) q.set("channel", params.channel);
  if (params?.before) q.set("before", params.before);
  if (params?.unconsolidated) q.set("unconsolidated", "true");
  if (params?.limit !== undefined) q.set("limit", String(params.limit));
  if (params?.detail) q.set("detail", "full");
  if (params?.level) q.set("level", String(params.level));
  const qs = q.toString();
  return api.get(`/v1/episodes${qs ? `?${qs}` : ""}`);
}

export function getEpisodeCount(params?: {
  unconsolidated?: boolean;
  channel?: string;
}): Promise<EpisodeCountResponse> {
  const q = new URLSearchParams();
  if (params?.unconsolidated) q.set("unconsolidated", "true");
  if (params?.channel) q.set("channel", params.channel);
  const qs = q.toString();
  return api.get(`/v1/episodes/count${qs ? `?${qs}` : ""}`);
}

export function searchEpisodes(
  query: string,
  limit = 10,
  detail = false,
): Promise<Episode[] | EpisodeCard[]> {
  return api.post("/v1/episodes/search", {
    query,
    limit,
    detail: detail ? "full" : undefined,
  });
}

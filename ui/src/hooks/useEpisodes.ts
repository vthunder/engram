import { useQuery } from "@tanstack/react-query";
import { listEpisodes, getEpisodeCount } from "@/api/episodes";
import type { Episode } from "@/api/types";

export function useEpisodes(params?: {
  channel?: string;
  before?: string;
  unconsolidated?: boolean;
  limit?: number;
}) {
  return useQuery({
    queryKey: ["episodes", params],
    queryFn: () =>
      listEpisodes({ ...params, detail: true, level: 8 }) as Promise<Episode[]>,
  });
}

export function useEpisodeCount(params?: {
  unconsolidated?: boolean;
  channel?: string;
}) {
  return useQuery({
    queryKey: ["episodeCount", params],
    queryFn: () => getEpisodeCount(params),
  });
}

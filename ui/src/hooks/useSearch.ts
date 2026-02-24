import { useQuery } from "@tanstack/react-query";
import { searchEngrams } from "@/api/engrams";
import { searchEpisodes } from "@/api/episodes";
import { searchEntities } from "@/api/entities";
import type { Engram, Episode, Entity } from "@/api/types";

export interface SearchResults {
  engrams: Engram[];
  episodes: Episode[];
  entities: Entity[];
}

export function useSearch(query: string) {
  return useQuery({
    queryKey: ["search", query],
    queryFn: async (): Promise<SearchResults> => {
      const [engrams, episodes, entities] = await Promise.all([
        searchEngrams(query, 10, true) as Promise<Engram[] | null>,
        searchEpisodes(query, 10, true) as Promise<Episode[] | null>,
        searchEntities(query, 10, true) as Promise<Entity[] | null>,
      ]);
      return { engrams: engrams ?? [], episodes: episodes ?? [], entities: entities ?? [] };
    },
    enabled: query.trim().length > 0,
  });
}

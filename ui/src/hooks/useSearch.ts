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
        searchEngrams(query, 10, true) as Promise<Engram[]>,
        searchEpisodes(query, 10, true) as Promise<Episode[]>,
        searchEntities(query, 10, true) as Promise<Entity[]>,
      ]);
      return { engrams, episodes, entities };
    },
    enabled: query.trim().length > 0,
  });
}

import { useQuery } from "@tanstack/react-query";
import { listEntities, getEntity, getEntityEngrams } from "@/api/entities";
import type { Entity, EntityType, Engram } from "@/api/types";

export function useEntities(params?: { type?: EntityType; limit?: number }) {
  return useQuery({
    queryKey: ["entities", params],
    queryFn: () =>
      listEntities({ ...params, detail: true }) as Promise<Entity[]>,
  });
}

export function useEntity(id: string) {
  return useQuery({
    queryKey: ["entity", id],
    queryFn: () => getEntity(id),
    enabled: !!id,
  });
}

export function useEntityEngrams(id: string) {
  return useQuery({
    queryKey: ["entityEngrams", id],
    queryFn: () => getEntityEngrams(id, true) as Promise<Engram[]>,
    enabled: !!id,
  });
}

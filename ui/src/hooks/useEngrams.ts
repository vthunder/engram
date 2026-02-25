import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { listEngrams, getEngram, getEngramContext, getEngramChildren, searchEngrams, deleteEngram } from "@/api/engrams";
import type { Engram } from "@/api/types";

export function useEngrams(params?: { threshold?: number; limit?: number; depth?: number }) {
  return useQuery({
    queryKey: ["engrams", params],
    queryFn: () => listEngrams({ ...params, detail: true, level: 8 }) as Promise<Engram[]>,
  });
}

export function useEngram(id: string) {
  return useQuery({
    queryKey: ["engram", id],
    queryFn: () => getEngram(id),
    enabled: !!id,
  });
}

export function useEngramContext(id: string) {
  return useQuery({
    queryKey: ["engramContext", id],
    queryFn: () => getEngramContext(id, true),
    enabled: !!id,
  });
}

export function useEngramChildren(id: string, level = 0) {
  return useQuery({
    queryKey: ["engramChildren", id, level],
    queryFn: () => getEngramChildren(id, level),
    enabled: !!id,
  });
}

export function useEngramAtLevel(id: string, level: number) {
  return useQuery({
    queryKey: ["engram", id, level],
    queryFn: () => getEngram(id, true, level),
    enabled: !!id,
  });
}

export function useSearchEngrams(query: string, limit = 10) {
  return useQuery({
    queryKey: ["searchEngrams", query, limit],
    queryFn: () => searchEngrams(query, limit, true) as Promise<Engram[]>,
    enabled: query.length > 0,
  });
}

export function useDeleteEngram() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => deleteEngram(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["engrams"] });
    },
  });
}

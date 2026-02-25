import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { listSchemas, getSchema, induceSchemas, deleteSchema } from "@/api/schemas";

export function useSchemas() {
  return useQuery({
    queryKey: ["schemas"],
    queryFn: listSchemas,
  });
}

export function useSchema(id: string) {
  return useQuery({
    queryKey: ["schema", id],
    queryFn: () => getSchema(id),
    enabled: !!id,
  });
}

export function useInduceSchemas() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: induceSchemas,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["schemas"] });
    },
  });
}

export function useDeleteSchema() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => deleteSchema(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["schemas"] });
    },
  });
}

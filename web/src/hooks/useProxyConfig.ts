import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getProxyConfig, saveProxyConfig } from "../lib/api";

export function useProxyConfig() {
  return useQuery({ queryKey: ["proxy-config"], queryFn: getProxyConfig });
}

export function useSaveProxyConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: saveProxyConfig,
    onSuccess: () => qc.invalidateQueries({ queryKey: ["proxy-config"] }),
  });
}

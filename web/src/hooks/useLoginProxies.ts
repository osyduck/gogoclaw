import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getLoginProxies, saveLoginProxies } from "../lib/api";

export function useLoginProxies() {
  return useQuery({ queryKey: ["login-proxies"], queryFn: getLoginProxies });
}

export function useSaveLoginProxies() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: saveLoginProxies,
    onSuccess: () => qc.invalidateQueries({ queryKey: ["login-proxies"] }),
  });
}

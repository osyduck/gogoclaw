import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { deleteAccount, listAccounts, refreshAccount, refreshAll } from "../lib/api";

export function useAccounts() {
  return useQuery({ queryKey: ["accounts"], queryFn: listAccounts });
}

function useAccountsInvalidator() {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ queryKey: ["accounts"] });
}

export function useRefreshAccount() {
  const invalidate = useAccountsInvalidator();
  return useMutation({ mutationFn: refreshAccount, onSuccess: invalidate });
}

export function useRefreshAll() {
  const invalidate = useAccountsInvalidator();
  return useMutation({ mutationFn: refreshAll, onSuccess: invalidate });
}

export function useDeleteAccount() {
  const invalidate = useAccountsInvalidator();
  return useMutation({ mutationFn: deleteAccount, onSuccess: invalidate });
}

import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";

// useServerEvents opens the SSE stream and refetches the accounts list whenever
// the server reports a login/refresh/delete event.
export function useServerEvents() {
  const qc = useQueryClient();
  useEffect(() => {
    const es = new EventSource("/api/events");
    es.onmessage = () => {
      qc.invalidateQueries({ queryKey: ["accounts"] });
    };
    return () => es.close();
  }, [qc]);
}

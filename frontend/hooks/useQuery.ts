"use client";
import { useCallback, useEffect, useState } from "react";
import { api, errorMessage, APIError } from "@/lib/api";
export function useQuery<T>(path: string | null) {
  const [revision, setRevision] = useState(0);
  const [state, setState] = useState<{
    path: string | null;
    data?: T;
    error?: string;
    status?: number;
    loading: boolean;
  }>({ path, loading: !!path });
  const reload = useCallback(() => setRevision((value) => value + 1), []);
  useEffect(() => {
    window.addEventListener("kionga:refresh", reload);
    return () => window.removeEventListener("kionga:refresh", reload);
  }, [reload]);
  useEffect(() => {
    if (!path) {
      setState({ path, loading: false });
      return;
    }
    const controller = new AbortController();
    setState((old) => ({
      path,
      data: old.path === path ? old.data : undefined,
      loading: true,
    }));
    api<T>(path, controller.signal)
      .then((data) => {
        if (!controller.signal.aborted)
          setState({ path, data, loading: false });
      })
      .catch((error) => {
        if (!controller.signal.aborted)
          setState({
            path,
            error: errorMessage(error),
            status: error instanceof APIError ? error.status : undefined,
            loading: false,
          });
      });
    return () => controller.abort();
  }, [path, revision]);
  return { ...state, reload, refetch: reload };
}

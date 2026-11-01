"use client";
import { createContext, useContext, useEffect } from "react";
export const UnsavedContext = createContext<(value: boolean) => void>(() => {});
export function useUnsaved(value: boolean) {
  const set = useContext(UnsavedContext);
  useEffect(() => {
    set(value);
    const before = (event: BeforeUnloadEvent) => {
      if (value) {
        event.preventDefault();
        event.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", before);
    return () => {
      set(false);
      window.removeEventListener("beforeunload", before);
    };
  }, [value, set]);
}

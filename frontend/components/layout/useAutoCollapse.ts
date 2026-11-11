"use client";
import { useCallback, useEffect, useRef, useState, type FocusEvent, type PointerEvent } from "react";

/**
 * Expands a panel while a mouse is over it or keyboard focus is inside it, and
 * collapses it after the pointer leaves. Short delays stop it flickering when the
 * cursor only sweeps past the edge. Touch input is ignored, so tapping a link on a
 * touch screen never opens and closes the panel underneath the finger.
 */
export function useAutoCollapse({ enabled, openDelay = 120, closeDelay = 300 }: {
  enabled: boolean;
  openDelay?: number;
  closeDelay?: number;
}) {
  const [expanded, setExpanded] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pointerInside = useRef(false);
  const focusInside = useRef(false);
  const held = useRef(false);

  const schedule = useCallback((open: boolean, delay: number) => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      timer.current = null;
      setExpanded(open);
    }, delay);
  }, []);

  const settle = useCallback(() => {
    const open = pointerInside.current || focusInside.current || held.current;
    schedule(open, open ? openDelay : closeDelay);
  }, [schedule, openDelay, closeDelay]);

  useEffect(() => () => { if (timer.current) clearTimeout(timer.current); }, []);
  // Pointer and focus are tracked even while disabled, so turning auto-collapse on
  // with the cursor or focus already inside keeps the panel open.
  useEffect(() => {
    if (timer.current) clearTimeout(timer.current);
    setExpanded(enabled && (pointerInside.current || focusInside.current || held.current));
  }, [enabled]);

  const handlers = {
    onPointerEnter(event: PointerEvent) {
      if (event.pointerType === "touch") return;
      pointerInside.current = true;
      if (enabled) settle();
    },
    onPointerLeave(event: PointerEvent) {
      if (event.pointerType === "touch") return;
      pointerInside.current = false;
      if (enabled) settle();
    },
    onFocus() {
      focusInside.current = true;
      if (!enabled) return;
      if (timer.current) clearTimeout(timer.current);
      setExpanded(true); // keyboard users see the labels at once
    },
    onBlur(event: FocusEvent) {
      const next = event.relatedTarget as Element | null;
      if (next && event.currentTarget.contains(next)) return; // focus moved within the panel
      focusInside.current = false;
      if (enabled) settle();
    },
  };

  /** Keep the panel open while something inside needs it, such as a resize drag. */
  const hold = useCallback((value: boolean) => {
    held.current = value;
    if (enabled) settle();
  }, [enabled, settle]);

  return { expanded: enabled && expanded, handlers, hold };
}

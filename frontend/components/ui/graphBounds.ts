/** A rectangle in graph coordinates. */
export type GraphRect = { x: number; y: number; width: number; height: number };

/**
 * Keeps a dragged node fully inside the visible area, so it can't be clipped out of
 * view when the pointer leaves the canvas. A node wider than the area stays at its
 * left or top edge, and the graph's origin always wins.
 */
export function clampToVisible(
  position: { x: number; y: number },
  size: { width: number; height: number },
  visible: GraphRect,
): { x: number; y: number } {
  const clamp = (value: number, start: number, room: number, length: number) =>
    Math.max(0, start, Math.min(value, start + Math.max(0, room - length)));
  return {
    x: clamp(position.x, visible.x, visible.width, size.width),
    y: clamp(position.y, visible.y, visible.height, size.height),
  };
}

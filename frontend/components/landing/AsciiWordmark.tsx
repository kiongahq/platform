"use client";
import { useEffect, useRef, useState } from "react";
const glyphs: Record<string, string[]> = {
  K: ["#   #", "#  # ", "# #  ", "##   ", "# #  ", "#  # ", "#   #"],
  I: ["#####", "  #  ", "  #  ", "  #  ", "  #  ", "  #  ", "#####"],
  O: [" ### ", "#   #", "#   #", "#   #", "#   #", "#   #", " ### "],
  N: ["#   #", "##  #", "##  #", "# # #", "#  ##", "#  ##", "#   #"],
  G: [" ### ", "#   #", "#    ", "# ###", "#   #", "#   #", " ### "],
  A: [" ### ", "#   #", "#   #", "#####", "#   #", "#   #", "#   #"],
};
export const WORDMARK_ROWS = Array.from({ length: 7 }, (_, row) =>
  "KIONGA"
    .split("")
    .map((letter) => glyphs[letter][row])
    .join("  "),
);
export function AsciiWordmark() {
  const canvas = useRef<HTMLCanvasElement>(null),
    [painted, setPainted] = useState(false);
  useEffect(() => {
    const element = canvas.current;
    if (!element || !window.CanvasRenderingContext2D) return;
    const ctx = element.getContext("2d");
    if (!ctx) return;
    const motion = window.matchMedia("(prefers-reduced-motion: reduce)");
    let width = 0, height = 0, footerHeight = 0, cellWidth = 6, cellHeight = 9;
    let accent = "", deep = "", font = "";
    let wordTop = 0, wordBottom = 0;
    let frame = 0, lastFrame = 0, elapsed = 0, visible = false, disposed = false;
    let points: { x: number; y: number; letter: boolean; seed: number }[] = [];

    function resize() {
      const bounds = element!.getBoundingClientRect();
      width = Math.round(bounds.width);
      height = Math.round(bounds.height);
      footerHeight = element!.parentElement!.getBoundingClientRect().height;
      if (!width || !height) return;
      const dpr = Math.min(2, window.devicePixelRatio || 1);
      element!.width = width * dpr;
      element!.height = height * dpr;
      ctx!.setTransform(dpr, 0, 0, dpr, 0, 0);
      const css = getComputedStyle(element!);
      accent = css.getPropertyValue("--color-accent").trim();
      deep = css.getPropertyValue("--color-accent-deep").trim();
      cellWidth = width < 600 ? 3 : 6;
      cellHeight = width < 600 ? 5 : 9;
      font = `${width < 600 ? 5 : 9}px ${css.fontFamily}`;
      const wordWidth = Math.min(width * 0.92, 1440);
      const wordHeight = Math.min(wordWidth * 0.28, footerHeight * 0.6);
      const left = (width - wordWidth) / 2;
      // Keep the utility links above the word, including the stacked mobile layout.
      const top = height - wordHeight - Math.max(24, footerHeight * 0.1);
      wordTop = top;
      wordBottom = top + wordHeight;
      points = [];
      for (let y = cellHeight / 2; y < height; y += cellHeight) {
        for (let x = cellWidth / 2; x < width; x += cellWidth) {
          const col = Math.floor((x - left) / wordWidth * WORDMARK_ROWS[0].length);
          const row = Math.floor((y - top) / wordHeight * WORDMARK_ROWS.length);
          const letter = row >= 0 && row < 7 && col >= 0 && col < WORDMARK_ROWS[0].length && WORDMARK_ROWS[row][col] === "#";
          const seed = Math.abs(Math.sin(x * 12.9898 + y * 78.233) * 43758.5453) % 1;
          points.push({ x, y, letter, seed });
        }
      }
      draw();
    }

    function draw() {
      if (!width || !height) return;
      const time = motion.matches ? 14 : elapsed;
      const fill = Math.min(1, time / 12);
      const surface = wordBottom - (wordBottom - wordTop + cellHeight * 3) * fill;
      ctx!.clearRect(0, 0, width, height);
      ctx!.font = font;
      ctx!.textAlign = "center";
      ctx!.textBaseline = "middle";
      for (const point of points) {
        const { x, y, letter, seed } = point;
        if (!letter && y < height - footerHeight) continue;
        const wave = Math.sin(x / width * 11 + y / height * 7 - time * 0.2);
        if (letter) {
          // Rain settles into fixed letter cells as the waterline rises.
          // Settled glyphs never scatter or replay when the footer re-enters view.
          const ripple = Math.sin(x / width * 16 + time * 0.35) * cellHeight;
          const settled = motion.matches ? 1 : Math.max(0, Math.min(1,
            (y - surface + ripple) / (cellHeight * 2)));
          const glyph = "KIONGA01#%@"[Math.floor(seed * 100) % 10];
          if (settled > 0) {
            ctx!.globalAlpha = settled * 0.85;
            ctx!.fillStyle = seed > 0.7 ? deep : accent;
            ctx!.fillText(glyph, x, y);
          } else if (!motion.matches && seed < 0.045 && fill < 1) {
            // Sparse vertical drops feed the rising surface from the rain above.
            const distance = Math.max(cellHeight, surface - (height - footerHeight));
            const fall = (time * 24 + seed * 12000) % distance;
            ctx!.globalAlpha = 0.12;
            ctx!.fillStyle = accent;
            ctx!.fillText(glyph, x, height - footerHeight + fall);
          }
        } else {
          const strand = Math.pow(Math.max(0, wave), 10);
          ctx!.globalAlpha = y < height - footerHeight ? 0 : 0.02 + strand * 0.08;
          ctx!.fillStyle = accent;
          ctx!.fillText(".:+*=~"[Math.floor(seed * 13 + time * 0.35) % 6], x, y);
        }
      }
      ctx!.globalAlpha = 1;
      setPainted(true);
    }

    function tick(now: number) {
      frame = 0;
      if (disposed || !visible || motion.matches || document.hidden) return;
      if (now - lastFrame >= 50) {
        elapsed += lastFrame ? Math.min((now - lastFrame) / 1000, 0.1) : 0;
        lastFrame = now;
        draw();
      }
      frame = requestAnimationFrame(tick);
    }
    function syncAnimation() {
      cancelAnimationFrame(frame);
      lastFrame = 0;
      draw();
      if (visible && !motion.matches && !document.hidden) frame = requestAnimationFrame(tick);
    }
    const observer = new ResizeObserver(resize);
    observer.observe(element);
    const intersection = new IntersectionObserver(([entry]) => {
      visible = entry.isIntersecting && entry.intersectionRatio >= 0.35;
      syncAnimation();
    }, { threshold: [0, 0.35] });
    intersection.observe(element.parentElement!);
    const theme = new MutationObserver(resize);
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme", "class"] });
    motion.addEventListener("change", syncAnimation);
    document.addEventListener("visibilitychange", syncAnimation);
    resize();
    document.fonts?.ready.then(() => { if (!disposed) resize(); });
    return () => {
      disposed = true;
      cancelAnimationFrame(frame);
      observer.disconnect();
      intersection.disconnect();
      theme.disconnect();
      motion.removeEventListener("change", syncAnimation);
      document.removeEventListener("visibilitychange", syncAnimation);
    };
  }, []);
  return (
    <div
      className={`ascii-wordmark${painted ? " ascii-wordmark-painted" : ""}`}
      role="img"
      aria-label="Kionga, drawn in ASCII characters"
    >
      <pre aria-hidden="true">{WORDMARK_ROWS.join("\n")}</pre>
      <canvas ref={canvas} aria-hidden="true" />
    </div>
  );
}

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
      const time = motion.matches ? 6 : elapsed;
      const bounds = element!.getBoundingClientRect();
      const visibleTop = Math.max(0, -bounds.top);
      const visibleBottom = Math.min(height, window.innerHeight - bounds.top);
      ctx!.clearRect(0, 0, width, height);
      ctx!.font = font;
      ctx!.textAlign = "center";
      ctx!.textBaseline = "middle";
      for (const point of points) {
        const { x, y, letter, seed } = point;
        if (!letter && y < height - footerHeight) continue;
        const wave = Math.sin(x / width * 11 + y / height * 7 - time * 0.65);
        if (letter) {
          // Eight origins cover the viewport's edges and corners. Curved streams
          // gather into a quiet nucleus before one restrained outward release.
          const gather = Math.max(0, Math.min(1, (time - seed * 0.35) / 1.65));
          const converge = gather * gather * (3 - 2 * gather);
          const progress = Math.max(0, Math.min(1, (time - 2.05 - seed * 0.2) / 1.65));
          const t = progress - 1;
          const ease = 1 + 1.45 * t * t * t + 0.45 * t * t;
          const shimmer = Math.sin(x * 0.012 - time * 0.9 + y * 0.018);
          ctx!.globalAlpha = (progress > 0 ? 0.3 + progress * 0.65 : 0.13 + converge * 0.12) * (0.9 + shimmer * 0.08);
          ctx!.fillStyle = shimmer > 0.45 ? deep : accent;
          const gatheredX = width / 2 + Math.sin(seed * 40) * width * 0.035;
          const gatheredY = height - footerHeight * 0.4 + Math.cos(seed * 31) * footerHeight * 0.045;
          const edge = Math.floor(seed * 8);
          const startX = edge === 1 || edge === 5 ? width * seed : edge >= 2 && edge <= 4 ? width + 24 : -24;
          const startY = edge <= 2 ? visibleTop - 24 : edge >= 4 && edge <= 6 ? visibleBottom + 24 : visibleTop + (visibleBottom - visibleTop) * seed;
          const curve = Math.sin(converge * Math.PI) * (seed - 0.5);
          const gatherX = startX + (gatheredX - startX) * converge + curve * width * 0.32
            + Math.sin(x * 1.7 + y * 0.6) * width * 0.025 * (1 - converge);
          const gatherY = startY + (gatheredY - startY) * converge - curve * footerHeight * 0.3
            + Math.cos(x * 0.9 + y * 1.3) * footerHeight * 0.12 * (1 - converge);
          const drawX = progress > 0 ? gatheredX + (x - gatheredX) * ease : gatherX;
          const drawY = progress > 0 ? gatheredY + (y - gatheredY) * ease : gatherY;
          const glyph = "KIONGA01#%@"[Math.floor(seed * 110) % 11];
          ctx!.fillText(glyph, drawX, drawY);
          if (gather > 0 && gather < 1 && seed < 0.18) {
            ctx!.globalAlpha *= 0.25;
            ctx!.fillText("·", drawX + (startX - drawX) * 0.025, drawY + (startY - drawY) * 0.025);
          }
        } else {
          const strand = Math.pow(Math.max(0, wave), 10);
          ctx!.globalAlpha = y < height - footerHeight ? 0 : 0.02 + strand * 0.08;
          ctx!.fillStyle = accent;
          ctx!.fillText(".:+*=~"[Math.floor(seed * 13 + time * 1.5) % 6], x, y);
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

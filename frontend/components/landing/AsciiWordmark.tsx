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
    let disposed = false;
    function paint() {
      const bounds = element!.getBoundingClientRect(),
        width = Math.round(bounds.width),
        height = Math.round(bounds.height);
      if (width < 1 || height < 1) return;
      const dpr = Math.min(2, devicePixelRatio || 1);
      element!.width = width * dpr;
      element!.height = height * dpr;
      ctx!.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx!.clearRect(0, 0, width, height);
      const css = getComputedStyle(element!),
        display = getComputedStyle(element!.parentElement!).fontFamily;
      const mask = document.createElement("canvas");
      mask.width = width;
      mask.height = height;
      const ink = mask.getContext("2d")!;
      let size = height * 1.1;
      ink.font = `600 ${size}px ${display}`;
      while (ink.measureText("Kionga").width > width * 0.98) {
        size *= 0.95;
        ink.font = `600 ${size}px ${display}`;
      }
      const measured = ink.measureText("Kionga");
      ink.fillStyle = css.getPropertyValue("--color-ink").trim();
      ink.textBaseline = "alphabetic";
      ink.fillText("Kionga", (width - measured.width) / 2, height * 0.78);
      const pixels = ink.getImageData(0, 0, width, height).data,
        cw = width < 600 ? 4 : 7,
        ch = width < 600 ? 7 : 12;
      const colors = [
        "--color-accent-deep",
        "--color-accent",
        "--color-glyph-4",
        "--color-ink",
      ].map((name) => css.getPropertyValue(name).trim());
      ctx!.font = `${width < 600 ? 7 : 12}px ${css.fontFamily}`;
      ctx!.textBaseline = "middle";
      for (let y = 0; y < height; y += ch)
        for (let x = 0; x < width; x += cw) {
          const sample =
            (Math.min(height - 1, y + Math.floor(ch / 2)) * width +
              Math.min(width - 1, x + Math.floor(cw / 2))) *
              4 +
            3;
          if (pixels[sample] < 90) continue;
          const level = Math.floor(x / cw + y / ch);
          ctx!.fillStyle = colors[Math.floor((x / width) * 5) % colors.length];
          ctx!.fillText(["#", "@", "%", "*"][level % 4], x, y + ch / 2);
        }
      setPainted(true);
    }
    const observer = new ResizeObserver(paint);
    observer.observe(element);
    const theme = new MutationObserver(paint);
    theme.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme", "class"],
    });
    paint();
    document.fonts?.ready.then(() => {
      if (!disposed) paint();
    });
    return () => {
      disposed = true;
      observer.disconnect();
      theme.disconnect();
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

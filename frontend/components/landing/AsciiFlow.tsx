"use client";
import { useEffect, useRef, useState } from "react";

/** A full-section illustration of project files becoming runs and artifacts. */
export function AsciiFlow() {
  const canvas = useRef<HTMLCanvasElement>(null);
  const [reduced, setReduced] = useState(false),
    [painted, setPainted] = useState(false);
  useEffect(() => {
    const element = canvas.current;
    if (!element || !window.CanvasRenderingContext2D) return;
    const ctx = element.getContext("2d");
    if (!ctx) return;
    const host = element.closest("section")!,
      copy = host.querySelector<HTMLElement>(".hero-copy");
    const motion = matchMedia("(prefers-reduced-motion: reduce)");
    setReduced(motion.matches);
    let width = 0,
      height = 0,
      cols = 0,
      rows = 0,
      frame = 0,
      last = 0,
      clock = 0,
      visible = true,
      disposed = false;
    const cellWidth = 8,
      cellHeight = 12,
      trailLifetime = 1200,
      trailRadius = 76;
    let quiet = new Float32Array(0),
      wake = new Float32Array(0);
    const trail: { x: number; y: number; time: number }[] = [];
    const pointer = { x: -1000, y: -1000 };
    const palette = () => {
      const css = getComputedStyle(element);
      const token = (name: string) => css.getPropertyValue(name).trim();
      return {
        tints: [
          "--color-glyph-1",
          "--color-glyph-2",
          "--color-glyph-3",
          "--color-glyph-4",
          "--color-accent",
        ].map(token),
        ink: token("--color-ink"),
        accent: token("--color-accent"),
        danger: token("--color-danger"),
        paper: token("--color-paper"),
        font: css.fontFamily,
      };
    };
    let colors = palette();
    const incoming = [
      "data/events.csv",
      "notebook.ipynb",
      "train.py",
      "pipeline.yaml",
      "model/v1",
      "agent.graph",
    ];
    const outgoing = [
      "dataset / features",
      "run / train",
      "model / version",
      "run / evaluate",
      "endpoint / predict",
      "agent / version",
    ];
    function laneY(lane: number, x: number) {
      const gate = width * 0.61,
        blend = Math.min(1, Math.max(0, x / gate));
      const spread =
        Math.sin(x * 0.009 - clock * 0.24 + lane * 1.7) *
        (height * 0.075) *
        (1 - blend) ** 2;
      return height * (0.12 + lane * 0.15) + spread;
    }
    function draw() {
      ctx!.clearRect(0, 0, width, height);
      ctx!.font = `11px ${colors.font}`;
      ctx!.textBaseline = "middle";
      const now = performance.now();
      while (trail.length && now - trail[0].time > trailLifetime) trail.shift();
      wake.fill(0);
      for (const point of trail) {
        const strength = (1 - (now - point.time) / trailLifetime) ** 2;
        const left = Math.max(
          0,
          Math.floor((point.x - trailRadius) / cellWidth),
        );
        const right = Math.min(
          cols - 1,
          Math.ceil((point.x + trailRadius) / cellWidth),
        );
        const top = Math.max(
          0,
          Math.floor((point.y - trailRadius) / cellHeight),
        );
        const bottom = Math.min(
          rows - 1,
          Math.ceil((point.y + trailRadius) / cellHeight),
        );
        for (let row = top; row <= bottom; row++)
          for (let col = left; col <= right; col++) {
            const dx = col * cellWidth - point.x,
              dy = row * cellHeight + cellHeight / 2 - point.y;
            const distanceSquared = dx * dx + dy * dy;
            if (distanceSquared > trailRadius * trailRadius) continue;
            const index = row * cols + col;
            wake[index] = Math.max(
              wake[index],
              strength * Math.exp(-distanceSquared / 1800),
            );
          }
      }
      const ramp = [".", ":", "-", "=", "+", "*", "#", "%", "@"],
        gate = Math.round(cols * 0.61);
      for (let row = 0; row < rows; row++)
        for (let col = 0; col < cols; col++) {
          const calm = quiet[row * cols + col] ?? 1;
          if (calm < 0.025) continue;
          const x = col * cellWidth,
            y = row * cellHeight + cellHeight / 2;
          let density = 0;
          for (let lane = 0; lane < 6; lane++) {
            const proximity = Math.hypot(x - pointer.x, y - pointer.y);
            const bend =
              proximity < 150
                ? Math.sin(col * 0.08 + lane) * (1 - proximity / 150) * 22
                : 0;
            const distance = y - laneY(lane, x) - bend;
            const thickness = col < gate ? 4200 : 900;
            density = Math.max(
              density,
              Math.exp((-distance * distance) / thickness) *
                (0.7 + 0.3 * Math.sin(col * 0.055 - clock * 0.65 + lane)),
            );
          }
          // Fresh cursor positions become a dense ribbon; older positions fade in place.
          const energy = wake[row * cols + col];
          density =
            Math.min(1, Math.max(density, energy) + energy * 0.3) * calm;
          if (density < 0.08) continue;
          ctx!.fillStyle = colors.tints[Math.min(4, Math.floor(density * 5))];
          ctx!.fillText(
            col === gate ? "┊" : ramp[Math.min(8, Math.floor(density * 9))],
            x,
            y,
          );
        }
      // Packet labels are examples, not service telemetry. One illustrates a retry.
      for (let lane = 0; lane < 6; lane++) {
        const phase = (clock * 0.032 + lane * 0.163 + 0.06) % 1;
        const waiting = lane === 3 && phase > 0.63 && phase < 0.75;
        const progress =
          lane === 3
            ? phase < 0.63
              ? phase
              : phase < 0.75
                ? 0.63
                : 0.63 + (phase - 0.75) * 1.48
            : phase;
        const x = progress * (width + 190) - 95,
          y = laneY(lane, x);
        const col = Math.max(
            0,
            Math.min(cols - 1, Math.floor((x + 45) / cellWidth)),
          ),
          row = Math.max(0, Math.min(rows - 1, Math.floor(y / cellHeight)));
        if ((quiet[row * cols + col] ?? 0) < 0.9) continue;
        const label = waiting
          ? "evaluate / retry"
          : progress < 0.61
            ? incoming[lane]
            : outgoing[lane];
        const labelWidth = ctx!.measureText(label).width;
        ctx!.fillStyle = colors.paper;
        ctx!.fillRect(x - 6, y - 11, labelWidth + 12, 22);
        ctx!.fillStyle = waiting
          ? colors.danger
          : progress < 0.61
            ? colors.accent
            : colors.ink;
        ctx!.fillText(label, x, y);
      }
    }
    function tick(now: number) {
      if (disposed || motion.matches || !visible || document.hidden) {
        frame = 0;
        return;
      }
      if (now - last >= 48) {
        clock += Math.min(0.1, last ? (now - last) / 1000 : 0);
        last = now;
        draw();
      }
      frame = requestAnimationFrame(tick);
    }
    function resume() {
      cancelAnimationFrame(frame);
      last = 0;
      draw();
      if (!disposed && !motion.matches && visible && !document.hidden)
        frame = requestAnimationFrame(tick);
    }
    function resize() {
      const bounds = element!.getBoundingClientRect();
      width = bounds.width;
      height = bounds.height;
      const dpr = Math.min(2, window.devicePixelRatio || 1);
      element!.width = Math.round(width * dpr);
      element!.height = Math.round(height * dpr);
      ctx!.setTransform(dpr, 0, 0, dpr, 0, 0);
      cols = Math.ceil(width / cellWidth);
      rows = Math.ceil(height / cellHeight);
      quiet = new Float32Array(cols * rows).fill(1);
      wake = new Float32Array(cols * rows);
      trail.length = 0;
      const boxes = Array.from(copy?.children || [])
        .map((child) => child.getBoundingClientRect())
        .map((box) => ({
          left: box.left - bounds.left - 20,
          right: box.right - bounds.left + 20,
          top: box.top - bounds.top - 12,
          bottom: box.bottom - bounds.top + 12,
        }));
      for (let row = 0; row < rows; row++)
        for (let col = 0; col < cols; col++)
          for (const box of boxes) {
            const x = col * cellWidth,
              y = row * cellHeight;
            const distance = Math.hypot(
              Math.max(box.left - x, 0, x - box.right),
              Math.max(box.top - y, 0, y - box.bottom),
            );
            quiet[row * cols + col] = Math.min(
              quiet[row * cols + col],
              Math.min(1, distance / 56) ** 2,
            );
          }
      colors = palette();
      draw();
      setPainted(true);
    }
    const observer = new ResizeObserver(resize);
    observer.observe(element);
    if (copy) observer.observe(copy);
    const intersection = new IntersectionObserver((entries) => {
      visible = entries[0]?.isIntersecting ?? true;
      resume();
    });
    intersection.observe(element);
    const theme = new MutationObserver(() => {
      colors = palette();
      draw();
    });
    theme.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme", "class"],
    });
    const move = (event: PointerEvent) => {
      if (motion.matches || event.pointerType === "touch") return;
      const bounds = element!.getBoundingClientRect();
      const x = event.clientX - bounds.left,
        y = event.clientY - bounds.top;
      const previous = trail.at(-1),
        now = performance.now();
      const distance = previous
        ? Math.hypot(x - previous.x, y - previous.y)
        : 0;
      // Interpolate fast mouse movements so the wake remains continuous.
      const steps = Math.min(24, Math.max(1, Math.ceil(distance / 12)));
      for (let step = 1; step <= steps; step++) {
        const fraction = step / steps;
        trail.push({
          x: previous ? previous.x + (x - previous.x) * fraction : x,
          y: previous ? previous.y + (y - previous.y) * fraction : y,
          time: now,
        });
      }
      if (trail.length > 96) trail.splice(0, trail.length - 96);
      pointer.x = x;
      pointer.y = y;
    };
    const leave = () => {
      pointer.x = -1000;
      pointer.y = -1000;
    };
    const preference = () => {
      setReduced(motion.matches);
      trail.length = 0;
      pointer.x = pointer.y = -1000;
      resume();
    };
    host.addEventListener("pointermove", move);
    host.addEventListener("pointerleave", leave);
    document.addEventListener("visibilitychange", resume);
    motion.addEventListener("change", preference);
    resize();
    resume();
    document.fonts?.ready.then(() => {
      if (!disposed) resize();
    });
    return () => {
      disposed = true;
      cancelAnimationFrame(frame);
      observer.disconnect();
      intersection.disconnect();
      theme.disconnect();
      host.removeEventListener("pointermove", move);
      host.removeEventListener("pointerleave", leave);
      document.removeEventListener("visibilitychange", resume);
      motion.removeEventListener("change", preference);
    };
  }, []);
  return (
    <figure
      className={`ascii-flow ascii-section-field${painted ? " ascii-painted" : ""}`}
      aria-label="Illustrated project workflow"
      data-motion={reduced ? "reduced" : "playing"}
    >
      <div className="ascii-flow-field">
        <pre className="ascii-flow-fallback" aria-hidden="true">
          {
            "..::++##%%@@  files  ┊  runs / versions  @@%%##++::..\n..::++##%%@@         ┊                  @@%%##++::..\n..::++##%%@@         ┊                  @@%%##++::.."
          }
        </pre>
        <canvas ref={canvas} aria-hidden="true" />
      </div>
      <div className="ascii-flow-head">
        <span>Illustrated project flow</span>
      </div>
      <figcaption className="sr-only">
        Project files flow through training and evaluation into versioned models
        and agents. This is an illustration, not live platform activity.
      </figcaption>
    </figure>
  );
}

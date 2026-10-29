/* Sidebar width: drag the sidebar's right edge to resize it. Dragging narrower
 * than SNAP collapses it to icons; dragging a collapsed sidebar outwards
 * expands it again. Arrow keys resize from the keyboard (Shift for bigger
 * steps), Home collapses, End maximises, double-click resets. The width is a
 * presentation preference kept in localStorage; collapse state stays owned by
 * toggleSidebar() so the toggle button and its stored preference agree. */
(() => {
  const handle = document.querySelector("#sidebar-resizer");
  if (!handle) return;
  const DEFAULT = 240, MIN = 180, MAX = 360, SNAP = 120, COLLAPSED = 64, KEY = "kionga.sidebar.width";
  const root = document.documentElement;
  const collapsed = () => shell.classList.contains("sidebar-collapsed");
  const clamp = value => Math.min(MAX, Math.max(MIN, Math.round(value)));
  let width = DEFAULT;

  function sync() { handle.setAttribute("aria-valuenow", String(collapsed() ? COLLAPSED : width)); }
  function apply(next, save) {
    width = clamp(next);
    root.style.setProperty("--sidebar-w", `${width}px`);
    if (save) try { localStorage.setItem(KEY, String(width)); } catch { /* optional */ }
    sync();
  }
  function setCollapsed(value) {
    if (collapsed() !== value) toggleSidebar();
    sync();
  }
  function resizeTo(next) {
    if (next < SNAP) return setCollapsed(true);
    setCollapsed(false);
    apply(next, true);
  }

  // Without a stored width the stylesheet's breakpoint default applies.
  try {
    const stored = Number(localStorage.getItem(KEY));
    if (stored) apply(stored, false);
  } catch { /* storage unavailable */ }
  sync();

  handle.addEventListener("pointerdown", event => {
    if (event.button !== 0 || isMobile()) return;
    event.preventDefault();
    const left = document.querySelector("#sidebar").getBoundingClientRect().left;
    shell.classList.add("sidebar-resizing");
    handle.setPointerCapture?.(event.pointerId);
    const move = moveEvent => resizeTo(moveEvent.clientX - left);
    const end = () => {
      shell.classList.remove("sidebar-resizing");
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", end);
      handle.removeEventListener("pointercancel", end);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", end);
    handle.addEventListener("pointercancel", end);
  });

  handle.addEventListener("keydown", event => {
    const step = event.shiftKey ? 48 : 16;
    if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
      event.preventDefault();
      if (collapsed()) return event.key === "ArrowRight" && setCollapsed(false);
      if (event.key === "ArrowLeft" && width <= MIN) return setCollapsed(true);
      resizeTo(width + (event.key === "ArrowRight" ? step : -step));
    } else if (event.key === "Home") {
      event.preventDefault();
      setCollapsed(true);
    } else if (event.key === "End") {
      event.preventDefault();
      setCollapsed(false);
      apply(MAX, true);
    }
  });

  handle.addEventListener("dblclick", () => {
    setCollapsed(false);
    apply(DEFAULT, true);
  });

  // The toggle button can change collapse state too; keep the separator's value current.
  sidebarToggle.addEventListener("click", sync);
})();

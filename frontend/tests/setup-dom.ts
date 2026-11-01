import { JSDOM } from "jsdom";
export const dom = new JSDOM(
  '<!doctype html><html><head><meta name="description" content=""></head><body></body></html>',
  { url: "http://localhost" },
);
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  location: dom.window.location,
  history: dom.window.history,
  HTMLElement: dom.window.HTMLElement,
});
// Graph layout is verified in real-browser acceptance tests. Unit tests retain
// the graph DOM and callbacks without pretending JSDOM computes geometry.
class TestResizeObserver { observe() {} unobserve() {} disconnect() {} }
Object.assign(globalThis, {
  ResizeObserver: TestResizeObserver,
  getComputedStyle: dom.window.getComputedStyle.bind(dom.window),
  requestAnimationFrame: (callback: FrameRequestCallback) => setTimeout(()=>callback(performance.now()),16),
  cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
});

// React Flow refreshes port bounds after a registry change. JSDOM has no matrix API.
Object.defineProperty(dom.window,'DOMMatrixReadOnly',{value:class {m11=1;m22=1;},configurable:true});

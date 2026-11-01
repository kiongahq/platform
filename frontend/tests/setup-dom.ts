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

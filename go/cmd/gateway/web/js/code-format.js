/* Numbered, escaped code views and line gutters for editable YAML/JSON. */
(function (root) {
  const escape = value => String(value ?? "").replace(/[&<>"']/g, character => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[character]));
  const syncers = new WeakMap();

  function highlightedLine(line, language) {
    if (language === "yaml" && /^\s*#/.test(line)) return `<span class="code-comment">${escape(line)}</span>`;
    const pattern = language === "json" ? /^(\s*)("(?:[^"\\]|\\.)+")(\s*:\s*)(.*)$/ : /^(\s*(?:-\s+)?)([\w.-]+)(\s*:\s*)(.*)$/;
    const match = line.match(pattern);
    if (!match) return escape(line) || "&nbsp;";
    const value = match[4];
    const valueClass = /^(true|false|null|~|\d+(?:\.\d+)?)\s*(?:#.*)?$/.test(value) ? "code-literal" : /^['"]/.test(value) ? "code-string" : "";
    return `${escape(match[1])}<span class="code-key">${escape(match[2])}</span>${escape(match[3])}${valueClass ? `<span class="${valueClass}">${escape(value)}</span>` : escape(value)}`;
  }

  function render(source, language = "yaml") {
    const lines = String(source ?? "").replace(/\r\n?/g, "\n").split("\n");
    const label = language === "json" ? "JSON" : "YAML";
    return `<div class="code-view" role="region" tabindex="0" aria-label="${label}, ${lines.length} lines"><div class="code-view-lines">${lines.map((line, index) => `<div class="code-line"><span class="code-line-number" aria-hidden="true">${index + 1}</span><code>${highlightedLine(line, language)}</code></div>`).join("")}</div></div>`;
  }

  function enhance(textarea) {
    if (textarea.closest(".numbered-editor")) return;
    const wrapper = document.createElement("div");
    wrapper.className = "numbered-editor";
    const gutter = document.createElement("div");
    gutter.className = "editor-gutter";
    gutter.setAttribute("aria-hidden", "true");
    const surface = document.createElement("div");
    surface.className = "editor-surface";
    const mirror = document.createElement("pre");
    mirror.className = "editor-highlight";
    mirror.setAttribute("aria-hidden", "true");
    const language = textarea.classList.contains("yaml-editor") ? "yaml" : "json";
    textarea.setAttribute("wrap", "off");
    textarea.before(wrapper);
    wrapper.append(gutter, surface);
    surface.append(mirror, textarea);
    const sync = () => {
      const count = textarea.value.split("\n").length;
      if (Number(wrapper.dataset.lines) !== count) {
        gutter.innerHTML = Array.from({length: count}, (_, index) => `<span>${index + 1}</span>`).join("");
        wrapper.dataset.lines = String(count);
      }
      mirror.innerHTML = textarea.value.split("\n").map(line => highlightedLine(line, language)).join("\n") + "\n";
      gutter.scrollTop = textarea.scrollTop;
      mirror.scrollTop = textarea.scrollTop;
      mirror.scrollLeft = textarea.scrollLeft;
    };
    textarea.addEventListener("input", sync);
    textarea.addEventListener("scroll", sync);
    textarea.addEventListener("focus", sync);
    syncers.set(textarea, sync);
    sync();
  }

  function enhanceAll(container = document) {
    const selector = '.yaml-editor, .policy-editor, #definition-parameters, #submit-parameters-json, #predict-input, #function-payload';
    if (container.matches?.(selector)) enhance(container);
    container.querySelectorAll?.(selector).forEach(enhance);
  }

  root.KiongaCodeFormat = {render, enhance, enhanceAll, sync: textarea => syncers.get(textarea)?.()};
  if (typeof document !== "undefined") {
    enhanceAll();
    new MutationObserver(records => records.forEach(record => record.addedNodes.forEach(node => {
      if (node.nodeType === 1) enhanceAll(node);
    }))).observe(document.body, {childList: true, subtree: true});
  }
  if (typeof module !== "undefined") module.exports = {render, highlightedLine};
})(typeof window !== "undefined" ? window : globalThis);

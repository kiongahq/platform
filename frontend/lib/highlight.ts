/** Vendored, pinned grammars; a loading failure leaves readable plain code. */
export async function highlightCode(root: HTMLElement) {
  const blocks = root.querySelectorAll("pre:not(.numbered-code) code");
  if (!blocks.length) return;
  try {
    const coreURL = "/vendor/highlight/core.min.js";
    const { default: hljs } = await import(/* webpackIgnore: true */ coreURL);
    const names = [
      "bash",
      "dockerfile",
      "go",
      "ini",
      "javascript",
      "json",
      "plaintext",
      "python",
      "shell",
      "sql",
      "typescript",
      "xml",
      "yaml",
    ];
    await Promise.all(
      names.map(async (name) => {
        const url = `/vendor/highlight/languages/${name}.min.js`;
        const grammar = await import(/* webpackIgnore: true */ url);
        hljs.registerLanguage(name, grammar.default);
      }),
    );
    blocks.forEach((block) => {
      if (block.getAttribute("data-highlighted")) return;
      const language = block.className.match(/language-([\w+#-]+)/)?.[1];
      if (language && !hljs.getLanguage(language))
        block.classList.replace(`language-${language}`, "language-plaintext");
      hljs.highlightElement(block);
    });
  } catch {
    /* Optional decoration. */
  }
}

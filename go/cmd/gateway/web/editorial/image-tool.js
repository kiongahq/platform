/* KiongaImage: the Editor.js image block for the editorial workspace.
 *
 * Replaces @editorjs/image because publishing needs what that tool lacks:
 * required alt text, alignment, upload progress, and images that can only
 * come from our media endpoint (the stored block keeps a media_id, never a
 * free URL). Upload sources: file picker, drag-and-drop onto the block or
 * the editor, and paste (Editor.js routes dropped/pasted files to onPaste).
 *
 * config.uploader(file, onProgress) -> Promise<{media_id, url, alt}>
 */
(function (global) {
  "use strict";

  const ACCEPT = ["image/jpeg", "image/png", "image/webp", "image/gif"];
  const ICON = '<svg width="17" height="15" viewBox="0 0 336 276" aria-hidden="true"><path fill="currentColor" d="M291 150V79c0-19-15-34-34-34H79c-19 0-34 15-34 34v42l67-44 81 72 56-29 42 30zm0 52-43-30-56 30-81-67-66 39v23c0 19 15 34 34 34h178c17 0 31-13 34-29zM79 0h178c44 0 79 35 79 79v118c0 44-35 79-79 79H79c-44 0-79-35-79-79V79C0 35 35 0 79 0z"/></svg>';
  let counter = 0;

  class KiongaImage {
    static get toolbox() { return {title: "Image", icon: ICON}; }
    static get isReadOnlySupported() { return true; }
    static get enableLineBreaks() { return true; }
    static get pasteConfig() { return {files: {mimeTypes: ACCEPT}}; }

    constructor({data, config, readOnly}) {
      this.data = {media_id: "", alt: "", caption: "", alignment: "center", file: {}, ...(data || {})};
      this.config = config || {};
      this.readOnly = readOnly;
      this.wrapper = null;
      this.uid = `kimg-${++counter}`;
    }

    render() {
      this.wrapper = document.createElement("div");
      this.wrapper.className = "kimg";
      if (this.data.media_id) this.showImage(); else this.showUploader();
      return this.wrapper;
    }

    showUploader(message = "") {
      const input = `${this.uid}-file`;
      this.wrapper.innerHTML = `<div class="kimg-drop" data-kimg-drop><p><b>Add an image</b> — JPEG, PNG, WebP or GIF up to 10 MB.</p>
        <p class="field-help">Drop a file here, paste one, or choose a file.</p>
        <label class="button-link kimg-pick" for="${input}">Choose image…</label>
        <input class="sr-only" id="${input}" type="file" accept="${ACCEPT.join(",")}" data-kimg-file aria-label="Choose image to upload">
        <div class="kimg-progress" hidden><progress max="100" value="0" aria-label="Upload progress"></progress><span>0%</span></div>
        <p class="field-error" role="alert">${EditorialAPI.esc(message)}</p></div>`;
      if (this.readOnly) return;
      const drop = this.wrapper.querySelector("[data-kimg-drop]");
      this.wrapper.querySelector("[data-kimg-file]").addEventListener("change", event => { const file = event.target.files[0]; if (file) this.upload(file); });
      drop.addEventListener("dragover", event => { event.preventDefault(); drop.classList.add("over"); });
      drop.addEventListener("dragleave", () => drop.classList.remove("over"));
      drop.addEventListener("drop", event => {
        event.preventDefault(); event.stopPropagation(); drop.classList.remove("over");
        const file = event.dataTransfer.files[0]; if (file) this.upload(file);
      });
    }

    async upload(file) {
      if (!ACCEPT.includes(file.type)) { this.showUploader("Only JPEG, PNG, WebP and GIF images are accepted."); return; }
      if (file.size > 10 * 1024 * 1024) { this.showUploader("Images are limited to 10 MB."); return; }
      if (!this.wrapper.querySelector("[data-kimg-drop]")) this.showUploader();
      const progress = this.wrapper.querySelector(".kimg-progress");
      progress.hidden = false;
      const update = percent => { progress.querySelector("progress").value = percent; progress.querySelector("span").textContent = `${percent}%`; };
      try {
        const result = await this.config.uploader(file, update);
        this.data.media_id = result.media_id;
        this.data.file = {url: result.url};
        if (!this.data.alt && result.alt) this.data.alt = result.alt;
        this.showImage();
        this.wrapper.querySelector("[data-kimg-alt]")?.focus();
        this.wrapper.dispatchEvent(new CustomEvent("kimg:change", {bubbles: true}));
      } catch (error) {
        this.showUploader(error.message || "Upload failed.");
      }
    }

    showImage() {
      const esc = EditorialAPI.esc;
      const url = (this.data.file && this.data.file.url) || `/media/blog/${this.data.media_id}/960`;
      this.wrapper.className = `kimg kimg-${this.data.alignment || "center"}`;
      this.wrapper.innerHTML = `<figure class="kimg-figure"><img src="${esc(url)}" alt="${esc(this.data.alt)}" loading="lazy"></figure>
        <div class="kimg-fields">
          <label class="kimg-alt">Alt text <small>(required to publish — describe what the image shows)</small><input data-kimg-alt value="${esc(this.data.alt)}" ${this.readOnly ? "disabled" : ""} maxlength="300" placeholder="e.g. Diagram of the gateway routing a request to a model server"></label>
          <label>Caption<input data-kimg-caption value="${esc(this.plainCaption())}" ${this.readOnly ? "disabled" : ""} maxlength="300" placeholder="Optional caption"></label>
          <label>Alignment<select data-kimg-align ${this.readOnly ? "disabled" : ""}>
            <option value="center">Centered</option><option value="wide">Wide</option><option value="full">Full width</option></select></label>
        </div>`;
      const align = this.wrapper.querySelector("[data-kimg-align]");
      align.value = this.data.alignment || "center";
      this.markAlt();
      if (this.readOnly) return;
      for (const field of this.wrapper.querySelectorAll("input, select")) {
        // Keep Editor.js from treating Enter/Backspace in these fields as block commands.
        field.addEventListener("keydown", event => event.stopPropagation());
        field.addEventListener("paste", event => event.stopPropagation());
      }
      this.wrapper.querySelector("[data-kimg-alt]").addEventListener("input", event => { this.data.alt = event.target.value; this.markAlt(); this.wrapper.querySelector("img").alt = event.target.value; });
      this.wrapper.querySelector("[data-kimg-caption]").addEventListener("input", event => { this.data.caption = EditorialAPI.esc(event.target.value); });
      align.addEventListener("change", event => { this.data.alignment = event.target.value; this.wrapper.className = `kimg kimg-${event.target.value}`; this.wrapper.dispatchEvent(new CustomEvent("kimg:change", {bubbles: true})); });
      for (const field of this.wrapper.querySelectorAll("input")) field.addEventListener("input", () => this.wrapper.dispatchEvent(new CustomEvent("kimg:change", {bubbles: true})));
    }

    plainCaption() {
      const node = document.createElement("div");
      node.innerHTML = this.data.caption || "";
      return node.textContent;
    }

    markAlt() {
      const input = this.wrapper.querySelector("[data-kimg-alt]");
      if (!input) return;
      const missing = !String(this.data.alt || "").trim();
      input.setAttribute("aria-invalid", missing ? "true" : "false");
      this.wrapper.classList.toggle("kimg-missing-alt", missing);
    }

    onPaste(event) {
      if (event.type === "file" && event.detail && event.detail.file) {
        if (!this.wrapper) this.render();
        this.upload(event.detail.file);
      }
    }

    save() {
      return {media_id: this.data.media_id, alt: String(this.data.alt || "").trim(), caption: this.data.caption || "", alignment: this.data.alignment || "center", file: {url: (this.data.file && this.data.file.url) || ""}};
    }

    validate(saved) { return Boolean(saved.media_id); }
  }

  global.KiongaImage = KiongaImage;
})(window);

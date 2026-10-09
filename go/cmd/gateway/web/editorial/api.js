/* Editorial API client and small DOM helpers shared by the workspace. */
(function (global) {
  "use strict";

  const esc = value => String(value == null ? "" : value).replace(/[&<>"']/g, character => ({"&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"}[character]));
  const ROLE_LABELS = {author: "Author", editor: "Editor", editorial_admin: "Editorial admin"};
  const STATUS_LABELS = {draft: "Draft", in_review: "In review", scheduled: "Scheduled", published: "Published", archived: "Archived"};
  const ACTION_LABELS = {submit: "Submit for review", withdraw: "Withdraw from review", request_changes: "Request changes", publish: "Approve & publish", schedule: "Schedule", unpublish: "Unpublish", archive: "Archive", restore: "Restore from archive"};

  class APIError extends Error {
    constructor(status, body) {
      super((body && body.message) || `Request failed (${status})`);
      this.status = status;
      this.code = body && body.error;
      this.body = body || {};
      this.latest = body && body.latest;
    }
  }

  async function request(path, {method = "GET", body, headers = {}} = {}) {
    let response;
    try {
      response = await fetch(path, {
        method, credentials: "same-origin",
        headers: body === undefined ? headers : {"Content-Type": "application/json", ...headers},
        body: body === undefined ? undefined : JSON.stringify(body),
      });
    } catch (error) {
      error.offline = true;
      throw error;
    }
    if (response.status === 401) {
      // The login round-trip drops URL fragments; remember the route.
      try { sessionStorage.setItem("kionga.editorial.return", location.hash); } catch (_) { /* private mode */ }
      location.assign(`/auth/login?return_to=${encodeURIComponent(location.pathname + location.hash)}`);
      throw new APIError(401, {message: "Sign in to continue."});
    }
    if (response.status === 204) return null;
    const text = await response.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch (_) { data = {message: text}; }
    if (!response.ok) throw new APIError(response.status, data);
    return data;
  }

  /* Multipart upload with progress (fetch has no upload progress). */
  function upload(file, {alt = "", caption = "", attribution = "", onProgress = () => {}} = {}) {
    return new Promise((resolve, reject) => {
      const form = new FormData();
      form.append("file", file);
      form.append("alt", alt);
      form.append("caption", caption);
      form.append("attribution", attribution);
      const xhr = new XMLHttpRequest();
      xhr.open("POST", "/api/v1/editorial/media");
      xhr.withCredentials = true;
      xhr.upload.addEventListener("progress", event => { if (event.lengthComputable) onProgress(Math.round(event.loaded / event.total * 100)); });
      xhr.addEventListener("load", () => {
        let data = null;
        try { data = JSON.parse(xhr.responseText); } catch (_) { data = {message: xhr.responseText}; }
        if (xhr.status >= 200 && xhr.status < 300) { onProgress(100); resolve(data); } else reject(new APIError(xhr.status, data));
      });
      xhr.addEventListener("error", () => { const error = new Error("Network error while uploading"); error.offline = true; reject(error); });
      xhr.send(form);
    });
  }

  function timeAgo(value) {
    if (!value) return "";
    const date = new Date(value);
    const seconds = Math.round((Date.now() - date.getTime()) / 1000);
    if (seconds < 60) return "just now";
    if (seconds < 3600) return `${Math.round(seconds / 60)} min ago`;
    if (seconds < 86400) return `${Math.round(seconds / 3600)} h ago`;
    return date.toLocaleDateString(undefined, {year: "numeric", month: "short", day: "numeric"});
  }

  function toast(message, kind = "info") {
    const node = document.getElementById("ed-toast");
    if (!node) return;
    node.textContent = message;
    node.dataset.kind = kind;
    node.hidden = false;
    clearTimeout(toast.timer);
    toast.timer = setTimeout(() => { node.hidden = true; }, 4200);
  }

  function statusBadge(status) { return `<span class="status ${esc(status)}">${esc(STATUS_LABELS[status] || status)}</span>`; }

  global.EditorialAPI = {request, upload, APIError, esc, timeAgo, toast, statusBadge, ROLE_LABELS, STATUS_LABELS, ACTION_LABELS};
})(window);

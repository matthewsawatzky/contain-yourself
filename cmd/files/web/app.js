// Workspace file browser.
//
// Every value that comes from the filesystem is inserted with textContent or
// as an attribute, never as HTML. File names are attacker-controlled in the
// sense that matters here: whoever uses the workstation chooses them.

const rows = document.getElementById("rows");
const crumbs = document.getElementById("crumbs");
const alertBox = document.getElementById("alert");
const empty = document.getElementById("empty");
const viewer = document.getElementById("viewer");
const viewerBody = document.getElementById("viewer-body");
const viewerName = document.getElementById("viewer-name");
const viewerDownload = document.getElementById("viewer-download");

let current = "/";

const api = path => "api/" + path;
const fileURL = (p, disposition) =>
  api("file") + "?path=" + encodeURIComponent(p) +
  (disposition ? "&disposition=" + disposition : "");

function showError(message) {
  alertBox.textContent = message;
  alertBox.hidden = !message;
}

function formatSize(bytes) {
  if (!bytes) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

function formatTime(iso) {
  if (!iso) return "—";
  const date = new Date(iso);
  return Number.isNaN(date.valueOf())
    ? "—"
    : date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

const GLYPHS = { dir: "▸", image: "▣", video: "▶", audio: "♪", text: "≡", other: "•" };

function join(base, name) {
  return base === "/" ? "/" + name : base + "/" + name;
}

function renderCrumbs(path) {
  crumbs.replaceChildren();
  const parts = path.split("/").filter(Boolean);
  const root = document.createElement("a");
  root.textContent = "workspace";
  root.href = "#";
  root.addEventListener("click", event => { event.preventDefault(); load("/"); });
  crumbs.append(root);
  let walked = "";
  parts.forEach((part, index) => {
    walked = join(walked || "/", part);
    const sep = document.createElement("span");
    sep.className = "sep";
    sep.textContent = "›";
    crumbs.append(sep);
    if (index === parts.length - 1) {
      const here = document.createElement("span");
      here.className = "current";
      here.textContent = part;
      crumbs.append(here);
    } else {
      const link = document.createElement("a");
      const destination = walked;
      link.textContent = part;
      link.href = "#";
      link.addEventListener("click", event => { event.preventDefault(); load(destination); });
      crumbs.append(link);
    }
  });
}

function renderRows(listing) {
  rows.replaceChildren();
  empty.hidden = listing.entries.length > 0;

  if (listing.parent !== "") {
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.colSpan = 4;
    const up = document.createElement("span");
    up.className = "name";
    const glyph = document.createElement("span");
    glyph.className = "glyph dir";
    glyph.textContent = "↑";
    const label = document.createElement("span");
    label.textContent = "Up one level";
    up.append(glyph, label);
    up.addEventListener("click", () => load(listing.parent));
    td.append(up);
    tr.append(td);
    rows.append(tr);
  }

  for (const entry of listing.entries) {
    const fullPath = join(listing.path, entry.name);
    const tr = document.createElement("tr");

    const nameCell = document.createElement("td");
    const name = document.createElement("span");
    name.className = "name";
    const glyph = document.createElement("span");
    glyph.className = "glyph" + (entry.dir ? " dir" : "");
    glyph.textContent = GLYPHS[entry.kind] || GLYPHS.other;
    const label = document.createElement("span");
    label.textContent = entry.name;
    name.append(glyph, label);
    name.addEventListener("click", () =>
      entry.dir ? load(fullPath) : preview(entry, fullPath));
    nameCell.append(name);

    const sizeCell = document.createElement("td");
    sizeCell.className = "col-size";
    sizeCell.textContent = entry.dir ? "—" : formatSize(entry.size);

    const timeCell = document.createElement("td");
    timeCell.className = "col-time";
    timeCell.textContent = formatTime(entry.modified);

    const actionCell = document.createElement("td");
    actionCell.className = "col-act";
    const actions = document.createElement("span");
    actions.className = "row-actions";
    if (!entry.dir) {
      const download = document.createElement("a");
      download.className = "button";
      download.textContent = "Download";
      download.href = fileURL(fullPath, "attachment");
      download.setAttribute("download", entry.name);
      actions.append(download);
    }
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "remove";
    remove.textContent = "Delete";
    remove.addEventListener("click", () => destroy(entry, fullPath));
    actions.append(remove);
    actionCell.append(actions);

    tr.append(nameCell, sizeCell, timeCell, actionCell);
    rows.append(tr);
  }
}

async function load(path) {
  showError("");
  try {
    const response = await fetch(api("list") + "?path=" + encodeURIComponent(path));
    const body = await response.json();
    if (!response.ok) throw new Error(body.error || response.statusText);
    current = body.path;
    renderCrumbs(body.path);
    renderRows(body);
  } catch (error) {
    showError(`Could not open ${path}: ${error.message}`);
  }
}

function preview(entry, fullPath) {
  viewerBody.replaceChildren();
  viewerName.textContent = entry.name;
  viewerDownload.href = fileURL(fullPath, "attachment");
  viewerDownload.setAttribute("download", entry.name);

  if (entry.kind === "image") {
    const img = document.createElement("img");
    img.src = fileURL(fullPath);
    img.alt = entry.name;
    viewerBody.append(img);
  } else if (entry.kind === "video" || entry.kind === "audio") {
    const media = document.createElement("video");
    media.src = fileURL(fullPath);
    media.controls = true;
    media.preload = "metadata";
    viewerBody.append(media);
  } else if (entry.kind === "text") {
    const pre = document.createElement("pre");
    pre.textContent = "Loading…";
    viewerBody.append(pre);
    fetch(fileURL(fullPath))
      .then(r => r.text())
      // 2 MB is enough to read a log without hanging the tab on a huge file.
      .then(text => { pre.textContent = text.slice(0, 2 * 1024 * 1024); })
      .catch(error => { pre.textContent = `Could not read file: ${error.message}`; });
  } else {
    const note = document.createElement("p");
    note.textContent = "No preview for this file type. Use Download.";
    viewerBody.append(note);
  }
  viewer.hidden = false;
}

function closeViewer() {
  viewer.hidden = true;
  // Dropping the nodes stops any in-flight media download.
  viewerBody.replaceChildren();
}

async function destroy(entry, fullPath) {
  const what = entry.dir ? "folder" : "file";
  if (!window.confirm(`Delete the ${what} "${entry.name}"?`)) return;
  showError("");
  try {
    const response = await fetch(api("delete") + "?path=" + encodeURIComponent(fullPath),
      { method: "POST" });
    const body = await response.json();
    if (!response.ok) throw new Error(body.error || response.statusText);
    load(current);
  } catch (error) {
    showError(`Could not delete ${entry.name}: ${error.message}`);
  }
}

document.getElementById("new-folder").addEventListener("click", async () => {
  const name = window.prompt("New folder name");
  if (!name) return;
  showError("");
  try {
    const response = await fetch(
      api("mkdir") + "?path=" + encodeURIComponent(join(current, name)),
      { method: "POST" });
    const body = await response.json();
    if (!response.ok) throw new Error(body.error || response.statusText);
    load(current);
  } catch (error) {
    showError(`Could not create folder: ${error.message}`);
  }
});

document.getElementById("upload").addEventListener("change", async event => {
  const files = [...event.target.files];
  event.target.value = "";
  if (files.length === 0) return;
  showError("");
  const form = new FormData();
  for (const file of files) form.append("file", file);
  try {
    const response = await fetch(api("upload") + "?path=" + encodeURIComponent(current),
      { method: "POST", body: form });
    const body = await response.json();
    if (!response.ok) throw new Error(body.error || response.statusText);
    load(current);
  } catch (error) {
    showError(`Upload failed: ${error.message}`);
  }
});

document.getElementById("viewer-close").addEventListener("click", closeViewer);
document.addEventListener("keydown", event => {
  if (event.key === "Escape" && !viewer.hidden) closeViewer();
});

// Follow the controller's accent. App traffic is proxied on the controller's
// own origin, so this relative request reaches it; failure is not worth
// reporting, the built-in palette already works.
fetch("/api/v1/theme")
  .then(r => (r.ok ? r.json() : Promise.reject(new Error("unavailable"))))
  .then(({ theme }) => {
    const root = document.documentElement.style;
    root.setProperty("--accent", theme.accent);
    root.setProperty("--on-accent", theme.on_accent);
    root.setProperty("--bg", theme.background);
    root.setProperty("--panel", theme.surface);
    root.setProperty("--panel-soft", theme.surface_sunk);
    root.setProperty("--line", theme.line);
    root.setProperty("--line-strong", theme.line_strong);
    root.setProperty("--text", theme.text);
    root.setProperty("--muted", theme.muted);
  })
  .catch(() => {});

load("/");

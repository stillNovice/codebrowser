"use strict";

const $ = (id) => document.getElementById(id);
const state = { mode: "changes", path: null, changes: [], comments: [], diffRef: "worktree", config: {}, review: { ref: "" } };

// codebrowse's own metadata file — never a reviewable change.
const COMMENTS_FILE = ".codebrowse-review.jsonl";

// ---------- file viewer ----------


// ---------- data ----------

async function api(url, opts) {
  const r = await fetch(url, opts);
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || r.statusText);
  return r;
}

async function loadConfig() {
  state.config = await (await api("/api/config")).json();
  document.querySelector(".logo").textContent = "◧ codebrowse @" + (state.config.version || "dev");
  // Short repo name up front; full path on hover (title) for the demo-less days.
  const root = state.config.root || "";
  const short = root.split("/").filter(Boolean).pop() || root;
  const repoEl = $("repo");
  repoEl.textContent = short + (state.config.attachedSession ? "  ⚡ attached agent session" : "");
  repoEl.title = root; // hover reveals where codebrowse is actually pointed
  // Click toggles the full path inline — explicit visibility, no demo hiding.
  repoEl.onclick = () => {
    if (repoEl.dataset.full === "1") {
      repoEl.dataset.full = "";
      repoEl.textContent = short + (state.config.attachedSession ? "  ⚡ attached agent session" : "");
    } else {
      repoEl.dataset.full = "1";
      repoEl.innerHTML = "";
      const p = document.createElement("span");
      p.className = "full-path";
      p.textContent = root;
      repoEl.prepend(p);
    }
  };
  // Browser is a review surface; the only model that matters is the one
  // in the terminal pi session (reported by the codebrowse-inbox extension).
  $("model").textContent = "agent:";
}

let allFiles = [];
async function loadFiles() {
  allFiles = (await (await api("/api/tree")).json()) || [];
  renderFiles();
}

// ---------- file tree (IntelliJ-style) ----------

const ICONS = {
  java: ["\u2615", "ic-java"],   // ☕
  md: ["M\u2193", "ic-md"], xml: ["<>", "ic-xml"], gradle: ["G", "ic-gradle"],
  yml: ["Y", "ic-yaml"], yaml: ["Y", "ic-yaml"], json: ["{}", "ic-json"],
  go: ["Go", "ic-go"], py: ["Py", "ic-py"], js: ["JS", "ic-js"], ts: ["TS", "ic-ts"],
  sh: ["$_", "ic-sh"], sql: ["\u25a4", "ic-sql"], properties: ["=", "ic-cfg"],
  txt: ["\u2630", "ic-file"], lock: ["\u26bf", "ic-lock"],
  png: ["\u25a3", "ic-img"], jpg: ["\u25a3", "ic-img"], gif: ["\u25a3", "ic-img"], svg: ["\u25a3", "ic-img"],
};
const NAME_ICONS = {
  "pom.xml": ["P", "ic-pom"], "Dockerfile": ["\u2611", "ic-cfg"],
  ".gitignore": ["\u2298", "ic-lock"], ".env.example": ["=", "ic-cfg"],
};

function iconFor(name, isDir) {
  if (isDir) return ["\ud83d\udcc1", "ic-dir"]; // 📁
  if (NAME_ICONS[name]) return NAME_ICONS[name];
  const ext = name.includes(".") ? name.split(".").pop().toLowerCase() : "";
  return ICONS[ext] || ["\u25ab", "ic-file"];
}

// Build nested tree; compress single-child directory chains into one row
// (like IntelliJ's "compact packages").
function buildTree(paths) {
  const root = { name: "", dirs: new Map(), files: [] };
  for (const p of paths) {
    const parts = p.split("/");
    let node = root;
    for (let i = 0; i < parts.length - 1; i++) {
      if (!node.dirs.has(parts[i])) node.dirs.set(parts[i], { name: parts[i], dirs: new Map(), files: [], prefix: parts.slice(0, i + 1).join("/") });
      node = node.dirs.get(parts[i]);
    }
    node.files.push({ name: parts[parts.length - 1], path: p });
  }
  // compress single-child chains
  function compress(node) {
    for (const [k, d] of node.dirs) compress(d);
    // merge child dir into parent label when it's the ONLY child and has no files
    let merged = true;
    while (merged) {
      merged = false;
      for (const [k, d] of node.dirs) {
        if (d.files.length === 0 && d.dirs.size === 1) {
          const [ck, cd] = [...d.dirs.entries()][0];
          node.dirs.delete(k);
          node.dirs.set(d.name + "/" + cd.name, {
            name: d.name + "/" + cd.name, dirs: cd.dirs, files: cd.files, prefix: cd.prefix,
          });
          merged = true;
          break;
        }
      }
    }
  }
  compress(root);
  return root;
}

state.collapsed = new Set(JSON.parse(localStorage.getItem("cb.collapsed") || "[]"));

function renderFiles() {
  const q = $("file-search").value.trim().toLowerCase();
  const el = $("file-list");
  el.innerHTML = "";
  if (!allFiles.length) {
    const d = document.createElement("div");
    d.className = "empty-state";
    d.textContent = "no files";
    el.appendChild(d);
    return;
  }
  // Search mode: flat filtered list (icons, basename) — tree would hide matches.
  if (q) {
    for (const f of allFiles.filter(f => f.path.toLowerCase().includes(q)).slice(0, 5000)) {
      el.appendChild(fileRow(f, 0));
    }
    return;
  }
  const root = buildTree(allFiles.map(f => f.path));
  const render = (node, depth) => {
    const dirs = [...node.dirs.values()].sort((a, b) => a.name.localeCompare(b.name));
    const files = [...node.files].sort((a, b) => a.name.localeCompare(b.name));
    for (const d of dirs) {
      el.appendChild(dirRow(d, depth));
      render(d, depth + 1);
    }
    for (const f of files) el.appendChild(fileRow(f, depth));
  };
  render(root, 0);
}

function guides(depth) {
  const g = document.createElement("span");
  g.className = "guides";
  for (let i = 0; i < depth; i++) g.appendChild(document.createElement("i"));
  return g;
}

function dirRow(d, depth, rerender = renderFiles) {
  const row = document.createElement("div");
  const open = !state.collapsed.has(d.prefix);
  row.className = "trow dir" + (open ? " open" : "");
  row.appendChild(guides(depth));
  const tw = document.createElement("span");
  tw.className = "twisty static-folder";
  tw.textContent = "";
  const ic = document.createElement("span");
  const [glyph, cls] = iconFor(d.name, true);
  ic.className = "icon " + cls;
  ic.textContent = glyph;
  const nm = document.createElement("span");
  nm.className = "name";
  nm.textContent = d.name;
  row.appendChild(tw); row.appendChild(ic); row.appendChild(nm);
  row.title = d.prefix;
  row.setAttribute("aria-hidden", "true");
  return row;
}

function fileRow(f, depth) {
  if (!f.name) f.name = f.path.split("/").pop(); // /api/tree entries are {path}
  const row = document.createElement("div");
  row.className = "trow file" + (f.path === state.path ? " active" : "");
  row.appendChild(guides(depth));
  const tw = document.createElement("span");
  tw.className = "twisty";
  const ic = document.createElement("span");
  const [glyph, cls] = iconFor(f.name, false);
  ic.className = "icon " + cls;
  ic.textContent = glyph;
  const nm = document.createElement("span");
  nm.className = "name";
  nm.textContent = f.name;
  row.appendChild(tw); row.appendChild(ic); row.appendChild(nm);
  row.title = f.path; // full path on hover
  row.onclick = () => openFile(f.path);
  return row;
}

// ---------- left pane: search, wrap, resize ----------

$("file-search").addEventListener("input", () => {
  if (state.mode === "files") renderFiles();
  else loadChanges(); // changes list honours the same filter box
});

$("wrap-names").addEventListener("change", (e) => {
  $("file-list").classList.toggle("wrap", e.target.checked);
  localStorage.setItem("cb.wrap", e.target.checked ? "1" : "0");
});
if (localStorage.getItem("cb.wrap") === "1") {
  $("wrap-names").checked = true;
  $("file-list").classList.add("wrap");
}

{
  const resizer = $("left-resizer");
  const saved = localStorage.getItem("cb.leftw");
  if (saved) document.documentElement.style.setProperty("--left-w", Math.min(420, Math.max(140, parseInt(saved))) + "px");
  let dragging = false;
  resizer.addEventListener("mousedown", (e) => {
    dragging = true;
    resizer.classList.add("active");
    e.preventDefault();
  });
  window.addEventListener("mousemove", (e) => {
    if (!dragging) return;
    const w = Math.min(600, Math.max(140, e.clientX));
    document.documentElement.style.setProperty("--left-w", w + "px");
  });
  window.addEventListener("mouseup", () => {
    if (!dragging) return;
    dragging = false;
    resizer.classList.remove("active");
    const w = getComputedStyle(document.documentElement).getPropertyValue("--left-w");
    localStorage.setItem("cb.leftw", parseInt(w));
  });
}

{
  const resizer = $("right-resizer");
  const minCenter = 420;
  const minRight = 280;
  const maxRight = () => {
    const left = parseInt(getComputedStyle(document.documentElement).getPropertyValue("--left-w")) || 260;
    return Math.max(minRight, Math.min(720, window.innerWidth - left - 30 - minCenter));
  };
  const clampRight = (width) => Math.max(minRight, Math.min(maxRight(), width));
  const saved = localStorage.getItem("cb.rightw");
  if (saved) document.documentElement.style.setProperty("--right-w", clampRight(parseInt(saved)) + "px");
  let dragging = false;
  resizer.addEventListener("mousedown", (e) => {
    dragging = true;
    resizer.classList.add("active");
    e.preventDefault();
  });
  window.addEventListener("mousemove", (e) => {
    if (!dragging) return;
    const w = clampRight(window.innerWidth - e.clientX);
    document.documentElement.style.setProperty("--right-w", w + "px");
  });
  window.addEventListener("mouseup", () => {
    if (!dragging) return;
    dragging = false;
    resizer.classList.remove("active");
    const w = getComputedStyle(document.documentElement).getPropertyValue("--right-w");
    localStorage.setItem("cb.rightw", parseInt(w));
  });
  window.addEventListener("resize", () => {
    const current = parseInt(getComputedStyle(document.documentElement).getPropertyValue("--right-w")) || 360;
    const w = clampRight(current);
    document.documentElement.style.setProperty("--right-w", w + "px");
    localStorage.setItem("cb.rightw", w);
  });
}

async function openFile(path, preserveScroll = false) {
  const wrap = $("editor-wrap");
  const sl = preserveScroll ? wrap.scrollLeft : 0;
  const st = preserveScroll ? wrap.scrollTop : 0;
  state.path = path;
  state.fileAnchor = null;
  // Keep the left file tree selection synchronized with the open editor.
  renderFiles();
  if (!preserveScroll) { switchMode("files"); hideCommentDock(); }
  const text = await (await api("/api/file?path=" + encodeURIComponent(path))).text();
  const kind = richViewKind(path);
  const showRich = kind !== "";
  state.richKind = kind;
  $("md-view-buttons").classList.toggle("hidden", !showRich);
  $("md-view-label").classList.toggle("hidden", !showRich);
  $("md-view-label").textContent = kind === "html" ? "HTML" : "Markdown";
  state.mdSource = showRich ? text : "";
  state.mdView = false;
  showFile(text);
  if (preserveScroll) { wrap.scrollLeft = sl; wrap.scrollTop = st; }
  document.title = "codebrowse — " + path;
}

// rich view kinds: "markdown", "html", or "" (no rich view support).
function richViewKind(path) {
  if (/\.md$/i.test(path)) return "markdown";
  if (/\.html?$/i.test(path)) return "html";
  return "";
}

function renderHtml(text) {
  $("diff-view").classList.add("hidden");
  $("editor-wrap").classList.add("hidden");
  const view = $("markdown-view");
  view.classList.remove("hidden");
  view.innerHTML = text;
}

// Render Markdown with the vendored `marked` parser (full GFM) and highlight
// code blocks with `highlight.js`. Safe by default: raw HTML is escaped.
function renderMarkdown(text, target) {
  const embedded = !!target;
  if (!embedded) {
    $("diff-view").classList.add("hidden");
    $("editor-wrap").classList.add("hidden");
    $("markdown-view").classList.remove("hidden");
  }
  const view = target || $("markdown-view");
  let html = "";
  if (window.marked && typeof window.marked.parse === "function") {
    if (typeof window.marked.setOptions === "function") {
      window.marked.setOptions({ gfm: true, breaks: false });
    }
    const md = typeof marked.parse === "function" ? marked : window.marked;
    html = md.parse(text || "");
  } else {
    // Fallback: escape and let pre-wrap keep it readable.
    const esc = (text || "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    html = "<pre class=\"md-code\">" + esc + "</pre>";
  }
  view.innerHTML = html;
  // Syntax-highlight every fenced code block.
  if (window.hljs) {
    view.querySelectorAll("pre code").forEach((code) => {
      try { hljs.highlightElement(code); } catch (_) {}
    });
  }
  // Open external links in a new tab.
  view.querySelectorAll("a[href^='http']").forEach((a) => {
    a.target = "_blank";
    a.rel = "noopener noreferrer";
  });
}
$("markdown-view").addEventListener("mouseup", () => {
  const selection = window.getSelection();
  const text = selection ? selection.toString().trim() : "";
  if (text) showMarkdownPrompt(text);
});

function showFile(text) {
  $("markdown-view").classList.add("hidden");
  $("editor-wrap").classList.remove("hidden");
  const pre = $("editor");
  const gutter = $("editor-gutter");
  pre.textContent = text;
  gutter.innerHTML = "";
  // Extension-aware syntax highlighting (vendored highlight.js).
  if (window.hljs) {
    delete pre.dataset.highlighted;
    pre.className = "code hljs";
    try { hljs.highlightElement(pre); } catch (_) { /* unknown ext: plain text */ }
  }
  const n = text.split("\n").length;
  const frag = document.createDocumentFragment();
  for (let i = 1; i <= n; i++) {
    const g = document.createElement("div");
    g.className = "gline";
    g.textContent = i;
    g.onmousedown = (e) => {
      e.preventDefault(); // don't start text selection while dragging
      if (e.shiftKey && state.fileAnchor && state.fileAnchor !== i) {
        const a = Math.min(state.fileAnchor, i), b = Math.max(state.fileAnchor, i);
        showCommentDock({ file: state.path, lineNo: a, lineEnd: b, ref: "worktree" });
        markFileRange(a, b);
        return;
      }
      state.fileAnchor = i;
      state.dragLine = i;
      state.dragging = true;
      showCommentDock({ file: state.path, lineNo: i, lineEnd: 0, ref: "worktree" });
      markFileRange(i, i);
    };
    g.onmouseenter = () => {
      if (!state.dragging || state.dragLine === i) return;
      state.dragLine = i;
      const a = Math.min(state.fileAnchor, i), b = Math.max(state.fileAnchor, i);
      showCommentDock({ file: state.path, lineNo: a, lineEnd: b, ref: "worktree" });
      markFileRange(a, b);
    };
    frag.appendChild(g);
  }
  gutter.appendChild(frag);
}

document.addEventListener("mouseup", () => {
  state.dragging = false;
  if (state.diffDragging) {
    const selected = [...document.querySelectorAll("#diff-view .dl.commentable.selected")];
    const last = state.selectionEnd || selected[selected.length - 1];
    if (last && last._meta) startComment(last, {
      ...last._meta,
      lineNo: selected.length ? selected[0]._lineNo : last._lineNo,
      lineEnd: selected.length > 1 ? last._lineNo : 0
    });
  }
  state.diffDragging = false;
});

// ---------- symbol navigation (click identifier → callers) ----------
//
// Click a Go identifier in the editor: a popover lists every call site
// of that symbol ("callers"), each entry navigates to the file+line.
// Backed by GET /api/xref — a name-based AST index built server-side.
{
  const editor = $("editor");
  const wrap = $("editor-wrap");
  let pop = null;

  const hide = () => { if (pop) { pop.remove(); pop = null; } };
  document.addEventListener("mousedown", (e) => {
    if (pop && !pop.contains(e.target)) hide();
  });
  window.addEventListener("Escape", hide);

  async function showCallersPop(name, ev) {
    hide();
    pop = document.createElement("div");
    pop.id = "xref-pop";
    pop.innerHTML = '<div class="xref-head">callers of <b></b><span class="xref-close">×</span></div><div class="xref-body"></div>';
    pop.querySelector("b").textContent = name;
    pop.querySelector(".xref-close").onclick = hide;
    const body = pop.querySelector(".xref-body");
    body.textContent = "indexing…";
    // Position near the click, clamped into the editor viewport.
    const r = wrap.getBoundingClientRect();
    pop.style.left = Math.min(ev.clientX - r.left + 12, r.width - 340) + "px";
    pop.style.top = Math.min(ev.clientY - r.top + 12, r.height - 120) + "px";
    wrap.appendChild(pop);
    try {
      const d = await (await api("/api/xref?name=" + encodeURIComponent(name))).json();
      body.textContent = "";
      if (!d.callers.length) {
        body.innerHTML = '<div class="xref-empty">no call sites found</div>';
      } else {
        for (const c of d.callers) {
          const row = document.createElement("div");
          row.className = "xref-row";
          const short = c.caller ? c.caller : "(package level)";
          row.innerHTML = '<span class="xref-caller"></span><span class="xref-loc"></span>';
          row.querySelector(".xref-caller").textContent = short;
          row.querySelector(".xref-loc").textContent = c.file + ":" + c.line;
          row.onclick = () => { hide(); openFile(c.file, true).then(() => gotoLine(c.line)); };
          body.appendChild(row);
        }
      }
    } catch (e) {
      body.textContent = "lookup failed: " + e.message;
    }
  }

  editor.addEventListener("click", (ev) => {
    if (!state.path || !/\.go$/.test(state.path)) return;
    const sel = window.getSelection();
    if (sel && sel.toString().length) return; // text selected: comment flow wins
    const range = document.caretRangeFromPoint(ev.clientX, ev.clientY);
    if (!range || !range.startContainer) return;
    // Global caret offset in the editor's text → line & column.
    const pre = document.createRange();
    pre.selectNodeContents(editor);
    try { pre.setEnd(range.startContainer, range.startOffset); } catch (_) { return; }
    const before = pre.toString();
    const nl = before.lastIndexOf("\n");
    const line = nl < 0 ? 1 : before.slice(0, nl + 1).split("\n").length;
    const col = before.length - (nl + 1);
    const text = (editor.textContent || "").split("\n")[line - 1] || "";
    const word = wordAt(text, col);
    if (!word || /^(if|for|range|return|func|var|const|type|struct|interface|package|import|go|defer|switch|case|break|continue|map|chan|new|make|nil|true|false)$/.test(word)) return;
    showCallersPop(word, ev);
  });

  function caretColumn(range, ev) {
    try {
      const r = range.cloneRange();
      const pre = document.createRange();
      pre.selectNodeContents(editor);
      pre.setEnd(range.startContainer, range.startOffset);
      return pre.toString().length - (pre.toString().lastIndexOf("\n") + 1 >= 0
        ? pre.toString().lastIndexOf("\n") + 1 : 0);
    } catch (_) { return 0; }
  }

  function wordAt(text, col) {
    if (col > text.length) col = text.length;
    const before = text.slice(0, col), after = text.slice(col);
    const m1 = before.match(/[A-Za-z0-9_.]*$/), m2 = after.match(/^[A-Za-z0-9_]*/);
    let w = (m1 ? m1[0] : "") + (m2 ? m2[0] : "");
    // selector call: recv.method — prefer method after last dot
    const dot = w.lastIndexOf(".");
    if (dot >= 0) w = w.slice(dot + 1);
    return w || null;
  }
}

// Navigate the open editor to a 1-based line and flash it.
function gotoLine(n) {
  const wrap = $("editor-wrap");
  const g = document.querySelectorAll("#editor-gutter .gline")[n - 1];
  if (!g) return;
  g.scrollIntoView({ block: "center" });
  markFileRange(n, n);
  setTimeout(() => markFileRange(0, 0), 1200);
}

function markFileRange(a, b) {
  document.querySelectorAll("#editor-gutter .gline").forEach((g, idx) => {
    g.classList.toggle("selected", idx + 1 >= a && idx + 1 <= b);
  });
}

function styleDiscussButton(button) {
  const select = $("chat-model-select");
  const option = select && select.selectedOptions && select.selectedOptions[0];
  const source = ((option && (option.dataset.provider || option.textContent)) || state.config.terminalModel || "agent").toLowerCase();
  let badge = "✦", kind = "agent";
  if (source.includes("claude")) { badge = "C"; kind = "claude"; }
  else if (source.includes("codex") || source.includes("openai")) { badge = "⌘"; kind = "codex"; }
  else if (source === "pi" || source.includes("pi/")) { badge = "π"; kind = "pi"; }
  button.textContent = badge + "  discuss with agent";
  button.classList.add("agent-discuss", "agent-" + kind);
}

function showCommentDock(meta) {
  // Inline prompt form, inserted directly beneath the clicked line —
  // same interaction as diff-view comments. Positioned over the code;
  // scrolls with it (editor-wrap is the positioning context).
  hideCommentDock();
  const wrap = $("editor-wrap");
  const form = document.createElement("form");
  form.id = "inline-cform";
  const lineH = 19.5, padTop = 12;
  // Anchor below the last selected line so the prompt trails the drag end.
  const anchorLine = meta.lineEnd || meta.lineNo;
  form.style.top = (padTop + anchorLine * lineH) + "px";
  const loc = document.createElement("span");
  loc.className = "loc";
  loc.textContent = "L" + meta.lineNo +
    (meta.lineEnd && meta.lineEnd !== meta.lineNo ? "-" + meta.lineEnd : "");
  const input = document.createElement("textarea");
  input.placeholder = "Ask agent to change this… (Enter sends, Shift+Enter adds a line, Esc cancels)";
  form.appendChild(loc);
  form.appendChild(input);
  wrap.appendChild(form);
  input.focus();

  const save = async (kind) => {
    const t = input.value.trim();
    if (!t) return null;
    const lines = ($("editor").textContent || "").split("\n");
    const snippet = lines.slice(meta.lineNo - 1, meta.lineEnd || meta.lineNo).join("\n");
    const r = await api("/api/comments", { method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ file: meta.file, line: snippet.slice(0, 1000),
        lineNo: meta.lineNo, lineEnd: meta.lineEnd || 0, ref: meta.ref, text: t,
        kind: kind || "ask" }) });
    return r.ok ? await r.json() : null;
  };
  const finish = () => { hideCommentDock(); loadComments(); expandRight(); };
  const snippet = () => {
    const lines = ($("editor").textContent || "").split("\n");
    return lines.slice(meta.lineNo - 1, meta.lineEnd || meta.lineNo).join("\n");
  };
  // Stream this prompt directly to the agent chat (keep panel open).
  const directSend = () => {
    const t = input.value.trim();
    if (!t) return;
    hideCommentDock();
    chatSend(composePrompt({ ...meta, line: snippet() }, t));
  };
  // Enter sends straight to the agent chat. In review mode every prompt
  // lands with the agent; in build mode Enter also routes to the agent.
  form.onsubmit = (e) => { e.preventDefault(); directSend(); };

  // Action row. Every prompt action routes straight to the agent chat
  // (pi backend): Enter or "send to agent now". Collect/note remain
  // as annotations for the comment queue.
  const row = document.createElement("div");
  row.className = "cform-actions";
  if (state.config.backend === "pi") {
    const sendB = document.createElement("button");
    sendB.type = "button";
    sendB.className = "primary";
    sendB.textContent = "⇪ send to agent now";
    sendB.onclick = directSend;
    row.appendChild(sendB);
  } else {
    const collectB = document.createElement("button");
    collectB.type = "button";
    collectB.textContent = "collect for later";
    collectB.title = "Save as a review comment; push it from the panel when ready";
    collectB.onclick = async () => { if (await save()) finish(); };
    const noteB = document.createElement("button");
    noteB.type = "button";
    noteB.className = "note-btn";
    noteB.textContent = "✎ note";
    noteB.title = "Save as a review note — an annotation only, never sent to agent";
    noteB.onclick = async () => { if (await save("note")) finish(); };
    row.append(collectB, noteB);
  }
  form.appendChild(row);
  input.onkeydown = (e) => { if (e.key === "Escape") hideCommentDock(); };
}

document.addEventListener("input", (e) => {
  const input = e.target;
  if (input instanceof HTMLTextAreaElement && input.closest("#inline-cform, .comment-form, #md-prompt")) {
    input.style.height = "auto";
    input.style.height = Math.min(input.scrollHeight, 140) + "px";
  }
});

document.addEventListener("keydown", (e) => {
  const input = e.target;
  if (!(input instanceof HTMLTextAreaElement) || !input.closest("#inline-cform, .comment-form, #md-prompt")) return;
  if (e.key === "Escape") { hideCommentDock(); return; }
  if (e.key === "Enter" && !e.shiftKey) {
    e.preventDefault();
    input.closest("form")?.requestSubmit();
  }
});

// ---------- workspace content search (/ to focus, Esc to exit) ----------
// Two-tier filter box: plain text = filename filter (existing behavior);
// a leading slash switches the box into content search backed by
// GET /api/search (literal fast, ?re=1 regex via /…/ trailing slashes).
{
  const bar = $("content-search-bar");
  const label = $("content-search-label");
  const box = $("file-search");
  let searchSeq = 0;
  let contentSearch = false;

  const exitContentSearch = () => {
    if (!contentSearch) return;
    contentSearch = false;
    bar.classList.add("hidden");
    box.value = "";
    box.placeholder = "filter files…  (/ to search content)";
    if (state.mode === "files") renderFiles(); else loadChanges();
    box.focus();
  };
  $("content-search-close").onclick = exitContentSearch;

  box.addEventListener("keydown", (e) => {
    if (e.key === "/" && box.value === "" && !contentSearch) {
      e.preventDefault();
      contentSearch = true;
      bar.classList.remove("hidden");
      box.placeholder = "/query/ or /regex/  (Esc to exit)";
      return;
    }
    if (e.key === "Escape" && contentSearch) { e.preventDefault(); exitContentSearch(); }
  });

  const runContentSearch = async (q) => {
    const my = ++searchSeq;
    let re = "0", term = q;
    if (q.startsWith("/") && q.endsWith("/") && q.length > 2) {
      re = "1"; term = q.slice(1, -1);
    }
    label.textContent = "searching “" + term + "”…";
    try {
      const r = await api("/api/search?q=" + encodeURIComponent(term) + "&re=" + re);
      if (!r.ok) { label.textContent = "search failed"; return; }
      const data = await r.json();
      if (my !== searchSeq) return; // stale response
      label.textContent = data.matches.length
        ? "“" + term + "”: " + data.matches.length + " matches in " + data.files + " files" + (data.truncated ? " (truncated)" : "")
        : "“" + term + "”: no matches";
      const el = $("file-list");
      el.innerHTML = "";
      for (const m of data.matches) {
        const d = document.createElement("div");
        d.className = "change search-hit";
        d.title = m.path + ":" + m.lineNo;
        const where = document.createElement("span");
        where.className = "xy";
        where.textContent = m.path.split("/").pop() + ":" + m.lineNo;
        d.appendChild(where);
        d.appendChild(document.createTextNode(m.line));
        d.onclick = () => { switchMode("files"); openFile(m.path, false); };
        el.appendChild(d);
      }
    } catch (_) { label.textContent = "search failed"; }
  };

  // Wrap the existing input handlers: when in content mode, intercept.
  box.addEventListener("input", () => {
    if (!contentSearch) return; // original filter handler runs
    const q = box.value.trim();
    if (!q || q === "/") { $("content-search-close").click(); return; }
    clearTimeout(runContentSearch._t);
    runContentSearch._t = setTimeout(() => runContentSearch(q), 250);
  }, true); // capture: ours first, original filter is a no-op on empty results
}

function hideCommentDock() {
  const f = $("inline-cform");
  if (f) f.remove();
  const md = $("md-prompt");
  if (md) md.remove();
  markFileRange(0, -1);
}

function showMarkdownPrompt(selectedText) {
  const old = $("md-prompt");
  if (old) old.remove();
  const form = document.createElement("form");
  form.id = "md-prompt";
  const label = document.createElement("span");
  label.className = "md-prompt-selection";
  label.textContent = "selected Markdown";
  const input = document.createElement("textarea");
  input.placeholder = "Ask agent about this…";
  const row = document.createElement("div"); row.className = "cform-actions";
  const save = async (push) => {
    const text = input.value.trim(); if (!text) return;
    const r = await api("/api/comments", { method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ file: state.path, line: selectedText.slice(0, 2000), lineNo: 0, ref: state.diffRef || "worktree", text, kind: "ask" }) });
    if (!r.ok) return;
    const c = await r.json();
    if (push && c.id) await patchComment(c.id, "push");
    form.remove(); loadComments(); expandRight();
  };
  const discuss = document.createElement("button"); discuss.type = "button"; styleDiscussButton(discuss);
  discuss.onclick = () => { form.remove(); discussInChat({ file: state.path, lineNo: 0, ref: state.diffRef || "worktree", line: selectedText }, input.value.trim()); };
  const collect = document.createElement("button"); collect.type = "button"; collect.textContent = "collect"; collect.onclick = () => save(false);
  row.append(discuss, collect); form.append(label, input, row); $("markdown-view").appendChild(form); input.focus();
}

async function loadChanges() {
  const el = $("file-list");
  // Preserve scroll so opening a file mid-list doesn't yank the user to top.
  const scrollTop = el.scrollTop;
  el.innerHTML = "";
  if (state.review.ref) { await loadReviewChanges(el); el.scrollTop = scrollTop; return; }
  // Non-git dirs still work: the server falls back to listing all
  // files as untracked, so first-time writes show up here.
  const noGit = !state.config.isRepo;
  const prevPaths = new Set((state.changes || []).map(c => c.path));
  state.changes = ((await (await api("/api/status")).json()) || []).filter(c => !c.path.endsWith(COMMENTS_FILE));
  const commitBtn = $("commit-btn");
  const cleanTree = !state.changes.length;
  if (commitBtn) {
    commitBtn.textContent = cleanTree ? "⇧ Push Committed" :
      (state.commitMode === "push" ? "✓ commit & push" : "✓ commit");
    commitBtn.title = cleanTree ? "Push the latest committed changes" :
      (state.commitMode === "push" ? "Stage everything, commit, and push" : "Stage everything and commit");
  }
  // SSE said "something changed" — but git status alone can't detect edits
  // to an ALREADY-modified file (same xy+path fingerprint), nor comments
  // marked done. So: comments + open-file reload run on every event; only
  // the changes-mode auto-jump stays gated on the status fingerprint.
  const fingerprint = state.changes.map(c => c.xy + c.path).join("|");
  const changedTree = fingerprint !== state.treeFingerprint;
  state.treeFingerprint = fingerprint;
  if (state.liveRefresh) {
    const typing = document.activeElement &&
      (document.activeElement.tagName === "INPUT" || document.activeElement.tagName === "TEXTAREA");
    loadComments();
    if (state.mode === "files" && state.path && !typing && !$("inline-cform")) {
      reloadOpenFileInPlace();
    }
    if (state.mode === "changes" && changedTree) {
      const fresh = state.changes.filter(c => !prevPaths.has(c.path));
      // Never switch modes or interrupt reading: auto-open only happens
      // while the user is already in Changes mode and not typing.
      if (fresh.length && !typing) {
        state.liveRefresh = false; // avoid ping-pong loops
        openDiff(fresh[0].path, false, fresh[0].xy);
        return;
      }
      // Already viewing a diff for a file whose status changed? reload it
      // in place — scroll position, list focus and expansion state survive.
      if (state.diffPath && prevPaths.has(state.diffPath)) {
        const still = state.changes.find(c => c.path === state.diffPath);
        if (still) { reloadDiffInPlace(still.path, state.diffStaged, still.xy); return; }
      }
    } else if (state.mode === "changes" && state.diffPath && !typing) {
      // Status fingerprint unchanged, but content may have been edited in
      // place (same xy+path). px0-style quiet reload: only when text moved.
      reloadDiffInPlace(state.diffPath, state.diffStaged, state.diffRef === "staged" ? "M " : "M ");
    }
  }
  $("change-count").textContent = state.changes.length ? `(${state.changes.length})` : "";

  const addLabel = (t) => {
    const d = document.createElement("div");
    d.className = "section-label";
    d.textContent = t;
    el.appendChild(d);
  };
  const cq = $("file-search").value.trim().toLowerCase();
  const addChange = (c, staged) => {
    if (cq && !c.path.toLowerCase().includes(cq)) return;
    const d = document.createElement("div");
    d.className = "change" + (c.path === state.diffPath ? " active" : "");
    d.title = c.path;
    const xy = document.createElement("span");
    xy.className = "xy";
    xy.textContent = c.xy;
    d.appendChild(xy);
    d.appendChild(document.createTextNode(c.path));
    d.onclick = () => openDiff(c.path, staged, c.xy);
    el.appendChild(d);
  };

  const unstaged = state.changes.filter(c => c.xy[1] !== " " && c.xy !== "??" || c.xy === "??");
  const staged   = state.changes.filter(c => c.xy[0] !== " " && c.xy[0] !== "?");

  addLabel(noGit ? "Files (no git yet — all untracked)" : "Unstaged changes");
  if (!unstaged.length) {
    const d = document.createElement("div");
    d.className = "empty-state";
    d.textContent = "working tree clean";
    el.appendChild(d);
  }
  unstaged.forEach(c => addChange(c, false));

  if (staged.length) {
    addLabel("Staged changes");
    staged.forEach(c => addChange(c, true));
  }

  try {
    const commits = (await (await api("/api/commits")).json()) || [];
    if (commits && commits.length) {
      addLabel("Recent commits");
      for (const c of commits) {
        const d = document.createElement("div");
        d.className = "change";
        d.title = c.subject;
        const h = document.createElement("span");
        h.className = "commit-hash";
        h.textContent = c.hash;
        d.appendChild(h);
        d.appendChild(document.createTextNode(c.subject));
        d.onclick = () => openCommit(c.hash);
        el.appendChild(d);
      }
    }
  } catch (_) { /* no history yet */ }
  // Nothing open yet? Auto-select the first changed file so the diff
  // pane is never empty on entry.
  if (!state.diffPath) {
    const first = unstaged[0] || staged[0];
    if (first) openDiff(first.path, !!staged[0] && !unstaged[0], first.xy);
  }
  el.scrollTop = scrollTop;
}

// ---------- diff rendering ----------

// Review mode: files changed in base...ref (e.g. a PR branch vs master).
async function loadReviewChanges(el) {
  const q = $("file-search").value.trim().toLowerCase();
  const files = (await (await api("/api/changed-files?base=" +
    encodeURIComponent(state.review.base) + "&ref=" +
    encodeURIComponent(state.review.ref))).json()) || [];
  if (state.liveRefresh) loadComments();
  const lbl = document.createElement("div");
  lbl.className = "section-label";
  lbl.textContent = state.review.ref + " vs " + state.review.base;
  el.appendChild(lbl);
  const shown = files.filter(f => !q || f.path.toLowerCase().includes(q));
  if (!shown.length) {
    const d = document.createElement("div");
    d.className = "empty-state";
    d.textContent = "no differences";
    el.appendChild(d);
    return;
  }
  // Indented, collapsible tree (same renderer as Files mode).
  const xyByPath = {};
  for (const f of shown) xyByPath[f.path] = f.xy;
  const root = buildTree(shown.map(f => f.path));
  const render = (node, depth) => {
    const dirs = [...node.dirs.values()].sort((a, b) => a.name.localeCompare(b.name));
    const files2 = [...node.files].sort((a, b) => a.name.localeCompare(b.name));
    for (const d of dirs) {
      el.appendChild(dirRow(d, depth, () => loadChanges()));
      render(d, depth + 1);
    }
    for (const f of files2) {
      const row = document.createElement("div");
      row.className = "change" + (f.path === state.diffPath ? " active" : "");
      row.title = f.path;
      row.appendChild(guides(depth));
      const xy = document.createElement("span");
      xy.className = "xy";
      xy.textContent = xyByPath[f.path] || "M ";
      row.appendChild(xy);
      row.appendChild(document.createTextNode(f.name));
      row.onclick = () => openDiff(f.path, false, xyByPath[f.path]);
      el.appendChild(row);
    }
  };
  render(root, 0);
  $("change-count").textContent = files.length ? `(${files.length})` : "";
}

// ---- quiet in-place reloads (px0-style, preserve scroll/focus) ----
// Refetch content behind the user's back and swap text only when it
// actually changed; scroll position, active row and expansion state
// survive. No mode switches, no list re-render, no highlight flash.

async function reloadOpenFileInPlace() {
  const path = state.path;
  try {
    const r = await api("/api/file?path=" + encodeURIComponent(path));
    if (!r.ok) return;
    const text = await r.text();
    if (path !== state.path || text === $("editor").textContent) return;
    // Single mutation point, scroll preserved (same-height content keeps
    // scrollTop valid; shrunken content clamps automatically by the browser).
    showFile(text);
  } catch (_) { /* transient — next SSE event retries */ }
}

async function reloadDiffInPlace(path, staged, xy) {
  try {
    let text;
    if (state.review.ref) {
      // Stacked review view: refetch the full base...ref diff.
      const url = "/api/diff?base=" + encodeURIComponent(state.review.base) +
        "&ref=" + encodeURIComponent(state.review.ref);
      text = await (await api(url)).text();
      stackedCache = { ref: state.review.ref, text };
    } else if (xy === "??") {
      const t = await (await api("/api/file?path=" + encodeURIComponent(path))).text();
      text = "new untracked file: " + path + "\n" +
        t.split("\n").map(l => "+" + l).join("\n");
    } else {
      text = await (await api("/api/diff?path=" + encodeURIComponent(path) +
        (staged ? "&staged=1" : ""))).text();
    }
    if (text === null || path !== state.diffPath) return;
    if (text === state.diffText || text === "") return; // nothing moved
    // Swap only the diff DOM; keep the viewport anchored by scroll ratio
    // (diff length may have changed — absolute scrollTop would drift).
    const el = $("diff-view");
    const ratio = el.scrollHeight > 0 ? el.scrollTop / el.scrollHeight : 0;
    renderDiff(text || "(no diff)");
    el.scrollTop = Math.round(ratio * el.scrollHeight);
  } catch (_) { /* transient — next SSE event retries */ }
}

function renderRichMarkdownDiff(text) {
  $("diff-view").classList.add("hidden");
  $("editor-wrap").classList.add("hidden");
  const view = $("markdown-view"); view.classList.remove("hidden"); view.innerHTML = "";
  const lines = text.split("\n"); let kind = "context", block = [];
  const flush = () => {
    if (!block.length) return;
    const section = document.createElement("section"); section.className = "md-diff-block md-diff-" + kind;
    renderMarkdown(block.join("\n"), section, true); view.appendChild(section); block = [];
  };
  for (const line of lines) {
    if (line.startsWith("@@")) { flush(); const h=document.createElement("div"); h.className="md-diff-hunk"; h.textContent=line; view.appendChild(h); kind="context"; continue; }
    if (line.startsWith("diff ") || line.startsWith("index ") || line.startsWith("--- ") || line.startsWith("+++ ")) continue;
    if (line.startsWith("+") && !line.startsWith("++")) { if (kind !== "add") { flush(); kind="add"; } block.push(line.slice(1)); continue; }
    if (line.startsWith("-") && !line.startsWith("--")) { if (kind !== "del") { flush(); kind="del"; } block.push(line.slice(1)); continue; }
    if (kind !== "context") { flush(); kind="context"; }
    block.push(line.startsWith(" ") ? line.slice(1) : line);
  }
  flush();
}

async function openDiff(path, staged, xy) {
  switchMode("changes");
  state.mdView = false;
  state.diffPath = path; state.diffStaged = staged;
  document.querySelectorAll("#file-list .change").forEach(row => row.classList.toggle("active", row.title === path));
  state.diffRef = staged ? "staged" : "worktree";
  if (state.review.ref) {
    state.diffRef = state.review.ref;
    // Review mode: render ALL changed files stacked in one scroll (GitHub
    // PR style). Clicking a file in the list scrolls to its section; the
    // section in view highlights the list entry.
    await renderAllDiffs();
    if (path) scrollToDiffSection(path);
    return;
  }
  const kind = richViewKind(path);
  const showRich = kind !== "";
  state.richKind = kind;
  $("md-view-buttons").classList.toggle("hidden", !showRich);
  $("md-view-label").classList.toggle("hidden", !showRich);
  $("md-view-label").textContent = kind === "html" ? "HTML" : "Markdown";
  // Untracked files have no diff — show the file content instead.
  if (xy === "??") {
    const text = await (await api("/api/file?path=" + encodeURIComponent(path))).text();
    renderDiff("new untracked file: " + path + "\n" +
      text.split("\n").map(l => "+" + l).join("\n"));
    return;
  }
  const diff = await (await api("/api/diff?path=" + encodeURIComponent(path) +
    (staged ? "&staged=1" : ""))).text();
  renderDiff(diff || "(no diff)");
}

// ---- stacked multi-file diffs (GitHub PR style continuous scroll) ----

// Fetch the full multi-file diff for the active ref and render every
// file's hunks sequentially inside #diff-view. Cached per ref.
let stackedCache = { ref: null, text: null };

async function renderAllDiffs() {
  const ref = state.review.ref || (state.diffStaged ? "staged" : "worktree");
  if (stackedCache.ref !== ref || stackedCache.text == null) {
    const url = state.review.ref
      ? "/api/diff?base=" + encodeURIComponent(state.review.base) + "&ref=" + encodeURIComponent(state.review.ref)
      : "/api/diff" + (state.diffStaged ? "?staged=1" : "");
    const text = await (await api(url)).text();
    stackedCache = { ref, text };
  }
  renderDiff(stackedCache.text || "(no changes)");
}

// Scroll #diff-view so the section for `path` sits at the top.
function scrollToDiffSection(path) {
  requestAnimationFrame(() => {
    const sec = document.querySelector('#diff-view [data-file-section="' + CSS.escape(path) + '"]');
    if (sec) $("diff-view").scrollTop = sec.offsetTop - 4;
  });
}

async function openCommit(hash) {
  switchMode("changes");
  state.diffRef = hash;
  const diff = await (await api("/api/diff?commit=" + encodeURIComponent(hash))).text();
  renderDiff(diff || "(empty commit)");
}

// GitHub-style batching for "expand hidden lines" controls.
const EXPAND_BATCH = 20;
let expandToken = 0;          // bumped per render; stale settles are dropped
let pendingDownRows = [];     // "down to EOF" rows waiting for a line count
const expandFileCache = new Map(); // ref\0path -> lines (cleared per render)

async function fetchLines(file) {
  const ref = state.diffRef === "worktree" || !state.diffRef ? "" : state.diffRef;
  const key = ref + "\u0000" + file;
  if (expandFileCache.has(key)) return expandFileCache.get(key);
  const url = "/api/file?path=" + encodeURIComponent(file) + (ref ? "&ref=" + encodeURIComponent(ref) : "");
  const text = await (await api(url)).text();
  const lines = text.split("\n");
  if (lines.length && lines[lines.length - 1] === "") lines.pop(); // trailing newline
  expandFileCache.set(key, lines);
  return lines;
}

function makeExpandedLine(hunk, ln, text) {
  const d = document.createElement("div");
  d.className = "dl ctx expanded";
  const go = document.createElement("span");
  go.className = "gutter"; go.textContent = hunk.startOld + (ln - hunk.startNew);
  const gn = document.createElement("span");
  gn.className = "gutter"; gn.textContent = ln;
  d.append(go, gn);
  d.appendChild(document.createTextNode(text || " "));
  d._lineNo = ln;
  d._meta = { file: hunk.file, lineNo: ln, ref: state.diffRef };
  return d;
}

function expandRowFor(hunk) {
  const row = document.createElement("div");
  row.className = "dl meta";
  const up = hunk.dir === "up";
  const btn = document.createElement("span");
  btn.className = up ? "expand-up" : "expand-down";
  btn.textContent = up ? "▴ expand" : "▾ expand";
  btn.title = hunk.count > 0 ? ("Expand " + hunk.count + " hidden line(s)") : "Expand hidden lines";
  btn.onclick = () => expandHunk(row, hunk);
  row.append(btn);
  row._hunk = hunk;
  return row;
}

// A trailing "▾ expand to EOF" row only makes sense when the file actually has
// lines past the last hunk; resolve those lazily once the file is fetched.
async function settleTrailingExpands(token) {
  const rows = pendingDownRows;
  pendingDownRows = [];
  for (const { row, hunk } of rows) {
    if (token !== expandToken || !row.isConnected) continue;
    try {
      const lines = await fetchLines(hunk.file);
      if (token !== expandToken || !row.isConnected) continue;
      const total = lines.length - (hunk.startNew - 1);
      if (total <= 0) { row.remove(); continue; }
      hunk.count = total;
      const btn = row.querySelector(".expand-down");
      if (btn) btn.title = "Expand " + total + " hidden line(s)";
    } catch (_) { row.remove(); }
  }
}

function renderDiff(text) {
  state.diffText = text;
  const el = $("diff-view");
  el.innerHTML = "";
  expandFileCache.clear();
  pendingDownRows = [];
  const token = ++expandToken;
  let curFile = "", newLine = 0, oldLine = 0, firstHunk = true;
  for (const line of text.split("\n")) {
    const d = document.createElement("div");
    let cls = "meta", gOld = "", gNew = "";
    let openFile = curFile;
    if (line.startsWith("+++ b/") || line.startsWith("+++ ")) {
      // File boundary: flush the previous file's trailing hidden lines (expand-down).
      if (openFile && newLine > 0) {
        insertExpandRow(el, { dir: "down", file: openFile, startOld: oldLine, startNew: newLine, count: 0 });
      }
      curFile = line.slice(6);
    }
    if (curFile !== openFile) {
      firstHunk = true; oldLine = 0; newLine = 0;
      // New file section in the stacked view: sticky header + anchor
      // for list-click scroll and scrollspy.
      if (curFile) {
        const head = document.createElement("div");
        head.className = "file-section-head";
        head.dataset.fileSection = curFile;
        head.textContent = curFile;
        el.appendChild(head);
      }
    }
    if (line.startsWith("@@")) {
      cls = "hunk";
      const m = line.match(/-(\d+)[^+]*\+(\d+)/);
      if (m) {
        const hOld = parseInt(m[1]), hNew = parseInt(m[2]);
        if (firstHunk && curFile) {
          // Hidden unchanged lines above the first hunk (expand-up).
          const up = hNew - 1;
          if (up > 0) insertExpandRow(el, { dir: "up", file: curFile, startOld: 1, startNew: 1, count: up });
        } else if (!firstHunk && curFile) {
          const gap = hNew - newLine; // unchanged lines hidden between hunks
          if (gap > 0) insertExpandRow(el, { file: curFile, startOld: oldLine, startNew: newLine, count: gap });
        }
        firstHunk = false;
        oldLine = hOld; newLine = hNew;
      }
    } else if (line.startsWith("+") && !line.startsWith("+++")) {
      cls = "add commentable"; gNew = newLine;
    } else if (line.startsWith("-") && !line.startsWith("---")) {
      cls = "del"; gOld = oldLine++;
    } else if (!line.startsWith("diff ") && !line.startsWith("index ") && !line.startsWith("---") && !line.startsWith("+++") && curFile) {
      cls = "ctx commentable"; gOld = oldLine++; gNew = newLine;
    }
    d.className = "dl " + cls;
    if (gOld !== "" || gNew !== "") {
      const go = document.createElement("span");
      go.className = "gutter";
      go.textContent = gOld;
      const gn = document.createElement("span");
      gn.className = "gutter";
      gn.textContent = gNew;
      d.append(go, gn);
    }
    d.appendChild(document.createTextNode(line || " "));
    if (cls.includes("commentable")) {
      const ln = cls.includes("add") ? newLine++ : (cls.includes("ctx") ? gNew : 0);
      if (cls.includes("ctx")) newLine++;
      const meta = { file: curFile, line: line.slice(1), lineNo: ln, ref: state.diffRef };
      const selectRange = (a, b, elAfter) => {
        const lines = [];
        let last = null;
        for (const el2 of document.querySelectorAll("#diff-view .dl.commentable")) {
          const n = el2._lineNo;
          el2.classList.toggle("selected", n >= a && n <= b);
          if (n >= a && n <= b) { lines.push(el2.textContent.slice(1)); last = el2; }
        }
        state.selectionEnd = elAfter || last || d;
        // While dragging, keep the prompt out of the diff until mouseup;
        // otherwise it overlays the next lines and stops the drag target.
        if (!state.diffDragging) {
          startComment(state.selectionEnd, { file: curFile, line: lines.join("\n"),
            lineNo: a, lineEnd: b, ref: state.diffRef });
        }
      };
      d.onmousedown = (e) => {
        if (e.button !== 0) return;
        if (e.shiftKey && state.selAnchor && state.selAnchor.file === curFile &&
            state.selAnchor.lineNo && ln && state.selAnchor.lineNo !== ln) {
          const a = Math.min(state.selAnchor.lineNo, ln), b = Math.max(state.selAnchor.lineNo, ln);
          selectRange(a, b, d);
          return;
        }
        e.preventDefault(); // no text-selection while dragging lines
        state.selAnchor = meta;
        state.diffDragging = true;
        selectRange(ln, ln, d);
      };
      d.onmouseenter = () => {
        if (!state.diffDragging || !state.selAnchor || state.selAnchor.file !== curFile) return;
        const a = Math.min(state.selAnchor.lineNo, ln), b = Math.max(state.selAnchor.lineNo, ln);
        selectRange(a, b, d);
      };
      d._lineNo = ln;
      d._meta = meta;
    } else if (line.startsWith(" ") || line === "") {
      if (curFile && newLine) newLine++;
    }
    el.appendChild(d);
  }
  // Last file's trailing hidden lines (no following +++ boundary).
  if (curFile && newLine > 0) {
    insertExpandRow(el, { dir: "down", file: curFile, startOld: oldLine, startNew: newLine, count: 0 });
  }
  settleTrailingExpands(token);
  initDiffScrollSpy();
}

// Scrollspy: while the stacked diff scrolls, keep the left Changes list
// highlighting the file currently in view (GitHub PR behavior).
let spyBound = false;
function initDiffScrollSpy() {
  const el = $("diff-view");
  if (!spyBound) {
    let raf = null;
    el.addEventListener("scroll", () => {
      if (raf) return; // coalesce to one rAF per frame
      raf = requestAnimationFrame(() => {
        raf = null;
        updateActiveSection();
      });
    });
    spyBound = true;
  }
  updateActiveSection();
}

function updateActiveSection() {
  if (!state.review.ref) return; // stacked scroll is review-mode only
  const el = $("diff-view");
  const heads = el.querySelectorAll("[data-file-section]");
  let current = null;
  const top = el.scrollTop + 8;
  for (const h of heads) {
    if (h.offsetTop <= top) current = h.dataset.fileSection;
    else break;
  }
  if (!current && heads.length) current = heads[0].dataset.fileSection;
  if (current) highlightListRow(current);
}

// Sync the Changes list active row without touching state.diffPath.
function highlightListRow(path) {
  document.querySelectorAll("#file-list .change").forEach(row => {
    row.classList.toggle("active", row.title === path);
  });
}

// GitHub-style "expand" row revealing unchanged lines the diff elided.
// `hunk.dir` is "up" (above first hunk) or "down" (between hunks / below last
// hunk). `hunk.count === 0` means "everything down to end of file".
function insertExpandRow(el, hunk) {
  const row = expandRowFor(hunk);
  if (hunk.count === 0 && hunk.dir === "down") pendingDownRows.push({ row, hunk });
  el.appendChild(row);
  return row;
}

// Reveal the next EXPAND_BATCH hidden lines, then park a fresh expand row on
// the still-hidden side (above for "up", below otherwise) — GitHub "load more".
async function expandHunk(row, hunk) {
  row.classList.add("loading");
  try {
    const lines = await fetchLines(hunk.file);
    const total = hunk.count > 0 ? hunk.count : Math.max(0, lines.length - (hunk.startNew - 1));
    if (total <= 0) { row.remove(); return; }
    const batch = Math.min(EXPAND_BATCH, total);
    let from, to; // 1-based inclusive new-file line range to reveal
    if (hunk.dir === "up") {
      // Hidden lines sit above the hunk: reveal the ones closest to it first.
      to = hunk.startNew + total - 1;
      from = to - batch + 1;
    } else {
      from = hunk.startNew;
      to = from + batch - 1;
    }
    if (to > lines.length) to = lines.length;
    if (from < 1) from = 1;
    const frag = document.createDocumentFragment();
    for (let ln = from; ln <= to; ln++) frag.appendChild(makeExpandedLine(hunk, ln, lines[ln - 1]));
    const remaining = total - (to - from + 1);
    if (remaining > 0) {
      const next = { dir: hunk.dir, file: hunk.file, count: remaining };
      if (hunk.dir === "up") {
        next.startOld = hunk.startOld; next.startNew = hunk.startNew;
      } else {
        next.startOld = hunk.startOld + (to - from + 1);
        next.startNew = to + 1;
      }
      const nrow = expandRowFor(next);
      if (hunk.dir === "up") row.replaceWith(nrow, frag);
      else row.replaceWith(frag, nrow);
    } else {
      row.replaceWith(frag);
    }
  } catch (_) {
    row.classList.remove("loading");
    row.title = "could not load file";
  }
}

// ---------- review comments ----------

async function patchComment(id, action) {
  await api("/api/comments", { method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ id, action }) });
}

async function loadComments() {
  const cs = (await (await api("/api/comments")).json()) || [];
  // Comments are scoped to the active review target. Historical comments from
  // other branches/worktrees must not inflate the current draft badge.
  const activeRef = state.review.ref || "worktree";
  const visible = cs.filter(c => (c.ref || "worktree") === activeRef);
  state.comments = visible;
  const open = visible.filter(c => !c.done);
  $("comment-count").textContent = open.length ? `(${open.length})` : "";
  $("comment-count-collapsed").textContent = open.length ? `${open.length} comments` : "comments";
  const el = $("comments-list");
  el.innerHTML = "";
  for (const c of visible) {
    const d = document.createElement("div");
    d.className = "comment" + (c.done ? " done" : "") + (c.kind === "note" ? " note" : "");
    const loc = document.createElement("div");
    loc.className = "loc";
    const range = c.lineEnd && c.lineEnd !== c.lineNo ? `-${c.lineEnd}` : "";
    loc.textContent = c.file + (c.lineNo ? ":" + c.lineNo + range : "") +
      (c.ref && c.ref !== "worktree" ? " @" + c.ref : "");
    if (c.kind === "note") {
      const nb = document.createElement("span");
      nb.className = "note-badge";
      nb.textContent = "✎ note";
      nb.title = "Review note — annotation only, never sent to agent";
      loc.appendChild(nb);
    }
    const snip = document.createElement("div");
    snip.className = "snippet";
    snip.textContent = c.line;
    const tx = document.createElement("div");
    tx.className = "ctext";
    tx.textContent = c.text;
    let wk = null;
    if (c.working && !c.done) { // pi picked it up, fix in flight
      wk = document.createElement("div");
      wk.className = "working-tag";
      wk.textContent = "● agent is working on it…";
    }
    if (c.done && c.reply) { // pi's summary of what it changed
      const rp = document.createElement("div");
      rp.className = "reply";
      rp.textContent = "⤷ " + c.reply;
      tx.appendChild(rp);
    }
    const ops = document.createElement("div");
    ops.className = "ops";
    // No manual done/reopen button: the terminal pi marks comments done
    // after addressing them; if one was closed wrongly, delete + re-add.
    if (c.done && !state.review.ref) { // revert is a working-tree tool
      const revB = document.createElement("button");
      revB.textContent = "⟲ revert change";
      revB.title = "Undo the change made for this comment (discards uncommitted edits, or reverts the agent's commit)";
      revB.onclick = async () => {
        if (!confirm("Revert the last change to " + c.file + "?")) return;
        const r = await api("/api/revert", { method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ file: c.file }) });
        if (!r.ok) { alert("Revert failed: " + await r.text()); return; }
        loadComments();
        loadChanges();
      };
      ops.appendChild(revB);
    }
    // Notes (PR feedback) are pushable in review mode; ask-comments always are.
    if (!c.done && !c.pushed && (c.kind !== "note" || state.review.ref)) {
      const pushB = document.createElement("button");
      pushB.textContent = c.kind === "note" ? "⇪ push to PR" : "⇪ push to agent";
      pushB.title = c.kind === "note"
        ? "Send to agent so it can post this as a PR review comment"
        : "Send this comment to the live agent session";
      pushB.onclick = async () => {
        await patchComment(c.id, "push");
        loadComments();
      };
      ops.appendChild(pushB);
    }
    if (c.pushed && !c.done) {
      const tag = document.createElement("span");
      tag.className = "pushed-tag";
      tag.textContent = "⇪ pushed";
      ops.appendChild(tag);
    }
    const delB = document.createElement("button");
    delB.textContent = "delete";
    delB.onclick = async () => {
      await api("/api/comments?id=" + encodeURIComponent(c.id), { method: "DELETE" });
      loadComments();
    };
    ops.appendChild(delB);
    if (wk) d.append(loc, snip, tx, wk, ops);
    else d.append(loc, snip, tx, ops);
    el.appendChild(d);
  }
  // Push-all bar: always visible so the affordance is discoverable
  const pending = open.filter(c => !c.pushed && (c.kind !== "note" || state.review.ref));
  const bar = document.createElement("div");
  bar.id = "push-all-bar";
  const b = document.createElement("button");
  b.textContent = pending.length
    ? `⇪ push all (${pending.length}) to terminal pi`
    : "⇪ push all to agent — add comments first (click a diff line)";
  b.disabled = !pending.length;
  b.onclick = async () => {
    b.disabled = true;
    for (const c of pending) await patchComment(c.id, "push");
    loadComments();
  };
  bar.appendChild(b);
  el.appendChild(bar);
}

function startComment(lineEl, meta) {
  document.querySelectorAll(".comment-form").forEach(f => f.remove());
  const f = document.createElement("form");
  f.className = "comment-form";
  const loc = document.createElement("span");
  loc.className = "loc";
  loc.textContent = "L" + meta.lineNo +
    (meta.lineEnd && meta.lineEnd !== meta.lineNo ? "-" + meta.lineEnd : "");
  const input = document.createElement("textarea");
  input.placeholder = "Ask agent to change this… (Enter sends, Shift+Enter adds a line, Esc cancels)";
  f.appendChild(loc);
  f.appendChild(input);
  lineEl.after(f);
  input.focus();

  const save = async (kind) => {
    const t = input.value.trim();
    if (!t) return null;
    const r = await api("/api/comments", { method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ file: meta.file, line: meta.line,
        lineNo: meta.lineNo, lineEnd: meta.lineEnd || 0,
        ref: meta.ref, text: t, kind: kind || "ask" }) });
    return r.ok ? await r.json() : null;
  };
  const finish = () => {
    lineEl.classList.add("commented");
    document.querySelectorAll("#diff-view .dl.selected").forEach(el2 => el2.classList.remove("selected"));
    f.remove();
    loadComments();
    expandRight();
  };
  f.addEventListener("submit", async (e) => { e.preventDefault(); if (await save()) finish(); });

  const row = document.createElement("div");
  row.className = "cform-actions";
  const sendB = document.createElement("button");
  sendB.type = "button";
  sendB.className = "primary";
  sendB.textContent = "⇪ send to agent now";
  sendB.onclick = async () => {
    const c = await save();
    if (c && c.id) await patchComment(c.id, "push");
    finish();
  };
  const collectB = document.createElement("button");
  collectB.type = "button";
  collectB.textContent = "collect for later";
  collectB.onclick = async () => { if (await save()) finish(); };
  const noteB = document.createElement("button");
  noteB.type = "button";
  noteB.className = "note-btn";
  noteB.textContent = "✎ note";
  noteB.title = "Save as a review note — an annotation only, never sent to agent";
  noteB.onclick = async () => { if (await save("note")) finish(); };
  if (state.config.backend === "pi") {
    const discB = document.createElement("button");
    discB.type = "button";
    discB.className = "discuss-btn";
    styleDiscussButton(discB);
    discB.title = "Send this straight to the agent chat";
    discB.onclick = () => { f.remove(); discussInChat(meta, input.value.trim()); };
    row.appendChild(discB);
  }
  row.append(collectB, noteB);
  f.appendChild(row);
  input.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      document.querySelectorAll("#diff-view .dl.selected").forEach(el2 => el2.classList.remove("selected"));
      f.remove();
    }
  });
}

// ---------- right panel collapse ----------

function expandRight() {
  document.body.classList.remove("right-closed");
  activatePanel("comments");
}

// One visible panel at a time: "chat" (agent) OR "comments".
function activatePanel(name) {
  document.body.classList.remove("panel-chat", "panel-comments");
  document.body.classList.add("panel-" + name);
  localStorage.setItem("cb.panel", name);
  document.querySelectorAll(".panel-tab").forEach(t => {
    t.classList.toggle("active", t.dataset.panel === name);
  });
}

function initPanelTabs() {
  const hasChat = state.config.backend === "pi";
  const chatTab = document.querySelector('.panel-tab[data-panel="chat"]');
  if (!hasChat) {
    chatTab.classList.add("hidden");
    $("chat-model-select").classList.add("hidden");
  }
  const saved = localStorage.getItem("cb.panel");
  let def = hasChat ? "chat" : "comments";
  if (saved === "chat" || saved === "comments") def = saved;
  if (!hasChat && def === "chat") def = "comments";
  activatePanel(def);
  document.querySelectorAll(".panel-tab").forEach(t => {
    t.onclick = () => activatePanel(t.dataset.panel);
  });
}

// ---------- mode tabs ----------

async function initReviewSelector() {
  if (!state.config.isRepo) return;
  const b = await (await api("/api/branches")).json();
  if (!b || !b.base) return; // no obvious base branch → no review mode
  state.review.base = b.base;
  const sel = $("review-sel");
  const mk = (v, label) => {
    const o = document.createElement("option");
    o.value = v; o.textContent = label;
    sel.appendChild(o);
  };
  mk("", "⟡ working tree");
  if (b.current) mk(b.current, "⑂ " + b.current + " vs " + b.base);
  for (const br of b.branches) {
    if (br === b.current || br === b.base || br.endsWith("/" + b.base)) continue;
    mk(br, "⑂ " + br + " vs " + b.base);
  }
  sel.onchange = () => {
    state.review.ref = sel.value;
    state.diffPath = null;
    document.body.classList.toggle("review-mode", !!sel.value);
    loadChanges();
    loadComments(); // re-render: revert buttons hidden in review mode
  };
}

function switchMode(mode) {
  state.mode = mode;
  $("review-target").classList.toggle("hidden", mode !== "changes");
  $("tab-files").classList.toggle("active", mode === "files");
  $("tab-changes").classList.toggle("active", mode === "changes");
  $("editor-wrap").classList.toggle("hidden", mode !== "files");
  $("markdown-view").classList.add("hidden");
  $("diff-view").classList.toggle("hidden", mode !== "changes");
  if (mode !== "files") hideCommentDock();
  if (mode === "files") loadFiles(); else loadChanges();
}
$("tab-files").onclick = () => switchMode("files");
$("tab-changes").onclick = () => switchMode("changes");
$("md-code-view").onclick = async () => {
  state.mdView = false;
  if (state.mode === "changes" && state.diffPath) await openDiff(state.diffPath, state.diffStaged, "M");
  else if (state.mode === "files" && state.path) showFile(state.mdSource || "");
};
$("md-code-view").hidden = false;
$("md-rich-view").onclick = async () => {
  state.mdView = true;
  if (state.richKind === "html") { renderHtml(state.mdSource || ""); return; }
  if (state.mode === "changes" && state.diffPath) renderRichMarkdownDiff(state.diffText || "");
  else if (state.mode === "files") renderMarkdown(state.mdSource || "");
};

// ---------- live refresh (server-sent events) ----------
// The server watches the working tree and pushes an event only when
// something actually changed — no browser-side polling timer.
{
  const es = new EventSource("/api/events");
  let debounce = null;
  es.onmessage = () => {
    clearTimeout(debounce);
    debounce = setTimeout(() => {
      state.liveRefresh = true;
      loadChanges().catch(() => {});
      // Server-side diff content may have changed (new commits): drop
      // the stacked multi-file diff cache so next render refetches.
      stackedCache = { ref: null, text: null };
      // A change event usually means the terminal pi is active — refresh
      // the reported terminal-model label too.
      api("/api/config").then(r => r.json()).then(cfg => {
        if (cfg.terminalModel) {
          $("model").textContent = "agent:";
        }
      }).catch(() => {});
    }, 300); // coalesce bursts (pi editing several files)
  };
}

// ---------- boot ----------

(async () => {
  try {
    await loadConfig();
    initPanelTabs();
    initChat().catch(() => {});
    await initReviewSelector();
    if (state.config.mode === "review") {
      // Review mode always starts at the working tree; picking a branch
      // is an explicit user action via the selector.
      document.body.classList.add("review-mode");
    }
    state.mode = "changes";
    switchMode(state.mode); // performs the single initial tree load
    await loadComments();
    if (state.config.openFile) openFile(state.config.openFile);
  } catch (e) {
    const b = $("err-banner");
    b.textContent = "boot failed: " + e.message;
    b.classList.remove("hidden");
  }
})();

// ---------- commit bar ----------
{
  const btn = $("commit-btn"), msg = $("commit-msg"),
        menuBtn = $("commit-menu-btn"), menu = $("commit-menu");
  state.commitMode = localStorage.getItem("cb.commitmode") || "commit"; // or "push"
  const applyMode = () => {
    btn.textContent = state.commitMode === "push" ? "✓ commit & push" : "✓ commit";
    btn.title = state.commitMode === "push"
      ? "Stage everything, commit, and push"
      : "Stage everything and commit";
  };
  applyMode();
  menuBtn.onclick = (e) => { e.stopPropagation(); menu.classList.toggle("hidden"); };
  document.addEventListener("click", () => menu.classList.add("hidden"));
  menu.querySelectorAll(".cm-item").forEach(it => {
    it.onclick = () => {
      state.commitMode = it.dataset.mode;
      localStorage.setItem("cb.commitmode", state.commitMode);
      applyMode();
      menu.classList.add("hidden");
    };
  });
  btn.onclick = async () => {
    if (!state.changes.length) {
      btn.disabled = true;
      const r = await api("/api/push", { method: "POST" });
      btn.disabled = false;
      if (!r.ok) { alert("Push failed: " + await r.text()); return; }
      btn.textContent = "✓ Pushed";
      btn.title = "Changes pushed successfully";
      return;
    }
    const m = msg.value.trim();
    if (!m) { msg.focus(); return; }
    const open = state.comments.filter(c => !c.done && c.pushed);
    if (open.length && !confirm(open.length + " pushed comment(s) not yet marked done. Commit anyway?")) return;
    btn.disabled = true;
    const r = await api("/api/commit", { method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message: m, push: state.commitMode === "push" }) });
    btn.disabled = false;
    if (!r.ok) { alert((r.status === 502 ? "" : "Commit failed: ") + await r.text()); return; }
    msg.value = "";
    // Clear the committed diff before refreshing status; otherwise the old
    // worktree/staged diff remains visible after the changes list is clean.
    state.diffPath = null;
    state.diffText = "";
    state.diffRef = "worktree";
    $("diff-view").innerHTML = "";
    $("md-view-buttons").classList.add("hidden");
    $("md-view-label").classList.add("hidden");
    loadChanges();
  };
  msg.addEventListener("keydown", (e) => { if (e.key === "Enter") btn.click(); });
}

// ---------- chat panel (attached pi session) ----------
const chatLog = $("chat-log"), chatInput = $("chat-input");

function setChatText(d, text) {
  let body = d.querySelector(".chat-text");
  if (!body) {
    body = document.createElement("div");
    body.className = "chat-text";
    d.appendChild(body);
  }
  d.dataset.fullText = text;
  // Render agent replies as Markdown (headings, lists, code fences), same
  // renderer the rich file view uses. Streaming re-renders are cheap at chat scale.
  renderMarkdown(text, body);
  let link = d.querySelector(".chat-full-link");
  if (text.length > 1200 && !link) {
    link = document.createElement("button");
    link.type = "button";
    link.className = "chat-full-link";
    link.textContent = "open full text ↗";
    link.onclick = () => {
      const blob = new Blob([d.dataset.fullText || ""], { type: "text/plain;charset=utf-8" });
      const url = URL.createObjectURL(blob);
      window.open(url, "_blank", "noopener");
      setTimeout(() => URL.revokeObjectURL(url), 60000);
    };
    d.appendChild(link);
  }
}

function chatBubble(role, text) {
  const d = document.createElement("div");
  d.className = "chat-msg " + role;
  const meta = document.createElement("div");
  meta.className = "chat-meta";
  meta.textContent = role === "user" ? "You" : "Agent";
  d.appendChild(meta);
  setChatText(d, text);
  chatLog.appendChild(d);
  chatLog.scrollTop = chatLog.scrollHeight;
  return d;
}

// Status line helper: shows agent activity (role, tool, working dots).
function setChatStatus(text, spinning) {
  const s = $("chat-status");
  if (!s) return;
  s.textContent = "";
  if (!text) { s.classList.remove("on"); return; }
  s.classList.add("on");
  if (spinning) {
    const dots = document.createElement("span");
    dots.className = "working-dots";
    dots.setAttribute("role", "img");
    dots.setAttribute("aria-label", "working");
    s.appendChild(dots);
  }
  s.appendChild(document.createTextNode(text));
}

async function initChatModels() {
  const select = $("chat-model-select");
  if (!select) return;
  try {
    const models = (await (await api("/api/models")).json()) || [];
    select.innerHTML = "";
    for (const m of models) {
      const opt = document.createElement("option"); opt.value = m.key; opt.dataset.provider = m.provider || ""; opt.textContent = m.name || m.key; select.appendChild(opt);
    }
    const preferred = localStorage.getItem("cb.chat-model") || state.config.terminalModel || state.config.defaultModel || "";
    if ([...select.options].some(o => o.value === preferred)) select.value = preferred;
    const selected = models.find(m => m.key === select.value);
    if (selected) $("model").textContent = "agent:";
    select.onchange = () => {
      localStorage.setItem("cb.chat-model", select.value);
      const selected = models.find(m => m.key === select.value);
      if (selected) $("model").textContent = "agent:";
    };
  } catch (_) { select.classList.add("hidden"); }
}

async function initChat() {
  if (state.config.backend !== "pi") return; // no attached session
  await initChatModels();
  try {
    const hist = (await (await api("/api/chat/history")).json()) || [];
    for (const m of hist.slice(-60)) chatBubble(m.role, m.text);
  } catch (e) { /* history is best-effort */ }
  $("chat-send").onclick = () => chatSend();
  $("chat-stop").onclick = () => fetch("/api/chat/stop", { method: "POST" });
  chatInput.addEventListener("input", () => {
    chatInput.style.height = "auto";
    chatInput.style.height = Math.min(chatInput.scrollHeight, 140) + "px";
  });
  chatInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      chatSend();
    }
  });
}

async function chatSend(prefilled) {
  const text = (prefilled != null ? prefilled : chatInput.value).trim();
  if (!text) return;
  chatInput.value = "";
  chatInput.style.height = "auto";
  document.body.classList.remove("right-closed");
  activatePanel("chat");
  document.body.classList.add("chat-working");
  $("chat-send").disabled = true;
  $("chat-stop").classList.remove("hidden");
  setChatStatus("Agent is thinking…", true);
  chatBubble("user", text);
  const asst = chatBubble("assistant", "");
  asst.classList.add("streaming");
  let acc = "";
  try {
    const r = await fetch("/api/chat", { method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model: (document.getElementById("chat-model-select") || {}).value || "", messages: [{ role: "user", content: text }] }) });
    if (!r.ok) {
      const msg = await r.text().catch(() => "");
      throw new Error(msg || ("HTTP " + r.status));
    }
    const reader = r.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      const parts = buf.split("\n\n");
      buf = parts.pop();
      for (const part of parts) {
        const line = part.split("\n").find(l => l.startsWith("data: "));
        if (!line) continue;
        const payload = line.slice(6);
        if (payload === "[DONE]") continue;
        try {
          const ev = JSON.parse(payload);
          if (ev.delta) {
            acc += ev.delta; setChatText(asst, acc);
            // Keep a capped agent bubble pinned to its latest content.
            asst.scrollTop = asst.scrollHeight;
          }
          else if (ev.tool) {
            if (ev.phase === "start") setChatStatus("⚙ " + ev.tool + (ev.summary ? " — " + ev.summary : ""), true);
            else if (ev.phase === "end") setChatStatus("Agent is thinking…", true);
          } else if (ev.error) {
            acc = "";
            setChatText(asst, ev.error.startsWith("⚠") ? ev.error : "⚠ " + ev.error);
          }
        } catch (e) { /* partial line */ }
        chatLog.scrollTop = chatLog.scrollHeight;
      }
    }
  } catch (e) {
    setChatText(asst, "⚠ " + e.message);
  }
  asst.classList.remove("streaming");
  if (!acc) {
    const existing = asst.querySelector(".chat-text");
    if (!existing || !existing.textContent.trim()) setChatText(asst, "■ stopped");
  }
  document.body.classList.remove("chat-working");
  setChatStatus("", false);
  $("chat-stop").classList.add("hidden");
  $("chat-send").disabled = false;
  chatInput.focus();
}

// Compose the contextual prompt used for agent-directed questions.
function composePrompt(meta, question) {
  return meta.file + ":" + meta.lineNo +
    (meta.lineEnd && meta.lineEnd !== meta.lineNo ? "-" + meta.lineEnd : "") +
    " (@" + meta.ref + ")\n```\n" + meta.line + "\n```\n\n" + question;
}

// Seed AND send the chat from a comment prompt ("discuss" button).
// The composed context + question go straight to the agent — no need
// to walk over to the chat panel and hit send.
function discussInChat(meta, question) {
  document.body.classList.remove("right-closed");
  activatePanel("chat");
  chatSend(composePrompt(meta, question));
}

// ---------- review bar: post all notes as one inline PR review ----------
{
  const btn = $("post-review-btn");
  const refreshBtn = () => {
    const notes = state.comments.filter(c => c.kind === "note" && !c.done && !c.pushed);
    btn.textContent = notes.length
      ? `⇪ Post review to PR (${notes.length} comment${notes.length > 1 ? "s" : ""})`
      : "⇪ Post review to PR";
    btn.disabled = !notes.length;
  };
  btn.onclick = async () => {
    const notes = state.comments.filter(c => c.kind === "note" && !c.done && !c.pushed);
    if (!notes.length) return;
    btn.disabled = true;
    for (const c of notes) await patchComment(c.id, "push");
    await loadComments();
    refreshBtn();
  };
  // keep the count fresh whenever comments reload
  const orig = loadComments;
  loadComments = async () => { await orig(); refreshBtn(); };
  refreshBtn();
}

// Stop only this codebrowse server; the repository and attached pi session remain intact.
$("close-codebrowse").onclick = async () => {
  if (!confirm("Close this codebrowse session? The repository and agent session will not be changed.")) return;
  const b = $("close-codebrowse");
  b.disabled = true;
  b.textContent = "closing…";
  try { await fetch("/api/shutdown", { method: "POST" }); } catch (_) {}
  document.body.innerHTML = '<div class="server-closed">codebrowse closed<br><small>You can close this browser tab.</small></div>';
};

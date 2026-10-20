"use strict";

const $ = id => document.getElementById(id);
const titles = {memory:"记忆图谱",executions:"执行记录",settings:"设置"};
const storageKey = "agent-host.workspace.v1";
let saved = {};
try { saved = JSON.parse(localStorage.getItem(storageKey)) || {}; } catch { /* Storage is optional. */ }
if (typeof saved !== "object" || Array.isArray(saved)) saved = {};
const draft = $("draft");
draft.value = typeof saved.draft === "string" ? saved.draft : "";
let clearedDraft = null;
function updateComposer() {
  const hasText = draft.value.length > 0;
  $("clear-draft").disabled = !hasText;
  $("save-draft").disabled = !draft.value.trim();
  $("undo-clear").hidden = clearedDraft === null;
  $("draft-count").textContent = `${Array.from(draft.value).length.toLocaleString()} 字`;
  draft.style.height = "auto";
  draft.style.height = `${draft.scrollHeight}px`;
}
function persist(message) {
  // Preserve dormant canvas layout and other saved values for a later return.
  try {
    localStorage.setItem(storageKey, JSON.stringify({...saved, draft:draft.value}));
    $("draft-status").textContent = message || "已自动保存";
    $("draft-status").dataset.state = "saved";
  } catch {
    $("draft-status").textContent = "无法保存，关闭页面前请复制草稿";
    $("draft-status").dataset.state = "error";
  }
  updateComposer();
}
function showChat(show) {
  $("chat-panel").hidden = !show;
  $("chat-toggle").setAttribute("aria-expanded", String(show));
  document.dispatchEvent(new Event("agh:chat-visibility"));
  if (show) updateComposer();
}
function navigate() {
  const requested = location.hash.slice(1).split("?")[0];
  const view = Object.hasOwn(titles, requested) ? requested : "memory";
  if (requested !== view) history.replaceState(null, "", `#${view}`);
  if (requested === "chat") showChat(true);
  document.querySelectorAll(".view").forEach(node => { node.hidden = node.id !== view; });
  document.querySelectorAll("[data-view]").forEach(node => {
    const active = node.dataset.view === view;
    node.classList.toggle("active", active);
    if (active) node.setAttribute("aria-current", "page"); else node.removeAttribute("aria-current");
  });
  $("view-title").textContent = titles[view];
  const frame = $(view).querySelector("iframe");
  if (!frame) return;
  const key = new URLSearchParams(location.hash.split("?")[1] || "").get("key");
  const src = frame.dataset.src + (view === "executions" && key ? `?key=${encodeURIComponent(key)}` : "");
  if (!frame.getAttribute("src") || view === "executions" && key && frame.dataset.key !== key) {
    frame.src = src;
    frame.dataset.key = key || "";
  }
}
for (const frame of document.querySelectorAll("iframe")) frame.addEventListener("load", () => {
  const doc = frame.contentDocument;
  if (!doc) return;
  const css = doc.createElement("link");
  css.rel = "stylesheet"; css.href = "/embedded.css"; doc.head.append(css);
  doc.addEventListener("click", event => {
    const anchor = event.target.closest("a[href]");
    if (!anchor) return;
    const url = new URL(anchor.href);
    if (url.origin !== location.origin) return;
    const routes = {"/":"memory","/index.html":"memory","/memory.html":"memory","/settings.html":"settings","/executions.html":"executions"};
    if (!routes[url.pathname]) return;
    event.preventDefault();
    location.hash = url.searchParams.has("key") ? `executions${url.search}` : url.hash || routes[url.pathname];
  });
});
$("chat-toggle").onclick = () => {
  const open = $("chat-panel").hidden;
  showChat(open);
  if (open) draft.focus();
};
$("panel-close").onclick = () => { showChat(false); $("chat-toggle").focus(); };
$("save-draft").onclick = () => {
  if (!draft.value.trim()) return;
  persist("草稿已保存");
  draft.focus();
};
$("clear-draft").onclick = () => {
  if (!draft.value) return;
  clearedDraft = draft.value;
  draft.value = "";
  persist("草稿已清空，可撤销");
  draft.focus();
};
$("undo-clear").onclick = () => {
  if (clearedDraft === null) return;
  draft.value = clearedDraft; clearedDraft = null;
  persist("已恢复草稿"); draft.focus();
};
draft.oninput = () => { clearedDraft = null; persist(); };
draft.onkeydown = event => {
  if (event.isComposing || event.repeat) return;
  if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key === "Enter") {
    event.preventDefault(); $("save-draft").onclick();
  }
};
$("chat-panel").addEventListener("keydown", event => {
  if (event.defaultPrevented || event.isComposing || event.key !== "Escape") return;
  event.preventDefault(); $("panel-close").onclick();
});
document.querySelectorAll("[data-prompt]").forEach(button => { button.onclick = () => {
  clearedDraft = null;
  draft.value += `${draft.value ? "\n\n" : ""}${button.dataset.prompt}`;
  persist(); showChat(true); draft.focus();
}; });
window.addEventListener("hashchange", navigate);
window.addEventListener("resize", updateComposer);
updateComposer();
if (matchMedia("(max-width:850px)").matches) showChat(false);
const legacyKey = new URLSearchParams(location.search).get("key");
if (legacyKey && !location.hash) location.hash = `#executions?key=${encodeURIComponent(legacyKey)}`;
navigate();

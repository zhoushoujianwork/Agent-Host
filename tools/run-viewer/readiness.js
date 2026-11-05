"use strict";
// Shared readiness notice. Pages pass the data they already fetched; this file
// never polls. It shows one plain-language summary, one primary action and the
// raw technical text inside a Details disclosure.
(function (root) {
  const MAX_DETAIL = 600;
  const CONNECT_EVENT = "agh:connect-host";
  const DRAFT_NOTE = "草稿会保留；完成设置后请手动发送。";
  // Shown when no native bridge exists. Names flags only, never config content.
  const LAUNCH_HELP = "Browser entry: restart the viewer with --backup-root ROOT (Host root for settings) and, when Chat uses a private handoff file other than ROOT/config/web-chat.json, --chat-config FILE. A config that belongs to another root is never adopted implicitly.";
  const array = value => Array.isArray(value) ? value : [];
  const clip = value => String(value ?? "").slice(0, MAX_DETAIL);
  const build = (state, summary, action, details = []) =>
    ({state, summary, action, details: details.filter(Boolean).map(clip)});
  const retry = {kind: "retry", label: "重试"};

  function environment(win = root) {
    const env = {embedded: false, bridge: false, settings: false};
    try { env.embedded = win.top !== win; } catch { env.embedded = true; }
    try { env.bridge = Boolean(win.top.webkit?.messageHandlers?.aghConnect); } catch { /* Cross-origin top: no bridge. */ }
    try { env.settings = win.document.body.dataset.page === "settings"; } catch { /* No document. */ }
    return env;
  }
  // The Settings page itself gets no Settings action, so the fallback cannot loop.
  const connectAction = env => env.bridge ? {kind: "connect", label: "设置本地 Agent"}
    : env.settings ? null : {kind: "settings", label: "前往设置"};

  function fromChat(snapshot, {env = environment(), failed = false, error = ""} = {}) {
    if (failed) return build("unavailable", `暂时无法读取 Chat 状态，正在重新连接。${DRAFT_NOTE}`, retry, [error]);
    if (snapshot?.enabled !== false) return build("ready", "", null);
    const action = connectAction(env);
    const summary = env.bridge ? "还不能发送消息：本地 Agent 尚未设置。" : "还不能发送消息：此 Host 尚未提供可用的 Chat 配置。";
    return build("missing", `${summary}${DRAFT_NOTE}`, action,
      [snapshot.notice, action?.kind === "connect" ? "" : LAUNCH_HELP]);
  }
  // A source that is initialized but has no runs is valid data, not a failure.
  function fromSources(index, {env = environment()} = {}) {
    const sources = array(index?.sources);
    const failed = sources.filter(source => source.status === "read_failed");
    if (failed.length) {
      const some = failed.length < sources.length;
      return build("unavailable", some ? "部分执行记录来源无法读取，下方列表可能不完整。" : "执行记录来源无法读取。", retry,
        failed.map(source => [source.channel || source.name, source.home, source.error].filter(Boolean).join(" · ")));
    }
    const missing = sources.filter(source => source.status === "missing_database");
    if (missing.length) return build("missing", missing.length === sources.length
      ? "尚未连接可读取的任务记录，请设置本地 Agent 或导入已有配置。" : "部分任务记录来源尚未初始化。", connectAction(env),
      missing.map(source => [source.channel || source.name, source.home].filter(Boolean).join(" · ")));
    const quiet = ["no_runs", "scope_filtered"];
    return sources.length && sources.every(source => quiet.includes(source.status)) ? build("empty", "", null) : build("ready", "", null);
  }
  // A request that failed while its page keeps showing older data (or none).
  function fromFailure(summary, error) { return build("unavailable", summary, retry, [error?.message ?? error]); }
  function fromStats(data, {error = ""} = {}) {
    if (error) return build("unavailable", "统计暂时无法读取。", retry, [error]);
    const sources = array(data?.sources);
    if (sources.length && !sources.some(source => source.status === "ok")) {
      return build("unavailable", "所有统计来源都无法读取，下方数值不代表真实执行量。", retry,
        sources.map(source => [source.name, source.status, source.reason].filter(Boolean).join(" · ")));
    }
    return build(sources.length && data?.totals?.runs === 0 ? "empty" : "ready", "", null);
  }
  function fromSettings(result, {env = environment()} = {}) {
    if (result?.enabled) return build("ready", "", null);
    const action = connectAction(env);
    return build("missing", env.bridge ? "本地 Agent 尚未设置，还不能管理智能体和模型。" : "当前是独立预览，尚未连接 Host，还不能管理智能体和模型。", action, [action?.kind === "connect" ? "" : LAUNCH_HELP]);
  }

  function connectHost(win = root) {
    const event = new win.CustomEvent(CONNECT_EVENT);
    try { win.top.document.dispatchEvent(event); }
    catch { win.parent.postMessage({type: CONNECT_EVENT}, win.location.origin); }
  }
  function openSettings(win = root) {
    const workspace = target => { try { return target.document.body.dataset.page === "workspace"; } catch { return false; } };
    if (workspace(win)) win.location.hash = "#settings";
    else if (win.top !== win && workspace(win.top)) win.top.location.hash = "#settings";
    else win.location.href = "/settings.html";
  }
  // Only the top-level page talks to the native shell. Frames reach it through
  // the event above, or a same-origin message when the document is unreachable.
  function installBridge(win = root) {
    if (win.top !== win) return;
    const post = () => win.webkit?.messageHandlers?.aghConnect?.postMessage({type: "connect-host"});
    win.document.addEventListener(CONNECT_EVENT, post);
    win.addEventListener("message", event => {
      if (event.origin === win.location.origin && event.source && event.source !== win && event.source.parent === win
        && event.data?.type === CONNECT_EVENT) post();
    });
  }

  const shown = new WeakMap();
  function clear(host) {
    if (!shown.has(host)) return;
    host.replaceChildren();
    shown.delete(host);
  }
  // Re-showing an identical model keeps the DOM, so an open Details stays open
  // across periodic refreshes.
  function show(host, model, handlers = {}, win = root) {
    if (!host) return false;
    if (!model || (model.state !== "missing" && model.state !== "unavailable")) { clear(host); return false; }
    const key = JSON.stringify(model), current = shown.get(host);
    if (current?.key === key && host.children[0] === current.element) return true;
    const doc = host.ownerDocument || win.document;
    const make = (tag, className, text) => {
      const node = doc.createElement(tag);
      node.className = className;
      if (text !== undefined) node.textContent = text;
      return node;
    };
    const element = make("div", "readiness");
    element.dataset.state = model.state;
    element.append(make("p", "readiness-summary", model.summary));
    if (model.action && (model.action.kind !== "retry" || handlers.retry)) {
      const button = make("button", "readiness-action", model.action.label);
      button.type = "button";
      button.onclick = async () => {
        button.disabled = true;
        try {
          if (model.action.kind === "connect") connectHost(win);
          else if (model.action.kind === "settings") openSettings(win);
          else await handlers.retry();
        } catch { /* The caller re-renders its own failure state. */ }
        finally { button.disabled = false; }
      };
      element.append(button);
    }
    if (model.details.length) {
      const details = make("details", "readiness-details");
      details.append(make("summary", "", "详情"), make("pre", "", model.details.join("\n")));
      element.append(details);
    }
    host.replaceChildren(element);
    shown.set(host, {key, element});
    return true;
  }

  const api = {environment, fromChat, fromFailure, fromSources, fromStats, fromSettings, show, connectHost, openSettings, installBridge, CONNECT_EVENT};
  if (typeof module !== "undefined") module.exports = api;
  if (root && root.document) { root.AGHReadiness = api; installBridge(root); }
})(typeof window !== "undefined" ? window : undefined);

"use strict";
const {test} = require("node:test"), assert = require("node:assert/strict"), vm = require("node:vm"), fs = require("node:fs"), path = require("node:path");
const R = require("./readiness.js");
const read = file => fs.readFileSync(path.join(__dirname, file), "utf8");
const browser = {embedded: false, bridge: false, settings: false};
const app = {...browser, bridge: true};

function element(tag = "div") {
  return {tag, className: "", textContent: "", dataset: {}, children: [], disabled: false,
    append(...nodes) { this.children.push(...nodes); },
    replaceChildren(...nodes) { this.children = nodes; }};
}
const doc = {createElement: element};
const host = () => ({...element(), ownerDocument: doc});
const text = node => [node.textContent, ...node.children.map(text)].join("\n");
const find = (node, className) => node.className === className ? node : node.children.map(child => find(child, className)).find(Boolean);

test("chat readiness: only a disabled Host is missing; configured empty chat is ready", () => {
  assert.equal(R.fromChat({enabled: true, turns: []}, {env: browser}).state, "ready");
  const missing = R.fromChat({enabled: false, notice: "尚未配置 Chat 执行环境"}, {env: browser});
  assert.equal(missing.state, "missing");
  assert.match(missing.summary, /草稿会保留/);
  assert.deepEqual(missing.action, {kind: "settings", label: "前往设置"});
  assert.ok(missing.details.some(line => line.includes("--chat-config")) && missing.details.some(line => line.includes("--backup-root")));
  assert.deepEqual(R.fromChat({enabled: false}, {env: app}).action, {kind: "connect", label: "设置本地 Agent"});
  assert.ok(!R.fromChat({enabled: false}, {env: app}).details.some(line => line.includes("--chat-config")));
});

test("chat poll failure offers Retry and keeps the raw error in Details", () => {
  const model = R.fromChat(null, {failed: true, error: "HTTP 500 /Users/me/.agh"});
  assert.equal(model.state, "unavailable");
  assert.equal(model.action.kind, "retry");
  assert.deepEqual(model.details, ["HTTP 500 /Users/me/.agh"]);
  assert.doesNotMatch(model.summary, /HTTP|\/Users/);
});

test("an initialized app-owned source with no runs is empty, not a missing database", () => {
  const initialized = R.fromSources({sources: [{status: "no_runs", home: "/support/runtime/cli-state"}]}, {env: app});
  assert.equal(initialized.state, "empty");
  assert.equal(R.show(host(), initialized), false);
  const absent = R.fromSources({sources: [{status: "missing_database", home: "/x"}]}, {env: app});
  assert.equal(absent.state, "missing");
  assert.match(absent.summary, /设置本地 Agent/);
  assert.equal(R.fromChat({enabled: false}, {env: app}).action.label, "设置本地 Agent");
});

test("record sources: unreadable is unavailable, configured empty sources are valid", () => {
  for (const status of ["no_runs", "scope_filtered"]) {
    assert.equal(R.fromSources({sources: [{status}]}).state, "empty", status);
  }
  assert.equal(R.fromSources({sources: [{status: "ready"}, {status: "no_runs"}]}).state, "ready");
  assert.equal(R.fromSources({sources: []}).state, "ready");
  const missing = R.fromSources({sources: [{status: "missing_database", home: "/private/state"}]}, {env: app});
  assert.equal(missing.state, "missing");
  assert.equal(missing.action.kind, "connect");
  assert.deepEqual(missing.details, ["/private/state"]);
  const mixed = R.fromSources({sources: [{status: "ready"}, {channel: "feishu", status: "read_failed", home: "/state/feishu", error: "locked"}]});
  assert.equal(mixed.state, "unavailable");
  assert.match(mixed.summary, /部分/);
  assert.deepEqual(mixed.details, ["feishu · /state/feishu · locked"]);
  assert.doesNotMatch(R.fromSources({sources: [{status: "read_failed", home: "/state", error: "x"}]}).summary, /部分|\/state/);
});

test("stats: all sources unreadable is unavailable; zero runs from readable sources is empty", () => {
  const down = R.fromStats({totals: {runs: 0}, sources: [{name: "cli", status: "unreadable", reason: "busy"}]});
  assert.equal(down.state, "unavailable");
  assert.deepEqual(down.details, ["cli · unreadable · busy"]);
  assert.equal(R.fromStats({totals: {runs: 0}, sources: [{name: "cli", status: "ok", runs: 0}]}).state, "empty");
  assert.equal(R.fromStats({totals: {runs: 3}, sources: [{name: "cli", status: "ok"}, {name: "x", status: "unreadable"}]}).state, "ready");
  const failed = R.fromStats(null, {error: "HTTP 500"});
  assert.equal(failed.state, "unavailable");
  assert.equal(failed.action.kind, "retry");
});

test("settings preview: bridge gives Connect, plain browser explains launch flags without a Settings loop", () => {
  assert.equal(R.fromSettings({enabled: true}, {env: app}).state, "ready");
  const native = R.fromSettings({enabled: false}, {env: {...app, settings: true}});
  assert.deepEqual(native.action, {kind: "connect", label: "设置本地 Agent"});
  assert.match(native.summary, /本地 Agent 尚未设置/);
  const standalone = R.fromSettings({enabled: false}, {env: {...browser, settings: true}});
  assert.equal(standalone.state, "missing");
  assert.equal(standalone.action, null);
  assert.ok(standalone.details.some(line => line.includes("--chat-config") && line.includes("--backup-root")));
  assert.match(standalone.details.join(), /never adopted implicitly/);
});

test("show renders one summary, one action and escaped Details; ready clears", () => {
  const slot = host(), retries = [];
  const model = R.fromFailure("读取失败。", "<img src=x onerror=bad()>");
  assert.equal(R.show(slot, model, {retry: () => retries.push(1)}), true);
  const section = slot.children[0];
  assert.equal(section.children.filter(child => child.tag === "button").length, 1);
  assert.equal(find(section, "readiness-summary").textContent, "读取失败。");
  assert.equal(find(section, "readiness-details").children[1].textContent, "<img src=x onerror=bad()>");
  assert.equal(section.innerHTML, undefined);
  find(section, "readiness-action").onclick();
  assert.deepEqual(retries, [1]);
  assert.equal(R.show(slot, R.fromSources({sources: [{status: "ready"}]})), false);
  assert.equal(slot.children.length, 0);
});

test("show keeps the DOM for an identical model so open Details survive refreshes", () => {
  const slot = host(), model = R.fromFailure("x", "y"), retry = {retry() {}};
  R.show(slot, model, retry);
  const first = slot.children[0];
  R.show(slot, R.fromFailure("x", "y"), retry);
  assert.equal(slot.children[0], first);
  R.show(slot, R.fromFailure("x", "z"), retry);
  assert.notEqual(slot.children[0], first);
});

test("Retry is omitted without a handler; Details are bounded", () => {
  const slot = host();
  R.show(slot, R.fromFailure("x", "e".repeat(5000)));
  assert.equal(find(slot.children[0], "readiness-action"), undefined);
  assert.equal(find(slot.children[0], "readiness-details").children[1].textContent.length, 600);
});

test("Connect dispatches agh:connect-host on the top document; Settings navigation avoids loops", () => {
  const events = [], hashes = [];
  class CustomEvent { constructor(type) { this.type = type; } }
  const top = {document: {body: {dataset: {page: "workspace"}}, dispatchEvent: event => events.push(event.type)}, location: {}};
  const frame = {CustomEvent, top, parent: top, location: {origin: "http://127.0.0.1:1", href: ""}, document: {body: {dataset: {page: "executions"}}}};
  R.connectHost(frame);
  assert.deepEqual(events, ["agh:connect-host"]);
  Object.defineProperty(top.location, "hash", {set: value => hashes.push(value), get: () => ""});
  R.openSettings(frame);
  assert.deepEqual(hashes, ["#settings"]);
  const settings = {top: null, location: {href: ""}, document: {body: {dataset: {page: "settings"}}}};
  settings.top = settings;
  R.openSettings(settings);
  assert.equal(settings.location.href, "/settings.html");
});

test("top-level bridge posts to the native handler for same-origin direct frames only", () => {
  const posted = [], listeners = {}, docListeners = {};
  const win = {location: {origin: "http://127.0.0.1:1"}, webkit: {messageHandlers: {aghConnect: {postMessage: value => posted.push(value)}}},
    document: {addEventListener: (type, fn) => { docListeners[type] = fn; }}, addEventListener: (type, fn) => { listeners[type] = fn; }};
  win.top = win;
  R.installBridge(win);
  docListeners["agh:connect-host"]();
  assert.equal(posted.length, 1);
  const child = {parent: win}, stranger = {parent: {}};
  listeners.message({origin: win.location.origin, source: child, data: {type: "agh:connect-host"}});
  assert.equal(posted.length, 2);
  listeners.message({origin: "https://evil.example", source: child, data: {type: "agh:connect-host"}});
  listeners.message({origin: win.location.origin, source: stranger, data: {type: "agh:connect-host"}});
  listeners.message({origin: win.location.origin, source: child, data: {type: "other"}});
  assert.equal(posted.length, 2);
  const inner = {top: {}, document: {addEventListener() { throw Error("frames must not install the native bridge"); }}};
  assert.doesNotThrow(() => R.installBridge(inner));
});

test("pages load the shared files and the server allowlist serves them", () => {
  for (const page of ["index.html", "executions.html", "stats.html", "settings.html"]) {
    const html = read(page);
    assert.match(html, /\/readiness\.js/, page);
    assert.match(html, /\/readiness\.css/, page);
    assert.match(html, /id="(chat-)?readiness"/, page);
  }
  assert.match(read("server.py"), /"\/readiness\.js", "\/readiness\.css"/);
});

test("chat renders readiness from its own snapshot, keeps the draft and never sends", async () => {
  const nodes = new Map(), calls = [], timers = [];
  const make = () => ({hidden: false, disabled: false, value: "", textContent: "", dataset: {}, children: [], scrollHeight: 1, scrollTop: 0, clientHeight: 1,
    append(...v) { this.children.push(...v); }, replaceChildren(...v) { this.children = v; }, addEventListener() {}});
  const el = id => { if (!nodes.has(id)) nodes.set(id, make()); return nodes.get(id); };
  el("draft").value = "keep me"; el("draft").oninput = () => {};
  let snapshot = {enabled: false, notice: "尚未配置", mutation_token: "t", turns: [], active_id: null}, down = false;
  const shown = [];
  const context = {document: {getElementById: el, createElement: make, addEventListener() {}}, TextEncoder, crypto: {randomUUID: () => "id"},
    getSelection: () => ({toString: () => ""}), navigator: {}, setTimeout: fn => timers.push(fn),
    AGHReadiness: {fromChat: (s, o) => ({state: o?.failed ? "unavailable" : s.enabled ? "ready" : "missing"}), show: (slot, model, handlers) => { shown.push({slot, model, handlers}); return model.state !== "ready"; }},
    fetch: async (url, options = {}) => { calls.push({url, ...options}); if (down) throw Error("offline"); return {ok: true, json: async () => JSON.parse(JSON.stringify(snapshot))}; }};
  vm.createContext(context);
  for (const file of ["markdown-parser.js", "markdown.js", "chat.js"]) vm.runInContext(read(file), context);
  const flush = () => new Promise(resolve => setImmediate(resolve));
  await flush();
  assert.equal(shown.at(-1).model.state, "missing");
  assert.equal(shown.at(-1).slot, el("chat-readiness"));
  assert.equal(typeof shown.at(-1).handlers.retry, "function");
  assert.equal(el("draft").value, "keep me");
  assert.equal(el("send-chat").disabled, true);
  snapshot = {...snapshot, enabled: true};
  timers.shift()(); await flush();
  assert.equal(shown.at(-1).model.state, "ready");
  down = true;
  timers.shift()(); await flush();
  assert.equal(shown.at(-1).model.state, "unavailable");
  assert.equal(el("draft").value, "keep me");
  assert.equal(calls.filter(call => call.method === "POST").length, 0);
});

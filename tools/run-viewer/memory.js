"use strict";

// Render recorded identities and judgments. This page never extracts or writes memory.
const $ = (id) => document.getElementById(id);
const list = (value) => Array.isArray(value) ? value : [];
const dom = (tag, className = "", text) => {
  const node = document.createElement(tag);
  node.className = className;
  if (text !== undefined) node.textContent = String(text);
  return node;
};
const kinds = {project_fact: "项目事实", constraint: "长期约束", preference: "用户偏好", decision: "已采纳决策", procedure: "已审核经验"};
const kindClass = {project_fact: "fact", constraint: "constraint", preference: "preference", decision: "decision", procedure: "procedure"};
const statuses = {active: "可供召回", conflicted: "存在冲突", invalidated: "已失效", expired: "已到期"};
const topics = {database: "数据库", port: "端口", timeout: "超时", owner: "负责人", "credential.type.key": "密钥类型", "credential.type.password": "密码类型", "credential.type.token": "令牌类型", "credential.type.certificate": "证书类型", "credential.rotation": "凭据轮换", "credential.rotation.period": "轮换周期", "physical.key": "实体钥匙"};
const reasons = {different_object: "对象不匹配", different_topic: "属性不匹配", different_credential_type: "凭据类型不同", different_key_domain: "钥匙领域不同", missing_rotation_period: "缺少轮换周期", unresolved_query_object: "查询对象未确定", unresolved_memory_object: "记忆对象未确定", candidate_limit: "候选数量限制", compatible: "对象与属性适用"};
const choices = {remember: "值得长期记忆", skip: "不应保存", independent: "相互独立", same: "语义重复", conflict: "存在冲突", uncertain: "关系未确定"};
const state = {data: null, source: "", object: null, topic: null, busy: false, selected: null};
const text = (value) => String(value || "");
const annotation = (item) => item.annotation || {};
const objectList = (item) => list(annotation(item).objects);
const topicList = (item) => list(annotation(item).topics);
const tag = (value, className = "") => dom("span", `tag ${className}`, value);
const short = (value, length = 8) => text(value).slice(0, length);
const date = (value) => value && !Number.isNaN(Date.parse(value)) ? new Date(value).toLocaleString("zh-CN", {hour12: false}) : "未记录";
const recordURL = (key) => `/?key=${encodeURIComponent(key)}`;
const status = (item) => item.memory.status === "active" && item.memory.expires_at && Date.parse(item.memory.expires_at) <= Date.now() ? "expired" : item.memory.status;
const sourceItems = () => list(state.data?.memories).filter((item) => state.source === "all" || item.source.id === state.source);
const sourceRecalls = () => list(state.data?.recalls).filter((run) => state.source === "all" || run.source.id === state.source);
const objects = (items) => {
  const values = new Map();
  for (const item of items) for (const object of objectList(item)) {
    const key = `${item.source.id}:${object.id}`;
    const entry = values.get(key) || {...object, key, count: 0};
    entry.count++; values.set(key, entry);
  }
  return [...values.values()];
};
function filteredItems() {
  const query = $("search").value.trim().toLowerCase();
  const wanted = $("status-filter").value;
  return sourceItems().filter((item) => {
    const search = [item.memory.text, item.memory.id, item.memory.conditions,
      ...objectList(item).map((o) => o.name), ...topicList(item).flatMap((t) => [t.key, topics[t.key]])].join(" ").toLowerCase();
    return (!query || search.includes(query)) && (wanted === "all" || status(item) === wanted)
      && (!state.object || objectList(item).some((o) => `${item.source.id}:${o.id}` === state.object.key))
      && (!state.topic || topicList(item).some((t) => t.key === state.topic));
  });
}
function selectObject(object) { state.object = object; state.topic = null; render(); }
function clearFilter() { state.object = null; state.topic = null; render(); }

async function refresh() {
  if (state.busy) return;
  state.busy = true; $("refresh").disabled = true;
  try {
    const response = await fetch("/api/memories", {cache: "no-store"});
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
    const unchanged = JSON.stringify(data) === JSON.stringify(state.data);
    state.data = data;
    if (!state.source && data.channels) state.source = "all";
    if (!state.source) state.source = data.sources.find((s) => s.kind === "database" && s.memory_count)?.id
      || data.sources.find((s) => s.memory_count)?.id || "all";
    if (state.source !== "all" && !data.sources.some((s) => s.id === state.source)) {
      throw new Error("已选数据源不再可用，请选择其他来源。");
    }
    $("source-filter").replaceChildren();
    for (const source of [{id: "all", name: "全部数据源"}, ...data.sources]) {
      const option = dom("option", "", source.name);
      option.value = source.id; $("source-filter").append(option);
    }
    $("source-filter").value = state.source;
    $("error").hidden = true;
    $("sync-state").textContent = `已同步 ${new Date().toLocaleTimeString("zh-CN", {hour12: false})}`;
    $("connection-state").textContent = "已连接 · 每 5 秒读取";
    if (!unchanged) render();
  } catch (error) {
    $("error").hidden = false;
    $("error").textContent = `读取失败：${error.message}${state.data ? "\n页面保留上次读取的快照，数据尚未同步。" : ""}`;
    $("sync-state").textContent = "同步失败 · 数据可能过时";
    $("connection-state").textContent = "连接失败 · 未同步";
  } finally { state.busy = false; $("refresh").disabled = false; }
}

function render() {
  if (!state.data) return;
  const items = sourceItems();
  const objectValues = objects(items);
  const topicValues = new Set(items.flatMap((item) => topicList(item).map((t) => t.key)));
  const injected = new Set(items.flatMap((item) => item.uses.filter((use) => use.status === "injected").map((use) => `${item.source.id}:${use.id}`)));
  $("metrics").replaceChildren();
  for (const [label, value, note, icon] of [
    ["已记录记忆", items.length, "保留原文与版本", "◇"],
    ["关联对象", objectValues.length, "仅统计已记录的对象身份", "◎"],
    ["覆盖主题", topicValues.size, "主题标签与对象分别呈现", "⌘"],
    ["记忆使用轮次", injected.size, "实际注入 Agent 的执行轮次", "↗"],
  ]) {
    const metric = dom("article", "metric");
    metric.append(dom("span", "metric-label", label), dom("span", "metric-icon", icon), dom("strong", "metric-value", value), dom("span", "metric-note", note));
    $("metrics").append(metric);
  }
  $("nav-memory-count").textContent = items.length;
  $("object-list").replaceChildren();
  for (const object of objectValues.slice(0, 20)) {
    const button = dom("button", `object-link ${state.object?.key === object.key ? "selected" : ""}`);
    button.append(dom("span", "object-bullet"), dom("span", "", object.name), dom("small", "", `${object.count}`));
    button.addEventListener("click", () => selectObject(object)); $("object-list").append(button);
  }
  if (!objectValues.length) $("object-list").append(dom("p", "rail-empty", "此来源尚无对象标注"));
  if (objectValues.length > 20) $("object-list").append(dom("p", "rail-empty", `显示前 20 / ${objectValues.length} 个对象`));
  const source = state.data.sources.find((s) => s.id === state.source);
  $("source-note").textContent = source?.kind === "report" ? "归档实测 · 历史观察" : source?.kind === "database" ? "本机数据库 · 当前状态" : "按来源分别保留身份";
  const notice = $("snapshot-notice");
  notice.hidden = !items.some((item) => item.snapshot) && !state.data.truncated;
  notice.textContent = `${items.some((item) => item.snapshot) ? "归档快照：这里展示报告中观察到的记忆与关系。原始实验数据库已清理，历史 active 状态不代表当前仍有效。" : ""}${state.data.truncated ? ` 已载入最多 ${state.data.memory_limit} 条；观察总数 ${state.data.observed_total}，此视图并不完整。` : ""}`;
  $("scope-note").textContent = `AGENT ${state.data.agent_id || "default"} · AUDIENCE ${state.data.audience_id || "owner"}${state.data.scope ? ` · ${state.data.project ? `PROJECT ${state.data.project} · ` : ""}MEMORY SCOPE ${state.data.scope}` : " · SOURCES KEPT SEPARATE"}`;
  const visible = filteredItems();
  renderGraph(visible); renderRecall(); renderCards(visible);
  if ($("memory-detail").open && state.selected) {
    const current = items.find((item) => item.key === state.selected);
    if (current) renderDetail(current);
  }
}

function svgNode(tagName, attributes = {}, value) {
  const node = document.createElementNS("http://www.w3.org/2000/svg", tagName);
  for (const [name, value] of Object.entries(attributes)) node.setAttribute(name, value);
  if (value !== undefined) node.textContent = value;
  return node;
}
function renderGraph(items) {
  const graph = $("graph"); graph.replaceChildren();
  if (!items.length) {graph.append(dom("p", "graph-placeholder", "此筛选下没有已记录的关系。\n记忆保存后，对象与主题会出现在这里。")); $("graph-note").textContent = "没有可展示的关联"; return;}
  const shown = [...items].sort((a, b) => objectList(b).length - objectList(a).length || a.memory.text.localeCompare(b.memory.text, "zh-CN")).slice(0, 5);
  const height = Math.max(300, shown.length * 70 + 75);
  const svg = svgNode("svg", {viewBox: `0 0 820 ${height}`, role: "group", "aria-label": "来自原始标注的对象、主题和记忆连线"});
  graph.append(svg);
  for (const [x, label] of [[37, "OBJECT"], [286, "TOPIC"], [518, "MEMORY RECORD"]]) svg.append(svgNode("text", {x, y: 26, class: "graph-column-label"}, label));
  const edges = svgNode("g"); const nodes = svgNode("g"); svg.append(edges, nodes);
  function link(x1, y1, x2, y2, className = "") {
    const center = (x1 + x2) / 2;
    edges.append(svgNode("path", {d: `M ${x1} ${y1} C ${center} ${y1},${center} ${y2},${x2} ${y2}`, class: `graph-edge ${className}`}));
  }
  function node(type, x, y, width, name, subtitle, click, selected = false) {
    const group = svgNode("g", {class: `graph-node node-${type}${selected ? " selected" : ""}`, transform: `translate(${x},${y})`, role: "button", tabindex: "0", "aria-label": `${name}，${subtitle}`});
    group.append(svgNode("title", {}, `${name} · ${subtitle}`), svgNode("rect", {width, height: 49, rx: 8}),
      svgNode("circle", {cx: 14, cy: 17, r: 2.5, class: "node-orbit"}),
      svgNode("text", {x: 26, y: 21, class: "node-name"}, name.length > 20 ? name.slice(0, 19) + "…" : name),
      svgNode("text", {x: 26, y: 36, class: "node-subtitle"}, subtitle));
    group.addEventListener("click", click);
    group.addEventListener("keydown", (event) => {if (["Enter", " "].includes(event.key)) {event.preventDefault(); click();}});
    nodes.append(group);
  }
  shown.forEach((item, index) => {
    const y = 53 + index * 70;
    const object = objectList(item)[0];
    const available = topicList(item);
    const topic = available.find((t) => t.key === "credential.rotation.period") || available.find((t) => !t.key.startsWith("credential.type.")) || available[0];
    if (object) {
      const objectValue = {...object, key: `${item.source.id}:${object.id}`};
      node("object", 27, y, 173, object.name, object.kind.toUpperCase(), () => selectObject(objectValue), state.object?.key === objectValue.key);
    } else {
      nodes.append(svgNode("text", {x: 39, y: y + 27, class: "graph-column-label"}, "未建立独立对象"));
    }
    if (topic) {
      node("topic", 269, y, 166, topics[topic.key] || topic.key, "TOPIC · SOURCE SPAN", () => {state.topic = topic.key; render();}, state.topic === topic.key);
      // Shared words alone cannot create an object/property edge.
      if (object && list(topic.object_ids).includes(object.id)) link(200, y + 25, 269, y + 25, "object-edge");
      link(435, y + 25, 505, y + 25);
    } else if (object) link(200, y + 25, 505, y + 25, "object-edge");
    node("memory", 505, y, 289, item.memory.text.split("\n")[0], `${(item.memory.kind || "unknown").toUpperCase()} · R${item.memory.revision ?? "?"}`, () => openDetail(item));
  });
  $("graph-note").textContent = `${items.length > shown.length ? `显示前 ${shown.length} / ${items.length} 条 · ` : ""}点击节点，查看关联记录 · 未标注关系保持空缺`;
}

function renderRecall() {
  const body = $("recent-recall"); body.replaceChildren();
  const visible = new Set(filteredItems().map((item) => `${item.source.id}:${item.memory.id}`));
  const recall = sourceRecalls().find((run) => list(run.context).some((memory) => visible.has(`${run.source.id}:${memory.id}`)));
  if (!recall) {body.append(dom("p", "empty-message", "当前筛选下尚未观察到记忆注入。执行一次新任务后，使用记录会出现在这里。")); return;}
  body.append(dom("div", "recall-caption", "RECORDED REQUEST"), dom("p", "recall-query", recall.input));
  const result = dom("div", "recall-result");
  result.append(tag("↗ 已注入 Agent"));
  for (const memory of list(recall.context).slice(0, 2)) result.append(dom("strong", "", memory.text));
  result.append(dom("p", "", `${short(recall.id)} · ${date(recall.created_at)}`)); body.append(result);
  const stats = dom("div", "recall-stats");
  for (const [count, name] of [[list(recall.context).length, "条注入"], [list(recall.decisions).filter((d) => !d.accepted).length, "条排除"]]) {
    const item = dom("span"); item.append(dom("strong", "", count), document.createTextNode(name)); stats.append(item);
  }
  body.append(stats);
  const link = dom("a", "recall-link", "查看召回过程 →"); link.href = recordURL(recall.key); body.append(link);
}

function renderCards(items) {
  $("library-count").textContent = items.length; $("memory-grid").replaceChildren();
  const filter = $("active-filter"); filter.replaceChildren(); filter.hidden = !state.object && !state.topic;
  if (!filter.hidden) {
    filter.append(dom("span", "", state.object ? `对象：${state.object.name}` : `主题：${topics[state.topic] || state.topic}`));
    const clear = dom("button", "", "清除筛选 ×"); clear.addEventListener("click", clearFilter); filter.append(clear);
  }
  for (const item of items) {
    const memory = item.memory;
    const card = dom("button", `memory-card ${state.selected === item.key ? "selected" : ""}`);
    card.setAttribute("aria-label", `查看记忆：${memory.text}`);
    const top = dom("div", "card-top");
    const stateLabel = dom("span", `state-dot ${status(item) !== "active" ? "bad" : ""}`);
    stateLabel.append(dom("i"), document.createTextNode(statuses[status(item)] || status(item) || "状态未记录"));
    top.append(tag(kinds[memory.kind] || memory.kind, kindClass[memory.kind]), stateLabel);
    const tags = dom("div", "memory-tags");
    objectList(item).forEach((o) => tags.append(tag(`◇ ${o.name}`)));
    [...new Set(topicList(item).map((t) => t.key))].slice(0, 3).forEach((key) => tags.append(tag(topics[key] || key)));
    if (!tags.childNodes.length) tags.append(tag("尚无对象或主题标注"));
    const foot = dom("div", "card-bottom");
    foot.append(dom("span", "", memory.source_type === "reviewed_experience" ? "来自人工审核经验" : memory.source_type === "user_statement" ? "来自用户声明" : "来源类型未记录"), dom("strong", "", `已使用 ${item.uses.filter((use) => use.status === "injected").length} 次`));
    card.append(top, dom("h3", "", memory.text.split("\n")[0]), tags, foot,
      dom("div", "card-proof", `MEM ${short(memory.id)} · REV ${memory.revision ?? "?"} · ${item.snapshot ? "ARCHIVED" : "CURRENT"}`), dom("span", "card-arrow", "↗"));
    card.addEventListener("click", () => openDetail(item)); $("memory-grid").append(card);
  }
  if (!items.length) $("memory-grid").append(dom("div", "library-empty", "当前筛选下没有记忆记录。可以清除筛选，或选择其他数据源。"));
}

function openDetail(item) {state.selected = item.key; renderDetail(item); if (!$("memory-detail").open) $("memory-detail").showModal();}
function section(parent, title) {const node = dom("section", "detail-section"); node.append(dom("h3", "", title)); parent.append(node); return node;}
function kv(parent, entries) {
  const node = dom("dl", "detail-kv");
  entries.forEach(([name, value]) => node.append(dom("dt", "", name), dom("dd", "", value ?? "未记录"))); parent.append(node);
}
function judgment(parent, title, answer, threshold) {
  const box = dom("div", "judgment-box");
  const probability = answer?.probabilities?.[answer?.choice];
  box.append(dom("span", "", title), dom("strong", "", choices[answer?.choice] || kinds[answer?.choice] || answer?.choice || "未记录"));
  if (typeof probability === "number") {
    const track = dom("div", "judgment-track"), fill = dom("div", "judgment-fill");
    fill.style.width = `${Math.max(0, Math.min(1, probability)) * 100}%`; track.append(fill);
    if (typeof threshold === "number") {const marker = dom("div", "threshold-marker"); marker.style.left = `${threshold * 100}%`; track.append(marker);}
    box.append(track, dom("small", "", `模型报告概率 ${probability.toFixed(2)}${typeof threshold === "number" ? ` · 准入阈值 ${threshold.toFixed(2)}` : " · 类别概率无单独阈值"}`));
  }
  parent.append(box);
}
function renderDetail(item) {
  const body = $("detail-body"); body.replaceChildren();
  const memory = item.memory, decision = item.decision;
  const tags = dom("div", "detail-tags"); tags.append(tag(kinds[memory.kind] || memory.kind, kindClass[memory.kind]), tag(statuses[status(item)] || status(item)), tag(item.snapshot ? "归档观察" : "当前记录"));
  body.append(tags, dom("p", "detail-statement", memory.text));
  const flow = section(body, "形成与使用记录");
  const injected = item.uses.filter((use) => use.status === "injected");
  const lifecycle = dom("div", "lifecycle");
  for (const [name, note, known] of [
    ["来源执行", item.origin_run ? "源 Run 已关联" : "源 Run 未记录", !!item.origin_run],
    ["Jev 准入", decision ? choices[decision.eligibility?.choice] || "已有判断" : "未观察到", !!decision],
    ["关系比较", decision ? decision.relations.length ? `${decision.relations.length} 条比较` : "无旧记忆" : "未观察到", !!decision],
    ["记忆保存", `已观察 R${memory.revision ?? "?"}`, true],
    ["后续使用", `${injected.length} 轮实际注入`, injected.length > 0],
  ]) {const step = dom("div", `lifecycle-step ${known ? "" : "missing"}`, name); step.append(dom("small", "", note)); lifecycle.append(step);}
  flow.append(lifecycle);
  if (decision) {
    const judged = section(body, "Jev 的结构化判断"); const grid = dom("div", "judgment-grid");
    judgment(grid, "是否值得长期记忆", decision.eligibility, decision.threshold);
    judgment(grid, "记忆类型", decision.memory_kind); judged.append(grid);
    kv(judged, [["返回模型", decision.model], ["Host 策略结果", decision.reason], ["关系判断", decision.relations.length ? decision.relations.map((r) => `${choices[r.choice] || r.choice} (${r.probabilities?.[r.choice] ?? "?"})`).join("、") : "没有旧记忆，本轮未发起关系判断请求"]]);
  } else flow.append(dom("p", "detail-note", item.origin_run
    ? "已关联源执行，但本视图未建立该记忆与准入判断的对应关系。人工审核经验可跳转到源执行查看筛选与审核记录。"
    : "此记忆快照未关联源执行 ID，不能据此补造该条记忆的准入过程。可查看下方已有观察和原始记录。"));
  const links = section(body, "对象与主题标注");
  const annotationTags = dom("div", "detail-tags");
  objectList(item).forEach((o) => annotationTags.append(tag(`对象 · ${o.name}`)));
  topicList(item).forEach((t) => annotationTags.append(tag(`主题 · ${topics[t.key] || t.key}`)));
  links.append(annotationTags);
  if (!annotationTags.childNodes.length) links.append(dom("p", "", "此记录没有可用的对象或主题标注。"));
  links.append(dom("p", "detail-note", item.annotation_origin === "stored" ? `来自 SQLite 已保存的派生标注 · ${annotation(item).version || "版本未记录"}` : item.annotation_origin === "recorded_input" ? `来自同文输入的已记录查询标注 · ${annotation(item).version}。这是历史观察，不代表重新读取了原实验数据库中的标注。` : "旧记录未提供派生标注；页面不会自行做实体识别。"));
  const usage = section(body, "何时被使用，何时被排除");
  for (const use of [...item.uses].reverse()) {
    const row = dom("div", "use-row");
    row.append(tag(use.status === "injected" ? "已注入" : use.status === "excluded" ? "已排除" : "候选未注入"));
    const desc = dom("p", "", use.input);
    desc.append(dom("small", "", `${date(use.created_at)} · ${reasons[use.reason] || use.reason || "已记录上下文"}${use.revision ? ` · R${use.revision}` : ""}`));
    const link = dom("a", "", "查看执行 ↗"); link.href = recordURL(use.key); row.append(desc, link); usage.append(row);
  }
  if (!item.uses.length) usage.append(dom("p", "", "在已载入的执行记录中，尚未观察到该记忆的召回。"));
  const provenance = section(body, "原文与来源");
  kv(provenance, [["记忆 ID", memory.id], ["Agent 实例", memory.agent_id || "default"], ["受众分区", memory.audience_id || "owner"], ["声明成员", memory.sender_id || "历史记录未提供"], ["记忆命名空间", memory.scope], ["来源类型", memory.source_type], ["来源执行", memory.source_run_id], ["适用条件", memory.conditions || "未单独记录"], ["到期时间", memory.expires_at ? date(memory.expires_at) : "未设置"], ["数据源", item.source.name]]);
  if (item.origin_run) {const link = dom("a", "recall-link", "查看源执行与模型事件 →"); link.href = recordURL(item.origin_run.key); provenance.append(link);}
  if (item.annotation_record_key) {const link = dom("a", "recall-link", "查看同文输入标注 →"); link.href = recordURL(item.annotation_record_key); provenance.append(link);}
  const raw = dom("details"); raw.append(dom("summary", "", "查看记忆及派生标注原始数据"), dom("pre", "", JSON.stringify({memory, annotation: item.annotation, annotation_origin: item.annotation_origin}, null, 2))); provenance.append(raw);
  body.append(dom("p", "detail-note", "来源为用户声明，或人工接受的经验。记忆状态和模型概率不构成对内容的独立真实性验证。"));
}

$("source-filter").addEventListener("change", () => {state.source = $("source-filter").value; state.object = null; state.topic = null; state.selected = null; $("memory-detail").close(); render();});
$("search").addEventListener("input", render);
$("status-filter").addEventListener("change", render);
$("refresh").addEventListener("click", refresh);
$("close-detail").addEventListener("click", () => $("memory-detail").close());
$("memory-detail").addEventListener("click", (event) => {if (event.target === $("memory-detail")) {const bounds = event.target.getBoundingClientRect(); if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) event.target.close();}});
setInterval(() => {if ($("auto-refresh").checked && !document.hidden) refresh();}, 5000);
document.addEventListener("visibilitychange", () => {if (!document.hidden && $("auto-refresh").checked) refresh();});
refresh();

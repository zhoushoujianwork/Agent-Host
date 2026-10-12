"use strict";

// Presentation consumes the host journal; it never executes or reviews a task.
const $ = (id) => document.getElementById(id);
const array = (value) => Array.isArray(value) ? value : [];
const json = (value) => JSON.stringify(value, null, 2);
const dom = (tag, className = "", text) => {
  const node = document.createElement(tag);
  node.className = className;
  if (text !== undefined) node.textContent = String(text);
  return node;
};
const labels = {
  running: "执行中", completed: "已完成", failed: "失败", canceled: "已取消", unknown: "未知",
  unverified: "尚未验收", saved: "已保存", unchanged: "无新增", pending: "待处理",
  skipped: "未处理", empty: "无合格经验", review_pending: "等待人工审核", reviewed: "已审核",
  accepted: "已接受", rejected: "已拒绝", active: "可供召回", conflicted: "存在冲突",
  invalidated: "已失效", expired: "已到期", pending_execution: "等待执行",
};
const tone = (status) => {
  if (["completed", "saved", "accepted", "active", "reviewed"].includes(status)) return "good";
  if (["failed", "rejected", "conflicted", "invalidated", "expired"].includes(status)) return "bad";
  if (status === "running") return "running";
  if (["pending", "review_pending", "unverified", "pending_execution"].includes(status)) return "pending";
  return "";
};
const badge = (status, text) => dom("span", `badge ${tone(status)}`, text || labels[status] || status || "未记录");
const duration = (ms) => typeof ms !== "number" ? "—" : ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(2)} s`;
const date = (value) => value && !Number.isNaN(Date.parse(value)) ? new Date(value).toLocaleString("zh-CN", {hour12: false}) : "时间未记录";
const short = (value) => String(value || "").slice(0, 8);
const phaseNames = {
  request: "接收请求", recall: "召回记忆", execute: "执行 Agent", remember: "判断用户陈述",
  persist: "保存陈述", evidence: "整理工具证据", extract: "提炼操作经验", assess: "Jev 筛选",
  candidates: "保存候选", receipt: "生成执行回执", review: "审核与关系检查", publish: "发布记忆",
};
const runLabels = {repair: "首次修复", resume: "原生会话续接", reuse: "经验召回与复用", after_invalidation: "失效后召回验证"};
const state = {index: null, key: null, record: null, tab: "overview", phase: "all", native: false, busy: false, generation: 0};

function card(title, subtitle = "") {
  const node = dom("article", "card");
  const head = dom("div", "card-head");
  const heading = dom("div");
  heading.append(dom("h2", "", title));
  if (subtitle) heading.append(dom("p", "", subtitle));
  head.append(heading);
  const body = dom("div", "card-body");
  node.append(head, body);
  return {node, head, body};
}
function placeholder(parent, text) { parent.append(dom("div", "placeholder", text)); }
function rawDetails(parent, title, value) {
  const details = dom("details");
  details.append(dom("summary", "", title), dom("pre", "", json(value)));
  parent.append(details);
}
function kv(parent, fields) {
  const list = dom("dl", "kv");
  fields.forEach(([name, value]) => list.append(dom("dt", "", name), dom("dd", "", value ?? "未记录")));
  parent.append(list);
}
function tools(record) {
  const grouped = new Map();
  for (const event of record.events) {
    const tool = event.data?.tool;
    if (event.phase !== "execute" || !tool?.id) continue;
    const value = grouped.get(tool.id) || {id: tool.id, name: tool.name || "未记录名称"};
    if (tool.stage === "call") Object.assign(value, {input: tool.input, call_seq: event.seq});
    if (tool.stage === "result") Object.assign(value, {result: tool.result, is_error: tool.is_error, result_seq: event.seq});
    grouped.set(tool.id, value);
  }
  return [...grouped.values()];
}
function candidates(record) {
  const values = new Map(array(record.candidates).map((value) => [value.id, {...value}]));
  // Saved IDs plus eligible assessments can reconstruct a journal-only candidate,
  // but must be labeled as observed, not as a current database row.
  const eligible = record.events.filter((e) => e.type === "experience.assessed" && e.data?.assessment?.eligible);
  let offset = 0;
  for (const saved of record.events.filter((e) => e.type === "candidates.saved")) {
    for (const id of array(saved.data?.candidate_ids)) {
      const assessed = eligible[offset++];
      if (!values.has(id)) values.set(id, {id, status: "pending", observed: true,
        draft: assessed?.data.assessment.audit?.evaluations?.[0]?.state?.candidate || {},
        assessment: assessed?.data.assessment, run_id: record.run.id});
    }
  }
  if (record.snapshot) {
    for (const event of record.events) {
      if (!["candidate.accepted", "candidate.rejected"].includes(event.type)) continue;
      const id = event.data?.candidate_id;
      if (!id) continue;
      const value = values.get(id) || {id, draft: {}, observed: true};
      Object.assign(value, {status: event.type === "candidate.accepted" ? "accepted" : "rejected",
        memory_id: event.data?.memory?.id, reviewed_at: event.at});
      values.set(id, value);
    }
  }
  return [...values.values()];
}
function phases(record) {
  const observed = record.events.filter((e) => e.type.startsWith("phase.") && e.data?.phase_label);
  const names = [...new Set([...Object.keys(phaseNames), ...observed.map((e) => e.phase)])];
  return names.map((name) => {
    const events = record.events.filter((e) => e.phase === name);
    const last = events.at(-1);
    const starts = events.filter((e) => e.type === "phase.started");
    const finishes = events.filter((e) => e.type === "phase.finished");
    const finish = finishes.at(-1);
    const active = new Set();
    for (const e of events) {
      const id = e.data?.tool_call_id;
      if (!id) continue;
      if (e.type === "phase.started") active.add(id);
      if (e.type === "phase.finished") active.delete(id);
    }
    let status = finish?.status || last?.status || "unseen";
    if (starts.length && (!finish || starts.at(-1).seq > finish.seq)) status = "running";
    if (active.size) status = "running";
    if (name === "request" && events.length) status = "completed";
    if (name === "receipt" && last?.type === "run.finished") status = "completed";
    if (!events.length && ["review", "publish"].includes(name) && record.run.experience_status === "review_pending") status = "pending";
    return {name, label: observed.find((e) => e.phase === name)?.data.phase_label || phaseNames[name] || name,
      parent: events.find((e) => e.data?.parent_phase)?.data.parent_phase,
      events, status, component: finish?.component || last?.component || "—",
      duration: finish?.data?.duration_ms, attempts: starts.length};
  });
}
function stream(record) {
  return record.events.filter((e) => e.phase === "execute" && e.type === "text.delta").map((e) => e.delta || "").join("");
}
function outcome(name, record) {
  if (name === "recall") return `${array(record.run.context).length} 条记忆注入执行`;
  if (name === "execute") return `${tools(record).length} 次工具调用`;
  if (name === "remember") {
    const decision = record.events.filter((e) => e.type === "decision.completed").at(-1)?.data?.decision;
    return decision ? (decision.store ? "长期陈述 → 保存" : "非合格长期陈述 → 不保存") : "";
  }
  if (name === "assess") {
    const assessed = record.events.filter((e) => e.type === "experience.assessed");
    return assessed.length ? `${assessed.filter((e) => e.data?.assessment?.eligible).length} / ${assessed.length} 提案 → 候选区` : "";
  }
  if (name === "candidates") return `${candidates(record).length} 条候选 · 未审核不召回`;
  if (name === "persist") return `陈述记忆：${labels[record.run.memory_status] || record.run.memory_status || "未记录"}`;
  if (name === "publish") {
    const memory = record.events.filter((e) => e.type === "candidate.accepted").at(-1)?.data?.memory;
    return memory ? `发布时状态：${labels[memory.status] || memory.status}` : "";
  }
  if (name === "review") {
    const audit = record.events.filter((e) => e.type === "candidate.compared").at(-1)?.data?.relations?.audit;
    return audit?.reason === "no_existing_memory" ? "本地检查：无已有记忆" : "";
  }
  return "";
}

async function api(path) {
  const response = await fetch(path, {cache: "no-store"});
  const value = await response.json();
  if (!response.ok) throw new Error(value.error || `HTTP ${response.status}`);
  return value;
}
function showError(error) {
  $("error").hidden = false;
  $("error").textContent = `读取失败：${error.message}\n${state.record ? "当前页面保留上次读取的数据；它可能已过时。" : "执行记录尚未加载。"}`;
  $("sync-state").textContent = "连接失败 · 数据未同步";
}
async function select(key) {
  const generation = ++state.generation;
  state.key = key;
  state.record = null;
  $("content").hidden = true;
  $("sync-state").textContent = "正在读取…";
  renderList();
  try {
    const record = await api(`/api/record?key=${encodeURIComponent(key)}`);
    if (generation !== state.generation) return;
    state.record = record;
    const url = new URL(location.href);
    url.searchParams.set("key", key);
    history.replaceState(null, "", url);
    $("error").hidden = true;
    $("sync-state").textContent = `已同步 ${new Date().toLocaleTimeString("zh-CN", {hour12: false})}`;
    renderRecord();
  } catch (error) { if (generation === state.generation) showError(error); }
}
async function refresh() {
  if (state.busy) return;
  state.busy = true;
  $("refresh").disabled = true;
  try {
    const index = await api("/api/records");
    state.index = index;
    renderList();
    $("empty").hidden = index.records.length !== 0;
    if (!index.records.length) {
      state.key = null;
      state.record = null;
      $("content").hidden = true;
    } else if (!state.key) {
      await select(new URL(location.href).searchParams.get("key") || index.default_key);
      // select owns its error state.
      return;
    } else {
      const key = state.key;
      const generation = state.generation;
      const record = await api(`/api/record?key=${encodeURIComponent(key)}`);
      if (key !== state.key || generation !== state.generation) return;
      if (json(record) !== json(state.record)) {
        state.record = record;
        renderRecord();
      }
    }
    $("error").hidden = true;
    $("sync-state").textContent = `已同步 ${new Date().toLocaleTimeString("zh-CN", {hour12: false})}`;
  } catch (error) { showError(error); }
  finally { state.busy = false; $("refresh").disabled = false; }
}
function renderList() {
  if (!state.index) return;
  const query = $("search").value.toLowerCase().trim();
  const source = $("source-filter").value;
  const rows = state.index.records.filter((row) => (source === "all" || row.source.kind === source)
    && [row.input, row.id, row.project_id, row.project_name, json(row.projects || []), row.workspace, row.scope,
      row.session_id, row.native_session_id, row.agent_id, row.audience_id, row.source.name, row.label, runLabels[row.label]].join(" ").toLowerCase().includes(query));
  $("record-count").textContent = `${rows.length} 条`;
  $("record-count").title = `本机数据库 ${state.index.database_count} 条，最多加载最近 ${state.index.database_limit} 条；归档报告单独加载。`;
  const list = $("record-list");
  const scroll = list.scrollTop;
  list.replaceChildren();
  if (!rows.length) placeholder(list, "没有符合条件的记录");
  for (const row of rows) {
    const button = dom("button", `record ${row.key === state.key ? "selected" : ""}`);
    button.setAttribute("aria-pressed", String(row.key === state.key));
    const top = dom("div", "record-top");
    top.append(dom("span", "", `${row.source.kind === "report" ? "归档" : "本机"} · ${short(row.id)}`), badge(row.execution_status));
    button.append(top, dom("div", "record-title", runLabels[row.label] || row.input || "未记录请求正文"));
    const bottom = dom("div", "record-bottom");
    const project = row.projects?.length > 1 ? row.projects.map((p) => p.name || p.id).join(" + ") : row.project_name || row.project_id;
    bottom.append(dom("span", "", project ? `${row.source.name} · ${project}` : row.source.name), dom("span", "", row.created_at.slice(11, 16)));
    button.append(bottom);
    button.addEventListener("click", () => select(row.key));
    list.append(button);
  }
  list.scrollTop = scroll;
}
function metric(label, value, note) {
  const node = dom("div", "metric");
  node.append(dom("div", "metric-label", label), dom("div", "metric-value", value), dom("div", "metric-note", note));
  return node;
}
function renderRecord() {
  const record = state.record;
  if (!record) return;
  $("content").hidden = false;
  const run = record.run;
  const toolList = tools(record);
  const actual = record.events.find((e) => e.phase === "execute" && e.type === "agent.started")?.model || run.model;
  $("run-title").textContent = runLabels[run.label] || "执行详情";
  $("run-id").textContent = `RUN ${short(run.id)}`;
  $("run-time").textContent = date(run.created_at);
  $("source-badge").textContent = record.source.name;
  $("overall-status").replaceChildren(badge(run.execution_status, `执行${labels[run.execution_status] || run.execution_status}`));
  $("snapshot-note").hidden = !record.snapshot;
  $("snapshot-note").textContent = "归档报告快照：回执表示自动执行结束时的状态；后续人工审核、发布和失效记录单独展示。归档不会追踪当前数据库。";
  $("metrics").replaceChildren(
    metric("执行 Agent / 实际模型", actual || "尚未观察到实际模型", run.agent || "local-claude"),
    metric("本轮执行耗时", duration(record.receipt?.elapsed_ms), "截至自动执行回执 · 人工审核另计"),
    metric("工具调用 / 已关联结果", `${toolList.length} / ${toolList.filter((t) => t.result_seq).length}`, `${toolList.filter((t) => t.is_error).length} 次工具返回错误`),
    metric("召回记忆 / 经验候选", `${array(run.context).length} / ${candidates(record).length}`, "未审核候选不进入召回")
  );
  $("event-count").textContent = `${record.events.length} 个事件 · ${record.snapshot ? "历史快照" : "本机数据库当前记录"}`;
  document.querySelectorAll(".tab").forEach((button) => {
    button.classList.toggle("active", button.dataset.tab === state.tab);
    button.setAttribute("aria-selected", String(button.dataset.tab === state.tab));
  });
  const panel = $("panel");
  panel.replaceChildren();
  ({overview, events: eventsPanel, tools: toolsPanel, decisions, memory, raw: rawPanel})[state.tab](panel, record);
}

function overview(panel, record) {
  const route = card("核心执行路线", "点击阶段查看组件、耗时和对应事件；颜色来自记录中的阶段状态。");
  const graph = dom("div", "route");
  const all = phases(record);
  const groups = [["执行", ["request", "recall", "execute", ...all.filter((p) => p.parent === "execute").map((p) => p.name)]], ["用户陈述", ["remember", "persist"]],
    ["操作经验", ["evidence", "extract", "assess", "candidates"]], ["收尾与审核", ["receipt", "review", "publish"]]];
  for (const [title, names] of groups) {
    const row = dom("div", "route-row");
    row.append(dom("div", "route-label", title));
    for (const name of names) {
      const phase = all.find((value) => value.name === name);
      const button = dom("button", `phase-node ${tone(phase.status) || phase.status}`);
      const titleNode = dom("div", "node-title");
      titleNode.append(dom("span", "", phase.label), dom("span", "node-number", String(all.indexOf(phase) + 1).padStart(2, "0")));
      const foot = dom("div", "node-foot");
      foot.append(badge(phase.status, phase.status === "unseen" ? "未观察到" : undefined), dom("span", "", duration(phase.duration)));
      button.append(titleNode, dom("div", "node-component", phase.component), foot);
      const detail = outcome(name, record);
      if (detail) button.append(dom("div", "node-outcome", detail));
      button.addEventListener("click", () => showPhase(phase));
      row.append(button);
    }
    graph.append(row);
  }
  route.node.append(graph, dom("div", "route-hint", "自动闭环：召回 → 原生 Agent → 两条记忆加工路线 → 回执。人工审核发生在回执之后；这里展示已记录的操作。"));
  panel.append(route.node);

  const columns = dom("div", "two-columns");
  const statuses = card("分别看待四种状态", "Agent 运行完成，不等于业务验收或经验发布完成。");
  const grid = dom("div", "status-grid");
  const run = record.run;
  for (const [title, status, note] of [["Agent 执行", run.execution_status, "原生执行结束状态"],
    ["业务验收", run.acceptance_status, "尚未验收时不能宣称业务成功"],
    ["用户陈述记忆", run.memory_status, "只加工用户明确的长期陈述"],
    [record.snapshot ? "回执中的经验状态" : "当前经验状态", run.experience_status, "候选需要显式人工审核"]]) {
    const item = dom("div", "status-item");
    item.append(dom("div", "status-label", title), badge(status), dom("p", "", note));
    grid.append(item);
  }
  statuses.node.append(grid);
  const errors = [run.error, run.memory_error, run.experience_error].filter(Boolean);
  if (errors.length) statuses.body.append(dom("p", "note", errors.join("\n")));
  const timing = card("组件耗时", "每个阶段最近一次完成的耗时；重试与人工审核可在事件中查看。");
  const timed = all.filter((p) => typeof p.duration === "number").sort((a, b) => b.duration - a.duration);
  const maximum = Math.max(...timed.map((p) => p.duration), 1);
  const chart = dom("div", "timings");
  timed.forEach((p) => {
    const row = dom("div", "timing-row");
    const track = dom("div", "timing-track");
    const fill = dom("div", "timing-fill");
    fill.style.width = `${Math.max(1, p.duration / maximum * 100)}%`;
    track.append(fill);
    row.append(dom("span", "", p.label), track, dom("span", "", duration(p.duration)));
    chart.append(row);
  });
  timing.body.append(chart);
  columns.append(statuses.node, timing.node);
  panel.append(columns);
  const roles = card("本轮参与的 Agent 与模型", "模型来自已记录的执行事件；配置名称与实际响应模型可能不同。");
  const seen = new Set();
  for (const event of record.events) {
    if (!["agent.started", "decision.completed", "experience.assessed", "extraction.completed"].includes(event.type)
      || !event.model || !event.agent || event.agent === "host") continue;
    const key = `${event.agent}:${event.model}`;
    if (seen.has(key)) continue;
    seen.add(key);
    kv(roles.body, [[event.agent, `${event.model} · ${event.component}`]]);
  }
  if (!seen.size) placeholder(roles.body, "尚未观察到模型执行事件");
  panel.append(roles.node);
  const request = card("请求与执行上下文");
  request.body.append(dom("div", "text-block request", run.input || "正文未记录"));
  kv(request.body, [["项目", run.project_name && run.project_id ? `${run.project_name} (${run.project_id})` : run.project_name || run.project_id || "未绑定项目"],
    ["Agent 实例", run.agent_id || "default"], ["受众分区", run.audience_id || "owner"],
    ["执行目录 (cwd)", run.workspace || run.scope], ["附加目录", (run.additional_directories || []).join("\n") || "无"],
    ["记忆命名空间", run.projects?.length ? run.projects.map((p) => `${p.id}: ${p.scope}`).join("\n") : run.scope],
    ["Host 会话", run.session_id], ["原生会话", run.native_session_id],
    ["运行配置指纹", run.runtime_fingerprint || "未记录"], ["配置模型", run.requested_model]]);
  for (const project of run.projects || []) {
    kv(request.body, [[`参与项目 ${project.name || project.id}`, `${project.id}\n${project.workspace}`]]);
  }
  for (const supplement of run.supplements || []) {
    kv(request.body, [[`用户补充 ${supplement.message_id}`, supplement.text]]);
  }
  panel.append(request.node);
  const output = card(run.execution_status === "running" ? "执行中的流式输出" : "Agent 输出", "显示记录中的文本；模型输出本身不构成工具执行证据。");
  const text = run.execution_status === "running" ? stream(record) || run.output : run.output || stream(record);
  output.body.append(dom("div", "text-block", text || "尚未记录输出文本"));
  panel.append(output.node);
  if (record.experiment) {
    const exp = card("归档实验结论", "实验断言的通过状态独立于业务验收。");
    exp.head.append(badge(record.experiment.passed ? "completed" : "failed", record.experiment.passed ? "实验通过" : "实验未通过"));
    kv(exp.body, [["Claude CLI", record.experiment.native_version], ["实验失败说明", record.experiment.failure || "未记录失败说明"]]);
    rawDetails(exp.body, "查看实验断言与独立测试", record.experiment);
    panel.append(exp.node);
  }
}
function eventNode(event) {
  const node = dom("div", `event ${event.status === "failed" ? "bad" : ""}`);
  const head = dom("div", "event-head");
  head.append(dom("code", "muted", `#${event.seq}`), dom("strong", "", event.type), badge(event.status),
    dom("span", "muted", `${event.component || "—"} · ${event.agent || "—"}`), dom("span", "muted", date(event.at)));
  node.append(head, dom("p", "", event.message || event.delta || "无描述"));
  if (event.model) node.append(dom("p", "", `模型 ${event.model}`));
  rawDetails(node, "查看事件原文", event);
  return node;
}
function showPhase(phase) {
  $("phase-title").textContent = phase.label;
  $("phase-body").replaceChildren();
  kv($("phase-body"), [["组件", phase.component], ["状态", labels[phase.status] || "未观察到"], ["最近一次耗时", duration(phase.duration)], ["已记录启动次数", phase.attempts]]);
  const meaningful = phase.events.filter((e) => !["native.evidence", "extraction.evidence", "text.delta"].includes(e.type));
  if (!meaningful.length) placeholder($("phase-body"), "没有对应阶段事件。未观察到不能解释为阶段已成功执行。");
  meaningful.forEach((e) => $("phase-body").append(eventNode(e)));
  $("phase-dialog").showModal();
}
function eventsPanel(panel, record) {
  const view = card("阶段事件日志", "按 seq 排序；人工审核会追加到同一个 Run。elapsed_ms 在重试或审核时可能重新计时。");
  const toolbar = dom("div", "toolbar");
  const filter = dom("select");
  filter.setAttribute("aria-label", "筛选阶段");
  filter.append(dom("option", "", "全部阶段"));
  filter.firstChild.value = "all";
  phases(record).forEach((phase) => {
    const option = dom("option", "", phase.label); option.value = phase.name; filter.append(option);
  });
  filter.value = state.phase;
  filter.addEventListener("change", () => {state.phase = filter.value; renderRecord();});
  const label = dom("label");
  const native = dom("input"); native.type = "checkbox"; native.checked = state.native;
  native.addEventListener("change", () => {state.native = native.checked; renderRecord();});
  label.append(native, dom("span", "", "包括原生事件与文本分片"));
  toolbar.append(filter, label);
  view.node.insertBefore(toolbar, view.body);
  const events = record.events.filter((e) => (state.phase === "all" || e.phase === state.phase)
    && (state.native || !["native.evidence", "extraction.evidence", "text.delta"].includes(e.type)));
  view.body.append(dom("p", "section-label", `匹配 ${events.length} 个事件${events.length > 200 ? " · 此页显示最近 200 个；完整数据见原始记录" : ""}`));
  events.slice(-200).forEach((e) => view.body.append(eventNode(e)));
  if (!events.length) placeholder(view.body, "没有匹配的事件");
  panel.append(view.node);
}
function toolsPanel(panel, record) {
  const view = card("工具调用与结果证据", "按工具 ID 关联输入和结果。错误结果可能是修复前的失败测试，需结合后续调用判断。");
  const list = tools(record);
  if (!list.length) placeholder(view.body, "没有已记录的工具证据");
  for (const tool of list) {
    const node = dom("div", "tool"); node.id = `tool-${tool.id}`;
    const head = dom("div", "tool-header");
    const title = dom("div", "tool-name", tool.name); title.append(dom("small", "", tool.id));
    head.append(title, badge(tool.result_seq ? (tool.is_error ? "failed" : "completed") : "pending", tool.result_seq ? (tool.is_error ? "工具报告错误" : "结果已记录") : "尚无关联结果"));
    const columns = dom("div", "two-columns");
    for (const [name, sequence, value] of [["调用输入", tool.call_seq, tool.input], ["工具结果", tool.result_seq, tool.result]]) {
      const field = dom("div");
      field.append(dom("label", "", `${name} · ${sequence ? `事件 #${sequence}` : "未观察到"}`), dom("pre", "", typeof value === "string" ? value : value === undefined ? "内容未记录" : json(value)));
      columns.append(field);
    }
    node.append(head, columns); view.body.append(node);
  }
  panel.append(view.node);
}
function decisions(panel, record) {
  const view = card("结构化判断", "Claude 提炼提案，Jev 返回枚举及概率，Host 根据已配置阈值决定后续路线。confidence 与门槛概率分别显示。");
  const judgments = record.events.filter((e) => ["decision.completed", "decision.failed", "experience.assessed", "candidate.compared"].includes(e.type));
  if (!judgments.length) placeholder(view.body, "尚未记录判断结果");
  for (const event of judgments) {
    const value = event.data?.decision || event.data?.assessment || event.data?.relations;
    const audit = value?.details || value?.audit || {};
    const node = dom("div", "judgment");
    const head = dom("div", "judgment-title");
    head.append(dom("strong", "", `${phaseNames[event.phase]} · #${event.seq}`), badge(event.status));
    node.append(head);
    kv(node, [["执行者 / 实际模型", `${event.agent || "—"} / ${value?.model || event.model || "未调用模型或未记录"}`],
      ["Provider / 配置模型", `${audit.provider || "—"} / ${audit.requested_model || "—"}`],
      ["判断路线", value?.eligible !== undefined ? (value.eligible ? "合格 → 候选区 → 人工审核" : "不合格 → 不保存经验候选") : value?.store !== undefined ? (value.store ? "保存用户陈述" : "不保存用户陈述") : "审核关系检查"],
      ["原因", audit.reason || event.message], ["策略", audit.policy_version],
      ["概率门槛", audit.probability_threshold ?? audit.eligibility_probability_threshold ?? "未记录"]]);
    for (const evaluation of array(audit.evaluations)) {
      node.append(dom("p", "section-label", `判断步骤 ${evaluation.stage} · ${evaluation.response?.model || "未记录响应模型"}`));
      for (const [key, answer] of Object.entries(evaluation.response?.answers || {})) {
        const question = dom("div", "question");
        const heading = dom("div", "question-head");
        heading.append(dom("strong", "", key), dom("span", "", `选择 ${answer.choice ?? answer.value ?? "未记录"} · confidence ${answer.confidence ?? "—"}`));
        question.append(heading);
        const bars = dom("div", "probabilities");
        for (const [choice, probability] of Object.entries(answer.probabilities || {})) {
          const row = dom("div", "prob-row");
          const track = dom("div", "bar"); const fill = dom("div", `bar-fill ${choice === answer.choice ? "chosen" : ""}`);
          fill.style.width = `${Math.min(100, Math.max(0, Number(probability) * 100))}%`;
          track.append(fill); row.append(dom("span", "", choice), track, dom("span", "", Number(probability).toFixed(2))); bars.append(row);
        }
        question.append(bars); node.append(question);
      }
      if (evaluation.error) node.append(dom("p", "note", evaluation.error));
    }
    if (audit.reason === "no_existing_memory") node.append(dom("p", "note", "没有可比较的已有记忆，关系检查在本地完成；该步骤未发出 Jev 请求。"));
    rawDetails(node, "查看输入、问题定义与原始响应", event.data);
    view.body.append(node);
  }
  panel.append(view.node);
  const extraction = record.events.filter((e) => e.type === "extraction.completed");
  if (extraction.length) {
    const extract = card("Claude 提炼结果", "结构化提案仍需 Jev 筛选和人工审核。");
    for (const event of extraction) {
      kv(extract.body, [["模型 / Agent", `${event.model || "—"} / ${event.agent}`], ["提案数量", array(event.data?.extraction?.drafts).length]]);
      array(event.data?.extraction?.drafts).forEach((draft) => {
        extract.body.append(dom("h3", "", draft.title || "无标题"), dom("p", "text-block", draft.summary));
      });
      rawDetails(extract.body, "查看完整提炼提案与审计信息", event.data);
    }
    panel.append(extract.node);
  }
}
function evidenceLinks(parent, ids, record) {
  const chips = dom("div", "chips");
  const known = new Set(tools(record).map((tool) => tool.id));
  for (const id of array(ids)) {
    if (!known.has(id)) {chips.append(dom("span", "chip", `${id} · 本轮无对应证据`)); continue;}
    const button = dom("button", "chip", id);
    button.addEventListener("click", () => {
      state.tab = "tools"; renderRecord();
      $(`tool-${id}`)?.scrollIntoView({behavior: "smooth", block: "center"});
    });
    chips.append(button);
  }
  parent.append(chips);
}
function memoryCard(value, record, isCandidate = false) {
  const node = dom("div", "memory-card");
  const draft = isCandidate ? value.draft || {} : value;
  const expired = !isCandidate && value.status === "active" && value.expires_at && Date.parse(value.expires_at) <= Date.now();
  node.append(badge(expired ? "expired" : value.status), dom("div", "memory-id", value.id));
  const summary = value.source_type === "reviewed_experience" && array(value.steps).length
    ? (value.text || "").split("\n").slice(1, 2).join("\n") : value.text;
  node.append(dom("h3", "", isCandidate ? draft.title || "候选提案" : (value.text || "记忆").split("\n")[0]),
    dom("p", "", isCandidate ? draft.summary || "摘要未记录" : summary));
  if (draft.conditions) node.append(dom("p", "note", `适用条件：${draft.conditions}`));
  if (array(draft.steps).length) {
    const steps = dom("ol"); draft.steps.forEach((step) => steps.append(dom("li", "", step))); node.append(steps);
  }
  kv(node, isCandidate ? [["候选来源", value.observed ? "由日志重建的观察记录" : record.snapshot ? "归档中的检查及审核记录" : "数据库当前记录"],
    ["发布记忆 ID", value.memory_id || "尚未发布"], ["审核时间", value.reviewed_at ? date(value.reviewed_at) : "未记录"]]
    : [["来源类型", value.source_type || "未记录"], ["版本", value.revision], ["到期时间", value.expires_at ? date(value.expires_at) : "未设置"], ["源 Run", value.source_run_id]]);
  evidenceLinks(node, draft.evidence_ids, record);
  rawDetails(node, "查看完整对象", value);
  return node;
}
function memory(panel, record) {
  const recall = card("① 执行前召回", "这是本轮实际注入的快照，不会因随后失效而改写历史。");
  array(record.run.context).forEach((value) => recall.body.append(memoryCard(value, record)));
  if (!array(record.run.context).length) placeholder(recall.body, "本轮没有召回记忆");
  panel.append(recall.node);
  const queue = card("② 经验候选与审核", "pending 候选不会被下一轮召回。审核接受只说明已接受这条经验，不等于独立验证全部技术主张。");
  const draftList = candidates(record);
  draftList.forEach((value) => queue.body.append(memoryCard(value, record, true)));
  if (!draftList.length) placeholder(queue.body, "本轮没有已保存的经验候选；可在模型判断中查看筛选原因。");
  panel.append(queue.node);
  const published = card("③ 已发布记忆的状态", record.snapshot ? "归档中最后观察到的状态；与发布时事件快照分别保留。" : "数据库当前状态；失效、冲突和到期记忆不再用于正常召回。");
  const relevant = new Set([...array(record.run.context).map((m) => m.id), ...draftList.map((c) => c.memory_id).filter(Boolean)]);
  const list = array(record.memories).filter((m) => m.source_run_id === record.run.id || relevant.has(m.id));
  list.forEach((value) => published.body.append(memoryCard(value, record)));
  if (!list.length) placeholder(published.body, "本轮未观察到对应的发布记忆");
  if (record.snapshot) published.body.append(dom("p", "note", "发布事件中的 status=active 是当时的快照；此处若显示 invalidated，则是报告中后续检查观察到的失效状态。"));
  panel.append(published.node);
}
function rawPanel(panel, record) {
  const view = card("原始执行记录", "所有内容按文本展示，不执行记录内的脚本、命令或 HTML。");
  view.body.append(dom("pre", "raw", json(record)));
  panel.append(view.node);
}

$("search").addEventListener("input", renderList);
$("source-filter").addEventListener("change", renderList);
$("refresh").addEventListener("click", refresh);
document.querySelectorAll(".tab").forEach((button) => button.addEventListener("click", () => {state.tab = button.dataset.tab; renderRecord();}));
$("close-dialog").addEventListener("click", () => $("phase-dialog").close());
setInterval(() => {if ($("auto-refresh").checked && !document.hidden) refresh();}, 5000);
document.addEventListener("visibilitychange", () => {if (!document.hidden && $("auto-refresh").checked) refresh();});
refresh();

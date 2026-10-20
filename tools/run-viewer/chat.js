"use strict";
(() => {
  const el = id => document.getElementById(id);
  const statusNames = {starting:"正在准备",running:"正在执行",stopping:"正在中止…",completed:"执行完成",failed:"执行失败",canceled:"已中止",unknown:"结果未确认"};
  let state = {enabled:false,turns:[],active_id:null}, token = "", pending = false, request = null;
  const rendered = new Map();
  const scroll = el("chat-scroll");
  let follow = true;
  let latestStatus = "";
  function controls() {
    const busy = pending || Boolean(state.active_id);
    el("send-chat").hidden = Boolean(state.active_id);
    el("send-chat").disabled = busy || !state.enabled || state.needs_new || !el("draft").value.trim();
    el("stop-chat").hidden = !state.active_id;
    const active = state.turns.find(t => t.id === state.active_id);
    el("stop-chat").disabled = pending || active?.status === "stopping";
    el("stop-chat").textContent = active?.status === "stopping" ? "正在中止…" : "■ 中止执行";
    el("new-chat").disabled = busy;
  }
  function node(tag, className, text) {
    const result = document.createElement(tag);
    result.className = className;
    if (text !== undefined) result.textContent = text;
    return result;
  }
  function render() {
    el("chat-welcome").hidden = state.turns.length > 0;
    el("chat-notice").textContent = state.notice || "由配置的 Agent 执行，过程自动保留。";
    const ids = new Set(state.turns.map(t => t.id));
    for (const [id, item] of rendered) if (!ids.has(id)) { item.root.remove(); rendered.delete(id); }
    for (const turn of state.turns) {
      let item = rendered.get(turn.id);
      if (!item) {
        const root = node("article","chat-turn");
        const user = node("div","chat-user",turn.input);
        const label = node("div","chat-answer-label","Claude");
        const output = node("div","chat-native-output");
        const status = node("div","chat-turn-status");
        const error = node("p","chat-turn-error");
        const record = node("a","chat-record","查看执行记录 ↗");
        root.append(user,label,output,status,error,record);
        el("chat-messages").append(root);
        item = {root,output,status,error,record,blocks:new Map()}; rendered.set(turn.id,item);
      }
      const rows = turn.output || (turn.text ? [{id:"answer",kind:"text",content:turn.text}] : []);
      const rowIds = new Set(rows.map(row => row.id));
      for (const [id, block] of item.blocks) if (!rowIds.has(id)) { block.root.remove(); item.blocks.delete(id); }
      for (const row of rows) {
        let block = item.blocks.get(row.id);
        if (!block) {
          const root = node("div","chat-native-row"); root.dataset.kind = row.kind;
          const header = node("div","chat-native-heading");
          const name = node("code","");
          const state = node("span","chat-native-state"); header.append(name,state);
          const content = node(row.kind === "text" ? "div" : "pre",row.kind === "text" ? "chat-answer" : "chat-native-content");
          const progress = node("pre","chat-native-progress");
          const params = node("details","chat-native-parameters");
          const summary = node("summary","","parameters");
          const raw = node("pre","chat-native-content"); params.append(summary,raw);
          root.append(header,content,params,progress); item.output.append(root);
          block = {root,header,name,state,content,params,raw,progress}; item.blocks.set(row.id,block);
        }
        block.header.hidden = row.kind === "text";
        block.name.textContent = row.name || row.label || "";
        const stage = row.stage === "call" ? "tool_use" : "tool_result";
        block.state.textContent = row.kind === "tool" ? `${stage} · ${row.status}` : "";
        block.root.dataset.status = row.status || "";
        if (block.content.textContent !== row.content) block.content.textContent = row.content;
        block.content.hidden = !row.content;
        block.progress.hidden = !row.progress;
        if (block.progress.textContent !== row.progress) block.progress.textContent = row.progress || "";
        block.params.hidden = !row.parameters || row.parameters === row.content;
        if (block.raw.textContent !== row.parameters) block.raw.textContent = row.parameters || "";
      }
      item.output.hidden = !rows.length;
      item.root.dataset.status = turn.status;
      item.status.textContent = statusNames[turn.status] || turn.status;
      item.error.textContent = turn.error || ""; item.error.hidden = !turn.error;
      if (turn.output_truncated && !item.truncation) {
        item.truncation = node("p","chat-native-truncated","仅显示最近的原生输出，完整内容见执行记录。");
        item.root.append(item.truncation);
      }
      item.record.hidden = !turn.record_key;
      item.record.href = `#executions?key=${encodeURIComponent(turn.record_key || "")}`;
    }
    controls();
    const latest = state.turns.at(-1);
    const signature = latest ? `${latest.id}:${latest.status}` : "";
    if (signature !== latestStatus) {
      if (latest?.status === "canceled") el("chat-state").textContent = "已中止。点击「新对话」继续。";
      if (latest?.status === "failed") el("chat-state").textContent = "执行失败。请检查记录后开启新对话。";
      if (latest?.status === "completed") el("chat-state").textContent = "执行完成，可以继续提问。";
      latestStatus = signature;
    }
    if (follow) scroll.scrollTop = scroll.scrollHeight;
    el("latest-chat").hidden = follow || !state.turns.length;
  }
  async function refresh() {
    const response = await fetch("/api/chat",{cache:"no-store"});
    if (!response.ok) throw Error("无法读取执行状态");
    state = await response.json(); token = state.mutation_token;
    render();
  }
  async function action(path, value) {
    if (!token) throw Error("连接尚未就绪，请稍后重试。");
    const response = await fetch(path,{method:"POST",headers:{"Content-Type":"application/json","X-Host-Action":token},body:JSON.stringify(value)});
    const result = await response.json();
    if (!response.ok) throw Error(result.error || "操作失败，请查看执行状态。");
    return result;
  }
  async function send() {
    if (el("send-chat").disabled || pending) return;
    const message = el("draft").value;
    if (new TextEncoder().encode(message).length > 16 * 1024) {
      el("chat-state").textContent = "单次消息最多 16 KiB，请缩短后发送。"; return;
    }
    // A lost response retains this ID. Explicit retry cannot replay a task that
    // the server already accepted, including when it has since completed.
    if (!request || request.message !== message) request = {request_id:crypto.randomUUID(),message};
    pending = true; controls(); el("chat-state").textContent = "正在发送…";
    try {
      await action("/api/chat",request);
      if (el("draft").value === message) { el("draft").value = ""; el("draft").oninput(); }
      request = null; follow = true; el("chat-state").textContent = "已发送";
      await refresh();
    } catch (error) { el("chat-state").textContent = `${error.message} 未确认时请先查看当前执行，再重试。`; }
    finally { pending = false; controls(); }
  }
  el("send-chat").onclick = send;
  document.addEventListener("agh:chat-send",send);
  document.addEventListener("agh:draft-change",controls);
  el("stop-chat").onclick = async () => {
    if (!state.active_id || pending) return;
    pending = true; controls();
    try { await action("/api/chat/stop",{turn_id:state.active_id}); el("chat-state").textContent = "已请求中止，等待执行退出。"; await refresh(); }
    catch (error) { el("chat-state").textContent = error.message; }
    finally { pending = false; controls(); }
  };
  el("new-chat").onclick = async () => {
    if (pending || state.active_id) return;
    pending = true; controls();
    try { await action("/api/chat/new",{}); request = null; follow = true; await refresh(); el("chat-state").textContent = "已开启新对话，历史执行记录仍保留。"; }
    catch (error) { el("chat-state").textContent = error.message; }
    finally { pending = false; controls(); }
  };
  scroll.addEventListener("scroll",() => {
    follow = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 64;
    el("latest-chat").hidden = follow || !state.turns.length;
  });
  el("latest-chat").onclick = () => { follow = true; scroll.scrollTop = scroll.scrollHeight; el("latest-chat").hidden = true; };
  async function poll() {
    try { await refresh(); }
    catch { el("chat-state").textContent = "连接中断，正在重连；不会自动重发。"; }
    setTimeout(poll,state.active_id ? 350 : 2000);
  }
  poll();
})();

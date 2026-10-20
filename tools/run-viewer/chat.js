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
        const label = node("div","chat-answer-label","AGH");
        const output = node("div","chat-answer");
        const steps = node("details","chat-steps");
        steps.open = true;
        const summary = node("summary","","执行步骤");
        const list = node("ol",""); steps.append(summary,list);
        const status = node("div","chat-turn-status");
        const error = node("p","chat-turn-error");
        const record = node("a","chat-record","查看执行记录 ↗");
        root.append(user,label,output,steps,status,error,record);
        el("chat-messages").append(root);
        item = {root,output,steps,summary,list,status,error,record,stepCount:0}; rendered.set(turn.id,item);
      }
      if (item.output.textContent !== turn.text) item.output.textContent = turn.text;
      item.output.hidden = !turn.text;
      item.root.dataset.status = turn.status;
      item.status.textContent = statusNames[turn.status] || turn.status;
      item.error.textContent = turn.error || ""; item.error.hidden = !turn.error;
      const stepsKey = JSON.stringify(turn.steps);
      if (item.stepsKey !== stepsKey) {
        item.list.replaceChildren(...turn.steps.map(step => {
          const li = node("li","",step.label);
          li.dataset.status = step.status; li.append(node("span","",statusNames[step.status] || step.status));
          return li;
        }));
        item.summary.textContent = `执行步骤 · ${turn.steps.length}`;
        item.steps.hidden = !turn.steps.length; item.stepsKey = stepsKey;
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

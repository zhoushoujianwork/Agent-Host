"use strict";
(() => {
  const el = id => document.getElementById(id);
  const statusNames = {starting:"正在准备",running:"正在执行",stopping:"正在中止…",completed:"执行完成",failed:"执行失败",canceled:"已中止",unknown:"结果未确认"};
  let state = {enabled:false,turns:[],active_id:null}, token = "", pending = false, request = null;
  const rendered = new Map();
  const scroll = el("chat-scroll");
  let follow = true;
  let latestStatus = "";
  let selectedAgent, agentOptions = "";
  const drafts = new Map();
  function controls() {
    const busy = pending || Boolean(state.active_id);
    el("send-chat").hidden = false;
    el("send-chat").disabled = pending || (state.active_ids || (state.active_id ? [state.active_id] : [])).length >= (state.max_concurrent || 4) || !state.enabled || state.needs_new || !el("draft").value.trim();
    el("stop-chat").hidden = (state.active_ids || (state.active_id ? [state.active_id] : [])).length !== 1;
    const active = state.turns.find(t => t.id === state.active_id);
    el("stop-chat").disabled = pending || active?.status === "stopping";
    el("stop-chat").textContent = active?.status === "stopping" ? "正在中止…" : "■ 中止执行";
    el("new-chat").disabled = busy;
    el("chat-agent").disabled = busy || !state.enabled || (state.agents || []).length < 2;
  }
  function node(tag, className, text) {
    const result = document.createElement(tag);
    result.className = className;
    if (text !== undefined) result.textContent = text;
    return result;
  }
  function copyButton(label, value) {
    const button = node("button","chat-copy",label);
    button.type = "button"; button.title = label; button.ariaLabel = label;
    button.onclick = async () => {
      button.disabled = true;
      try {
        await navigator.clipboard.writeText(value());
        button.textContent = "已复制 ✓";
      } catch {
        button.textContent = "复制失败";
        el("chat-state").textContent = "无法访问剪贴板，请拖选文字后使用 Ctrl/Cmd+C 复制。";
      } finally {
        button.disabled = false;
        setTimeout(() => { button.textContent = label; },1800);
      }
    };
    return button;
  }
  function duration(ms) {
    const seconds = Math.floor(Math.max(0,ms) / 1000);
    return seconds >= 60 ? `${Math.floor(seconds / 60)}m ${seconds % 60}s` : `${seconds}s`;
  }
  function reconcile(parent, children) {
    const current = Array.from(parent.children);
    if (current.length !== children.length || current.some((child,index) => child !== children[index])) parent.replaceChildren(...children);
  }
  function render() {
    if (selectedAgent !== state.selected_agent) {
      if (selectedAgent !== undefined) {
        drafts.set(selectedAgent,el("draft").value);
        el("draft").value = drafts.get(state.selected_agent) || "";
        el("draft").oninput();
      }
      selectedAgent = state.selected_agent;
      request = null; follow = true;
    }
    const signatureOptions=JSON.stringify(state.agents || []);
    if (agentOptions !== signatureOptions) {
      el("chat-agent").replaceChildren(...(state.agents || []).map(agent=>{const option=node("option","",agent.name);option.value=agent.id;return option;}));
      agentOptions=signatureOptions;
    }
    if (el("chat-agent").value !== (state.selected_agent || "")) el("chat-agent").value = state.selected_agent || "";
    el("chat-agent-bar").hidden = (state.agents || []).length < 2;
    el("chat-welcome").hidden = state.turns.length > 0;
    el("chat-notice").textContent = state.notice || "由配置的 Agent 执行，过程自动保留。";
    const ids = new Set(state.turns.map(t => t.id));
    for (const [id, item] of rendered) if (!ids.has(id)) { item.root.remove(); rendered.delete(id); }
    for (const turn of state.turns) {
      let item = rendered.get(turn.id);
      if (!item) {
        const root = node("article","chat-turn");
        const user = node("div","chat-user",turn.input);
        const work = node("details","chat-work");
        const summary = node("summary","chat-work-summary");
        const activity = node("div","chat-work-content"); work.append(summary,activity);
        const output = node("div","chat-native-output");
        const status = node("div","chat-turn-status");
        const error = node("p","chat-turn-error");
        const record = node("a","chat-record","查看执行记录 ↗");
        const userActions = node("div","chat-actions chat-user-actions");
        userActions.append(copyButton("复制消息",() => user.textContent));
        const answerActions = node("div","chat-actions");
        const copyAnswer = copyButton("复制回复",() => item.reply);
        answerActions.append(copyAnswer);
        const stop = node("button","chat-copy","中止此条执行"); stop.type = "button";
        stop.onclick = () => stopTurn(turn.id);
        root.append(user,work,output,status,error,record,userActions,answerActions,stop);
        el("chat-messages").append(root);
        work.open = ["starting","running","stopping"].includes(turn.status);
        item = {root,work,summary,activity,output,status,error,record,answerActions,copyAnswer,stop,blocks:new Map()}; rendered.set(turn.id,item);
      }
      const rows = turn.output || (turn.text ? [{id:"answer",kind:"text",content:turn.text}] : []);
      const active = ["starting","running","stopping"].includes(turn.status);
      const calls = rows.filter(row => row.kind === "tool" && row.stage === "call");
      const lastTool = rows.findLastIndex(row => row.kind === "tool");
      const usefulStatus = row => row.kind === "status" && !["init","status","success"].includes(row.label);
      item.work.hidden = !calls.length && !rows.some(usefulStatus);
      const working = calls.findLast(row => row.status === "running");
      item.summary.textContent = [active ? working ? `正在调用 ${working.name}` : statusNames[turn.status] : turn.status === "completed" ? "处理完成" : statusNames[turn.status],
        Number.isFinite(turn.elapsed_ms) ? duration(turn.elapsed_ms) : "",
        calls.length ? `${turn.output_truncated ? "最近 " : ""}${calls.length} 次工具调用` : ""].filter(Boolean).join(" · ");
      const rowIds = new Set(rows.map(row => row.id));
      for (const [id, block] of item.blocks) if (!rowIds.has(id)) { block.root.remove(); item.blocks.delete(id); }
      const workNodes = [], answerNodes = [];
      for (const [index,row] of rows.entries()) {
        let block = item.blocks.get(row.id);
        if (!block) {
          const root = node("div","chat-native-row"); root.dataset.kind = row.kind;
          const header = node("div","chat-native-heading");
          const name = node("code","");
          const state = node("span","chat-native-state"); header.append(name,state);
          const content = node(row.kind === "text" ? "div" : "pre",row.kind === "text" ? "chat-answer" : "chat-native-content");
          const progress = node("pre","chat-native-progress");
          const params = node("details","chat-native-parameters");
          const summary = node("summary","","查看原始参数");
          const raw = node("pre","chat-native-content"); params.append(summary,raw);
          root.append(header,content,params,progress);
          block = {root,header,name,state,content,params,raw,progress}; item.blocks.set(row.id,block);
        }
        // Only text after the final tool result is the reply. Earlier commentary
        // and native work stay in chronological order inside the disclosure.
        const inWork = row.kind !== "text" || index < lastTool;
        (inWork ? workNodes : answerNodes).push(block.root);
        block.root.hidden = row.kind === "status" && !usefulStatus(row);
        block.header.hidden = row.kind === "text";
        block.name.textContent = row.name || row.label || "";
        const stage = row.stage === "call" ? "调用" : "输出";
        block.state.textContent = row.kind === "tool" ? `${stage} · ${{running:"执行中",completed:"完成",failed:"失败",canceled:"已中止",unknown:"未确认"}[row.status] || row.status}` : "";
        block.root.dataset.status = row.status || "";
        if (row.kind === "text") globalThis.AGHMarkdown(block.content,row.content || "");
        else if (block.content.textContent !== row.content) block.content.textContent = row.content;
        block.content.hidden = !row.content;
        block.progress.hidden = !row.progress;
        if (block.progress.textContent !== row.progress) block.progress.textContent = row.progress || "";
        block.params.hidden = !row.parameters || row.parameters === row.content;
        if (block.raw.textContent !== row.parameters) block.raw.textContent = row.parameters || "";
      }
      // Reuse nodes so open parameter disclosures survive streaming updates.
      reconcile(item.activity,workNodes);
      reconcile(item.output,answerNodes);
      item.output.hidden = !rows.some((row,index) => row.kind === "text" && index > lastTool);
      item.reply = rows.filter((row,index) => row.kind === "text" && index > lastTool).map(row => row.content || "").join("\n\n");
      item.answerActions.hidden = !item.reply;
      item.copyAnswer.disabled = active;
      item.stop.hidden = !active;
      item.stop.disabled = pending || turn.status === "stopping";
      item.root.dataset.status = turn.status;
      const model = [turn.model, rows.find(row => row.kind === "status" && row.label === "init")?.content].find(value => value && value !== "default") || (turn.transport === "acp" ? "模型未确认" : "Claude");
      item.status.textContent = `${model}${turn.transport === "acp" ? " · ACP" : ""} · ${statusNames[turn.status] || turn.status}`;
      const isLatest = state.turns.at(-1)?.id === turn.id;
      if (item.wasLatest && !isLatest) item.work.open = false;
      item.wasLatest = isLatest;
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
      if (latest?.status === "canceled") el("chat-state").textContent = "已中止此条执行，可以继续发送消息。";
      if (latest?.status === "failed") el("chat-state").textContent = ["state_busy","session_busy"].includes(latest.error_code) ? latest.error : "此条执行失败，可以检查记录后继续发送消息。";
      if (latest?.status === "completed") el("chat-state").textContent = "执行完成，可以继续提问。";
      latestStatus = signature;
    }
    if (follow && !globalThis.getSelection?.()?.toString()) scroll.scrollTop = scroll.scrollHeight;
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
    if (!request || request.message !== message) request = {request_id:crypto.randomUUID(),message,...(state.selected_agent?{agent_id:state.selected_agent}:{})};
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
  el("chat-agent").onchange = async () => {
    if (pending || state.active_id) { el("chat-agent").value=state.selected_agent; return; }
    const agent_id=el("chat-agent").value;
    pending=true;controls();
    try {await action("/api/chat/agent",{agent_id});await refresh();el("chat-state").textContent="已切换智能体，各自的对话与上下文独立保留。";}
    catch(error){el("chat-agent").value=state.selected_agent;el("chat-state").textContent=error.message;}
    finally{pending=false;controls();}
  };
  document.addEventListener("agh:chat-send",send);
  document.addEventListener("agh:draft-change",controls);
  async function stopTurn(turnId) {
    if (!turnId || pending) return;
    pending = true; controls();
    try { await action("/api/chat/stop",{turn_id:turnId}); el("chat-state").textContent = "已请求中止，等待执行退出。"; await refresh(); }
    catch (error) { el("chat-state").textContent = error.message; }
    finally { pending = false; controls(); }
  };
  el("stop-chat").onclick = () => stopTurn(state.active_id);
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

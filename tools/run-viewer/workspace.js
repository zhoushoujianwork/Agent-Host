"use strict";

const $ = (id) => document.getElementById(id);
const titles = {workbench:"工作台",chat:"Chat",memory:"记忆图谱",executions:"执行记录",settings:"设置"};
const element = (tag, cls, text) => {const n=document.createElement(tag);n.className=cls;if(text!==undefined)n.textContent=text;return n;};
const storageKey = "agent-host.workspace.v1";
let saved = {}, records = [], groups = [], dragging = false;
try {saved=JSON.parse(localStorage.getItem(storageKey))||{};} catch { /* Storage is optional. */ }
if (!saved || typeof saved!=="object" || Array.isArray(saved)) saved={};
let positions = saved.positions && typeof saved.positions==="object" && !Array.isArray(saved.positions) ? saved.positions : {};
const draft = $("draft");
draft.value = typeof saved.draft==="string" ? saved.draft : "";
function persist(message) {
  try {localStorage.setItem(storageKey,JSON.stringify({positions,draft:draft.value}));if(message)$("draft-status").textContent=message;}
  catch {$("draft-status").textContent="浏览器存储不可用，草稿与布局只保留到此页面关闭。";}
}
function showChat(show) {$("chat-panel").hidden=!show;$("chat-toggle").setAttribute("aria-expanded",String(show));}
function navigate() {
  const requested=location.hash.slice(1).split("?")[0];
  const view=Object.hasOwn(titles,requested)?requested:"workbench";
  document.querySelectorAll(".view").forEach(n=>n.hidden=n.id!==view);
  document.querySelectorAll("[data-view]").forEach(n=>{n.classList.toggle("active",n.dataset.view===view);if(n.dataset.view===view)n.setAttribute("aria-current","page");else n.removeAttribute("aria-current");});
  $("view-title").textContent=titles[view];
  if(view==="chat")showChat(true);
  if(["settings","memory","executions"].includes(view))showChat(false);
  const frame=$(view).querySelector("iframe");
  if(frame){const key=new URLSearchParams(location.hash.split("?")[1]||"").get("key");const src=frame.dataset.src+(key?`?key=${encodeURIComponent(key)}`:"");if(!frame.getAttribute("src")||view==="executions"&&key&&frame.dataset.key!==key){frame.src=src;frame.dataset.key=key||"";}}
}
for(const frame of document.querySelectorAll("iframe"))frame.addEventListener("load",()=>{
  const doc=frame.contentDocument;if(!doc)return;
  const css=doc.createElement("link");css.rel="stylesheet";css.href="/embedded.css";doc.head.append(css);
  doc.addEventListener("click",event=>{const a=event.target.closest("a[href]");if(!a)return;const url=new URL(a.href);if(url.origin!==location.origin)return;
    const routes={"/":"workbench","/index.html":"workbench","/memory.html":"memory","/settings.html":"settings","/executions.html":"executions"};
    if(routes[url.pathname]){event.preventDefault();const route=url.pathname==="/"&&url.searchParams.has("key")?"executions":routes[url.pathname];location.hash=route+(url.search||"");}
  });
});
function conversationGroups(rows) {
  const map=new Map();
  for(const row of rows){const key=JSON.stringify([row.source?.id,row.source?.kind,row.agent_id,row.audience_id,row.session_id||row.native_session_id||row.id]);let group=map.get(key);if(!group){group={key,rows:[]};map.set(key,group);}group.rows.push(row);}
  return [...map.values()].map(group=>{group.rows.sort((a,b)=>String(b.created_at).localeCompare(String(a.created_at)));group.latest=group.rows[0];return group;}).sort((a,b)=>String(b.latest.created_at).localeCompare(String(a.latest.created_at)));
}
function defaultPosition(index){const columns=Math.max(1,Math.floor(($("canvas-scroll").clientWidth-48)/300));return {x:24+(index%columns)*300,y:24+Math.floor(index/columns)*265};}
function positionFor(group,index){const p=positions[group.key];return p&&Number.isFinite(p.x)&&Number.isFinite(p.y)&&p.x>=0&&p.y>=0&&p.x<=100000&&p.y<=100000?p:defaultPosition(index);}
function sizeCanvas(){let width=$("canvas-scroll").clientWidth,height=$("canvas-scroll").clientHeight;for(const card of $("canvas").querySelectorAll(".conversation")){width=Math.max(width,card.offsetLeft+card.offsetWidth+30);height=Math.max(height,card.offsetTop+card.offsetHeight+30);}$("canvas").style.width=`${width}px`;$("canvas").style.height=`${height}px`;}
function render(){
  const query=$("board-search").value.trim().toLowerCase();
  const shown=groups.filter(g=>g.rows.some(r=>[r.input,r.agent_id,r.project_name,r.project_id,r.source?.name].join(" ").toLowerCase().includes(query)));
  $("canvas").replaceChildren();$("board-count").textContent=`${shown.length} / ${groups.length} 个对话`;
  if(!shown.length)$("canvas").append(element("div","board-empty",groups.length?"没有匹配的对话。试试其他关键词。":"还没有对话记录。通过已配置的 CLI 或渠道开始对话后，在这里刷新查看。"));
  for(const group of shown){const row=group.latest;const index=groups.indexOf(group);const p=positionFor(group,index);
    const card=element("article","conversation");card.style.left=`${p.x}px`;card.style.top=`${p.y}px`;
    const grip=element("button","card-grip");grip.type="button";grip.setAttribute("aria-label",`移动对话：${String(row.input||row.id).slice(0,80)}`);
    const status=element("span","card-status",({completed:"已完成",running:"执行中",failed:"失败",canceled:"已取消"})[row.execution_status]||row.execution_status||"未知");status.dataset.status=row.execution_status;
    grip.append(element("span","",`⠿ ${row.source?.name||"Local"}`),status);
    const content=element("div","card-content");content.append(element("h2","",row.input||row.label||"未记录请求正文"),element("p","",`◈ ${row.agent_id||"default"} · ${row.project_name||row.project_id||"未关联项目"}`),element("p","",`${group.rows.length} 次执行 · ${row.created_at?new Date(row.created_at).toLocaleString("zh-CN"):"时间未记录"}`));
    const actions=element("div","card-actions"),open=element("a","","查看最近执行 ↗"),context=element("button","","关联到草稿");open.href=`#executions?key=${encodeURIComponent(row.key)}`;context.onclick=()=>{$("context-title").textContent=row.input||row.id;$("context-subtitle").textContent=`${row.agent_id||"default"} · ${row.id}`;draft.value+=`${draft.value?"\n\n":""}参考执行 ${row.id}：${row.input||""}`;persist("已添加参考信息到本地草稿，尚未发送。");showChat(true);draft.focus();};actions.append(open,context);content.append(actions);card.append(grip,content);$("canvas").append(card);
    const move=(x,y)=>{const point={x:Math.max(0,Math.min(100000,x)),y:Math.max(0,Math.min(100000,y))};positions[group.key]=point;card.style.left=`${point.x}px`;card.style.top=`${point.y}px`;};
    let start=null;
    grip.onpointerdown=e=>{if(e.button!==0)return;start={id:e.pointerId,x:e.clientX,y:e.clientY,left:card.offsetLeft,top:card.offsetTop,scrollX:$("canvas-scroll").scrollLeft,scrollY:$("canvas-scroll").scrollTop};dragging=true;grip.setPointerCapture(e.pointerId);card.classList.add("moving");};
    grip.onpointermove=e=>{if(!start||start.id!==e.pointerId)return;move(start.left+e.clientX-start.x+$("canvas-scroll").scrollLeft-start.scrollX,start.top+e.clientY-start.y+$("canvas-scroll").scrollTop-start.scrollY);sizeCanvas();};
    const stop=()=>{if(!start)return;start=null;dragging=false;card.classList.remove("moving");persist();sizeCanvas();};grip.onpointerup=stop;grip.onpointercancel=stop;grip.onlostpointercapture=stop;
    grip.onkeydown=e=>{const delta={ArrowLeft:[-20,0],ArrowRight:[20,0],ArrowUp:[0,-20],ArrowDown:[0,20]}[e.key];if(delta){e.preventDefault();move(card.offsetLeft+delta[0],card.offsetTop+delta[1]);persist();sizeCanvas();}};
  }sizeCanvas();
}
async function refresh(){
  if(dragging)return;$("refresh").disabled=true;
  try{const response=await fetch("/api/records",{cache:"no-store"});if(!response.ok)throw new Error(`HTTP ${response.status}`);const value=await response.json();if(!Array.isArray(value.records))throw new Error("无效的记录响应");records=value.records;groups=conversationGroups(records);render();const errors=(value.sources||[]).filter(s=>["read_failed","missing_database"].includes(s.status));$("connection").textContent=errors.length?"部分数据源不可用":"本地记录已同步";$("board-notice").textContent=errors.length?errors.map(s=>`${s.channel||"local"}: ${s.error||s.status}`).join(" · "):"拖动卡片标题可调整位置；聚焦标题后也可用方向键移动。布局仅保存在当前浏览器。";}
  catch(error){$("connection").textContent="读取失败";$("board-notice").textContent=`无法读取记录：${error.message}。请检查数据源后重试。`;if(!records.length)$("canvas").replaceChildren(element("div","board-empty","无法加载对话记录，点击刷新重试。"));}
  finally{$("refresh").disabled=false;}
}
$("refresh").onclick=refresh;$("board-search").oninput=render;
$("arrange").onclick=()=>{positions={};persist();render();};
$("chat-toggle").onclick=()=>showChat($("chat-panel").hidden);
$("save-draft").onclick=()=>persist("草稿已保存在此浏览器，尚未发送。");
$("clear-draft").onclick=()=>{draft.value="";$("context-title").textContent="新的工作";$("context-subtitle").textContent="选择对话卡片查看关联记录";persist("本地草稿已清空。");};
draft.oninput=()=>persist("草稿已自动保存，尚未发送。");
document.querySelectorAll("[data-prompt]").forEach(button=>button.onclick=()=>{draft.value=button.dataset.prompt;persist("草稿已自动保存，尚未发送。");showChat(true);draft.focus();});
window.addEventListener("hashchange",navigate);window.addEventListener("resize",sizeCanvas);
if(matchMedia("(max-width:850px)").matches)showChat(false);
// Preserve old bookmarked execution links at the new entry point.
const legacyKey=new URLSearchParams(location.search).get("key");if(legacyKey&&!location.hash)location.hash=`executions?key=${encodeURIComponent(legacyKey)}`;
navigate();refresh();

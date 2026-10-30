"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),vm=require("node:vm"),fs=require("node:fs"),path=require("node:path");
const flush=()=>new Promise(resolve=>setImmediate(resolve));
async function setup() {
  const nodes=new Map(),events={},calls=[],timers=[],copied=[];
  let copyFails=false,selection="";
  function make(tag="div") {return {tag,hidden:false,disabled:false,value:"",textContent:"",dataset:{},children:[],scrollHeight:100,scrollTop:0,clientHeight:100,append(...v){this.children.push(...v);},replaceChildren(...v){this.children=v;},remove(){this.removed=true;},addEventListener(k,v){this[k]=v;}};}
  const el=id=>{if(!nodes.has(id))nodes.set(id,make());return nodes.get(id);};
  let snapshot={enabled:true,mutation_token:"token",turns:[],active_id:null},failResponse=false,sequence=0;
  el("draft").oninput=()=>{};
  const context={getSelection:()=>({toString:()=>selection}),navigator:{clipboard:{async writeText(text){if(copyFails)throw Error("denied");copied.push(text);}}},document:{getElementById:el,createElement:make,addEventListener(k,v){events[k]=v;}},TextEncoder,crypto:{randomUUID(){return "request-"+(++sequence);}},setTimeout(callback){timers.push(callback);},async fetch(url,options={}){
    calls.push({url,...options});
    if(options.method==="POST"){
      const value=JSON.parse(options.body);
      if(url==="/api/chat"){
        if(!snapshot.turns.length)snapshot={...snapshot,active_id:"turn",turns:[{id:"turn",input:value.message,text:"",output:[],status:"running",record_key:null}]};
        if(failResponse){failResponse=false;throw Error("lost reply");}
      }
      if(url==="/api/chat/agent")snapshot={...snapshot,selected_agent:value.agent_id,turns:[]};
      if(url==="/api/chat/session")snapshot={...snapshot,context_id:value.key,selected_agent:"daily",turns:[{id:value.key,input:value.key,text:"Original reply",status:"completed",read_only:true}]};
      return {ok:true,json:async()=>({turn_id:"turn"})};
    }
    return {ok:true,json:async()=>JSON.parse(JSON.stringify(snapshot))};
  }};
  vm.createContext(context);
  for(const file of ["markdown-parser.js","markdown.js","chat.js"]) vm.runInContext(fs.readFileSync(path.join(__dirname,file),"utf8"),context);
  await flush();
  return {el,calls,events,copied,select(text){selection=text;},denyClipboard(){copyFails=true;},set snapshot(v){snapshot=v;},get snapshot(){return snapshot;},loseResponse(){failResponse=true;},async poll(){timers.shift()();await flush();}};
}
test("accepted sends clear the matching draft and render HTML as inert Markdown text",async()=>{
  const h=await setup();h.el("draft").value="hello";h.events["agh:draft-change"]();await h.el("send-chat").onclick();
  assert.equal(h.el("draft").value,"");assert.equal(h.el("stop-chat").hidden,false);
  h.snapshot.turns[0].output=[{id:"1",kind:"text",content:'<img src=x onerror="bad()">'}];await h.poll();
  const turn=h.el("chat-messages").children[0];assert.equal(turn.children[2].children[0].children[1].children[0].children[0].textContent,'<img src=x onerror="bad()">');
  assert.equal(h.calls.filter(c=>c.method==="POST").length,1);
});

test("session selection replaces history, separates drafts and sends the selected context",async()=>{
  const h=await setup();
  h.snapshot={...h.snapshot,selected_agent:"daily",context_id:"first"};await h.poll();
  h.el("draft").value="draft for first";
  h.events["agh:session-select"]({detail:{key:"second"}});
  assert.equal(h.el("send-chat").disabled,true);
  await flush();await flush();
  assert.equal(h.el("draft").value,"");
  assert.equal(h.el("chat-messages").children.at(-1).children[0].textContent,"second");
  h.el("draft").value="draft for second";
  h.events["agh:session-select"]({detail:{key:"first"}});await flush();await flush();
  assert.equal(h.el("draft").value,"draft for first");
  h.events["agh:session-select"]({detail:{key:"second"}});await flush();await flush();
  assert.equal(h.el("draft").value,"draft for second");
  await h.el("send-chat").onclick();
  const send=h.calls.findLast(c=>c.url==="/api/chat"&&c.method==="POST");
  assert.equal(JSON.parse(send.body).context_id,"second");
});
test("a lost response keeps text and request identity for explicit retry without automatic replay",async()=>{
  const h=await setup();h.el("draft").value="hello";h.events["agh:draft-change"]();h.loseResponse();await h.el("send-chat").onclick();
  assert.equal(h.el("draft").value,"hello");const first=h.calls.find(c=>c.method==="POST");
  await h.el("send-chat").onclick();const posts=h.calls.filter(c=>c.method==="POST");
  assert.equal(posts.length,2);assert.equal(JSON.parse(first.body).request_id,JSON.parse(posts[1].body).request_id);
});

test("switching sessions preserves a lost-response request identity for safe retry",async()=>{
  const h=await setup();h.snapshot={...h.snapshot,selected_agent:"daily",context_id:"first"};await h.poll();
  h.el("draft").value="continue this task";h.events["agh:draft-change"]();h.loseResponse();await h.el("send-chat").onclick();
  const first=h.calls.find(c=>c.url==="/api/chat"&&c.method==="POST");
  for(const key of ["second","first"]){h.events["agh:session-select"]({detail:{key}});await flush();await flush();}
  assert.equal(h.el("draft").value,"continue this task");
  await h.el("send-chat").onclick();
  const retry=h.calls.findLast(c=>c.url==="/api/chat"&&c.method==="POST");
  assert.equal(JSON.parse(retry.body).request_id,JSON.parse(first.body).request_id);
});
test("stop remains pending until the Host snapshot confirms process exit",async()=>{
  const h=await setup();h.el("draft").value="hello";h.events["agh:draft-change"]();await h.el("send-chat").onclick();
  await h.el("stop-chat").onclick();assert.equal(h.el("send-chat").hidden,false);
  h.snapshot.active_id=null;h.snapshot.turns[0].status="canceled";h.snapshot.needs_new=true;await h.poll();
  assert.equal(h.el("send-chat").disabled,true);assert.equal(h.el("new-chat").disabled,false);
});

test("attached live runs expose stop, update output and bind stop to the selected context",async()=>{
  const h=await setup();
  h.snapshot={...h.snapshot,context_id:"attached",active_id:"history-a",active_ids:["history-a"],needs_new:true,
    turns:[{id:"history-a",status:"running",read_only:true,can_stop:true,text:"first",output:[]}]};
  await h.poll();
  assert.equal(h.el("stop-chat").hidden,false);
  assert.equal(h.el("chat-messages").children[0].children[9].hidden,false);
  h.snapshot.turns[0].text="first second";await h.poll();
  const turn=h.el("chat-messages").children[0];
  assert.equal(turn.children[2].children[0].children[1].children[0].children[0].textContent,"first second");
  await h.el("stop-chat").onclick();
  assert.deepEqual(JSON.parse(h.calls.find(c=>c.url==="/api/chat/stop").body),{turn_id:"history-a",context_id:"attached"});
  h.snapshot.turns[0].can_stop=false;await h.poll();
  assert.equal(h.el("stop-chat").hidden,true);
  assert.equal(turn.children[9].hidden,true);
});

test("overlapping room messages keep send available and stop the selected original message",async()=>{
  const h=await setup();
  h.snapshot.active_id="a";h.snapshot.active_ids=["a","b"];h.snapshot.max_concurrent=4;
  h.snapshot.turns=[{id:"a",input:"Alice",status:"running",output:[]},{id:"b",input:"Bob",status:"running",output:[]}];
  h.el("draft").value="third request";await h.poll();
  assert.equal(h.el("send-chat").hidden,false);assert.equal(h.el("send-chat").disabled,false);
  assert.equal(h.el("stop-chat").hidden,true);
  await h.el("chat-messages").children[1].children[9].onclick();
  const stop=h.calls.find(call=>call.url==="/api/chat/stop");
  assert.equal(JSON.parse(stop.body).turn_id,"b");
});

test("native tool payloads stay in transcript order and use safe literal nodes",async()=>{
  const h=await setup();h.el("draft").value="hello";h.events["agh:draft-change"]();await h.el("send-chat").onclick();
  h.snapshot.turns[0].output=[{id:"1",kind:"text",content:"before"},{id:"2",kind:"tool",stage:"call",name:"Bash",status:"running",content:"printf '<b>literal</b>'",parameters:'{"command":"printf"}'},{id:"3",kind:"tool",stage:"result",name:"Bash",status:"completed",content:"<b>literal</b>"},{id:"4",kind:"text",content:"after"}];
  await h.poll();const turn=h.el("chat-messages").children[0],rows=turn.children[1].children[1].children;
  assert.deepEqual(rows.map(row=>row.dataset.kind),["text","tool","tool"]);
  assert.equal(turn.children[2].children[0].dataset.kind,"text");
  assert.equal(rows[1].children[0].children[0].textContent,"Bash");assert.equal(rows[2].children[1].textContent,"<b>literal</b>");
  assert.equal(rows[1].children[2].hidden,false);rows[1].children[2].open=true;
  h.snapshot.turns[0].output[1].status="completed";await h.poll();assert.equal(rows[1].children[2].open,true);
});

test("completed work folds separately from Markdown reply and counts calls only",async()=>{
  const h=await setup();h.snapshot.turns=[{id:"old",input:"test",status:"completed",elapsed_ms:87000,output:[
    {id:"1",kind:"status",label:"init",content:"claude-test"},
    {id:"2",kind:"text",content:"Checking."},
    {id:"3",kind:"tool",stage:"call",name:"Bash",status:"completed",content:"pwd"},
    {id:"4",kind:"tool",stage:"result",name:"Bash",status:"completed",content:"/tmp"},
    {id:"5",kind:"status",label:"status",content:'{"status":"requesting"}'},
    {id:"6",kind:"text",content:"**Done**\n\n- Read `README.md`"},
    {id:"7",kind:"status",label:"success",content:""}]}];await h.poll();
  const turn=h.el("chat-messages").children[0],work=turn.children[1];
  assert.equal(work.open,false);assert.match(work.children[0].textContent,/1m 27s · 1 次工具调用/);
  assert.equal(work.children[1].children[0].hidden,true);
  const answer=turn.children[2].children[0].children[1];
  assert.equal(answer.children[0].children[0].tag,"strong");
  assert.equal(answer.children[1].tag,"ul");
  work.open=true;await h.poll();assert.equal(work.open,true);
  assert.match(turn.children[3].textContent,/claude-test/);
});

test("ACP footer distinguishes observed model from a default configuration alias",async()=>{
  const h=await setup();h.snapshot.turns=[{id:"acp",input:"hello",status:"completed",transport:"acp",model:"default",output:[]}];await h.poll();
  const turn=h.el("chat-messages").children[0];assert.equal(turn.children[3].textContent,"模型未确认 · ACP · 执行完成");
  h.snapshot.turns[0].model="observed-native-model";await h.poll();assert.equal(turn.children[3].textContent,"observed-native-model · ACP · 执行完成");
});

test("reply success and memory failure are separate, with the recorded error shown literally",async()=>{
  const h=await setup();
  h.snapshot.turns=[{id:"partial",input:"hi",status:"failed",execution_status:"completed",memory_status:"failed",
    memory_error:"Jev provider custom returned HTTP 404 (Not Found) <script>bad()</script>",
    error:"执行未正常完成，请查看执行记录或检查模型连接。",model:"Claude",text:"Hello",output:[]}];
  await h.poll();
  const turn=h.el("chat-messages").children[0];
  assert.equal(turn.children[3].textContent,"Claude · 执行完成");
  assert.equal(turn.children[2].hidden,false);
  assert.equal(turn.children[8].hidden,false);
  assert.equal(turn.children[3].dataset.status,"completed");
  assert.equal(turn.children[4].textContent,"记忆处理 · 失败");
  assert.equal(turn.children[4].dataset.status,"failed");
  assert.match(turn.children[5].textContent,/记忆处理：Jev.*HTTP 404.*<script>/);
  assert.equal(turn.children[5].children.length,0);
  assert.doesNotMatch(turn.children[5].textContent,/执行未正常完成/);
  assert.match(h.el("chat-state").textContent,/回复已完成，记忆处理失败/);
  h.snapshot.turns[0].status="running";h.snapshot.turns[0].memory_status=undefined;h.snapshot.turns[0].memory_error="";h.snapshot.turns[0].error="";
  await h.poll();
  assert.equal(turn.children[3].textContent,"Claude · 执行完成");
  assert.equal(turn.children[4].textContent,"记忆处理 · 正在处理");
});

test("execution failures remain failures and absent memory evidence stays unknown",async()=>{
  const h=await setup();
  h.snapshot.turns=[{id:"failed",input:"hi",status:"failed",execution_status:"failed",memory_status:"skipped",execution_error:"Model request timed out",output:[]}];
  await h.poll();const turn=h.el("chat-messages").children[0];
  assert.match(turn.children[3].textContent,/执行失败/);
  assert.equal(turn.children[4].textContent,"记忆处理 · 已跳过");
  assert.equal(turn.children[5].textContent,"执行：Model request timed out");
  h.snapshot.turns[0].memory_status=undefined;await h.poll();
  assert.equal(turn.children[4].textContent,"记忆处理 · 状态未确认");
});

test("Agent picker keeps drafts separate and cannot switch during execution",async()=>{
  const h=await setup();h.snapshot.agents=[{id:"a",name:"A"},{id:"b",name:"B"}];h.snapshot.selected_agent="a";await h.poll();
  assert.equal(h.el("chat-agent").disabled,false);
  h.el("draft").value="draft A";
  h.el("chat-agent").value="b";await h.el("chat-agent").onchange();
  assert.equal(h.el("draft").value,"");h.el("draft").value="draft B";
  h.el("chat-agent").value="a";await h.el("chat-agent").onchange();
  assert.equal(h.el("draft").value,"draft A");
  await h.el("send-chat").onclick();
  assert.equal(JSON.parse(h.calls.find(c=>c.url==="/api/chat"&&c.method==="POST").body).agent_id,"a");
  assert.equal(h.el("chat-agent").disabled,true);
  const count=h.calls.filter(c=>c.url==="/api/chat/agent").length;
  h.el("chat-agent").value="b";await h.el("chat-agent").onchange();
  assert.equal(h.calls.filter(c=>c.url==="/api/chat/agent").length,count);
});

test("unchanged polls preserve an open native Agent selector",async()=>{
  const h=await setup();h.snapshot={...h.snapshot,selected_agent:"a",agents:[{id:"a",name:"A"},{id:"b",name:"B"}]};await h.poll();
  const picker=h.el("chat-agent");let writes=0,current=picker.value;
  Object.defineProperty(picker,"value",{get(){return current;},set(value){writes++;current=value;}});
  await h.poll();assert.equal(writes,0);assert.equal(current,"a");
});

test("a single default Agent does not show a redundant Chat selector",async()=>{
  const h=await setup();h.snapshot={...h.snapshot,selected_agent:"daily",agents:[{id:"daily",name:"Default"}]};await h.poll();assert.equal(h.el("chat-agent-bar").hidden,true);
  h.snapshot.agents.push({id:"project",name:"Project"});await h.poll();assert.equal(h.el("chat-agent-bar").hidden,false);
});

test("message, reply and code copy preserve source and report clipboard denial",async()=>{
  const h=await setup();
  h.snapshot.turns=[{id:"copy",input:"original prompt",status:"completed",output:[{id:"1",kind:"text",content:"**Answer**\n\n```js\nconst value = 1;\n```"}]}];
  await h.poll();const turn=h.el("chat-messages").children[0];
  await turn.children[7].children[0].onclick();
  await turn.children[8].children[0].onclick();
  const pre=turn.children[2].children[0].children[1].children[1];
  await pre.children[1].onclick();
  assert.equal(h.copied[0],"original prompt");
  assert.equal(h.copied[1],h.snapshot.turns[0].output[0].content);
  assert.equal(h.copied[2].trim(),"const value = 1;");
  h.denyClipboard();await turn.children[7].children[0].onclick();
  assert.match(h.el("chat-state").textContent,/Ctrl\/Cmd\+C/);
});

test("polling does not scroll away selected text and busy launch remains sendable",async()=>{
  const h=await setup();h.select("selected text");h.el("chat-scroll").scrollTop=12;
  h.snapshot.turns=[{id:"busy",input:"prompt",status:"failed",error_code:"state_busy",error:"环境忙，本次尚未开始",output:[]}];
  await h.poll();assert.equal(h.el("chat-scroll").scrollTop,12);
  assert.equal(h.el("chat-state").textContent,"环境忙，本次尚未开始");
  h.el("draft").value="manual retry";h.events["agh:draft-change"]();
  assert.equal(h.el("send-chat").disabled,false);
  h.snapshot.turns[0].error_code="session_busy";
  h.snapshot.turns[0].id="same-session";
  h.snapshot.turns[0].error="此对话忙，独立对话可并发";
  await h.poll();assert.equal(h.el("chat-state").textContent,"此对话忙，独立对话可并发");
});

function visibleText(node) {
  if (node.hidden) return "";
  return node.textContent + node.children.map(visibleText).join("");
}
test("closeout suffix never flashes while streaming and is excluded from copied replies",async()=>{
  const h=await setup(), answer="AGH_LOCAL_ACP_OK";
  const protocol='<agh-closeout>{"run_id":"run","outcome":"resolved"}</agh-closeout>';
  h.snapshot.turns=[{id:"closeout",input:"smoke",status:"running",output:[{id:"answer",kind:"text",content:answer}]}];
  for(let end=1;end<=protocol.length;end++){
    h.snapshot.turns[0].output[0].content=answer+"\n\n"+protocol.slice(0,end);
    await h.poll();
    const output=h.el("chat-messages").children[0].children[2];
    assert.equal(visibleText(output).trim(),answer,`stream boundary ${end}`);
  }
  h.snapshot.turns[0].status="completed";await h.poll();
  const turn=h.el("chat-messages").children[0];
  await turn.children[8].children[0].onclick();assert.equal(h.copied.at(-1),answer);
  assert.equal(h.snapshot.turns[0].output[0].content,answer+"\n\n"+protocol);
});
test("restored replies and delimiters split across text rows use the same presentation filter",async()=>{
  const h=await setup();
  h.snapshot.turns=[{id:"restored",status:"completed",text:'AGH_LOCAL_ACP_OK\n<agh-closeout>{"outcome":"resolved"}</agh-closeout>',read_only:true}];
  await h.poll();let turn=h.el("chat-messages").children[0];
  assert.equal(visibleText(turn.children[2]).trim(),"AGH_LOCAL_ACP_OK");
  h.snapshot.turns[0].output=[{id:"a",kind:"text",content:"AGH_LOCAL_ACP_OK\n<agh-"},{id:"b",kind:"text",content:'closeout>{"outcome":"resolved"}</agh-closeout>'}];
  await h.poll();turn=h.el("chat-messages").children[0];
  assert.equal(visibleText(turn.children[2]).trim(),"AGH_LOCAL_ACP_OK");
  await turn.children[8].children[0].onclick();assert.equal(h.copied.at(-1),"AGH_LOCAL_ACP_OK");
});
test("ordinary reply text and fenced or inline protocol examples remain copyable",async()=>{
  for(const answer of ["Use <agh-closeout> as the tag name.","The <agh-closeout>{}</agh-closeout> example is ordinary prose.","Keep trailing whitespace.  ","Example: `<agh-closeout>{}</agh-closeout>`","Example: ``<agh-closeout>{}</agh-closeout>``",'```xml\n<agh-closeout>{}</agh-closeout>\n```',"~~~xml\n<agh-closeout>{}</agh-closeout>\n~~~","A < B and <img src=x>"]){
    const h=await setup();h.snapshot.turns=[{id:"example",status:"completed",text:answer}];await h.poll();
    await h.el("chat-messages").children[0].children[8].children[0].onclick();assert.equal(h.copied.at(-1),answer);
  }
});

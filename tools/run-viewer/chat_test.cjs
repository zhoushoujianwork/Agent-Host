"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),vm=require("node:vm"),fs=require("node:fs"),path=require("node:path");
const flush=()=>new Promise(resolve=>setImmediate(resolve));
async function setup() {
  const nodes=new Map(),events={},calls=[],timers=[];
  function make(tag="div") {return {tag,hidden:false,disabled:false,value:"",textContent:"",dataset:{},children:[],scrollHeight:100,scrollTop:0,clientHeight:100,append(...v){this.children.push(...v);},replaceChildren(...v){this.children=v;},remove(){this.removed=true;},addEventListener(k,v){this[k]=v;}};}
  const el=id=>{if(!nodes.has(id))nodes.set(id,make());return nodes.get(id);};
  let snapshot={enabled:true,mutation_token:"token",turns:[],active_id:null},failResponse=false,sequence=0;
  el("draft").oninput=()=>{};
  const context={document:{getElementById:el,createElement:make,addEventListener(k,v){events[k]=v;}},TextEncoder,crypto:{randomUUID(){return "request-"+(++sequence);}},setTimeout(callback){timers.push(callback);},async fetch(url,options={}){
    calls.push({url,...options});
    if(options.method==="POST"){
      const value=JSON.parse(options.body);
      if(url==="/api/chat"){
        if(!snapshot.turns.length)snapshot={...snapshot,active_id:"turn",turns:[{id:"turn",input:value.message,text:"",steps:[],status:"running",record_key:null}]};
        if(failResponse){failResponse=false;throw Error("lost reply");}
      }
      return {ok:true,json:async()=>({turn_id:"turn"})};
    }
    return {ok:true,json:async()=>JSON.parse(JSON.stringify(snapshot))};
  }};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,"chat.js"),"utf8"),context);
  await flush();
  return {el,calls,events,set snapshot(v){snapshot=v;},get snapshot(){return snapshot;},loseResponse(){failResponse=true;},async poll(){timers.shift()();await flush();}};
}
test("accepted sends clear the matching draft and render streamed text as literal content",async()=>{
  const h=await setup();h.el("draft").value="hello";h.events["agh:draft-change"]();await h.el("send-chat").onclick();
  assert.equal(h.el("draft").value,"");assert.equal(h.el("stop-chat").hidden,false);
  h.snapshot.turns[0].text='<img src=x onerror="bad()">';await h.poll();
  const turn=h.el("chat-messages").children[0];assert.equal(turn.children[2].textContent,'<img src=x onerror="bad()">');
  assert.equal(h.calls.filter(c=>c.method==="POST").length,1);
});
test("a lost response keeps text and request identity for explicit retry without automatic replay",async()=>{
  const h=await setup();h.el("draft").value="hello";h.events["agh:draft-change"]();h.loseResponse();await h.el("send-chat").onclick();
  assert.equal(h.el("draft").value,"hello");const first=h.calls.find(c=>c.method==="POST");
  await h.el("send-chat").onclick();const posts=h.calls.filter(c=>c.method==="POST");
  assert.equal(posts.length,2);assert.equal(JSON.parse(first.body).request_id,JSON.parse(posts[1].body).request_id);
});
test("stop remains pending until the Host snapshot confirms process exit",async()=>{
  const h=await setup();h.el("draft").value="hello";h.events["agh:draft-change"]();await h.el("send-chat").onclick();
  await h.el("stop-chat").onclick();assert.equal(h.el("send-chat").hidden,true);
  h.snapshot.active_id=null;h.snapshot.turns[0].status="canceled";h.snapshot.needs_new=true;await h.poll();
  assert.equal(h.el("send-chat").disabled,true);assert.equal(h.el("new-chat").disabled,false);
});

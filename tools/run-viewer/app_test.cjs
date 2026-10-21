"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),vm=require("node:vm");
const source=fs.readFileSync(require("node:path").join(__dirname,"app.js"),"utf8");
function setup(records) {
  const nodes=new Map(),selected=[];
  const dom=(tag,className="",text="")=>({tag,className,textContent:text,children:[],scrollTop:0,value:"",listeners:{},
    append(...children){this.children.push(...children);},replaceChildren(){this.children=[];},
    setAttribute(key,value){this[key]=value;},addEventListener(key,callback){this.listeners[key]=callback;}});
  const $=id=>{if(!nodes.has(id))nodes.set(id,dom("div"));return nodes.get(id);};
  $("source-filter").value="all";
  const state={index:{records,database_count:records.length,database_limit:200},key:null};
  const context={$ ,state,dom,json:JSON.stringify,short:value=>String(value).slice(0,8),date:value=>value,
    badge:status=>dom("span","badge",status),runLabels:{},select:key=>selected.push(key),placeholder:(node,text)=>node.append(dom("p","",text))};
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf("const expandedSessions"),source.indexOf("function metric(")),context);
  return {...context,selected,groups:()=>JSON.parse(vm.runInContext("JSON.stringify(sessionGroups(state.index.records))",context))};
}
const row=(id,fields={})=>({key:id,id,input:id,source:{id:"cli:db",kind:"database",name:"CLI"},agent_id:"daily",audience_id:"owner",created_at:`2026-10-21T00:00:0${id}Z`,execution_status:"completed",...fields});

test("room groups include forks, order intake rather than completion, and sort sessions by recent activity",()=>{
  const h=setup([row("3",{room_id:"a",session_id:"fork-3"}),row("1",{room_id:"a",session_id:"fork-1"}),row("2",{room_id:"b"})]);
  const groups=h.groups();
  assert.deepEqual(groups.map(g=>g.rows.map(r=>r.id)),[["1","3"],["2"]]);
  h.state.index.records=[row("1",{room_id:"a",received_at:"2026-10-21T08:00:00+08:00"}),row("2",{room_id:"a",received_at:"2026-10-20T23:59:59Z"})];
  assert.deepEqual(h.groups()[0].rows.map(r=>r.id),["2","1"]);
});
test("source, Agent, audience and reset room boundaries never merge",()=>{
  const h=setup([row("1",{room_id:"room:old"}),row("2",{room_id:"room:new"}),
    row("3",{room_id:"room:old",agent_id:"other"}),row("4",{room_id:"room:old",audience_id:"group"}),
    row("5",{room_id:"room:old",source:{id:"feishu:db",kind:"database",name:"Feishu"}})]);
  assert.equal(h.groups().length,5);
});
test("legacy sessions group only by recorded identity; unidentified records remain separate",()=>{
  const h=setup([row("1",{session_id:"old"}),row("2",{session_id:"old"}),row("3"),row("4"),
    row("5",{native_session_id:"native"}),row("6",{native_session_id:"native"})]);
  assert.deepEqual(h.groups().map(g=>g.rows.length),[2,1,1,2]);
});
test("selected conversation opens, shows numbered messages and sender, and retains detail links and expansion",()=>{
  const h=setup([row("2",{room_id:"a",sender_name:"Bob"}),row("1",{room_id:"a",sender_name:"Alice"})]);
  h.state.key="2";h.$("record-list").scrollTop=40;h.renderList();
  const group=h.$("record-list").children[0];
  assert.equal(group.open,true);assert.equal(h.$("record-list").scrollTop,40);
  assert.match(group.children[1].children[0].children[0].textContent,/^1\./);
  assert.equal(group.children[1].children[2].textContent,"Alice");
  group.children[2].listeners.click();assert.deepEqual(h.selected,["2"]);
  group.open=false;group.listeners.toggle();h.renderList();assert.equal(h.$("record-list").children[0].open,false);
  h.$("search").value="alice";h.renderList();
  assert.equal(h.$("record-list").children[0].children.length,3);
  assert.match(h.$("record-count").textContent,/1 个会话 · 2 条/);
});

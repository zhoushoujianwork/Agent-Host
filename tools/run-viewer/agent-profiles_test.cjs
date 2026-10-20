"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),vm=require("node:vm"),fs=require("node:fs"),path=require("node:path");
function setup(){
  class Element{
    constructor(tag){this.tag=tag;this.children=[];this.textContent="";this.value="";this.attributes={};}
    append(...items){this.children.push(...items);}
    replaceChildren(...items){this.children=items;}
    setAttribute(key,value){this.attributes[key]=value;}
  }
  const window={},context={window,document:{createElement:tag=>new Element(tag)}};
  for(const file of ["settings-fields.js","agent-profiles.js"])vm.runInNewContext(fs.readFileSync(path.join(__dirname,file),"utf8"),context);
  const root=new Element("div"),draft={default_agent:"default",agents:[{id:"default",name:"Saved name"}]},file={id:"file-a",category:"agents"};
  const profile={key:"a:dingtalk:default",file_id:"file-a",id:"default",name:"Saved name",destination:"dingtalk",default:true,groups:[],skills:[],connectors:[],instructions:[],activity:[],evidence_state:"unknown",projects:null};
  const walk=(n=root)=>[n,...n.children.flatMap(walk)];
  const click=text=>{const n=walk().find(n=>n.tag==="button"&&n.textContent===text);assert.ok(n,text);n.onclick();};
  const render=(items=[profile],editor)=>window.AGHAgentProfiles.render(root,items,file,draft,window.AGHSettings.fields(draft,"agents"),field=>{
    const input=new Element("input");input.value=field.value;input.path=field.path;input.oninput=()=>{draft.agents[0].name=input.value;};return input;
  },null,editor);
  return {root,draft,profile,walk,click,render,profiles:window.AGHAgentProfiles};
}
test("unsaved edits survive profile tabs while the heading remains saved configuration",()=>{
  const h=setup();h.render();let input=h.walk().find(n=>n.path?.at(-1)==="name");input.value="Unsaved name";input.oninput();
  h.click("渠道与群聊");h.click("概览");input=h.walk().find(n=>n.path?.at(-1)==="name");
  assert.equal(input.value,"Unsaved name");assert.equal(h.walk().find(n=>n.tag==="h2").textContent,"Saved name");
});
test("selection uses destination keys and an unobserved group is not represented as absent service",()=>{
  const h=setup();h.render([h.profile,{...h.profile,key:"a:feishu:default",destination:"feishu",name:"Feishu name"}]);
  h.click("Feishu name · 飞书");assert.equal(h.walk().find(n=>n.tag==="h2").textContent,"Feishu name");
  h.click("渠道与群聊");assert.ok(h.walk().some(n=>n.textContent.includes("这不代表 Agent 不会服务新的群聊")));
});
test("capability search filters cards without creating markup from source text",()=>{
  const h=setup();h.profile.skills=[{name:"<img onerror=bad>",revision:1,state:"observed_loaded"}];h.render();h.click("能力");
  assert.ok(h.walk().some(n=>n.tag==="h3"&&n.textContent==="<img onerror=bad>"));
  const search=h.walk().find(n=>n.type==="search");search.value="no-match";search.oninput();
  assert.ok(!h.walk().some(n=>n.tag==="h3"&&n.textContent==="<img onerror=bad>"));assert.ok(!h.walk().some(n=>n.tag==="img"));
});

test("Web profile distinguishes actual ACP configuration and native historical inventory",()=>{
  const h=setup();Object.assign(h.profile,{destination:"web",engine:"claude",transport:"acp",native_inventory:{transport:"acp",skills_observed:true,skills:["jms-ssh"],services:[{name:"confluence",status:"connected"}],observed_at:"2026-10-20"}});
  h.render();assert.ok(h.walk().some(n=>n.textContent.includes("acp · 1 个技能 · 1 个知识与工具服务")));
  h.click("能力");assert.ok(h.walk().some(n=>n.tag==="h3"&&n.textContent==="jms-ssh"));
  assert.ok(h.walk().some(n=>n.tag==="h3"&&n.textContent==="confluence"));
  assert.ok(h.walk().some(n=>n.textContent==="已连接（历史观测）"));
});

test("new Web Agent opens its editable configuration without borrowing previous run evidence",()=>{
  const h=setup();h.profile.destination="web";let editedIndex=null;
  h.render([h.profile],{add(){h.draft.agents.push({id:"new",name:"New"});return "new";},capabilities(parent,index){editedIndex=index;}});
  h.click("＋ 新增智能体");assert.equal(h.walk().find(n=>n.tag==="h2").textContent,"New");
  assert.equal(editedIndex,1);assert.ok(h.walk().some(n=>n.textContent==="新智能体 · 尚未保存"));
});

test("simple choices show one default and separate project Agents without channel duplicates",()=>{
  const h=setup(),base={...h.profile};
  const profiles=[{...base,key:"web:daily",id:"daily",file_id:"cli",destination:"web"},{...base,key:"cli:daily",id:"daily",file_id:"cli",destination:"cli"},{...base,key:"feishu:default",destination:"feishu",file_id:"feishu"},{...base,key:"ding:default",destination:"dingtalk"},{...base,key:"ding:gtm",id:"gtm-skilllab",name:"GTM",default:false,destination:"dingtalk"}];
  assert.deepEqual(Array.from(h.profiles.choices(profiles),p=>p.key),["web:daily","ding:gtm"]);
});
test("the same Web and CLI configuration is shown only once in a profile picker",()=>{
  const h=setup();h.profile.destination="web";h.render([h.profile,{...h.profile,key:"a:cli:default",destination:"cli"}]);
  assert.ok(!h.walk().some(n=>n.tag==="button"&&n.textContent.endsWith("· 本地 CLI")));
});

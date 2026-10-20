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
  const render=(items=[profile])=>window.AGHAgentProfiles.render(root,items,file,draft,window.AGHSettings.fields(draft,"agents"),field=>{
    const input=new Element("input");input.value=field.value;input.path=field.path;input.oninput=()=>{draft.agents[0].name=input.value;};return input;
  });
  return {root,draft,profile,walk,click,render};
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

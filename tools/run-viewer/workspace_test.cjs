"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),vm=require("node:vm"),fs=require("node:fs"),path=require("node:path");
function setup(hash="#workbench") {
  const listeners={},nodes=new Map();
  const node=id=>{if(!nodes.has(id))nodes.set(id,{id,hidden:false,value:"",style:{},dataset:{},attributes:{},clientWidth:1000,clientHeight:700,classList:{toggle(){}},setAttribute(k,v){this.attributes[k]=v;},removeAttribute(k){delete this.attributes[k];},querySelector(){return null;},querySelectorAll(){return [];},replaceChildren(){},append(){},focus(){this.focused=true;}});return nodes.get(id);};
  const views=["workbench","memory","executions","settings"].map(node),navs=views.map(v=>Object.assign(node("nav-"+v.id),{dataset:{view:v.id}})),prompt=Object.assign(node("prompt"),{dataset:{prompt:"Prepare next step"}});
  const location={hash,search:""};
  const document={getElementById:node,createElement:node,dispatchEvent(){},querySelectorAll(selector){return selector===".view"?views:selector==="[data-view]"?navs:selector==="[data-prompt]"?[prompt]:[];}};
  const context={document,location,history:{replaceState(a,b,hash){location.hash=hash;}},localStorage:{getItem(){return JSON.stringify({draft:"Existing draft"});},setItem(){}},requestAnimationFrame(fn){fn();},matchMedia(){return {matches:false};},URLSearchParams,Event:class{},fetch:async()=>({ok:true,json:async()=>({records:[],sources:[]})}),addEventListener(k,v){listeners[k]=v;}};
  context.window=context;vm.runInNewContext(fs.readFileSync(path.join(__dirname,"workspace.js"),"utf8"),context);
  return {node,location,prompt,navigate(hash){location.hash=hash;listeners.hashchange();}};
}
test("right Chat remains open across modules and respects an explicit close",()=>{
  const h=setup();for(const route of ["#memory","#settings","#executions"]){h.navigate(route);assert.equal(h.node("chat-panel").hidden,false);}
  h.node("chat-toggle").onclick();h.navigate("#workbench");assert.equal(h.node("chat-panel").hidden,true);
});
test("legacy Chat route opens the sidebar on the canvas without losing a draft",()=>{
  const h=setup("#chat");assert.equal(h.location.hash,"#workbench");assert.equal(h.node("workbench").hidden,false);assert.equal(h.node("chat-panel").hidden,false);assert.equal(h.node("draft").value,"Existing draft");
});
test("open Chat stays on the current page and prompt starters append to the draft",()=>{
  const h=setup("#memory");h.node("chat-toggle").onclick();h.node("open-chat").onclick();assert.equal(h.location.hash,"#memory");assert.equal(h.node("draft").focused,true);h.prompt.onclick();assert.equal(h.node("draft").value,"Existing draft\n\nPrepare next step");
});

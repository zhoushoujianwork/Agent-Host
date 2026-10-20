"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),vm=require("node:vm"),fs=require("node:fs"),path=require("node:path");
function setup(hash="",search="") {
  const listeners={},nodes=new Map();let stored=JSON.stringify({draft:"Existing draft",positions:{kept:{x:24,y:48}},futurePreference:true}),fetches=0;
  const node=id=>{if(!nodes.has(id))nodes.set(id,{id,hidden:false,value:"",dataset:{},attributes:{},classList:{toggle(){}},setAttribute(k,v){this.attributes[k]=v;},removeAttribute(k){delete this.attributes[k];},querySelector(){return null;},focus(){this.focused=true;}});return nodes.get(id);};
  const views=["memory","executions","settings"].map(node),navs=views.map(v=>Object.assign(node("nav-"+v.id),{dataset:{view:v.id}})),prompt=Object.assign(node("prompt"),{dataset:{prompt:"Prepare next step"}});
  const location={hash,search};
  const document={getElementById:node,dispatchEvent(){},querySelectorAll(selector){return selector===".view"?views:selector==="[data-view]"?navs:selector==="[data-prompt]"?[prompt]:[];}};
  const context={document,location,history:{replaceState(a,b,hash){location.hash=hash;}},localStorage:{getItem(){return stored;},setItem(k,v){stored=v;}},matchMedia(){return {matches:false};},URLSearchParams,Event:class{},fetch(){fetches++;throw Error("The shell must not load canvas records");},addEventListener(k,v){listeners[k]=v;}};
  context.window=context;vm.runInNewContext(fs.readFileSync(path.join(__dirname,"workspace.js"),"utf8"),context);
  return {node,location,prompt,stored:()=>JSON.parse(stored),fetches:()=>fetches,navigate(hash){location.hash=hash;listeners.hashchange();}};
}
test("home and dormant workbench links land on memory without loading canvas data",()=>{
  for(const route of ["","#workbench","#unknown"]){const h=setup(route);assert.equal(h.location.hash,"#memory");assert.equal(h.node("memory").hidden,false);assert.equal(h.fetches(),0);}
  const html=fs.readFileSync(path.join(__dirname,"index.html"),"utf8");assert.doesNotMatch(html,/id="workbench"|data-view="workbench"|id="canvas"/);
});
test("right Chat remains open across modules and respects an explicit close",()=>{
  const h=setup();for(const route of ["#memory","#settings","#executions"]){h.navigate(route);assert.equal(h.node("chat-panel").hidden,false);}
  h.node("chat-toggle").onclick();h.navigate("#memory");assert.equal(h.node("chat-panel").hidden,true);
});
test("legacy Chat and execution links remain usable",()=>{
  const h=setup("#chat");assert.equal(h.location.hash,"#memory");assert.equal(h.node("chat-panel").hidden,false);assert.equal(h.node("draft").value,"Existing draft");
  const legacy=setup("","?key=db%3Aone");assert.equal(legacy.location.hash,"#executions?key=db%3Aone");assert.equal(legacy.node("executions").hidden,false);
});
test("saving drafts and task starters preserve dormant layout and unrelated preferences",()=>{
  const h=setup("#memory");h.prompt.onclick();assert.equal(h.node("draft").value,"Existing draft\n\nPrepare next step");assert.equal(h.location.hash,"#memory");
  assert.deepEqual(h.stored().positions,{kept:{x:24,y:48}});assert.equal(h.stored().futurePreference,true);
  h.node("clear-draft").onclick();assert.equal(h.stored().draft,"");assert.deepEqual(h.stored().positions,{kept:{x:24,y:48}});
});

"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),vm=require("node:vm"),fs=require("node:fs"),path=require("node:path");
function setup(hash="",search="",storageFails=false) {
  const listeners={},nodes=new Map();let stored=JSON.stringify({draft:"Existing draft",positions:{kept:{x:24,y:48}},futurePreference:true}),fetches=0;
  const node=id=>{if(!nodes.has(id))nodes.set(id,{id,hidden:false,value:"",style:{},scrollHeight:120,dataset:{},attributes:{},classList:{toggle(){}},addEventListener(k,v){this[k]=v;},setAttribute(k,v){this.attributes[k]=v;},removeAttribute(k){delete this.attributes[k];},getAttribute(k){return this.attributes[k];},querySelector(){return id==="settings-dialog"?node("settings-frame"):null;},showModal(){this.open=true;},close(){this.open=false;},focus(){this.focused=true;}});return nodes.get(id);};
  const views=["memory","executions"].map(node),navs=[...views,node("settings")].map(v=>Object.assign(node("nav-"+v.id),{dataset:{view:v.id}})),prompt=Object.assign(node("prompt"),{dataset:{prompt:"Prepare next step"}});
  const location={hash,search};
  const document={getElementById:node,addEventListener(k,v){listeners[k]=v;},dispatchEvent(e){listeners[e.type]?.(e);},querySelector(){return node("nav-settings");},querySelectorAll(selector){return selector===".view"?views:selector==="[data-view]"?navs:selector==="[data-prompt]"?[prompt]:[];}};
  const context={document,location,history:{replaceState(a,b,hash){location.hash=hash;}},localStorage:{getItem(){return stored;},setItem(k,v){if(storageFails)throw Error("Storage blocked");stored=v;}},matchMedia(){return {matches:false};},URLSearchParams,Event:class{},fetch(){fetches++;throw Error("The shell must not load canvas records");},addEventListener(k,v){listeners[k]=v;}};
  context.window=context;vm.runInNewContext(fs.readFileSync(path.join(__dirname,"workspace.js"),"utf8"),context);
  return {node,location,prompt,stored:()=>JSON.parse(stored),fetches:()=>fetches,navigate(hash){location.hash=hash;listeners.hashchange();}};
}
test("home and dormant workbench links land on memory without loading canvas data",()=>{
  for(const route of ["","#workbench","#unknown"]){const h=setup(route);assert.equal(h.location.hash,"#memory");assert.equal(h.node("memory").hidden,false);assert.equal(h.fetches(),0);}
  const html=fs.readFileSync(path.join(__dirname,"index.html"),"utf8");assert.doesNotMatch(html,/id="workbench"|data-view="workbench"|id="canvas"/);
});
test("clear is reversible and new input dismisses the stale undo",()=>{
  const h=setup();h.node("clear-draft").onclick();assert.equal(h.node("send-chat").disabled,true);assert.equal(h.node("undo-clear").hidden,false);
  h.node("undo-clear").onclick();assert.equal(h.node("draft").value,"Existing draft");assert.equal(h.stored().draft,"Existing draft");
  h.node("clear-draft").onclick();h.node("draft").value="New thought";h.node("draft").oninput();
  assert.equal(h.node("undo-clear").hidden,true);h.node("undo-clear").onclick();assert.equal(h.node("draft").value,"New thought");
});
test("send shortcut respects IME composition and keeps drafts until acceptance",()=>{
  const h=setup(),draft=h.node("draft");let prevented=0;
  const key={key:"Enter",metaKey:true,preventDefault(){prevented++;}};
  draft.onkeydown({...key,isComposing:true});assert.equal(prevented,0);
  draft.value="  ";draft.oninput();draft.onkeydown(key);assert.equal(h.node("send-chat").disabled,true);
  draft.value="A😀";draft.oninput();draft.onkeydown(key);assert.equal(h.node("draft-count").textContent,"2 字");assert.equal(h.stored().draft,"A😀");
});
test("storage failures remain visible and retain recoverable text",()=>{
  const h=setup("","",true);h.node("clear-draft").onclick();h.node("undo-clear").onclick();
  assert.equal(h.node("draft").value,"Existing draft");assert.equal(h.node("draft-status").dataset.state,"error");
  h.node("draft").oninput();assert.match(h.node("draft-status").textContent,/无法保存/);
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

test("settings overlay preserves the underlying record and dismisses without losing Chat drafts",()=>{
  const h=setup("#executions?key=db%3Aone");h.navigate("#settings");
  assert.equal(h.node("settings-dialog").open,true);assert.equal(h.node("executions").hidden,false);
  assert.equal(h.node("view-title").textContent,"执行记录");
  h.node("settings-close").onclick();assert.equal(h.location.hash,"#executions?key=db%3Aone");
  assert.equal(h.node("settings-dialog").open,false);assert.equal(h.node("draft").value,"Existing draft");
  h.navigate("#settings");h.node("settings-dialog").cancel({preventDefault(){}});
  assert.equal(h.node("settings-dialog").open,false);
});
test("direct settings links open over memory",()=>{
  const h=setup("#settings");assert.equal(h.node("memory").hidden,false);assert.equal(h.node("settings-dialog").open,true);
  h.node("settings-close").onclick();assert.equal(h.location.hash,"#memory");
});

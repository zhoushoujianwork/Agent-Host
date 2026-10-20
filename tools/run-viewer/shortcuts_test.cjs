"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),vm=require("node:vm"),fs=require("node:fs"),path=require("node:path");
function setup(hash){
  const events={},frameEvents={},frameDocumentEvents={},windowEvents={},location={hash};let focused=false;
  const button={hidden:true,addEventListener(k,v){events[k]=v;}},panel={hidden:false};
  const doc={getElementById(id){return id==="close-chat"?button:id==="chat-panel"?panel:{setAttribute(){}};},querySelector(){return {focus(){focused=true;}};},querySelectorAll(){return [{addEventListener(k,v){frameEvents[k]=v;},contentDocument:{addEventListener(k,v){frameDocumentEvents[k]=v;}}}];},addEventListener(k,v){events[k]=v;}};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,"shortcuts.js"),"utf8"),{document:doc,window:{addEventListener(k,v){windowEvents[k]=v;}},location,URLSearchParams});
  const key=(overrides={},handler=events.keydown)=>{let prevented=false;handler({key:"w",metaKey:true,preventDefault(){prevented=true;},stopPropagation(){},...overrides});return prevented;};
  return {location,button,panel,key,events,frameEvents,frameDocumentEvents,focused:()=>focused};
}
test("Cmd+W closes Chat and returns focus to the workbench without deleting drafts",()=>{
  const h=setup("#chat");assert.equal(h.button.hidden,false);assert.equal(h.key(),true);assert.equal(h.location.hash,"workbench");assert.equal(h.panel.hidden,true);assert.equal(h.focused(),true);
});
test("opened conversation detail receives the same shortcut from its frame",()=>{
  const h=setup("#executions?key=db%3Aone");h.frameEvents.load();assert.equal(h.key({},h.frameDocumentEvents.keydown),true);assert.equal(h.location.hash,"workbench");
});
test("settings, memory, plain journal and workbench keep native close behavior",()=>{
  for(const route of ["#settings","#memory","#executions","#workbench"]){const h=setup(route);assert.equal(h.button.hidden,true);assert.equal(h.key(),false);assert.equal(h.location.hash,route);}
});
test("other modifiers, composition, repetition and handled events are ignored",()=>{
  for(const overrides of [{metaKey:false,ctrlKey:true},{shiftKey:true},{altKey:true},{ctrlKey:true},{isComposing:true},{repeat:true},{defaultPrevented:true}])assert.equal(setup("#chat").key(overrides),false);
});

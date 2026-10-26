"use strict";
const {test}=require("node:test"), assert=require("node:assert/strict"), fs=require("node:fs"), vm=require("node:vm");
const source=fs.readFileSync(require("node:path").join(__dirname,"app.js"),"utf8");
function setup(embedded=true) {
  const nodes=new Map(), calls=[], events=[];
  const $=id=>{if(!nodes.has(id))nodes.set(id,{});return nodes.get(id);};
  const window={}; window.parent=embedded?{document:{dispatchEvent:e=>events.push(e)}}:window;
  const context={$,window,location:{href:"/executions.html"},Map,CustomEvent:class {constructor(type,options){this.type=type;this.detail=options.detail;}},
    state:{key:"cli:db:a",generation:1},refresh:async()=>{},fetch:(path,options)=>new Promise(resolve=>calls.push({path,options,resolve}))};
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf("// Cancellation is keyed"),source.indexOf("\nfunction card(")),context);
  const record=(id="a",control={active:true,can_stop:true,reason:""})=>({run:{id},events:[],control,mutation_token:"owner-token",snapshot:false});
  context.state.record=record();
  return {...context,calls,events,record,select(key,id){context.state.key=key;context.state.generation++;context.state.record=record(id);context.renderRecordControls();},
    answer(value,status=200){calls.at(-1).resolve({ok:status===200,json:async()=>value});}};
}
test("record controls expose live actions, hide terminal/archive and explain unavailable control",()=>{
  const h=setup();h.renderRecordControls();assert.equal(h.$("record-actions").hidden,false);assert.equal(h.$("stop-record").disabled,false);
  h.state.record.control={active:true,can_stop:false,reason:"Use original entry"};h.renderRecordControls();
  assert.equal(h.$("stop-record").disabled,true);assert.equal(h.$("record-control-state").textContent,"Use original entry");
  h.state.record.snapshot=true;h.renderRecordControls();assert.equal(h.$("record-actions").hidden,true);
  h.state.record=h.record();h.state.record.events=[{type:"run.finished"}];h.renderRecordControls();assert.equal(h.$("record-actions").hidden,true);
  h.state.record=h.record("a",{active:false,can_stop:false});h.renderRecordControls();assert.equal(h.$("record-actions").hidden,true);
});
test("view live attaches explicitly without cancelling; standalone carries key in workspace hash",()=>{
  const h=setup();h.renderRecordControls();h.viewLiveRecord();
  assert.equal(h.events[0].type,"agh:session-select");assert.equal(h.events[0].detail.key,"cli:db:a");assert.equal(h.calls.length,0);
  const standalone=setup(false);standalone.renderRecordControls();standalone.viewLiveRecord();
  assert.equal(standalone.location.href,"/#executions?key=cli%3Adb%3Aa");
});
test("stop binds key/token, blocks repeats and stays stopping through polls until terminal receipt",async()=>{
  const h=setup();h.renderRecordControls();const stopping=h.stopRecord();
  assert.equal(h.$("stop-record").disabled,true);await h.stopRecord();assert.equal(h.calls.length,1);
  assert.equal(h.calls[0].path,"/api/record/stop");assert.equal(h.calls[0].options.headers["X-Host-Action"],"owner-token");
  assert.deepEqual(JSON.parse(h.calls[0].options.body),{key:"cli:db:a"});
  h.answer({key:"cli:db:a",run_id:"a",status:"stopping"});await stopping;
  h.state.record=h.record();h.renderRecordControls();assert.equal(h.$("stop-record").disabled,true);assert.match(h.$("stop-record").textContent,/正在中止/);
  h.state.record.control={active:false,can_stop:false};h.renderRecordControls();assert.equal(h.$("record-actions").hidden,false);
  h.state.record.events=[{type:"run.finished"}];h.renderRecordControls();assert.equal(h.$("record-actions").hidden,true);
});
test("late stop response cannot target or mark a different selected run; selection back retains acknowledgement",async()=>{
  const h=setup();h.renderRecordControls();const stopping=h.stopRecord();h.select("feishu:db:b","b");
  assert.equal(h.$("stop-record").disabled,true);await h.stopRecord();assert.equal(h.calls.length,1);
  h.answer({key:"cli:db:a",run_id:"a",status:"stopping"});await stopping;
  assert.equal(h.$("stop-record").disabled,false);assert.equal(h.$("record-control-state").textContent,"");
  h.select("cli:db:a","a");assert.equal(h.$("stop-record").disabled,true);assert.match(h.$("record-control-state").textContent,/最终回执/);
});
test("failed or mismatched cancellation is visible only on its record and never retried automatically",async()=>{
  for(const mismatch of [false,true]) {
    const h=setup();h.renderRecordControls();const stopping=h.stopRecord();h.select("feishu:db:b","b");
    h.answer(mismatch?{key:"other",run_id:"b",status:"stopping"}:{error:"Cancellation unconfirmed"},mismatch?200:409);await stopping;
    assert.equal(h.$("record-control-state").textContent,"");assert.equal(h.calls.length,1);
    h.select("cli:db:a","a");assert.match(h.$("record-control-state").textContent,mismatch?/不匹配/:/unconfirmed/);
  }
});
test("record polling remains visibility gated with active/idle cadence and never attaches",()=>{
  const poll=source.slice(source.indexOf("async function pollRecords()"),source.indexOf('document.addEventListener("visibilitychange"'));
  assert.match(poll,/auto-refresh.*checked && !document.hidden/);
  assert.match(poll,/active \? 1000 : 5000/);assert.doesNotMatch(poll,/session-select|viewLiveRecord|stopRecord/);
});

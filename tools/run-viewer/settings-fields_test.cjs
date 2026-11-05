"use strict";
const test=require("node:test"),assert=require("node:assert/strict"),vm=require("node:vm"),fs=require("node:fs");
const window={};vm.runInNewContext(fs.readFileSync(__dirname+"/settings-fields.js","utf8"),{window,Object});
const fields=(value,category,secret=false)=>Array.from(window.AGHSettings.fields(value,category,secret));

test("native skill mode transitions preserve strict zero-skill defaults",()=>{
  const installed=[{id:"one"},{id:"two"}],select=window.AGHSettings.capabilitySelection;
  assert.equal(select("native_skills","inherit",installed),null);
  assert.deepEqual(Array.from(select("native_skills","custom",installed)),[]);
  assert.deepEqual(Array.from(select("knowledge_sources","custom",installed)),["one","two"]);
});
test("Agent UI exposes only current first-use fields and does not mutate defaults",()=>{
  const config={default_agent:"daily",agents:[{id:"daily",name:"Daily",workspace:"/work",native_skills:[],knowledge_sources:[],raw:{keep:true}}]};
  const before=JSON.stringify(config),shown=fields(config,"agents");
  assert.deepEqual(shown.map(f=>Array.from(f.path)),[["default_agent"],["agents",0,"name"],["agents",0,"workspace"]]);
  assert.equal(JSON.stringify(config),before);
});
test("models and credentials retain current fields and hidden configuration",()=>{
  const shown=fields({default_profile:"main",profiles:{main:{execution:{model:"native",timeout:"1h",allowed_tools:["Read"]}},other:{execution:{model:"other"}}}},"models");
  assert.deepEqual(shown.map(f=>Array.from(f.path)),[["profiles","main","execution","model"]]);
  const credentials=fields({ANTHROPIC_API_KEY:"__AGENT_HOST_KEEP_SECRET__x",JEV_API_KEY:"hidden",RAW:"keep"},"providers");
  assert.deepEqual(credentials.map(f=>Array.from(f.path)),[["ANTHROPIC_BASE_URL"],["ANTHROPIC_API_KEY"]]);
  assert.equal(credentials[1].value,"__AGENT_HOST_KEEP_SECRET__x");
});
test("Mac local runtime shares Models but adds no backend selector",()=>{
  assert.equal(window.AGHSettings.section({category:"providers"}),"models");
  assert.equal(window.AGHSettings.section({category:"local-runtime"}),"models");
  assert.equal(window.AGHSettings.section({category:"agents"}),"agents");
  assert.deepEqual(fields({args:["--home","/h","--agent","default"]},"local-runtime"),[]);
});
test("flag helpers preserve unrelated handoff options without defaults",()=>{
  const S=window.AGHSettings,args=["--home","/h","--agent","default","--timeout=1m"];
  assert.deepEqual(Array.from(S.setFlag(args,"--agent","daily")),["--home","/h","--agent","daily","--timeout=1m"]);
  assert.deepEqual(Array.from(S.setFlag(args,"--timeout","2m")),["--home","/h","--agent","default","--timeout=2m"]);
  assert.deepEqual(Array.from(S.setFlag(args,"--agent","")),["--home","/h","--timeout=1m"]);
  assert.deepEqual(Array.from(S.setFlag(args,"--offload-config","/p")),[...args,"--offload-config","/p"]);
  assert.deepEqual(args,["--home","/h","--agent","default","--timeout=1m"]);
  assert.equal(S.getFlag(args,"--timeout"),"1m");
});
test("empty credentials are omitted and channel internals remain hidden",()=>{
  assert.deepEqual({...window.AGHSettings.withoutEmpty({ANTHROPIC_API_KEY:"keep",EMPTY:"",N:null,X:"v"})},{ANTHROPIC_API_KEY:"keep",X:"v"});
  const shown=fields({enabled:false,settings:{app_id:"app",poll_interval_seconds:5,allowed_sender_open_ids:["owner"]}},"channels");
  assert.deepEqual(shown.map(f=>Array.from(f.path)),[["enabled"],["settings","app_id"]]);
  assert.equal(fields("masked","providers",true).length,1);
});

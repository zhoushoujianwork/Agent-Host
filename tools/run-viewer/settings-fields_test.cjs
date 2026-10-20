"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),vm=require("node:vm"),fs=require("node:fs"),path=require("node:path");
const window={};vm.runInNewContext(fs.readFileSync(path.join(__dirname,"settings-fields.js"),"utf8"),{window});
const fields=(value,category,secret)=>Array.from(window.AGHSettings.fields(value,category,secret));
test("Agent UI exposes only first-use fields and does not mutate defaults",()=>{
  const config={default_agent:"a",agents:[{id:"a",name:"Assistant",workspace:"/tmp",projects:["private"],execution_profile:"fast",instructions_dir:"/private"}]};
  const before=JSON.stringify(config),shown=fields(config,"agents");
  assert.deepEqual(shown.map(f=>Array.from(f.path)),[["default_agent"],["agents",0,"name"],["agents",0,"workspace"]]);assert.equal(JSON.stringify(config),before);
});
test("models and credentials leave execution and auxiliary provider defaults in configuration",()=>{
  const shown=fields({default_profile:"main",profiles:{main:{execution:{model:"native",timeout:"1h",allowed_tools:["Read"]},environment_file:"credentials/p.json"},specialized:{execution:{model:"advanced"}}}},"models");
  assert.deepEqual(shown.map(f=>Array.from(f.path)),[["profiles","main","execution","model"]]);
  const credentials=fields({ANTHROPIC_API_KEY:"__AGENT_HOST_KEEP_SECRET__x",JEV_API_KEY:"hidden",CLAUDE_CODE_MAX_OUTPUT_TOKENS:"4096"},"providers");
  assert.deepEqual(credentials.map(f=>Array.from(f.path)),[["ANTHROPIC_BASE_URL"],["ANTHROPIC_API_KEY"]]);assert.equal(credentials[1].value,"__AGENT_HOST_KEEP_SECRET__x");
});
test("channel polling, permissions and file references are not editable in the simple form",()=>{
  const shown=fields({enabled:false,settings:{app_id:"app",poll_interval_seconds:5,allowed_sender_open_ids:["owner"],client_secret_file:"private"}},"channels");
  assert.deepEqual(shown.map(f=>Array.from(f.path)),[["enabled"],["settings","app_id"]]);
  assert.equal(window.AGHSettings.section({category:"providers"}),"models");
  assert.equal(fields("masked","providers",true).length,1);
});

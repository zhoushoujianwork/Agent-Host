"use strict";
const test=require("node:test"),assert=require("node:assert/strict");
const scope=require("./memory-scope.js");

test("all scopes and missing scope have distinct explicit option values",()=>{
  assert.notEqual(scope.ALL,scope.MISSING);
  const options=scope.options([
    {memory:{scope:"projects/alpha"}},{memory:{scope:"projects/alpha"}},
    {memory:{scope:""}},{memory:{}},
  ]);
  assert.deepEqual(options.map(({value,count})=>[value,count]),[
    [scope.ALL,4],[scope.MISSING,2],["projects/alpha",2],
  ]);
  assert.equal(options[1].label,"未记录 scope");
  assert.equal(options[2].label,"projects/alpha");
});

test("scope matching distinguishes all, missing and an exact recorded scope",()=>{
  assert.equal(scope.matches(scope.ALL,undefined),true);
  assert.equal(scope.matches(scope.ALL,"projects/alpha"),true);
  assert.equal(scope.matches(scope.MISSING,undefined),true);
  assert.equal(scope.matches(scope.MISSING,""),true);
  assert.equal(scope.matches(scope.MISSING,"projects/alpha"),false);
  assert.equal(scope.matches("projects/alpha","projects/alpha"),true);
  assert.equal(scope.matches("projects/alpha","projects/beta"),false);
});

test("scope labels keep useful suffixes without making missing ambiguous",()=>{
  assert.equal(scope.label(undefined),"未记录 scope");
  assert.equal(scope.label(scope.MISSING),"未记录 scope");
  assert.equal(scope.label("agents/default/projects/alpha"),"projects/alpha");
});

"use strict";
const {test} = require("node:test");
const assert = require("node:assert/strict");
const vm = require("node:vm");
const fs = require("node:fs");
const path = require("node:path");
const script = fs.readFileSync(path.join(__dirname,"theme.js"),"utf8");

function harness({stored=null,dark=false,blocked=false}={}) {
  const events={},systemEvents={},documentEvents={},dataset={};
  const options=["light","dark","system"].map(value=>({dataset:{themeChoice:value},attributes:{},events:{},setAttribute(k,v){this.attributes[k]=v;},addEventListener(k,v){this.events[k]=v;}}));
  const picker={value:"",events:{},addEventListener(k,v){this.events[k]=v;}};
  const status={textContent:""};const system={matches:dark,addEventListener(k,v){systemEvents[k]=v;}};
  const window={addEventListener(k,v){events[k]=v;}};window.top=window;
  const document={documentElement:{dataset,classList:{add(){}}},querySelectorAll(selector){return selector==="[data-theme-picker]"?[picker]:options;},querySelector(){return status;},addEventListener(k,v){documentEvents[k]=v;}};
  const storage={getItem(){if(blocked)throw Error("blocked");return stored;},setItem(k,v){if(blocked)throw Error("blocked");stored=v;}};
  vm.runInNewContext(script,{document,window,localStorage:storage,matchMedia:()=>system});
  documentEvents.DOMContentLoaded();
  return {dataset,picker,options,status,system,stored:()=>stored,choose(value){picker.value=value;picker.events.change();},systemChange(value){system.matches=value;systemEvents.change();},external(value){stored=value;events.storage({key:"agent-host.appearance.v1"});}};
}
test("system default tracks the operating system until explicitly overridden",()=>{
  const h=harness({dark:true});assert.equal(h.dataset.theme,"dark");assert.equal(h.picker.value,"system");
  h.systemChange(false);assert.equal(h.dataset.theme,"light");h.choose("dark");h.systemChange(false);assert.equal(h.dataset.theme,"dark");assert.equal(h.stored(),"dark");
});
test("saved choice restores and synchronizes controls across documents",()=>{
  const h=harness({stored:"dark"});assert.equal(h.dataset.theme,"dark");assert.equal(h.options[1].attributes["aria-pressed"],"true");
  h.external("light");assert.equal(h.dataset.theme,"light");assert.equal(h.picker.value,"light");assert.equal(h.options[0].attributes["aria-pressed"],"true");
  h.external(null);assert.equal(h.picker.value,"system");
});
test("invalid storage and unavailable storage preserve a usable page",()=>{
  assert.equal(harness({stored:"invalid"}).picker.value,"system");
  const h=harness({blocked:true});h.choose("dark");assert.equal(h.dataset.theme,"dark");assert.match(h.status.textContent,/存储不可用/);
});

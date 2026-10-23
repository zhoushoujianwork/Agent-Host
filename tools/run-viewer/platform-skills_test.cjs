"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),vm=require("node:vm"),fs=require("node:fs"),path=require("node:path");
function setup(fetch){
  class Element{
    constructor(tag){this.tag=tag;this.children=[];this.textContent="";this.value="";this.attributes={};}
    append(...items){this.children.push(...items);}
    replaceChildren(...items){this.children=items;}
    setAttribute(key,value){this.attributes[key]=value;}
  }
  const window={},context={window,document:{createElement:tag=>new Element(tag)},fetch,URLSearchParams};
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,"platform-skills.js"),"utf8"),context);
  const root=new Element("div"),walk=(n=root)=>[n,...n.children.flatMap(walk)];
  return {root,walk,api:window.AGHPlatformSkills};
}
const response=value=>({ok:true,json:async()=>value});
const record={id:"sftp",enabled:true,selected:1,revisions:[{published:true,entry:{id:"sftp",name:"<img onerror=bad>",description:"SFTP delivery",revision:1,digest:"v1"},file_inventory:[{path:"SKILL.md",bytes:10},{path:"references/us.md",bytes:100}]}]};
const catalog={enabled:true,destination:{id:"web",root:"/fixture/skills"},destinations:[{id:"web",label:"Web"},{id:"feishu",label:"Feishu"}],records:[record],audit:[]};
test("catalog uses text nodes, reports authored coverage, and binds file reads to a pinned revision",async()=>{
  const calls=[],h=setup(async url=>{calls.push(url);return response(url.includes("file=")?{path:"SKILL.md",content:"full source",bytes:11,complete:true}:catalog);});
  await h.api.render(h.root,{id:"sftp"});await new Promise(setImmediate);
  assert.ok(h.walk().some(n=>n.tag==="h2"&&n.textContent==="<img onerror=bad>"));
  assert.ok(!h.walk().some(n=>n.tag==="img"));
  assert.ok(h.walk().some(n=>n.textContent.includes("1 / 1 个手写纳管技能")));
  assert.ok(calls.some(url=>url.includes("digest=v1")&&url.includes("revision=1")));
  assert.ok(h.walk().some(n=>n.tag==="pre"&&n.textContent==="full source"));
  const search=h.walk().find(n=>n.type==="search");search.value="no-match";search.oninput();
  assert.ok(h.walk().some(n=>n.textContent.includes("0 / 1")));
});
test("slow old destination responses cannot replace a newer view",async()=>{
  let resolveOld;const h=setup(url=>url.includes("destination=old")?new Promise(resolve=>{resolveOld=resolve;}):Promise.resolve(response({...catalog,destination:{id:"feishu",root:"/new"},records:[]})));
  const old=h.api.render(h.root,{destination:"old"});await h.api.render(h.root,{destination:"feishu"});
  resolveOld(response(catalog));await old;
  assert.ok(h.walk().some(n=>n.textContent==="目录：/new"));
  assert.ok(!h.walk().some(n=>n.textContent==="目录：/fixture/skills"));
});
test("a slow previous file read cannot overwrite the selected regional reference",async()=>{
  let resolveSkill,resolveUS;
  const h=setup(url=>{
    if(url.includes("file=SKILL.md"))return new Promise(resolve=>{resolveSkill=resolve;});
    if(url.includes("file=references%2Fus.md"))return new Promise(resolve=>{resolveUS=resolve;});
    return Promise.resolve(response(catalog));
  });
  await h.api.render(h.root,{id:"sftp"});
  h.walk().find(n=>n.tag==="button"&&n.textContent==="references/us.md · 100 B").onclick();
  resolveUS(response({path:"references/us.md",content:"US marker",bytes:9,complete:true}));await new Promise(setImmediate);
  resolveSkill(response({path:"SKILL.md",content:"old instructions",bytes:16,complete:true}));await new Promise(setImmediate);
  assert.ok(h.walk().some(n=>n.tag==="pre"&&n.textContent==="US marker"));
  assert.ok(!h.walk().some(n=>n.textContent==="old instructions"));
});
test("unavailable destination keeps the selector and does not claim an empty complete catalog",async()=>{
  const h=setup(async()=>response({...catalog,error:"integrity failed",records:[]}));await h.api.render(h.root);
  assert.ok(h.walk().some(n=>n.tag==="select"&&n.attributes["aria-label"]==="技能运行入口"));
  assert.ok(h.walk().some(n=>n.textContent==="integrity failed"));
  assert.ok(!h.walk().some(n=>n.textContent.includes("完整目录")));
});
test("pending and withdrawn revisions do not masquerade as the enabled publication",()=>{
  const h=setup();
  assert.equal(h.api.lifecycle(record,{published:false,entry:{revision:2}}),"待审核");
  assert.equal(h.api.lifecycle(record,{published:true,withdrawn:true,entry:{revision:1}}),"已撤回");
  assert.equal(h.api.matches(record,"delivery","enabled"),true);
  assert.equal(h.api.matches(record,"delivery","disabled"),false);
});

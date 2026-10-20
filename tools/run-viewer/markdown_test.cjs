"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),vm=require("node:vm"),fs=require("node:fs"),path=require("node:path");
function setup() {
  const make=tag=>({tag,children:[],append(...nodes){this.children.push(...nodes);},replaceChildren(...nodes){this.children=nodes;}});
  const ctx=vm.createContext({document:{createElement:make}});
  for(const file of ["markdown-parser.js","markdown.js"])vm.runInContext(fs.readFileSync(path.join(__dirname,file),"utf8"),ctx);
  return {ctx,root:make("div"),render:ctx.AGHMarkdown};
}
function all(node){return [node,...node.children.flatMap(all)];}
test("chat Markdown covers headings, emphasis, lists, quotes, fences and tables",()=>{
  const h=setup();h.render(h.root,"## Result\n\n**Bold** and *italic* with `code`\n\n3. third\n4. fourth\n\n> Quote\n\n```js\nconst x = '<script>';\n```\n\n| Name | Value |\n| --- | --- |\n| A | B |\n\n---");
  const tags=all(h.root).map(n=>n.tag);
  for(const tag of ["h2","strong","em","code","ol","li","blockquote","pre","table","th","td","hr"])assert.ok(tags.includes(tag),tag);
  assert.equal(h.root.children[2].start,3);
});
test("model HTML, unsafe schemes and local paths cannot create executable elements or navigation",()=>{
  const h=setup();h.render(h.root,'<img src=x onerror=alert(1)>\n\n[bad](javascript:alert) [bad](data:text/html,x) [bad](//example.com) [file](/tmp/README.md) [good](https://example.com/docs)');
  const nodes=all(h.root),links=nodes.filter(n=>n.tag==="a");
  assert.equal(nodes.some(n=>n.tag==="img"||n.tag==="script"),false);
  assert.equal(links.length,1);assert.equal(links[0].href,"https://example.com/docs");
  assert.equal(links[0].target,"_blank");assert.equal(links[0].rel,"noopener noreferrer");
});
test("streaming keeps frozen Markdown nodes and tolerates unfinished syntax",()=>{
  const h=setup();h.render(h.root,"**Ready**\n\nPartial **");const first=h.root.children[0];
  h.render(h.root,"**Ready**\n\nPartial **answer**\n\n```sh\nprintf ok");
  assert.equal(h.root.children[0],first);
  assert.equal(h.root.children[1].children[1].tag,"strong");
  assert.equal(h.root.children[2].children[0].textContent,"printf ok");
  h.render(h.root,"Replacement");assert.equal(h.root.children.length,1);
});
test("inline code inside bold text renders as code rather than backticks",()=>{
  const h=setup();h.render(h.root,"**Result: `AGH OK`**");
  const strong=h.root.children[0].children[0];
  assert.equal(strong.tag,"strong");assert.equal(strong.children[1].tag,"code");
  assert.equal(strong.children[1].textContent,"AGH OK");
});

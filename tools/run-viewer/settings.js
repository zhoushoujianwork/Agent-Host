"use strict";

const $ = id => document.getElementById(id);
const KEEP = "__AGENT_HOST_KEEP_SECRET__";
const SECTIONS = {
  general: ["◐", "通用", "选择你喜欢的外观，其他选项已准备好默认值。"],
  agents: ["◈", "智能体", "选择默认智能体，告诉它在哪里工作。"],
  models: ["⌁", "模型与连接", "选择模型。使用自定义服务时，再填写地址和密钥。"],
  channels: ["▤", "消息渠道", "连接你使用的消息工具。不需要的渠道保持关闭即可。"],
  shortcuts: ["⌘", "快捷键", "少一点点击，更快回到工作。"],
};
let catalog = {files:[]}, selected = null, category = "general", draft, dirty = false, busy = false;
const clone = value => JSON.parse(JSON.stringify(value));
const node = (tag, cls, text) => {const n=document.createElement(tag);n.className=cls;if(text!==undefined)n.textContent=text;return n;};
const fileSection = file => window.AGHSettings.section(file);
const fileTitle = file => ({"agents.cli.json":"本地对话","agents.feishu.json":"飞书智能体","agents.dingtalk.json":"钉钉智能体","agents.json":"默认智能体","host.json":"模型","channels.feishu.json":"飞书","channels.dingtalk.json":"钉钉"})[file.path?.split("/").at(-1)] || (file.category==="providers" ? (file.secret ? "连接密钥 · " : "模型连接 · ")+file.title : file.title);
function feedback(text, success=false) {$("feedback").textContent=text;$("feedback").className=success?"success":"";$("feedback").hidden=!text;}
function changed() {
  dirty=!!selected&&!selected.error&&JSON.stringify(draft)!==JSON.stringify(selected.value);
  $("save-bar").hidden=!selected;
  $("save").disabled=!dirty||busy;$("reset").disabled=!dirty||busy;
  $("editor").inert=busy;$("categories").inert=busy;$("files").inert=busy;$("search").disabled=busy||dirty;$("refresh").disabled=busy;
  $("change-status").textContent=busy?"正在检查并保存…":dirty?"有尚未保存的修改":"设置已保存";
  $("save-hint").textContent=selected?.application==="cli"?"下次本地任务时生效。":"服务设置在重启 Host 后生效。";
}
function setAt(path,value) {
  if(!path.length)draft=value;
  else {let target=draft;for(const key of path.slice(0,-1)){if(!target[key])target[key]={};target=target[key];}if(value===undefined)delete target[path.at(-1)];else target[path.at(-1)]=value;}
  changed();
}
function mayLeave() {return !dirty||window.confirm("放弃尚未保存的设置？");}
function sectionMatches(id) {
  const q=$("search").value.trim().toLowerCase();
  return !q||[...SECTIONS[id],...catalog.files.filter(f=>fileSection(f)===id).map(fileTitle)].join(" ").toLowerCase().includes(q);
}
function renderNav() {
  $("categories").replaceChildren();
  for(const [id,[symbol,title]] of Object.entries(SECTIONS)) {
    if(!sectionMatches(id))continue;
    const b=node("button","category"+(category===id?" active":""));b.type="button";b.setAttribute("aria-current",category===id?"page":"false");b.append(node("span","symbol",symbol),node("span","",title));
    b.onclick=()=>{if(category!==id&&mayLeave()){category=id;selected=null;dirty=false;feedback("");render();}};$("categories").append(b);
  }
}
function render() {
  renderNav();const matches=sectionMatches(category);
  $("section-title").textContent=matches?SECTIONS[category][1]:"没有找到设置";
  $("section-description").textContent=matches?SECTIONS[category][2]:"试试搜索外观、智能体、模型或消息渠道。";
  $("general").hidden=category!=="general"||!matches;$("shortcuts").hidden=category!=="shortcuts"||!matches;
  const files=matches?catalog.files.filter(file=>fileSection(file)===category):[];
  if(!selected||!files.some(file=>file.id===selected.id)){selected=files[0]||null;draft=selected?clone(selected.value??null):null;dirty=false;}
  $("files").replaceChildren();
  for(const file of files){const b=node("button","file-tab"+(selected?.id===file.id?" active":""),fileTitle(file));b.type="button";b.onclick=()=>{if(file.id!==selected?.id&&mayLeave()){selected=file;draft=clone(file.value??null);dirty=false;feedback("");render();}};$("files").append(b);}
  $("files").hidden=files.length<2;
  $("empty").hidden=!!selected||["general","shortcuts"].includes(category)||!matches;
  $("empty").textContent=category==="channels"?"还没有配置消息渠道。你仍可通过本地 CLI 使用 AGH。":"还没有可编辑的配置。请从已配置的 Host 打开工作台。";
  renderEditor();changed();
}
function renderEditor() {
  $("editor").replaceChildren();if(!selected)return;
  if(selected.error){$("editor").append(node("p","notice",selected.error));return;}
  const fields=window.AGHSettings.fields(draft,selected.category,selected.secret);
  if(!fields.length){$("editor").append(node("p","notice","当前连接使用已有默认配置，无需在这里调整。"));return;}
  const card=node("section","simple-group");
  let group=null;
  for(const field of fields){
    const agentIndex=field.path[0]==="agents"?field.path[1]:null;
    if(agentIndex!==null&&group!==agentIndex){group=agentIndex;card.append(node("h3","group-label",draft.agents[agentIndex].name||draft.agents[agentIndex].id));}
    card.append(renderField(field));
  }
  $("editor").append(card);
}
function renderField(field) {
  const {path,value,label,hint,options}=field;
  const row=node("div","simple-row"),description=node("label","simple-label"),control=node("div","simple-control");
  const id="setting-"+path.map(String).map(encodeURIComponent).join("-");description.htmlFor=id;description.append(node("strong","",label));if(hint)description.append(node("small","",hint));
  const secret=selected.secret||/TOKEN|KEY|secret|password/i.test(String(path.at(-1)));
  const retained=typeof value==="string"&&value.startsWith(KEEP);
  let input;
  if(options){input=node("select","");for(const [v,title]of options)input.add(new Option(title,v));input.value=value;input.onchange=()=>setAt(path,input.value);}
  else if(typeof value==="boolean"){input=node("input","");input.type="checkbox";input.checked=value;input.onchange=()=>setAt(path,input.checked);}
  else {
    input=node("input","");input.type=secret?"password":"text";input.value=retained?"":String(value??"");input.spellcheck=false;
    if(secret){input.autocomplete="new-password";input.placeholder=retained?"已保存 · 留空保留":"填写密钥";}
    input.oninput=()=>{const original=path.reduce((v,k)=>v?.[k],selected.value);setAt(path,secret&&retained&&!input.value?value:!input.value&&original===undefined?undefined:input.value);};
  }
  input.id=id;control.append(input);
  if(secret&&retained){const clear=node("button","quiet","清除");clear.type="button";clear.onclick=()=>{setAt(path,"");input.value="";input.placeholder="保存后移除密钥";};control.append(clear);}
  row.append(description,control);return row;
}
async function load() {
  try {
    const response=await fetch("/api/settings",{cache:"no-store"}),result=await response.json();if(!response.ok)throw Error(result.error||"无法读取设置。");catalog=result;
    $("notice").hidden=!!result.enabled;$("notice").textContent="当前是独立预览；连接已配置的 Host 后可管理智能体和模型。";
    selected=null;dirty=false;render();
  }catch(error){feedback(error.message);}
}
$("save").onclick=async()=>{
  if(!selected||!dirty||busy)return;const invalid=$("editor").querySelector(":invalid");if(invalid){invalid.reportValidity();return;}
  busy=true;changed();feedback("");
  try {
    const response=await fetch("/api/settings",{method:"POST",headers:{"Content-Type":"application/json","X-Host-Action":catalog.mutation_token},body:JSON.stringify({file:selected.id,revision:selected.revision,value:draft})});
    const result=await response.json();if(!response.ok)throw Error(result.error||"保存失败，请刷新查看当前设置后再试。");
    catalog.files=catalog.files.map(file=>file.id===result.file.id?result.file:file);selected=result.file;draft=clone(selected.value);dirty=false;render();feedback("已检查并保存。"+(result.file.application==="cli"?"下次任务时生效。":"重启 Host 后生效。"),true);
  }catch(error){feedback(error.message);}finally{busy=false;changed();}
};
$("reset").onclick=()=>{if(selected){draft=clone(selected.value);renderEditor();changed();feedback("");}};
$("refresh").onclick=()=>{if(mayLeave())load();};
$("search").oninput=()=>{if(dirty){feedback("请先保存或放弃修改，再搜索其他设置。");return;}if(!sectionMatches(category)){category=Object.keys(SECTIONS).find(sectionMatches)||"general";selected=null;}render();};
window.addEventListener("beforeunload",event=>{if(dirty){event.preventDefault();event.returnValue="";}});
load();

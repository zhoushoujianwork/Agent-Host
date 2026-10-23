"use strict";

const $ = id => document.getElementById(id);
const KEEP = "__AGENT_HOST_KEEP_SECRET__";
const SECTIONS = {
  general: ["◐", "通用", "选择你喜欢的外观，其他选项已准备好默认值。"],
  agents: ["◈", "智能体", "日常使用默认智能体，专属项目独立配置。"],
  skills: ["◇", "平台技能", "查看纳管方法、固定版本与来源；默认 Agent 在相关任务前主动装载适用技能。"],
  models: ["⌁", "模型与连接", "选择模型。使用自定义服务时，再填写地址和密钥。"],
  channels: ["▤", "消息渠道", "连接你使用的消息工具。不需要的渠道保持关闭即可。"],
  shortcuts: ["⌘", "快捷键", "少一点点击，更快回到工作。"],
};
let catalog = {files:[]}, selected = null, category = "general", draft, dirty = false, busy = false;
let agentProfiles = [], profileError = "", agentChoiceKey = null;
const clone = value => JSON.parse(JSON.stringify(value));
const node = (tag, cls, text) => {const n=document.createElement(tag);n.className=cls;if(text!==undefined)n.textContent=text;return n;};
const fileSection = file => window.AGHSettings.section(file);
const fileTitle = file => ({"agents.cli.json":"本地对话","agents.feishu.json":"飞书智能体","agents.dingtalk.json":"钉钉智能体","agents.json":"默认智能体","host.json":"模型","channels.feishu.json":"飞书","channels.dingtalk.json":"钉钉"})[file.path?.split("/").at(-1)] || (file.category==="providers" ? (file.secret ? "连接密钥 · " : "模型连接 · ")+file.title : file.title);
function feedback(text, success=false) {$("feedback").textContent=text;$("feedback").className=success?"success":"";$("feedback").hidden=!text;}
function changed() {
  dirty=!!selected&&!selected.error&&JSON.stringify(draft)!==JSON.stringify(selected.value);
  $("save-bar").hidden=!selected||selected.read_only;
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
  return !q||[...SECTIONS[id],...catalog.files.filter(f=>fileSection(f)===id).map(fileTitle),...(id==="agents"?agentProfiles.map(p=>p.name):[])].join(" ").toLowerCase().includes(q);
}
function renderNav() {
  $("categories").replaceChildren();
  for(const [id,[symbol,title]] of Object.entries(SECTIONS)) {
    if(!sectionMatches(id))continue;
    const b=node("button","category"+(category===id?" active":""));b.type="button";b.setAttribute("aria-current",category===id?"page":"false");b.append(node("span","symbol",symbol),node("span","",title));
    b.onclick=()=>{if(category!==id&&mayLeave()){category=id;selected=null;dirty=false;feedback("");render();}};$("categories").append(b);
  }
}
function render(platformSelection) {
  renderNav();const matches=sectionMatches(category);
  $("section-title").textContent=matches?SECTIONS[category][1]:"没有找到设置";
  $("section-description").textContent=matches?SECTIONS[category][2]:"试试搜索外观、智能体、模型或消息渠道。";
  $("general").hidden=category!=="general"||!matches;$("shortcuts").hidden=category!=="shortcuts"||!matches;
  $("platform-skills").hidden=category!=="skills"||!matches;
  if(category==="skills"&&matches)window.AGHPlatformSkills.render($("platform-skills"),platformSelection);
  const files=matches?catalog.files.filter(file=>fileSection(file)===category):[];
  const choices=category==="agents"?window.AGHAgentProfiles.choices(agentProfiles).filter(p=>files.some(file=>file.id===p.file_id)):[];
  const choice=choices.find(p=>p.key===agentChoiceKey)||choices[0];
  const target=choice?files.find(file=>file.id===choice.file_id):files.find(file=>file.id===selected?.id)||files[0]||null;
  if(selected?.id!==target?.id||!selected){selected=target;draft=selected?clone(selected.value??null):null;dirty=false;}
  if(choice){agentChoiceKey=choice.key;window.AGHAgentProfiles.select(choice.key);}
  $("files").replaceChildren();$("files").setAttribute("aria-label",choices.length?"选择智能体":"选择配置");
  if(choices.length){
    for(const profile of choices){const b=node("button","file-tab"+(profile.key===choice.key?" active":""),profile.default?"默认智能体":profile.name);b.type="button";b.onclick=()=>{if(profile.key!==agentChoiceKey&&mayLeave()){agentChoiceKey=profile.key;selected=null;dirty=false;feedback("");render();}};$("files").append(b);}
  }else for(const file of files){const b=node("button","file-tab"+(selected?.id===file.id?" active":""),fileTitle(file));b.type="button";b.onclick=()=>{if(file.id!==selected?.id&&mayLeave()){selected=file;draft=clone(file.value??null);dirty=false;feedback("");render();}};$("files").append(b);}
  $("files").hidden=choices.length?choices.length<2:files.length<2;
  $("empty").hidden=!!selected||["general","shortcuts","skills"].includes(category)||!matches;
  $("empty").textContent=category==="channels"?"还没有配置消息渠道。你仍可通过本地 CLI 使用 AGH。":"还没有可编辑的配置。请从已配置的 Host 打开工作台。";
  renderEditor();changed();
}
function renderEditor() {
  $("editor").replaceChildren();if(!selected)return;
  if(selected.error){$("editor").append(node("p","notice",selected.error));return;}
  const fields=selected.read_only?[]:window.AGHSettings.fields(draft,selected.category,selected.secret);
  const agentEditor=selected.id===catalog.agent_options?.file_id?{
    add(){const id="agent-"+crypto.randomUUID().slice(0,8);draft.agents.push({id,name:"新智能体",projects:[],native_skills:null,knowledge_sources:null});changed();return id;},
    capabilities:renderAgentCapabilities,
  }:null;
  if(selected.category==="agents"&&window.AGHAgentProfiles.render($("editor"),agentProfiles,selected,draft,fields,renderField,profileError||catalog.agent_options_error,agentEditor,!!agentChoiceKey))return;
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
function renderAgentCapabilities(parent,index) {
  for(const [field,choices,title,hint]of [
    ["native_skills","skills","技能","选择当前原生配置中已安装的用户技能；原生命令与插件、项目技能和 Host 记忆仍由各自机制提供。"],
    ["knowledge_sources","knowledge_sources","知识源","选择此智能体实际挂载的检索来源；不会更改其他智能体的配置。"]]){
    const section=node("section","agent-capability-editor");section.append(node("h3","group-title",title),node("p","help-note",hint));
    const mode=node("select","");mode.setAttribute("aria-label",title+"配置方式");mode.add(new Option("使用默认能力","inherit"));mode.add(new Option("自定义选择","custom"));
    mode.value=draft.agents[index][field]==null?"inherit":"custom";section.append(mode);
    const list=node("div","agent-capability-list");section.append(list);
    function renderList(){
      list.replaceChildren();if(mode.value!=="custom")return;
      const selected=draft.agents[index][field];
      const actions=node("div","agent-capability-actions");
      for(const [label,values]of [["全选",()=>catalog.agent_options[choices].map(item=>item.id)],["清空选择",()=>[]]]){const button=node("button","quiet",label);button.type="button";button.onclick=()=>{setAt(["agents",index,field],values());renderList();};actions.append(button);}list.append(actions);
      const search=node("input","agent-search");search.type="search";search.placeholder="搜索"+title;search.setAttribute("aria-label",search.placeholder);list.append(search);
      const options=node("div","agent-capability-options");list.append(options);
      const available=catalog.agent_options[choices];
      function filter(){options.replaceChildren();const query=search.value.trim().toLowerCase();
        for(const item of available.filter(item=>item.name.toLowerCase().includes(query))){
          const label=node("label","agent-capability-option"),input=node("input","");input.type="checkbox";input.checked=draft.agents[index][field].includes(item.id);
          input.onchange=()=>{const next=new Set(draft.agents[index][field]);if(input.checked)next.add(item.id);else next.delete(item.id);setAt(["agents",index,field],[...next]);};
          label.append(input,node("span","",item.name));options.append(label);
        }
        if(!options.children.length)options.append(node("p","help-note","没有匹配的已安装能力。"));
      }
      search.oninput=filter;filter();list.append(node("p","help-note","未勾选任何项时，不挂载本组能力。"));
      for(const id of selected.filter(id=>!available.some(item=>item.id===id))){const remove=node("button","quiet",`移除已不可用项：${id}`);remove.type="button";remove.onclick=()=>{setAt(["agents",index,field],draft.agents[index][field].filter(value=>value!==id));renderList();};list.append(remove);}
    }
    mode.onchange=()=>{setAt(["agents",index,field],mode.value==="inherit"?null:catalog.agent_options[choices].map(item=>item.id));renderList();};renderList();parent.append(section);
  }
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
    await loadProfiles();selected=null;dirty=false;render();
  }catch(error){feedback(error.message);}
}
async function loadProfiles() {
  try{const response=await fetch("/api/agent-profiles",{cache:"no-store"});if(!response.ok)throw Error("智能体档案读取失败；可继续编辑已有设置。");const result=await response.json();agentProfiles=result.profiles||[];catalog.files=catalog.files.filter(f=>!f.read_only).concat(result.virtual_files||[]);profileError=result.web_error||"";}
  catch(error){agentProfiles=[];profileError=error.message;}
}
$("save").onclick=async()=>{
  if(!selected||!dirty||busy)return;const invalid=$("editor").querySelector(":invalid");if(invalid){invalid.reportValidity();return;}
  busy=true;changed();feedback("");
  try {
    const response=await fetch("/api/settings",{method:"POST",headers:{"Content-Type":"application/json","X-Host-Action":catalog.mutation_token},body:JSON.stringify({file:selected.id,revision:selected.revision,value:draft})});
    const result=await response.json();if(!response.ok)throw Error(result.error||"保存失败，请刷新查看当前设置后再试。");
    catalog.files=catalog.files.map(file=>file.id===result.file.id?result.file:file);selected=result.file;draft=clone(selected.value);dirty=false;await loadProfiles();render();feedback("已检查并保存。"+(result.file.application==="cli"?"下次任务时生效。":"重启 Host 后生效。"),true);
  }catch(error){feedback(error.message);}finally{busy=false;changed();}
};
$("reset").onclick=()=>{if(selected){draft=clone(selected.value);renderEditor();changed();feedback("");}};
$("refresh").onclick=()=>{if(mayLeave())load();};
$("search").oninput=()=>{if(dirty){feedback("请先保存或放弃修改，再搜索其他设置。");return;}if(!sectionMatches(category)){category=Object.keys(SECTIONS).find(sectionMatches)||"general";selected=null;}render();};
window.addEventListener("beforeunload",event=>{if(dirty){event.preventDefault();event.returnValue="";}});
window.AGHPlatformSkills.open=(destination,id="")=>{
  if(!mayLeave())return;
  category="skills";selected=null;dirty=false;$("search").value="";feedback("");render({destination,id});
};
load();

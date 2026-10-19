"use strict";

const $ = (id) => document.getElementById(id);
const KEEP = "__AGENT_HOST_KEEP_SECRET__";
const SECTIONS = {
  agents: ["◈", "Agents", "Choose who handles your tasks and the context they use."],
  models: ["◇", "Models & runtime", "Set the execution profiles and models used by your Host."],
  providers: ["⌁", "Provider connections", "Manage credentials locally. Saved secret values stay hidden."],
  channels: ["▤", "Channels", "Configure where requests arrive and which execution profile handles them."],
  sources: ["▧", "Data sources", "Choose the knowledge and business accounts available to your agents."],
  instructions: ["≡", "Agent instructions", "Give each agent its role, identity and working guidance."],
  handoff: ["↗", "CLI handoff", "Keep the daily-task Agent, workspace and CLI state connected correctly."],
  people: ["♧", "People", "Manage local user identities and private profile references."],
  backup: ["▣", "Backups", "Choose how the Host retains and protects its local data."],
};
const LABELS = {default_agent:"Default Agent",default_profile:"Default execution profile",profiles:"Execution profiles",agents:"Agents",id:"ID",name:"Display name",workspace:"Working directory",projects:"Project access",instructions_dir:"Instructions directory",instructions_file:"Instructions file",user_file:"Personal profile",group_bindings:"Group assignments",conversation_id:"Conversation ID",agent_id:"Agent",model:"Native model",jev_model:"Memory decision model",jev_provider:"Memory decision provider",jev_endpoint:"Memory decision endpoint",claude:"Claude executable",environment_file:"Provider credentials file",execution_profile:"Execution profile",enabled:"Enabled",settings:"Connection settings",accounts:"Accounts",sources:"Sources",bot:"Bot connection",dws:"DingTalk workspace",client_id:"Client ID",client_secret_file:"Client secret file",owner_user_id:"Owner user ID",owner_open_id:"Owner Open ID",timeout:"Task timeout",allowed_tools:"Allowed tools",native_profile:"Native profile",memory_scope:"Default memory scope",backend:"Storage backend",memory:"Memory storage",execution:"Task execution",args:"Saved CLI options",cwd:"Working directory",binary:"Host executable"};
Object.assign(LABELS,{home:"CLI state directory",agents_config:"Agent configuration",ANTHROPIC_BASE_URL:"Claude gateway",ANTHROPIC_AUTH_TOKEN:"Claude access token",ANTHROPIC_API_KEY:"Claude API key",JEV_API_KEY:"Memory provider API key",TYPESAFE_API_KEY:"Memory provider API key"});
const HELP = {projects:"Project IDs must exist in this entry's registry. An empty list disables project routing.",workspace:"Use an existing absolute directory. It is separate from the Host state directory.",instructions_dir:"A private folder containing the agent's AGENTS.md, SOUL.md or IDENTITY.md.",environment_file:"Credentials are loaded from this private file; their values are hidden in the console.",model:"Use a model supported by this native Claude configuration.",default_agent:"Must match an Agent ID in this configuration.",execution_profile:"Must match an execution profile under Models & runtime.",enabled:"Saving changes the configuration. Running services apply it after restart.",args:"The CLI handoff must use its own cli-state and Agent catalog, separate from channel services.",memory_scope:"A stable identity for saved knowledge. Changing it changes which memory is selected."};
let catalog = {files:[]}, selected = null, category = "agents", draft, dirty = false, busy = false;
const clone = (value) => JSON.parse(JSON.stringify(value));
const label = (key) => LABELS[key] || String(key).replace(/_/g," ").replace(/^./,(c)=>c.toUpperCase());
const node = (tag, cls, text) => { const element = document.createElement(tag); if(cls) element.className=cls; if(text!==undefined) element.textContent=text; return element; };
const secretKey = (key) => !String(key).endsWith("_file") && !String(key).endsWith("_path") && /secret|password|(?:^|_)token$|access_token|refresh_token|auth_token|bearer|api[_-]?key|authorization|^key$/i.test(key);

function feedback(text, success=false) { $("feedback").textContent=text; $("feedback").className=success?"success":""; $("feedback").hidden=!text; }
function changed() {
  dirty = !!selected && !selected.error && JSON.stringify(draft)!==JSON.stringify(selected.value);
  $("save").disabled=!dirty||busy; $("reset").disabled=!dirty||busy;
  $("editor").inert=busy; $("categories").inert=busy; $("files").inert=busy; $("search").disabled=busy; $("refresh").disabled=busy;
  $("change-status").textContent=busy?"Checking and saving…":dirty?"You have unsaved changes":"Everything is up to date";
  $("save-hint").textContent=selected?.application==="cli"||category==="handoff"?"CLI changes apply on the next task.":"Service changes apply after restart.";
}
function setAt(path,value) { if(!path.length) draft=value; else {let target=draft; for(const key of path.slice(0,-1)) target=target[key]; target[path.at(-1)]=value;} changed(); }
function getAt(path) { return path.reduce((value,key)=>value[key],draft); }
function matches(file) { const q=$("search").value.toLowerCase().trim(); return !q||[file.title,file.path,SECTIONS[file.category]?.[1],JSON.stringify(file.value)].join(" ").toLowerCase().includes(q); }
function mayLeave() { return !dirty || window.confirm("Discard the unsaved changes to this configuration?"); }
function renderNav() {
  $("categories").replaceChildren();
  for(const [id,[symbol,title]] of Object.entries(SECTIONS)) {
    const count=catalog.files.filter(f=>f.category===id&&matches(f)).length;
    const button=node("button","category"+(id===category?" active":"")); button.type="button";
    button.setAttribute("aria-current",id===category?"page":"false");
    button.append(node("span","symbol",symbol),node("span","",title),node("span","count",String(count)));
    button.onclick=()=>{if(id!==category&&mayLeave()){category=id; selected=null; dirty=false; feedback(""); render();}};
    $("categories").append(button);
  }
}
function choose(file) {selected=file; draft=clone(file.value??null); dirty=false; feedback(""); render();}
function render() {
  renderNav(); $("section-title").textContent=SECTIONS[category][1]; $("section-description").textContent=SECTIONS[category][2];
  const files=catalog.files.filter(f=>f.category===category&&matches(f));
  if(!selected||!files.some(f=>f.id===selected.id)) {selected=files[0]||null; draft=selected?clone(selected.value??null):null; dirty=false;}
  $("files").replaceChildren();
  for(const file of files) {const button=node("button","file-tab"+(selected?.id===file.id?" active":""),file.title);button.type="button";button.onclick=()=>{if(file.id!==selected?.id&&mayLeave())choose(file);};$("files").append(button);}
  $("empty").hidden=!!selected; renderEditor(); changed();
}
function renderEditor() {
  $("editor").replaceChildren(); if(!selected)return;
  const info=node("details","file-info");info.append(node("summary","","Configuration file"),node("code","",selected.path));$("editor").append(info);
  if(selected.error){$("editor").append(node("div","notice",selected.error));return;}
  const card=node("div","card"), head=node("div","card-heading");head.append(node("h2","",selected.title),node("small","","SAVED SETTINGS"));card.append(head);
  if(selected.format==="text"&&!selected.secret){const text=node("textarea","text-editor");text.value=draft;text.setAttribute("aria-label",selected.title);text.spellcheck=false;text.oninput=()=>setAt([],text.value);card.append(text);}
  else {const form=node("div","settings-form");if(draft&&typeof draft==="object"&&!Array.isArray(draft)){for(const [key,value]of Object.entries(draft))form.append(field(value,[key],key,0));form.append(addField([],0));}else form.append(field(draft,[],selected.secret?"Secret value":"Value",0,selected.secret));card.append(form);}
  $("editor").append(card);
}

function removeAt(path) {
  if(!path.length)return;
  const parent=getAt(path.slice(0,-1)),key=path.at(-1);
  if(Array.isArray(parent))parent.splice(key,1); else delete parent[key];
  renderEditor();changed();
}
function removeButton(path) {const button=node("button","mini remove","Remove");button.type="button";button.setAttribute("aria-label","Remove "+label(path.at(-1)));button.onclick=(event)=>{event.preventDefault();event.stopPropagation();removeAt(path);};return button;}
function emptyLike(value,key="") {
  if(key==="id")return "new-agent"; if(key==="name")return "New item";
  if(Array.isArray(value))return [];
  if(value&&typeof value==="object")return Object.fromEntries(Object.entries(value).map(([k,v])=>[k,emptyLike(v,k)]));
  return typeof value==="boolean"?false:typeof value==="number"?0:"";
}
function field(value,path,key,depth,forceSecret=false) {
  if(key==="args"&&path.length===1&&Array.isArray(value)&&value.length%2===0&&value.every((v,i)=>typeof v==="string"&&(i%2||v.startsWith("--")))) {
    const group=node("details","group");group.open=true;const summary=node("summary","","CLI task configuration");summary.append(node("small","","Named options"));group.append(summary);
    const body=node("div","group-body");for(let i=0;i<value.length;i+=2)body.append(field(value[i+1],[...path,i+1],value[i].slice(2).replace(/-/g,"_"),depth+1));group.append(body);return group;
  }
  if(value&&typeof value==="object") {
    const group=node("details","group");group.open=depth<2;
    const summary=node("summary","",label(key)); summary.append(node("small","",Array.isArray(value)?`${value.length} items`:"Settings"));group.append(summary);
    const body=node("div","group-body");
    if(Array.isArray(value)) {
      value.forEach((item,index)=>{const box=node("div","array-item"),head=node("div","item-heading");head.append(node("span","",item&&typeof item==="object"?(item.name||item.id||`Item ${index+1}`):`Item ${index+1}`),removeButton([...path,index]));box.append(head);
        if(item&&typeof item==="object"&&!Array.isArray(item)){for(const [k,v]of Object.entries(item))box.append(field(v,[...path,index,k],k,depth+1));box.append(addField([...path,index],depth+1));}
        else box.append(field(item,[...path,index],"Value",depth+1));body.append(box);});
      const add=node("button","mini","+ Add item");add.type="button";add.onclick=()=>{getAt(path).push(value.length?emptyLike(value[0]):"");renderEditor();changed();};const row=node("div","add-row");row.append(add);body.append(row);
    } else {for(const[k,v]of Object.entries(value))body.append(field(v,[...path,k],k,depth+1));body.append(addField(path,depth+1));}
    if(path.length){const row=node("div","advanced");row.append(removeButton(path));body.append(row);}group.append(body);return group;
  }
  const row=node("div","field"), description=node("label","field-label"), control=node("div","control");
  const id="setting-"+path.map(String).map(encodeURIComponent).join("-");description.htmlFor=id;description.append(node("strong","",label(key)));if(HELP[key])description.append(node("small","",HELP[key]));
  const secret=forceSecret||secretKey(key), retained=typeof value==="string"&&value.startsWith(KEEP);
  let input;
  if(typeof value==="boolean"){input=node("input");input.type="checkbox";input.checked=value;input.onchange=()=>setAt(path,input.checked);}
  else if(value===null){input=node("select");for(const type of ["Not set","Text","Number","Boolean","Object","List"])input.add(new Option(type,type));input.onchange=()=>{setAt(path,({Text:"",Number:0,Boolean:false,Object:{},List:[]})[input.value]??null);renderEditor();};}
  else {
    input=node(!secret&&typeof value==="string"&&(value.length>120||value.includes("\n"))?"textarea":"input");
    if(input.tagName==="INPUT")input.type=secret?"password":typeof value==="number"?"number":"text";
    if(typeof value==="number")input.step="any";
    input.value=retained?"":String(value);input.spellcheck=false;
    if(secret){input.autocomplete="new-password";input.placeholder=retained?"Saved value retained":"Enter a new value";}
    input.oninput=()=>setAt(path,typeof value==="number"?Number(input.value):input.value);
  }
  input.id=id;control.append(input);
  if(secret&&retained){const clear=node("button","mini","Clear");clear.type="button";clear.onclick=()=>{setAt(path,"");renderEditor();};control.append(clear);}
  if(path.length&&!Array.isArray(getAt(path.slice(0,-1)))){const remove=removeButton(path);remove.textContent="×";remove.classList.add("field-remove");control.append(remove);}
  row.append(description,control);return row;
}
function addField(path,depth) {
  const details=node("details","advanced"),summary=node("summary","","Add a setting");details.append(summary);
  const row=node("div","add-row"),key=node("input"),type=node("select"),button=node("button","mini","Add field");
  key.placeholder="Field name";key.setAttribute("aria-label","New field name");type.setAttribute("aria-label","New field type");
  for(const item of ["Text","Number","Boolean","Object","List"])type.add(new Option(item,item));
  button.type="button";button.onclick=()=>{const name=key.value.trim();if(!name||["__proto__","constructor","prototype"].includes(name)||Object.hasOwn(getAt(path),name)){feedback("Choose a new field name.");return;}setAt([...path,name],({Text:"",Number:0,Boolean:false,Object:{},List:[]})[type.value]);renderEditor();};
  row.append(key,type,button);details.append(row);return details;
}

async function load() {
  try {
    const response=await fetch("/api/settings",{cache:"no-store"});const result=await response.json();if(!response.ok)throw Error(result.error||"Settings could not be loaded.");
    catalog=result;
    $("notice").textContent=result.enabled?result.notice:"This workbench has no configuration directory. Open it from the configured Host.";
    if(selected){selected=catalog.files.find(file=>file.id===selected.id)||null;draft=selected?clone(selected.value??null):null;}
    dirty=false;render();
  } catch(error){feedback(error.message);}
}
$("save").onclick=async()=>{
  if(!selected||!dirty||busy)return;
  const invalid=$("editor").querySelector(":invalid");if(invalid){invalid.reportValidity();return;}
  busy=true;changed();feedback("");
  try {
    const response=await fetch("/api/settings",{method:"POST",headers:{"Content-Type":"application/json","X-Host-Action":catalog.mutation_token},body:JSON.stringify({file:selected.id,revision:selected.revision,value:draft})});
    const result=await response.json();if(!response.ok)throw Error(result.error||"Settings could not be saved. Refresh to inspect the outcome before retrying.");
    catalog.files=catalog.files.map(file=>file.id===result.file.id?result.file:file);choose(result.file);feedback(result.message,true);
  }catch(error){feedback(error.message);}finally{busy=false;changed();}
};
$("reset").onclick=()=>{if(selected)choose(selected);};
$("refresh").onclick=()=>{if(mayLeave())load();};
$("search").oninput=()=>{if(dirty){feedback("Save or discard changes before filtering settings.");return;}const found=catalog.files.find(matches);if(found&&!catalog.files.some(file=>file.category===category&&matches(file)))category=found.category;render();};
window.addEventListener("beforeunload",event=>{if(dirty){event.preventDefault();event.returnValue="";}});
load();

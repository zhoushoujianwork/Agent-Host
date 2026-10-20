"use strict";

window.AGHAgentProfiles = (() => {
  let selectedKey = null, activeTab = "overview";
  const tabs = {overview:"概览",configure:"配置",instructions:"指令",capabilities:"能力",context:"工作上下文",groups:"渠道与群聊",activity:"活动"};
  const el = (tag, cls, text) => {const n=document.createElement(tag);n.className=cls;if(text!==undefined)n.textContent=text;return n;};
  const destination = name => ({web:"Web Chat",cli:"本地 CLI",dingtalk:"钉钉",feishu:"飞书",unassigned:"未关联运行入口"})[name] || name;
  const state = value => ({configured:"已配置",disabled:"未启用",unknown:"未知",connected:"已连接（历史观测）",observed_loaded:"历史执行已装载",observed_assembled:"历史任务已装配"})[value] || value;
  function card(title, text, detail) {
    const c=el("section","agent-card");c.append(el("h3","",title),el("p","",text));
    if(detail)c.append(el("small","",detail));return c;
  }
  function note(parent, text) {parent.append(el("p","agent-note",text));}
  function choices(profiles) {
    const primary=profiles.find(p=>p.destination==="web"&&p.default)||profiles.find(p=>p.default);
    const result=primary?[primary]:[];
    for(const p of profiles){
      if(p.default||result.some(item=>item.file_id===p.file_id&&item.id===p.id))continue;
      result.push(p);
    }
    return result;
  }
  function render(container, profiles, file, draft, fields, renderField, error, editor, compact=false) {
    const available=profiles.filter(p=>p.file_id===file.id && !(p.destination==="cli"&&profiles.some(other=>other.file_id===p.file_id&&other.id===p.id&&other.destination==="web")));
    const web=available.find(p=>p.destination==="web");
    if(web&&editor)for(const agent of draft.agents||[]){
      if(!available.some(p=>p.id===agent.id))available.push({...web,id:agent.id,key:`${file.id}:web:${agent.id}`,name:agent.name||agent.id,default:false,chat_selected:false,native_inventory:null,skills:[],activity:[],instructions:[],groups:[],unsaved:true});
    }
    if(!available.length){note(container,error||"尚未取得智能体档案。可继续编辑下方已有配置。");return false;}
    if(error)note(container,error);
    const p=available.find(p=>p.key===selectedKey)||available[0];selectedKey=p.key;
    const rerender=()=>{container.replaceChildren();render(container,profiles,file,draft,file.read_only?[]:window.AGHSettings.fields(draft,file.category,file.secret),renderField,error,editor,compact);};
    const picker=el("div","agent-picker");
    for(const item of compact?[]:available){const b=el("button","agent-choice"+(p.key===item.key?" active":""),`${item.name} · ${destination(item.destination)}`);b.type="button";b.setAttribute("aria-pressed",String(p.key===item.key));b.onclick=()=>{selectedKey=item.key;activeTab="overview";rerender();};picker.append(b);}
    if(web&&editor&&!file.read_only){const add=el("button","quiet","＋ 新增智能体");add.type="button";add.onclick=()=>{const id=editor.add();selectedKey=`${file.id}:web:${id}`;activeTab="configure";rerender();};
      if(compact){const more=el("details","agent-more"),summary=el("summary","","更多智能体选项");more.append(summary,add);picker.append(more);}else picker.append(add);
    }
    container.append(picker);
    const header=el("header","agent-heading"),avatar=el("div","agent-avatar",p.name.slice(0,1)),identity=el("div","");
    identity.append(el("h2","",p.name),el("p","",`${destination(p.destination)} · ${p.chat_selected?"当前 Chat 选中 · ":""}${p.default?"默认接手未指定 Agent 的请求":["web","cli"].includes(p.destination)?"可在 Chat 中选择":"按明确绑定接手请求"}`));
    const status=el("span","agent-badge",p.unsaved?"新智能体 · 尚未保存":p.native_inventory?"已保存 · 有原生加载记录":"已保存 · 装载状态未知");header.append(avatar,identity,status);container.append(header);
    const nav=el("nav","agent-tabs");nav.setAttribute("aria-label","智能体档案");
    for(const [key,title]of Object.entries(tabs)){const b=el("button",activeTab===key?"active":"",title);b.type="button";b.setAttribute("aria-current",activeTab===key?"page":"false");b.onclick=()=>{activeTab=key;rerender();};nav.append(b);}
    container.append(nav);const content=el("div","agent-content");container.append(content);
    if(activeTab==="overview"){
      const grid=el("div","agent-grid");
      grid.append(card("执行模型",p.model||"使用原生模型默认值",`${p.engine||"执行引擎未知"}${p.transport?" · "+p.transport:""} · ${p.model_origin==="agent"?"来自 Agent 配置":"继承运行入口配置；实际执行以记录为准"}`),
        card("工作目录",p.workspace||"继承服务默认目录，具体路径未观测",p.workspace_origin==="agent"?"来自 Agent 配置":"继承运行入口"),
        card("服务范围",["cli","web"].includes(p.destination)?"本地独立会话":`${p.groups.length} 个已配置或观测群聊`,["cli","web"].includes(p.destination)?"使用本地连接中的 Agent；不接管运行中的渠道会话。":p.default?"未明确绑定的后续群聊也使用此 Agent；这不是完整群成员列表。":"只列出当前配置选中此 Agent 的群聊。"),
        card("配置状态","已保存，当前装载版本未知","服务设置修改后需重启；历史运行不证明当前配置已经装载。"));content.append(grid);
      if(p.destination==="web")note(content,"此档案来自当前 Web Chat 的实际连接；可在 Chat 顶部选择此目录中的智能体。渠道智能体保持各自的运行入口。");
      if(p.native_inventory)content.append(card("最近原生加载记录",`${p.native_inventory.transport} · ${p.native_inventory.skills_observed?p.native_inventory.skills.length+" 个技能":"技能数量未知"} · ${p.native_inventory.services.length} 个知识与工具服务`,`${p.native_inventory.observed_at}；这是该 Agent 状态库中的历史观测，不证明当前配置版本已装载。`));
      note(content,file.read_only?"此档案为只读；配置来源见工作上下文。":"本页展示已保存的配置和有限历史证据。下方保留已有编辑项；更改尚未保存时，档案仍展示已保存版本。");
      const edits=el("section","simple-group");const index=draft.agents.findIndex(a=>a.id===p.id);
      for(const field of fields.filter(f=>f.path[0]!=="agents"||f.path[1]===index))edits.append(renderField(field));content.append(edits);
    }else if(activeTab==="configure"){
      const index=draft.agents.findIndex(a=>a.id===p.id);
      if(file.read_only||!editor||p.destination!=="web"){note(content,"当前入口保留已有配置编辑；Web Chat 智能体可在此管理专属能力。");return true;}
      note(content,"保存后即可在 Chat 顶部切换；下次发送时使用新配置。已有聊天保留，执行环境变化时开启新的原生会话。");
      const edits=el("section","simple-group");
      for(const field of fields.filter(f=>f.path[0]!=="agents"||f.path[1]===index))edits.append(renderField(field));content.append(edits);
      editor.capabilities(content,index);
    }else if(activeTab==="instructions"){
      content.append(card("Host 工作规则","由 Host 按渠道、身份和当前任务装配。","角色指令补充原生执行引擎规则；来源材料不能授予权限。"));
      for(const item of p.instructions){const c=card(({instructions_file:"角色指令文件",instructions_dir:"指令目录",user_file:"用户上下文文件"})[item.kind],item.path,item.state==="unavailable"?"来源不可读取；请检查文件或权限。":"已保存来源；不证明运行时已经装载。用户上下文只展示来源。");if(item.text){c.append(el("pre","agent-instruction",item.text));if(item.truncated)note(c,"预览截取前 16000 个字符。");}content.append(c);}
      if(!p.instructions.length)note(content,"未设置 Agent 专属指令来源；任务仍会接收 Host 和原生引擎适用的规则。");
      if(p.execution_config)content.append(card("执行引擎配置",p.execution_config,"已配置来源；模型和工具能力需结合实际运行判断。"));
    }else if(activeTab==="capabilities"){
      const search=el("input","agent-search");search.type="search";search.placeholder="搜索技能、连接器或 Host 能力";search.setAttribute("aria-label",search.placeholder);content.append(search);
      const inventory=el("div","agent-inventory");content.append(inventory);
      const native=p.native_inventory;
      if(native)note(content,`最近观测：${native.observed_at} · ${native.transport}。技能列表随任务变化；历史加载不等于本轮调用或执行成功。`);
      const entries=[...(native?.skills||[]).map(name=>({type:"技能",title:name,text:"原生实际已加载（历史观测）",detail:native.observed_at})),
        ...p.skills.filter(s=>!(native?.skills||[]).includes("agent-host:"+s.name)).map(s=>({type:"技能",title:s.name,text:`记忆技能 · 版本 ${s.revision} · ${state(s.state)}`,detail:`${s.observed_at}；装配不等于原生加载或执行成功。`})),
        ...(native?.services||[]).map(s=>({type:"知识与工具服务",title:s.name,text:state(s.status),detail:`${native.observed_at}；原生初始化观测，未验证所有检索与写入操作。`})),
        ...p.connectors.map(c=>({type:"连接器与数据源",title:c.name,text:state(c.state),detail:`${c.scope==="agent_selection"?"此智能体已选择":"默认共享"}；${c.origin}。配置不证明账号连通。`})),
        {type:"Host 能力",title:"任务路由与上下文装配",text:"由 Host 提供",detail:"结合入口、Agent 和项目配置组织执行。"},
        {type:"Host 能力",title:"执行证据与长期记忆",text:"由 Host 提供",detail:"执行结束、业务验收、回复交付与记忆保存分别记录。"}];
      function filter(){inventory.replaceChildren();const q=search.value.trim().toLowerCase();for(const type of ["技能","知识与工具服务","连接器与数据源","Host 能力"]){inventory.append(el("h3","agent-section-label",type));const matches=entries.filter(e=>e.type===type&&[e.title,e.text,e.detail].join(" ").toLowerCase().includes(q));const grid=el("div","agent-grid");for(const e of matches)grid.append(card(e.title,e.text,e.detail));inventory.append(grid);if(!matches.length)note(inventory,q?"没有匹配项。":type==="技能"?"暂无技能加载证据；这不证明没有其他原生技能。":"尚无配置或原生观测记录。");}}
      search.oninput=filter;filter();
    }else if(activeTab==="context"){
      content.append(card("默认工作目录",p.workspace||"继承服务默认值，具体路径未知","实际任务可以选择关联项目。"),
        card("可选项目",p.projects===null?"继承已登记项目目录":p.projects.length?p.projects.join("、"):"未选择登记项目","项目范围不等于原生文件系统隔离。"),
        card("记忆范围","按 Agent 与会话受众分开组织","同一 Agent 服务多个群，不会因此合并群聊历史；私聊上下文不会成为群聊的共享上下文。"));
      note(content,`配置来源：${p.origin}`);
    }else if(activeTab==="groups"){
      content.append(card("运行入口",destination(p.destination),p.account?`账号 / 应用：${p.account} · ${p.enabled?"已配置启用":"未启用"}`:"未取得关联账号；入口状态不证明在线。"));
      if(p.default)note(content,"默认路由：没有明确 Agent 绑定的群聊请求由此 Agent 接手。未知群聊无法提前枚举。");
      for(const g of p.groups)content.append(card(g.conversation_id,g.routing==="explicit"?"明确绑定":"按当前默认路由接手",`${g.tenant?"组织："+g.tenant+" · ":""}${g.last_seen?"最近观测："+g.last_seen:"暂无活动记录"}${g.last_agent?" · 最近执行 Agent："+g.last_agent:""}`));
      if(!p.groups.length)note(content,"没有已配置或已记录群聊；这不代表 Agent 不会服务新的群聊。");
    }else{
      note(content,"最近最多 20 条执行；仅查询当前入口、账号与 Agent 的有限本地历史，不读取消息正文。");
      for(const run of p.activity){const c=card(run.created_at||run.id,`执行：${run.execution} · 验收：${run.acceptance} · 交付：${run.delivery}`,run.model?`运行模型：${run.model}`:null);const a=el("a","","查看执行证据 →");a.href="/executions.html?key="+encodeURIComponent(run.key);c.append(a);content.append(c);}
      if(!p.activity.length)note(content,"暂无可关联的执行记录。");
    }
    if(p.evidence_state==="read_failed")note(content,"本地执行证据读取失败；以上配置仍可查看，活动信息暂不可用。");
    else if(p.evidence_state==="unknown")note(content,"未连接此入口的执行证据，装载状态和活动记录未知。");
    return true;
  }
  return {render,choices,select(key){selectedKey=key;activeTab="overview";}};
})();

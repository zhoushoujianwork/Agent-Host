"use strict";

window.AGHPlatformSkills = (() => {
  const el = (tag, cls, text) => {const n=document.createElement(tag);n.className=cls;if(text!==undefined)n.textContent=text;return n;};
  let destination="", selectedID="", generation=0;
  function lifecycle(record, revision) {
    if(!revision)return "无版本";
    if(revision.withdrawn)return "已撤回";
    if(!revision.published)return revision.reviewer?"已审核 · 待发布":"待审核";
    if(revision.entry.expires_at&&Date.parse(revision.entry.expires_at)<=Date.now())return "已过期";
    return record.enabled&&record.selected===revision.entry.revision?"已启用 · 就绪待任务检查":"已发布 · 未启用";
  }
  function current(record) {return record.revisions.find(v=>v.entry.revision===record.selected)||record.revisions.at(-1);}
  function matches(record, query, state) {
    const revision=current(record),entry=revision?.entry||{};
    return [entry.name,entry.description,record.id].join(" ").toLowerCase().includes(query.toLowerCase())&&(!state||(state==="enabled"?record.enabled:!record.enabled));
  }
  async function request(query) {
    const response=await fetch("/api/platform-skills?"+new URLSearchParams(query),{cache:"no-store"});
    const result=await response.json();if(!response.ok)throw Error(result.error||"平台技能读取失败。");return result;
  }
  async function render(container, selection={}) {
    if(selection.destination!==undefined)destination=selection.destination;
    if(selection.id!==undefined)selectedID=selection.id;
    const token=++generation;
    container.replaceChildren(el("p","help-note","正在读取平台技能…"));
    let data;
    try{data=await request(destination?{destination}:{});}catch(error){if(token===generation)container.replaceChildren(el("p","notice",error.message));return;}
    if(token!==generation)return;
    container.replaceChildren();
    if(!data.enabled){container.append(el("p","notice","未连接已配置的 Host，平台技能目录不可用。"));return;}
    destination=data.destination.id;
    const controls=el("div","platform-controls"),picker=el("select","");picker.setAttribute("aria-label","技能运行入口");
    for(const d of data.destinations){const option=el("option","",d.label);option.value=d.id;picker.append(option);}picker.value=destination;
    picker.onchange=()=>{destination=picker.value;selectedID="";render(container);};controls.append(picker);
    const search=el("input","agent-search");search.type="search";search.placeholder="搜索技能名称、用途或 ID";search.setAttribute("aria-label",search.placeholder);controls.append(search);
    const filter=el("select","");filter.setAttribute("aria-label","技能启用状态");
    for(const [value,label]of [["","全部状态"],["enabled","已启用"],["disabled","未启用"]]){const option=el("option","",label);option.value=value;filter.append(option);}controls.append(filter);container.append(controls);
    if(data.error){container.append(el("p","notice",data.error));return;}
    container.append(el("p","help-note",`目录：${data.destination.root}`),el("p","notice","当前展示手写技能的纳管版本。经验与插件技能见智能体的历史快照；当前任务就绪状态尚未检查。导入、审核和启停请使用 CLI。"));
    const count=el("p","help-note"),list=el("div","platform-list"),detail=el("section","platform-detail");detail.setAttribute("aria-label","技能详情");container.append(count,list,detail);
    let fileGeneration=0;
    function show(record){
      selectedID=record.id;fileGeneration++;detail.replaceChildren();
      const versions=el("select","");versions.setAttribute("aria-label","查看技能版本");
      for(const revision of [...record.revisions].reverse()){const option=el("option","",`版本 ${revision.entry.revision} · ${lifecycle(record,revision)}`);option.value=String(revision.entry.revision);versions.append(option);}
      versions.value=String(current(record)?.entry.revision||"");detail.append(el("h2","",current(record)?.entry.name||record.id),versions);
      const body=el("div","");detail.append(body);
      function showVersion(){
        const revision=record.revisions.find(v=>String(v.entry.revision)===versions.value);if(!revision)return;
        const entry=revision.entry;fileGeneration++;body.replaceChildren();
        body.append(el("p","",entry.description),el("p","help-note",`ID：${entry.id} · 来源：手写技能 · 所有者：${entry.owner}`),el("p","help-note",`适用条件：${entry.applicability}`),el("p","help-note",`Agent：${(entry.agents||[]).join(", ")} · 受众：${(entry.audiences||[]).join(", ")} · 项目范围：${(entry.scopes||[]).join(", ")}`));
        body.append(el("p","help-note",`依赖插件：${(entry.required_plugins||[]).join(", ")||"无声明"} · 工具：${(entry.required_tools||[]).join(", ")||"无声明"}`),el("p","help-note",`审核：${revision.reviewer||"未审核"}${revision.review_note?" · "+revision.review_note:""}`),el("p","help-note",`来源：${entry.canonical_ref} · ${(entry.provenance||[]).join("; ")}`),el("p","platform-digest",`固定版本摘要：${entry.digest}`));
        const files=el("div","platform-files"),fileStatus=el("p","help-note"),content=el("pre","platform-source");content.setAttribute("tabindex","0");body.append(files,fileStatus,content);
        async function read(path){
          const fileToken=++fileGeneration;fileStatus.textContent="正在读取固定版本…";content.textContent="";
          try{const value=await request({destination,id:record.id,revision:String(entry.revision),digest:entry.digest,file:path});
            if(token!==generation||fileToken!==fileGeneration)return;
            if(value.error||!value.complete||typeof value.content!=="string")throw Error(value.error||"固定版本内容不可用，请刷新目录。");
            content.textContent=value.content;fileStatus.textContent=`${value.path} · 完整读取 · ${value.bytes} 字节${value.redacted?" · 检测到的凭据已隐藏":""}`;
          }catch(error){if(token===generation&&fileToken===fileGeneration)fileStatus.textContent=error.message;}
        }
        for(const file of revision.file_inventory||[]){
          const readable=file.path==="SKILL.md"||file.path.startsWith("references/");
          const button=el("button","quiet",`${file.path} · ${file.bytes} B`);button.type="button";button.disabled=!readable;button.onclick=()=>read(file.path);files.append(button);
        }
        if((revision.file_inventory||[]).some(f=>f.path==="SKILL.md"))read("SKILL.md");
        const history=el("details","platform-audit");history.append(el("summary","","版本操作记录"));
        for(const event of (data.audit||[]).filter(e=>e.id===record.id))history.append(el("p","help-note",`${event.at} · ${event.action} · v${event.revision} · ${event.actor}`));body.append(history);
      }
      versions.onchange=showVersion;showVersion();
    }
    function refresh(){
      list.replaceChildren();const rows=(data.records||[]).filter(r=>matches(r,search.value.trim(),filter.value));
      count.textContent=`${rows.length} / ${(data.records||[]).length} 个手写纳管技能 · 完整目录`;
      for(const record of rows){const revision=current(record),entry=revision?.entry||{};
        const button=el("button","platform-row");button.type="button";button.append(el("strong","",entry.name||record.id),el("span","",entry.description||"无说明"),el("small","",`${lifecycle(record,revision)} · v${revision?.entry.revision||"?"} · ${record.revisions.length} 个版本`));button.onclick=()=>show(record);list.append(button);
      }
      if(!rows.length)list.append(el("p","help-note",data.records?.length?"没有匹配的技能。":"此入口尚未登记手写技能；不代表没有经验或插件技能。"));
    }
    search.oninput=refresh;filter.onchange=refresh;refresh();
    const selected=(data.records||[]).find(r=>r.id===selectedID);if(selected)show(selected);
    else if(selectedID)detail.append(el("p","notice","此技能不在当前手写目录中。经验与插件技能保留各自来源；请查看对应执行快照。"));
  }
  return {render,lifecycle,matches,open:null};
})();

"use strict";

// Only first-use choices are exposed. Unlisted values remain in the saved draft.
window.AGHSettings = {
  section(file) { return file.category === "providers" ? "models" : file.category; },
  fields(value, category, secret = false) {
    const fields = [];
    const add = (path, label, hint = "", options = null, fallback = "") => {
      const current = path.reduce((v,k) => v?.[k], value);
      fields.push({path, label, hint, options, value: current ?? fallback});
    };
    if (secret) { add([], "连接密钥", "留空保留已保存的密钥。"); return fields; }
    if (category === "agents") {
      add(["default_agent"], "默认智能体", "没有指定角色时，由它接手任务。", (value.agents || []).map(a => [a.id,a.name || a.id]));
      (value.agents || []).forEach((agent,index) => {
        add(["agents",index,"name"], "名称", "", null, agent.id);
        add(["agents",index,"workspace"], "工作目录", "填写已有文件夹的完整路径；留空使用既有默认值。");
      });
    } else if (category === "models") {
      const profiles=value.profiles || {};
      const id=value.default_profile || (Object.hasOwn(profiles,"default")?"default":Object.keys(profiles)[0]);
      if(profiles[id]) add(["profiles",id,"execution","model"], "默认模型", "留空使用原生工具的默认模型。");
      if (value.execution) add(["execution","model"], "模型", "留空使用原生工具的默认模型。");
    } else if (category === "providers") {
      add(["ANTHROPIC_BASE_URL"], "服务地址", "使用自定义模型网关时填写。默认连接无需修改。");
      for (const key of ["ANTHROPIC_AUTH_TOKEN","ANTHROPIC_API_KEY"]) {
        if (Object.hasOwn(value,key) || key === "ANTHROPIC_AUTH_TOKEN" && !Object.hasOwn(value,"ANTHROPIC_API_KEY")) add([key], "API 密钥", "留空保留已保存的密钥。");
      }
    } else if (category === "channels") {
      add(["enabled"], "启用渠道", "保存后重启 Host 服务生效。", null, false);
      const labels = {profile:"登录账号",app_id:"应用 ID",bot_open_id:"机器人 ID",tenant_id:"组织 ID",owner_open_id:"使用者 ID",client_id:"应用 ID",robot_code:"机器人编码",corp_id:"组织 ID",owner_user_id:"使用者 ID"};
      const walk = (entry,path) => {
        if (!entry || typeof entry !== "object" || Array.isArray(entry)) return;
        for (const [key,child] of Object.entries(entry)) {
          if (labels[key]) add([...path,key], labels[key]);
          else if (key === "enabled") add([...path,key], path.at(-1) === "bot" ? "接收机器人消息" : "启用账号能力", "", null, false);
          else if (child && typeof child === "object") walk(child,[...path,key]);
        }
      };
      walk(value.settings,["settings"]);
    }
    return fields;
  },
};

"use strict";
(function () {
  const escape = value => String(value).replace(/[&<>"']/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));
  const metric = value => value == null ? "not measured" : escape(value);
  const rate = value => value == null ? "not measured" : `${(value * 100).toFixed(1)}%`;
  const coverage = pair => pair ? `${metric(pair[0])} of ${metric(pair[1])} runs` : "not measured";
  const table = (title, rows, covered) => `<section><h2>${escape(title)}</h2><p class="coverage">${covered}</p><table><thead><tr><th>指标</th><th>数量 / 值</th><th>覆盖率</th></tr></thead><tbody>${rows.map(([name, value, rowCoverage]) => `<tr><td>${escape(name)}</td><td>${metric(value)}</td><td>${rowCoverage || covered}</td></tr>`).join("")}</tbody></table>${rows.length ? "" : "<p>无记录</p>"}</section>`;
  function render(report, heading = "当前窗口") {
    const total = report.totals?.runs;
    const full = coverage(total == null ? null : [total, total]);
    const outcomes = report.outcomes || {};
    const usage = report.usage || {};
    const duration = report.duration_seconds || {};
    const tools = report.tools || {};
    const cov = report.coverage || {};
    const sources = report.sources || [];
    let html = `<article><h2>${escape(heading)}</h2><p>${metric(report.window?.since)} — ${metric(report.window?.until)}</p>`;
    html += `<aside class="banner">来源覆盖：${sources.filter(s => s.status === "ok").length} / ${sources.length}；${full}（仅可读来源，缺失来源未计入分母）。${sources.map(s => `<p>${escape(s.name)} · ${escape(s.status)} · ${metric(s.runs)} runs${s.reason ? " · " + escape(s.reason) : ""}</p>`).join("")}</aside>`;
    html += `<p class="notice">${total > 0 && report.acceptance?.unverified === total ? "所有执行的业务验收均未验证。" : "业务验收与执行完成分别记录。"}完成数量不代表成功率。</p>`;
    html += `<div class="cards">${[["Runs", total], ["Completed", outcomes.completed], ["Failed", outcomes.failed], ["Stale running", outcomes.stale_running], ["Unrecorded", outcomes.unrecorded]].map(([name, value]) => `<section><h3>${name}</h3><strong>${metric(value)}</strong><p class="coverage">${full}</p></section>`).join("")}</div>`;
    const daily = report.daily || [];
    const fields = ["completed", "canceled", "failed", "other"];
    const max = Math.max(1, ...daily.map(d => fields.reduce((sum, k) => sum + (d[k] || 0), 0)));
    html += `<section><h2>每日执行</h2><p class="coverage">${full}</p><p>completed · canceled · failed · other（运行中、过期运行及未记录）</p><div class="chart">${daily.map(d => `<div class="day"><span>${escape(d.date)}</span><div class="bar">${fields.map(k => `<svg class="${k}" width="${100 * (d[k] || 0) / max}%" height="24" role="img" aria-label="${k}: ${metric(d[k])}"><title>${k}: ${metric(d[k])}</title><rect width="100%" height="24"></rect></svg>`).join("")}</div><small>${fields.map(k => `${k}: ${metric(d[k])}`).join(" · ")}</small></div>`).join("")}</div>${daily.length ? "" : "<p>无记录</p>"}</section>`;
    html += '<div class="grid">';
    html += table("执行结果", Object.entries(outcomes), full);
    html += table("AI 成功率（resolved / judged）", [["成功率", rate(report.success_rate)]], coverage(cov.acceptance));
    for (const kind of report.by_task_kind || []) html += table(`任务类型：${kind.name}`, [["成功率", rate(kind.success_rate)], ...Object.entries(kind.outcomes || {})], coverage(kind.coverage));
    html += table("业务验收", Object.entries(report.acceptance || {}), full);
    html += table("错误分类", (report.error_classes || []).map(r => [r.class, r.runs]), full);
    for (const [key, title] of [["by_agent", "Agent"], ["by_channel", "渠道"], ["by_model", "模型"]]) html += table(title, (report[key] || []).map(r => [r.name, r.runs]), full);
    html += table("耗时（秒）", [["median", duration.median], ["p90", duration.p90], ["max", duration.max]], coverage(cov.duration));
    html += table("工具（每个工具按执行去重）", [["有工具活动", tools.runs_with_tool_activity, full], ["存在未命名工具", tools.unnamed_runs, full], ...(tools.named || []).map(r => [r.name, r.runs])], coverage(cov.tools_named));
    html += table("用量与费用", [["input tokens", usage.input_tokens, coverage(cov.input_tokens)], ["output tokens", usage.output_tokens, coverage(cov.output_tokens)], ["cost USD（原生估算，非账单）", usage.cost_usd, coverage(cov.cost_usd)], ["缺少费用的执行", usage.runs_missing_cost]], coverage(cov.usage));
    html += `</div><section><h2>需要关注</h2><p class="coverage">${full}；列表由 Host 限定条数。</p><ul>${(report.attention || []).map(item => `<li>${escape(item.reason)} · ${escape(item.run_id)} ${(item.record_keys || []).map(key => `<a href="/executions.html?key=${encodeURIComponent(key)}" target="_top">查看执行记录</a>`).join(" ")}${item.record_keys?.length ? "" : "（记录链接不可用）"}${item.link_resolution_incomplete ? "（部分来源无法解析记录链接）" : ""}</li>`).join("")}</ul></section></article>`;
    return html;
  }
  if (typeof module !== "undefined") module.exports = {render, metric, coverage};
  if (typeof document === "undefined") return;
  const windowSelect = document.getElementById("window"), compare = document.getElementById("compare");
  const taskKind = document.getElementById("task-kind");
  let generation = 0, pending = 0;
  async function refresh() {
    const current = ++generation;
    pending++;
    const query = new URLSearchParams({since: windowSelect.value, compare: compare.checked ? "1" : "0"});
    if (taskKind?.value) query.set("task-kind", taskKind.value);
    try {
      const response = await fetch(`/api/stats?${query}`);
      const data = await response.json();
      if (!response.ok) throw Error(data.error || `HTTP ${response.status}`);
      if (current !== generation) return;
      document.getElementById("report").innerHTML = render(data) + (data.previous ? render(data.previous, "前一等长窗口") : "");
      document.getElementById("status").textContent = `生成时间：${data.generated_at}`;
    } catch (error) {
      if (current !== generation) return;
      document.getElementById("report").replaceChildren();
      document.getElementById("status").textContent = `统计不可用：${error.message}`;
    } finally {
      pending--;
    }
  }
  windowSelect.onchange = compare.onchange = refresh;
  if (taskKind) taskKind.onchange = refresh;
  setInterval(() => { if (!document.hidden && !pending) refresh(); }, 5000);
  refresh();
}());

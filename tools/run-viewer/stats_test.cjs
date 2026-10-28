const {test} = require('node:test');
const assert = require('node:assert/strict');
const {render, metric, coverage} = require('./stats.js');
const fixture = require('../../internal/stats/testdata/report.json');
test('zero and missing measurements remain distinct', () => {
  assert.equal(metric(0), '0');
  assert.equal(metric(null), 'not measured');
  const html = render({totals:{runs:0}, outcomes:{completed:0}, daily:[], sources:[], duration_seconds:{median:null}});
  assert.match(html, /0 of 0 runs/);
  assert.match(html, /not measured/);
  assert.doesNotMatch(html, /NaN|Infinity|undefined/);
});
test('actual Host fixture renders all panels and unverified acceptance', () => {
  const html = render({...fixture, acceptance:{unverified:fixture.totals.runs}});
  for (const text of ['所有执行的业务验收均未验证', '完成数量不代表成功率', '每日执行', '错误分类', 'Agent', '渠道', '模型', '工具', '耗时', '用量与费用']) assert.ok(html.includes(text), text);
  assert.ok(html.includes(coverage(fixture.coverage.duration)));
});
test('partial sources, coverage, safe drill-down links and escaped data', () => {
  const html = render({totals:{runs:4}, sources:[{name:'cli',status:'ok',runs:4},{name:'feishu',status:'unreadable',runs:null,reason:'busy'}], coverage:{usage:[1,4]}, attention:[{run_id:'r1',reason:'<script>',record_keys:['cli:db:r1']}]});
  assert.match(html, /1 of 4 runs/);
  assert.match(html, /feishu · unreadable · not measured runs · busy/);
  assert.match(html, /executions.html\?key=cli%3Adb%3Ar1/);
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
});
test('page fetches only stats, renders comparison and clears failed data', async () => {
  const vm = require('node:vm');
  const fs = require('node:fs');
  const nodes = Object.fromEntries(['window', 'compare', 'report', 'status'].map(id => [id, {value:'7d', checked:true, innerHTML:'', replaceChildren(){this.innerHTML='';}}]));
  const requests = [];
  let interval, fail = false;
  const context = {document:{hidden:false, getElementById:id => nodes[id]}, URLSearchParams,
    setInterval(fn){interval=fn;}, fetch:async url => {requests.push(url); return {ok:!fail,json:async () => fail ? {error:'busy'} : {...fixture, previous:fixture}};}};
  vm.runInNewContext(fs.readFileSync(require.resolve('./stats.js'), 'utf8'), context);
  await new Promise(resolve => setImmediate(resolve));
  assert.ok(nodes.report.innerHTML.includes('前一等长窗口'));
  assert.deepEqual(requests, ['/api/stats?since=7d&compare=1']);
  fail = true;
  interval();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(nodes.report.innerHTML, '');
  assert.match(nodes.status.textContent, /busy/);
});

'use strict';
// ---------- helpers ----------
const $ = (s, el) => (el || document).querySelector(s);
const $$ = (s, el) => Array.from((el || document).querySelectorAll(s));
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
async function api(path, opts = {}) {
  const res = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...opts });
  let data = null;
  try { data = await res.json(); } catch (e) { /* ignore */ }
  if (res.status === 401) { showLogin(); throw new Error('unauthorized'); }
  if (!res.ok) throw new Error((data && data.error) || ('HTTP ' + res.status));
  return data;
}
function toast(msg) {
  const t = $('#toast'); t.textContent = msg; t.classList.remove('hidden');
  clearTimeout(t._h); t._h = setTimeout(() => t.classList.add('hidden'), 2600);
}
function fmtBytes(b) {
  if (!b && b !== 0) return '-';
  const u = ['B','KB','MB','GB','TB']; let i = 0; let v = b;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return v.toFixed(v >= 100 ? 0 : 1) + ' ' + u[i];
}
function fmtSpeed(b) { return fmtBytes(b) + '/s'; }
function fmtUptime(sec) {
  if (!sec) return '-';
  const d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60);
  return (d ? d + '天' : '') + h + '小时' + m + '分';
}
function pct(used, total) { return total ? Math.min(100, Math.round(used / total * 100)) : 0; }
function bar(used, total) {
  const p = pct(used, total);
  const cls = p >= 90 ? 'crit' : p >= 75 ? 'hot' : '';
  return `<div class="bar"><div class="${cls}" style="width:${p}%"></div></div>`;
}
function msCell(r) {
  if (!r || r.avg_ms < 0) return '<span class="ms-bad">超时</span>';
  const cls = r.avg_ms < 80 ? 'ms-good' : r.avg_ms < 180 ? 'ms-mid' : 'ms-bad';
  return `<span class="${cls}">${r.avg_ms.toFixed(0)} ms</span>` + (r.loss_pct > 0 ? ` <span class="muted small">丢${r.loss_pct.toFixed(0)}%</span>` : '');
}
const KIND_CN = { init: '初始化模板', finalize_ssh: 'SSH端口收尾', probe_now: '立即探测', deploy: '部署协议', undeploy: '卸载协议', restart_proto: '重启协议服务', sync: '同步' };
const MODULE_CN = { 'vless-reality': 'VLESS + Reality', 'ss2022': 'Shadowsocks 2022' };

// ---------- state ----------
let nodes = [];
let currentNodeId = null;
let settings = null;
let taskTimer = null;

// ---------- auth ----------
function showLogin() {
  $('#login-view').classList.remove('hidden');
  $('#app-view').classList.add('hidden');
}
function showApp() {
  $('#login-view').classList.add('hidden');
  $('#app-view').classList.remove('hidden');
}
$('#login-form').addEventListener('submit', async e => {
  e.preventDefault();
  $('#login-error').textContent = '';
  try {
    await api('/api/login', { method: 'POST', body: JSON.stringify({ password: $('#login-password').value }) });
    showApp(); refresh();
  } catch (err) { $('#login-error').textContent = '密码不正确'; }
});
$('#btn-logout').addEventListener('click', async () => {
  await api('/api/logout', { method: 'POST' }).catch(() => {});
  showLogin();
});

// ---------- nodes list ----------
async function refresh() {
  try {
    const data = await api('/api/nodes');
    nodes = data.nodes || [];
    const me = await api('/api/me').catch(() => null);
    if (me) $('#version').textContent = 'v' + me.version;
    renderSummary();
    if (currentNodeId) renderDetail(); else renderGrid();
  } catch (e) { /* 401 handled */ }
}
function renderSummary() {
  const online = nodes.filter(n => n.online).length;
  const deps = nodes.reduce((a, n) => a + (n.deployments ? n.deployments.length : 0), 0);
  $('#summary').innerHTML = `
    <div class="stat"><div class="num">${nodes.length}</div><div class="lbl">节点总数</div></div>
    <div class="stat"><div class="num">${online}</div><div class="lbl">在线</div></div>
    <div class="stat"><div class="num">${nodes.length - online}</div><div class="lbl">离线</div></div>
    <div class="stat"><div class="num">${deps}</div><div class="lbl">已部署协议</div></div>`;
}
function meter(label, used, total, extra) {
  return `<div class="meter"><div class="rowline"><span>${label}</span><span>${extra || (fmtBytes(used) + ' / ' + fmtBytes(total))}</span></div>${bar(used, total)}</div>`;
}
function renderGrid() {
  $('#node-grid').classList.remove('hidden');
  $('#node-detail').classList.add('hidden');
  const g = $('#node-grid');
  if (!nodes.length) {
    g.innerHTML = '<p class="muted">还没有节点，点右上角「添加 VPS」接入第一台。</p>';
    return;
  }
  g.innerHTML = nodes.map(n => {
    const st = n.status || {};
    return `<div class="node-card" data-action="open" data-id="${n.id}">
      <div class="node-head"><span class="node-name"><span class="dot ${n.online ? 'on' : 'off'}"></span>${esc(n.name)}</span>
      <span class="badge">${esc(st.arch || '')}</span></div>
      <div class="node-ip">${esc(n.ip)} · SSH ${n.ssh_port}${n.new_ssh_port ? ' → ' + n.new_ssh_port + '（待收尾）' : ''} · ${esc((n.hello && n.hello.os) || st.os || '')}</div>
      ${n.online && n.status ? `
      ${meter('CPU', st.cpu_percent, 100, st.cpu_percent.toFixed(1) + '%')}
      ${meter('内存', st.mem_used, st.mem_total)}
      ${meter('磁盘', st.disk_used, st.disk_total)}
      <div class="netline"><span>↓ <b>${fmtSpeed(st.net_in_speed)}</b></span><span>↑ <b>${fmtSpeed(st.net_out_speed)}</b></span>
      <span>流量 ↓${fmtBytes(st.net_in_total)} ↑${fmtBytes(st.net_out_total)}</span><span>运行 ${fmtUptime(st.uptime_sec)}</span></div>`
      : '<p class="muted">离线' + (n.last_seen ? ' · 最后在线 ' + new Date(n.last_seen).toLocaleString() : '') + '</p>'}
    </div>`;
  }).join('');
}
function curNode() { return nodes.find(n => n.id === currentNodeId); }

// ---------- node detail ----------
function renderDetail() {
  const n = curNode();
  if (!n) { currentNodeId = null; renderGrid(); return; }
  $('#node-grid').classList.add('hidden');
  const el = $('#node-detail');
  el.classList.remove('hidden');
  const st = n.status || {};
  const probes = (n.probes && n.probes.results) || [];
  const byCarrier = {};
  probes.forEach(r => { (byCarrier[r.carrier] = byCarrier[r.carrier] || []).push(r); });
  const carrierName = { CT: '电信', CU: '联通', CM: '移动', OTHER: '其他' };
  el.innerHTML = `
  <div class="detail-head">
    <button class="btn" data-action="back">← 返回</button>
    <h2><span class="dot ${n.online ? 'on' : 'off'}"></span>${esc(n.name)}</h2>
    <span class="muted">${esc(n.ip)} · SSH 端口 ${n.ssh_port}</span>
    ${n.online ? '<span class="badge">在线</span>' : '<span class="badge">离线</span>'}
    <span style="flex:1"></span>
    <button class="btn danger" data-action="delete-node">删除节点</button>
  </div>

  <div class="panel-box"><h3>状态</h3>
    <div class="kv">
      <div><span class="k">系统：</span>${esc(st.os || (n.hello && n.hello.os) || '-')}</div>
      <div><span class="k">内核：</span>${esc(st.kernel || (n.hello && n.hello.kernel) || '-')}</div>
      <div><span class="k">主机名：</span>${esc(st.hostname || (n.hello && n.hello.hostname) || '-')}</div>
      <div><span class="k">运行时间：</span>${fmtUptime(st.uptime_sec)}</div>
      <div><span class="k">负载：</span>${st.load1 !== undefined ? st.load1.toFixed(2) + ' / ' + st.load5.toFixed(2) + ' / ' + st.load15.toFixed(2) : '-'}</div>
      <div><span class="k">时区设置：</span>${esc(n.timezone || '-')}</div>
    </div>
    ${n.status ? `<div style="margin-top:10px">${meter('CPU', st.cpu_percent, 100, st.cpu_percent.toFixed(1) + '%')}${meter('内存', st.mem_used, st.mem_total)}${meter('磁盘', st.disk_used, st.disk_total)}
    <div class="netline"><span>↓ <b>${fmtSpeed(st.net_in_speed)}</b></span><span>↑ <b>${fmtSpeed(st.net_out_speed)}</b></span><span>累计 ↓${fmtBytes(st.net_in_total)} ↑${fmtBytes(st.net_out_total)}</span></div></div>` : '<p class="muted">暂无状态数据（agent 离线）</p>'}
  </div>

  <div class="panel-box"><h3>初始化模板</h3>
    <div class="row" style="flex-wrap:wrap">
      <input id="d-tz" list="tz-list" value="${esc(n.timezone || 'Asia/Shanghai')}" style="max-width:220px">
      <input id="d-newport" type="number" placeholder="新 SSH 端口（可空）" value="${n.new_ssh_port || ''}" style="max-width:190px">
      <button class="btn primary" data-action="init" ${n.online ? '' : 'disabled'}>执行初始化</button>
      ${n.new_ssh_port && n.new_ssh_port !== n.ssh_port ? '<button class="btn" data-action="finalize">收尾：关闭旧 SSH 端口</button>' : ''}
    </div>
    <p class="muted small">含：系统更新、常用工具、时区、SSH 端口修改（先放行新端口再切换）、fail2ban、ufw、BBR、小内存自动 swap。</p>
  </div>

  <div class="panel-box"><h3>协议部署（sing-box）</h3>
    <div id="dep-list">
    ${(n.deployments || []).map(d => `<div class="dep">
      <span class="badge proto">${esc(MODULE_CN[d.module] || d.module)}</span>
      <span>端口 <b>${d.port}</b></span>
      <span class="link">${esc(d.link || '')}</span>
      <span>
        <button class="btn small" data-action="copy" data-link="${esc(d.link || '')}">复制</button>
        <button class="btn small" data-action="qr" data-id="${d.id}" data-link="${esc(d.link || '')}">二维码</button>
        <button class="btn small danger" data-action="undeploy" data-id="${d.id}">卸载</button>
      </span>
    </div>`).join('') || '<p class="muted">还没部署协议</p>'}
    </div>
    <div class="row" style="flex-wrap:wrap;margin-top:8px">
      <select id="dep-module" style="max-width:190px">
        <option value="vless-reality">VLESS + Reality</option>
        <option value="ss2022">Shadowsocks 2022</option>
      </select>
      <input id="dep-port" type="number" placeholder="端口（空=随机）" style="max-width:170px">
      <input id="dep-dest" placeholder="Reality 目标（默认 www.microsoft.com:443）" style="max-width:320px">
      <button class="btn primary" data-action="deploy" ${n.online ? '' : 'disabled'}>部署</button>
      <button class="btn" data-action="restart-proto" ${n.online ? '' : 'disabled'}>重启协议服务</button>
    </div>
  </div>

  <div class="panel-box"><h3>网络 · 三网延迟</h3>
    <div class="row"><span class="muted">${n.probes ? '最近探测：' + new Date(n.probes.at).toLocaleString() : '暂无探测数据'}</span>
    <button class="btn small" data-action="probe" ${n.online ? '' : 'disabled'}>立即探测</button></div>
    ${probes.length ? `<table class="tbl"><thead><tr><th>目标</th><th>运营商</th><th>IP</th><th>协议</th><th>延迟</th></tr></thead><tbody>
      ${probes.map(r => `<tr><td>${esc(r.name)}</td><td>${esc(carrierName[r.carrier] || r.carrier)}</td><td>${esc(r.ip)}</td><td>IPv${r.family}</td><td>${msCell(r)}</td></tr>`).join('')}
    </tbody></table>` : ''}
    <h3 style="margin-top:18px">去程线路</h3>
    <div class="row">
      <select id="route-source" style="max-width:280px"><option value="">（在设置中添加数据源）</option></select>
      <button class="btn small" data-action="route-query">查询</button>
    </div>
    <pre id="route-result" class="logs hidden" style="margin-top:10px"></pre>
  </div>

  <div class="panel-box"><h3>任务记录</h3><div id="task-list"><p class="muted">加载中…</p></div></div>`;
  loadTasks(n.id);
  loadRouteSources();
}

async function loadTasks(nodeId) {
  try {
    const data = await api('/api/tasks?node_id=' + nodeId);
    const el = $('#task-list');
    if (!el) return;
    const tasks = data.tasks || [];
    el.innerHTML = tasks.length ? tasks.map(t => `<div class="taskrow" data-action="task" data-id="${t.id}">
      <span>${esc(KIND_CN[t.kind] || t.kind)}</span>
      <span class="st-${t.status}">${esc(t.status)}</span>
      <span class="muted">${new Date(t.created_at).toLocaleString()}</span></div>`).join('')
      : '<p class="muted">暂无任务</p>';
  } catch (e) { /* ignore */ }
}

async function loadRouteSources() {
  try {
    const s = await getSettings();
    const sel = $('#route-source');
    if (!sel) return;
    sel.innerHTML = (s.route_sources || []).map(rs => `<option value="${rs.id}">${esc(rs.name)}</option>`).join('') || '<option value="">（在设置中添加数据源）</option>';
  } catch (e) { /* ignore */ }
}

// ---------- task modal ----------
let openTaskId = null;
async function openTask(id) {
  openTaskId = id;
  $('#modal-task').classList.remove('hidden');
  await pollTask();
  clearInterval(taskTimer);
  taskTimer = setInterval(pollTask, 2000);
}
async function pollTask() {
  if (!openTaskId) return;
  try {
    const t = await api('/api/tasks/' + openTaskId);
    $('#task-title').textContent = (KIND_CN[t.kind] || t.kind) + ' · ' + t.status;
    $('#task-meta').textContent = '开始 ' + new Date(t.created_at).toLocaleString() + (t.error ? ' · 错误：' + t.error : '');
    const logs = $('#task-logs');
    logs.textContent = (t.logs || []).join('\n') || '（暂无日志）';
    logs.scrollTop = logs.scrollHeight;
    if (t.status === 'done' || t.status === 'failed') { clearInterval(taskTimer); refresh(); }
  } catch (e) { clearInterval(taskTimer); }
}

// ---------- modals ----------
function openModal(id) { $(id).classList.remove('hidden'); }
function closeModals() { $$('.modal').forEach(m => m.classList.add('hidden')); clearInterval(taskTimer); openTaskId = null; }
$$('.close-modal').forEach(b => b.addEventListener('click', closeModals));
$$('.modal').forEach(m => m.addEventListener('click', e => { if (e.target === m) closeModals(); }));

// auth type toggle
$('#auth-type').addEventListener('change', e => {
  const key = e.target.value === 'key';
  $('#fld-password').classList.toggle('hidden', key);
  $('#fld-key').classList.toggle('hidden', !key);
  $('#fld-keypass').classList.toggle('hidden', !key);
});
$('#btn-add').addEventListener('click', () => { $('#add-logs').classList.add('hidden'); $('#add-status').textContent = ''; openModal('#modal-add'); });

$('#add-form').addEventListener('submit', async e => {
  e.preventDefault();
  const fd = new FormData(e.target);
  const body = {
    name: fd.get('name'), ip: fd.get('ip'), ssh_port: Number(fd.get('ssh_port')),
    username: fd.get('username') || 'root', auth_type: fd.get('auth_type'),
    password: fd.get('password') || '', private_key: fd.get('private_key') || '',
    key_passphrase: fd.get('key_passphrase') || '',
    timezone: fd.get('timezone') || '', new_ssh_port: Number(fd.get('new_ssh_port')) || 0,
    run_init: fd.get('run_init') === 'on',
  };
  $('#add-submit').disabled = true;
  $('#add-status').textContent = '正在 SSH 接入并安装 agent…（可能需要几十秒）';
  try {
    const res = await fetch('/api/nodes', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const data = await res.json();
    const logs = $('#add-logs');
    logs.classList.remove('hidden');
    logs.textContent = (data.logs || []).join('\n');
    if (!res.ok) { $('#add-status').textContent = '接入失败：' + (data.error || res.status); return; }
    $('#add-status').textContent = '接入成功';
    toast('节点已添加');
    setTimeout(() => { closeModals(); e.target.reset(); refresh(); }, 900);
  } catch (err) {
    $('#add-status').textContent = '接入失败：' + err.message;
  } finally { $('#add-submit').disabled = false; }
});

// ---------- settings ----------
async function getSettings() {
  if (!settings) settings = await api('/api/settings');
  return settings;
}
$('#btn-settings').addEventListener('click', async () => {
  settings = await api('/api/settings');
  $('#set-public-addr').value = settings.public_addr || '';
  $('#set-probe-interval').value = settings.probe_interval_min || 5;
  renderTargetRows(settings.probe_targets || []);
  renderRouteRows(settings.route_sources || []);
  renderTokens();
  openModal('#modal-settings');
});
$$('.tab').forEach(t => t.addEventListener('click', () => {
  $$('.tab').forEach(x => x.classList.toggle('active', x === t));
  $$('.tab-page').forEach(p => p.classList.toggle('hidden', p.dataset.page !== t.dataset.tab));
}));
async function saveSettings() {
  await api('/api/settings', { method: 'PUT', body: JSON.stringify(settings) });
  toast('设置已保存');
}
$('#set-save-general').addEventListener('click', async () => {
  settings.public_addr = $('#set-public-addr').value.trim();
  settings.probe_interval_min = Number($('#set-probe-interval').value) || 5;
  await saveSettings();
});
function renderTargetRows(targets) {
  $('#targets-table tbody').innerHTML = targets.map(t => `<tr>
    <td><input value="${esc(t.name)}" data-f="name"></td>
    <td><select data-f="carrier">${['CT','CU','CM','OTHER'].map(c => `<option ${c === t.carrier ? 'selected' : ''}>${c}</option>`).join('')}</select></td>
    <td><input value="${esc(t.ip)}" data-f="ip"></td>
    <td><select data-f="family"><option value="4" ${t.family === 4 ? 'selected' : ''}>IPv4</option><option value="6" ${t.family === 6 ? 'selected' : ''}>IPv6</option></select></td>
    <td><button class="btn small danger" onclick="this.closest('tr').remove()">删</button></td></tr>`).join('');
}
$('#target-add').addEventListener('click', () => {
  settings.probe_targets = collectTargets();
  settings.probe_targets.push({ name: '', carrier: 'CT', ip: '', family: 4 });
  renderTargetRows(settings.probe_targets);
});
function collectTargets() {
  return $$('#targets-table tbody tr').map(tr => ({
    name: $('[data-f=name]', tr).value.trim(), carrier: $('[data-f=carrier]', tr).value,
    ip: $('[data-f=ip]', tr).value.trim(), family: Number($('[data-f=family]', tr).value),
  })).filter(t => t.ip);
}
$('#set-save-targets').addEventListener('click', async () => { settings.probe_targets = collectTargets(); await saveSettings(); });
function renderRouteRows(routes) {
  $('#routes-table tbody').innerHTML = routes.map(r => `<tr data-id="${r.id || ''}">
    <td><input value="${esc(r.name)}" data-f="name"></td>
    <td><input value="${esc(r.url_template)}" data-f="url" placeholder="https://example.com/route?ip={ip}"></td>
    <td><button class="btn small danger" onclick="this.closest('tr').remove()">删</button></td></tr>`).join('');
}
$('#route-add').addEventListener('click', () => {
  settings.route_sources = collectRoutes();
  settings.route_sources.push({ id: '', name: '', url_template: '' });
  renderRouteRows(settings.route_sources);
});
function collectRoutes() {
  return $$('#routes-table tbody tr').map(tr => ({
    id: tr.dataset.id || '', name: $('[data-f=name]', tr).value.trim(), url_template: $('[data-f=url]', tr).value.trim(),
  })).filter(r => r.url_template);
}
$('#set-save-routes').addEventListener('click', async () => { settings.route_sources = collectRoutes(); await saveSettings(); });

// tokens
async function renderTokens() {
  const data = await api('/api/tokens');
  $('#token-list').innerHTML = (data.tokens || []).map(t => `<div class="taskrow"><span>${esc(t.name)} · ${esc(t.prefix)}…</span>
    <span class="muted">${new Date(t.created_at).toLocaleDateString()}</span>
    <button class="btn small danger" data-action="token-del" data-id="${t.id}">删除</button></div>`).join('') || '<p class="muted">暂无 Token</p>';
}
$('#token-create').addEventListener('click', async () => {
  const data = await api('/api/tokens', { method: 'POST', body: JSON.stringify({ name: $('#token-name').value || 'token' }) });
  const el = $('#token-new');
  el.classList.remove('hidden');
  el.textContent = '新 Token（只显示一次，请保存）：\n' + data.token;
  renderTokens();
});
$('#pw-save').addEventListener('click', async () => {
  try {
    await api('/api/password', { method: 'POST', body: JSON.stringify({ old: $('#pw-old').value, new: $('#pw-new').value }) });
    toast('密码已修改');
  } catch (e) { toast('修改失败：' + e.message); }
});

// ---------- global click delegation ----------
document.addEventListener('click', async e => {
  const t = e.target.closest('[data-action]');
  if (!t) return;
  const action = t.dataset.action;
  const n = curNode();
  try {
    if (action === 'open') { currentNodeId = t.dataset.id; renderDetail(); }
    else if (action === 'back') { currentNodeId = null; renderGrid(); }
    else if (action === 'delete-node') {
      if (!confirm('确定删除节点 ' + n.name + '？（不会卸载远端 agent）')) return;
      await api('/api/nodes/' + n.id, { method: 'DELETE' });
      currentNodeId = null; toast('已删除'); refresh();
    }
    else if (action === 'init') {
      const res = await api(`/api/nodes/${n.id}/init`, { method: 'POST', body: JSON.stringify({ timezone: $('#d-tz').value, new_ssh_port: Number($('#d-newport').value) || 0 }) });
      openTask(res.task.id);
    }
    else if (action === 'finalize') {
      const res = await api(`/api/nodes/${n.id}/finalize-ssh`, { method: 'POST', body: '{}' });
      openTask(res.task.id);
    }
    else if (action === 'probe') {
      const res = await api(`/api/nodes/${n.id}/probe`, { method: 'POST', body: '{}' });
      toast('探测任务已下发'); openTask(res.task.id);
    }
    else if (action === 'deploy') {
      const body = { module: $('#dep-module').value, port: Number($('#dep-port').value) || 0, dest: $('#dep-dest').value.trim(), server_name: '' };
      const res = await api(`/api/nodes/${n.id}/deploy`, { method: 'POST', body: JSON.stringify(body) });
      openTask(res.task.id);
    }
    else if (action === 'undeploy') {
      if (!confirm('确定卸载该协议部署？')) return;
      const res = await api(`/api/nodes/${n.id}/deployments/${t.dataset.id}`, { method: 'DELETE' });
      openTask(res.task.id);
    }
    else if (action === 'restart-proto') {
      const res = await api(`/api/nodes/${n.id}/deployments/x/restart`, { method: 'POST', body: '{}' });
      openTask(res.task.id);
    }
    else if (action === 'copy') {
      await navigator.clipboard.writeText(t.dataset.link);
      toast('链接已复制');
    }
    else if (action === 'qr') {
      $('#qr-img').src = `/api/nodes/${n.id}/deployments/${t.dataset.id}/qrcode`;
      $('#qr-link').textContent = t.dataset.link;
      $('#qr-copy').onclick = async () => { await navigator.clipboard.writeText(t.dataset.link); toast('链接已复制'); };
      openModal('#modal-qr');
    }
    else if (action === 'route-query') {
      const sid = $('#route-source').value;
      if (!sid) { toast('先在设置里添加线路数据源'); return; }
      const res = await api(`/api/nodes/${n.id}/route`, { method: 'POST', body: JSON.stringify({ source_id: sid }) });
      const el = $('#route-result');
      el.classList.remove('hidden');
      el.textContent = res.body || ('HTTP ' + res.status + '（无内容）');
    }
    else if (action === 'task') { openTask(t.dataset.id); }
    else if (action === 'token-del') {
      await api('/api/tokens/' + t.dataset.id, { method: 'DELETE' });
      renderTokens();
    }
  } catch (err) { toast('操作失败：' + err.message); }
});

// ---------- boot ----------
setInterval(() => { if (!$('#app-view').classList.contains('hidden') && !$$('.modal:not(.hidden)').length) refresh(); }, 10000);
(async () => {
  try { await api('/api/me'); showApp(); refresh(); }
  catch (e) { showLogin(); }
})();

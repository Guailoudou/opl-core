'use strict';

const $ = id => document.getElementById(id);
const pending = new Map();
const uiLogs = [];
let coreLogText = '';
let socket;
let sequence = 1;
let state = null;
let config = null;
let apps = [];
let presets = [];
let servers = [];
let management = null;
let p2pBusy = '';
let roomBusy = '';
let joinBusy = false;
let draggedIndex = -1;

const errorText = {
  INVALID_INVITE: '邀请码无效或已损坏。', UNSUPPORTED_PROTOCOL: '邀请码协议版本不受支持，请更新双方程序。',
  AUTH_FAILED: '邀请码已失效或认证失败。', JOIN_DISABLED: '房主已暂停新成员加入。', ROOM_FULL: '房间地址已用完。',
  MEMBER_DISABLED: '该设备已被房主移除。', PAIR_PORT_IN_USE: '配对端口已被占用。',
  OPENP2P_NOT_READY: 'OpenP2P 网络尚未就绪，Core 会继续重试。', WIREGUARD_FAILED: '虚拟网卡启动失败，请确认管理员权限和驱动完整。',
  WIREGUARD_UPDATE_FAILED: '虚拟网络成员更新失败。', SECRET_STORE_FAILED: '密钥安全存储失败，请检查当前用户权限。',
  CONFIG_WRITE_FAILED: '配置保存失败，Core 已保留上一份有效配置。', INVALID_STATE: '当前状态不允许此操作。',
  MEMBER_NOT_FOUND: '房间中找不到该成员。', INVALID_REQUEST: '请求参数无效。', INTERNAL_ERROR: 'Core 内部错误，请查看日志。'
};

function connect() {
  const target = location.protocol === 'file:' ? '127.0.0.1:26780' : location.host;
  socket = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${target}/ws`);
  socket.onopen = async () => {
    setCoreConnection(true);
    log('Web 控制台已连接 Core');
    try {
      const [nextState, nextConfig, nextManagement] = await Promise.all([call('core.getState'), call('config.get'), call('management.get')]);
      state = nextState;
      config = nextConfig;
      management = nextManagement;
      apps = nextState.tunnels || nextConfig.Apps || [];
      render();
      if (management.address === '127.0.0.1' && management.interfaces.length > 1 && !localStorage.getItem('opl-management-interface-prompted')) {
        localStorage.setItem('opl-management-interface-prompted', '1');
        document.querySelector('[data-page="settings"]').click();
        toast('请选择用于访问 Web 控制台的局域网网卡');
      }
      await loadInvite();
    } catch (error) { showError(error); }
  };
  socket.onclose = () => {
    setCoreConnection(false);
    for (const item of pending.values()) item.reject(new Error('Core 连接已断开'));
    pending.clear();
    setTimeout(connect, 2000);
  };
  socket.onmessage = event => {
    let message;
    try { message = JSON.parse(event.data); } catch { return; }
    if (message.event === 'hello') $('coreVersion').textContent = `v${message.coreVersion}`;
    if (message.event === 'core.changed') {
      state = message.state;
      apps = state.tunnels || apps;
      render();
    }
    if (message.id && pending.has(message.id)) {
      const item = pending.get(message.id);
      pending.delete(message.id);
      clearTimeout(item.timer);
      if (message.error) {
        const error = new Error(errorText[message.error.code] || message.error.message);
        error.code = message.error.code;
        error.detail = message.error.message;
        item.reject(error);
      } else item.resolve(message.result);
    }
  };
}

function call(method, params, timeout = 15000) {
  return new Promise((resolve, reject) => {
    if (!socket || socket.readyState !== WebSocket.OPEN) return reject(new Error('Core 未连接'));
    const id = sequence++;
    const timer = timeout ? setTimeout(() => { pending.delete(id); reject(new Error('Core 请求超时')); }, timeout) : 0;
    pending.set(id, {resolve, reject, timer});
    socket.send(JSON.stringify({id, method, ...(params === undefined ? {} : {params})}));
  });
}

function setCoreConnection(connected) {
  $('coreStatus').textContent = connected ? 'Core 已连接' : 'Core 未连接';
  $('coreDot').className = `status-dot ${connected ? 'success' : 'danger'}`;
  $('mobileCoreDot').className = $('coreDot').className;
}

function render() {
  if (!state) return;
  renderService();
  renderTunnels();
  renderNetwork();
  renderManagement();
}

function renderManagement() {
  if (!management) return;
  $('managementInterface').replaceChildren(...management.interfaces.map(item => {
    const option = document.createElement('option');
    option.value = item.address;
    option.textContent = `${item.name} · ${item.address}`;
    return option;
  }));
  $('managementInterface').value = management.address;
  $('managementUrl').value = management.url;
  if (document.activeElement !== $('allowedClientIps')) $('allowedClientIps').value = (management.allowedIPs || []).join('\n');
}

function renderService() {
  const labels = {online: '服务已连接', starting: '正在连接服务器', faulted: '连接异常，Core 正在重试', stopped: '服务未启动'};
  const colors = {online: 'success', starting: 'warning', faulted: 'danger', stopped: 'neutral'};
  $('coreStatus').textContent = labels[state.openP2PState] || '服务未启动';
  $('coreDot').className = `status-dot ${colors[state.openP2PState] || 'neutral'}`;
  $('mobileCoreDot').className = $('coreDot').className;
}

function renderTunnels() {
  const activeNetwork = state.room.running || ['connecting', 'retrying', 'connected'].includes(state.joinState);
  const locked = state.openP2PRunning || activeNetwork || !!p2pBusy;
  $('localUid').value = config?.Network?.Node || '';
  $('shareBandwidth').value = config?.Network?.ShareBandwidth ?? 10;
  if (config && servers.length) $('serverSelect').value = String(Math.max(0, servers.findIndex(item => item.ServerHost === config.Network.ServerHost)));
  $('shareBandwidth').disabled = locked;
  $('newTunnel').disabled = locked;
  $('quickAdd').disabled = locked;
  $('disableAll').disabled = locked || !apps.some(app => app.Enabled === 1);
  $('toggleP2P').disabled = activeNetwork || !!p2pBusy;
  $('toggleP2P').textContent = p2pBusy === 'stopping' ? '关闭中…' : p2pBusy === 'starting' ? '启动中…' : state.openP2PRunning ? '关闭' : '启动';
  $('tunnelEmpty').hidden = apps.length !== 0;
  const statuses = new Map((state.tunnelStates || []).map(item => [`${item.protocol}:${item.srcPort}`, item]));
  $('tunnelList').replaceChildren(...apps.map((app, index) => tunnelCard(app, index, statuses.get(`${app.Protocol}:${app.SrcPort}`), locked)));
}

function tunnelCard(app, index, tunnelState, locked) {
  const card = document.createElement('article');
  card.className = 'card tunnel-card';
  card.draggable = !locked;
  const enabled = app.Enabled === 1;
  const connected = enabled && state.openP2PRunning && tunnelState?.connected;
  const waiting = enabled && state.openP2PRunning && !connected;
  const statusLabel = !enabled ? '已禁用' : !state.openP2PRunning ? '未启动' : connected ? '已连接' : tunnelState?.error ? tunnelError(tunnelState.error) : '连接中';
  const statusColor = connected ? 'success' : tunnelState?.error ? 'danger' : waiting ? 'warning' : 'neutral';
  card.innerHTML = `<div class="tunnel-main"><strong>${escapeHTML(app.AppName)} 隧道</strong><small>UID ${escapeHTML(app.PeerNode)}</small></div><div class="tunnel-protocol"><strong>${escapeHTML(app.Protocol.toUpperCase())}</strong><small>${app.DstPort} → ${app.SrcPort}</small></div><button class="local-address" title="复制本地连接地址">127.0.0.1:${app.SrcPort}</button><div class="tunnel-state"><span class="status-dot ${statusColor}"></span><strong>${escapeHTML(statusLabel)}</strong><small>${tunnelState?.error ? escapeHTML(tunnelState.error) : ''}</small></div><div class="tunnel-actions"><label title="启用隧道"><input class="enable-toggle" type="checkbox" ${enabled ? 'checked' : ''} ${locked ? 'disabled' : ''}><span>启用</span></label><button class="edit-tunnel" ${locked ? 'disabled' : ''}>编辑</button><button class="delete-tunnel" ${locked ? 'disabled' : ''}>删除</button><button class="move-up" aria-label="上移" ${locked || index === 0 ? 'disabled' : ''}>↑</button><button class="move-down" aria-label="下移" ${locked || index === apps.length - 1 ? 'disabled' : ''}>↓</button></div>`;
  card.querySelector('.local-address').onclick = () => copyText(`127.0.0.1:${app.SrcPort}`, '本地连接地址已复制');
  card.querySelector('.enable-toggle').onchange = event => updateTunnel(index, {Enabled: event.target.checked ? 1 : 0});
  card.querySelector('.edit-tunnel').onclick = () => openTunnelDialog(index);
  card.querySelector('.delete-tunnel').onclick = () => { if (confirm(`确定删除“${app.AppName}”隧道？`)) saveApps(apps.filter((_, i) => i !== index)); };
  card.querySelector('.move-up').onclick = () => moveTunnel(index, index - 1);
  card.querySelector('.move-down').onclick = () => moveTunnel(index, index + 1);
  card.ondragstart = () => { draggedIndex = index; card.classList.add('dragging'); };
  card.ondragend = () => { draggedIndex = -1; card.classList.remove('dragging'); document.querySelectorAll('.drop-target').forEach(node => node.classList.remove('drop-target')); };
  card.ondragover = event => { if (draggedIndex >= 0 && draggedIndex !== index) { event.preventDefault(); card.classList.add('drop-target'); } };
  card.ondragleave = () => card.classList.remove('drop-target');
  card.ondrop = event => { event.preventDefault(); card.classList.remove('drop-target'); if (draggedIndex >= 0 && draggedIndex !== index) moveTunnel(draggedIndex, index); };
  return card;
}

function renderNetwork() {
  const room = state.room || {};
  const joinActive = ['connecting', 'retrying', 'connected'].includes(state.joinState);
  $('hostToggle').disabled = state.openP2PRunning || joinActive || !!roomBusy;
  $('hostToggle').textContent = roomBusy === 'stopping' ? '关闭中…' : roomBusy === 'starting' ? '启动中…' : room.running ? '关闭网络' : '创建 / 启动网络';
  $('joinPermission').disabled = !room.running;
  $('joinPermission').textContent = room.joinEnabled ? '暂停加入' : '恢复加入';
  $('rotateInvite').disabled = !room.running;
  $('joinInvite').disabled = room.running || joinActive;
  $('deviceName').disabled = room.running || joinActive;
  $('joinToggle').disabled = room.running || state.openP2PRunning || (joinBusy && !joinActive);
  $('joinToggle').textContent = joinBusy ? '处理中…' : joinActive ? '离开网络' : '加入网络';
  $('memberCount').textContent = (room.members || []).length;
  $('memberEmpty').hidden = (room.members || []).length !== 0;
  $('memberList').replaceChildren(...(room.members || []).map(memberRow));

  let label = '未连接', color = 'neutral', error = '';
  if (room.running) { label = '房间运行中 · 局域网发现中继已启用'; color = 'success'; }
  else if (state.joinState === 'connected') { label = '已连接房主 · 局域网发现中继已启用'; color = 'success'; }
  else if (state.joinState === 'connecting') { label = '正在连接房主'; color = 'warning'; }
  else if (state.joinState === 'retrying') { label = '连接房主失败，Core 正在继续重试'; color = 'danger'; error = state.joinError || ''; }
  else if (state.joinState === 'failed') { label = '连接房主失败'; color = 'danger'; error = state.joinError || ''; }
  $('networkDot').className = `status-dot large ${color}`;
  $('networkStatus').textContent = label;
  $('networkError').hidden = !error;
  $('networkError').textContent = error ? `失败原因：${error}` : '';
  $('virtualIp').value = room.running ? room.hostIP || '' : state.virtualIP || '';
  $('relayAccepted').textContent = state.discovery?.accepted || 0;
  $('relayForwarded').textContent = state.discovery?.forwarded || 0;
  $('relayDropped').textContent = state.discovery?.dropped || 0;
  const devices = room.running ? room.members || [] : state.devices || [];
  $('totalRx').textContent = bytes(devices.reduce((sum, item) => sum + (item.rxBytes || 0), 0));
  $('totalTx').textContent = bytes(devices.reduce((sum, item) => sum + (item.txBytes || 0), 0));
  $('deviceTable').replaceChildren(...devices.map(deviceRow));
}

function memberRow(member) {
  const row = document.createElement('div');
  row.className = 'member-row';
  row.innerHTML = `<span><strong>${escapeHTML(member.name || '未命名')}</strong><small>${escapeHTML(shortKey(member.publicKey))}</small></span><span>${escapeHTML(member.ip || '-')}</span><span>${escapeHTML(member.state || '-')}</span><button>移除</button>`;
  row.querySelector('button').onclick = () => { if (confirm(`确定移除“${member.name || member.ip}”？该设备将不能继续使用当前成员密钥。`)) run(row.querySelector('button'), () => call('room.removeMember', {publicKey: member.publicKey})); };
  return row;
}

function deviceRow(item) {
  const hostMember = Object.hasOwn(item, 'publicKey');
  const row = document.createElement('tr');
  const values = [item.name || (hostMember ? '未命名' : '设备'), item.uid || shortKey(item.publicKey), item.ip || item.virtualIP || '-', item.state || (state.joinState === 'connected' ? 'online' : '-'), bytes(item.rxBytes), bytes(item.txBytes)];
  for (const value of values) { const cell = document.createElement('td'); cell.textContent = value || '-'; row.append(cell); }
  const action = document.createElement('td');
  if (hostMember) { const button = document.createElement('button'); button.textContent = '移除'; button.onclick = () => { if (confirm('确定移除该成员？')) run(button, () => call('room.removeMember', {publicKey: item.publicKey})); }; action.append(button); }
  row.append(action);
  return row;
}

async function saveApps(nextApps) {
  try {
    apps = await call('tunnel.replace', {apps: nextApps});
    if (config) config.Apps = apps;
    toast('隧道配置已保存');
    log('隧道配置已保存');
    renderTunnels();
  } catch (error) { showError(error); renderTunnels(); }
}

function updateTunnel(index, patch) {
  const next = apps.map((app, i) => i === index ? {...app, ...patch} : {...app});
  if (patch.Enabled === 1) next.forEach((app, i) => { if (i !== index && app.Enabled === 1 && app.Protocol === next[index].Protocol && app.SrcPort === next[index].SrcPort) app.Enabled = 0; });
  return saveApps(next);
}

function moveTunnel(from, to) {
  const next = [...apps];
  const [item] = next.splice(from, 1);
  next.splice(to, 0, item);
  saveApps(next);
}

function openTunnelDialog(index = -1) {
  const app = index >= 0 ? apps[index] : null;
  $('tunnelDialogTitle').textContent = app ? '编辑隧道' : '添加隧道';
  $('saveTunnel').textContent = app ? '保存更改' : '添加隧道';
  $('tunnelIndex').value = index;
  $('tunnelName').value = app?.AppName || '自定义';
  $('tunnelUid').value = app?.PeerNode || '';
  $('remotePort').value = app?.DstPort || '';
  $('localPort').value = app?.SrcPort || '';
  $('tunnelProtocol').value = app?.Protocol || 'tcp';
  $('presetSelect').value = '';
  $('presetNote').textContent = '';
  if (app) $('localPort').dataset.edited = '1'; else delete $('localPort').dataset.edited;
  $('tunnelDialog').showModal();
  $('tunnelUid').focus();
}

$('tunnelForm').onsubmit = event => {
  event.preventDefault();
  const index = Number($('tunnelIndex').value);
  const uid = $('tunnelUid').value.trim().replaceAll(' ', '');
  const name = $('tunnelName').value.trim().replaceAll(' ', '') || '自定义';
  if (!uid || uid === config?.Network?.Node) return showError(new Error(uid ? '不能创建连接到本机 UID 的隧道。' : '请输入房主 UID。'));
  const selectedPreset = $('presetSelect').value === '' ? null : presets[Number($('presetSelect').value)];
  let next = [...apps];
  if (selectedPreset && index < 0) {
    for (const item of selectedPreset.tunnel || []) next = addWithConflict(next, makeApp(selectedPreset.name, uid, item.type, item.Sport, item.Cport || item.CPort || item.Sport));
    toast(selectedPreset.note || `已添加${selectedPreset.name}预设`);
  } else {
    const remote = Number($('remotePort').value), local = Number($('localPort').value), protocol = $('tunnelProtocol').value;
    if (!validPort(remote) || !validPort(local)) return showError(new Error('端口必须在 1–65535 之间。'));
    const app = index >= 0 ? {...apps[index], AppName: name, PeerNode: uid, Protocol: protocol, DstPort: remote, SrcPort: local} : makeApp(name, uid, protocol, remote, local);
    if (index >= 0) next[index] = app; else next = addWithConflict(next, app);
  }
  $('tunnelDialog').close();
  saveApps(next);
};

function makeApp(name, uid, protocol, remote, local, enabled = 1) {
  return {AppName: name, Protocol: protocol.toLowerCase(), Whitelist: '', SrcPort: Number(local), PeerNode: uid, DstPort: Number(remote), DstHost: 'localhost', PeerUser: '', RelayNode: '', Enabled: enabled};
}

function addWithConflict(list, app) {
  return [...list.map(item => item.Enabled === 1 && item.Protocol === app.Protocol && item.SrcPort === app.SrcPort ? {...item, Enabled: 0} : item), app];
}

function parseCodes(input) {
  const parts = input.replace(/[\r\n ]/g, '').replaceAll('：', ':').replaceAll('；', ';').split(';').filter(Boolean);
  if (!parts.length) throw new Error('请输入有效的联机码。');
  return parts.map(part => {
    const fields = part.split(':');
    if (fields.length === 2) return {protocol: 'tcp', uid: fields[0], remote: port(fields[1]), local: port(fields[1])};
    if ((fields.length === 3 || fields.length === 4) && ['1', '2'].includes(fields[0])) return {protocol: fields[0] === '1' ? 'tcp' : 'udp', uid: fields[1], remote: port(fields[2]), local: port(fields[3] || fields[2])};
    throw new Error(`无法识别联机码片段：${part}`);
  });
}

function port(value) { const number = Number(value); if (!validPort(number)) throw new Error('端口必须在 1–65535 之间。'); return number; }
function validPort(value) { return Number.isInteger(value) && value >= 1 && value <= 65535; }

async function loadInvite() {
  if (!state?.room?.hostUid) return;
  try { $('roomInvite').value = (await call('room.getInvite')).invite || ''; } catch { /* no persisted room */ }
}

async function run(button, work, busyText = '处理中…') {
  const old = button.textContent;
  button.disabled = true;
  button.textContent = busyText;
  clearError();
  try { return await work(); } catch (error) { showError(error); } finally { button.disabled = false; button.textContent = old; }
}

function showError(error) {
  const detail = error.detail && error.detail !== error.message ? `\n详细信息：${error.detail}` : '';
  $('alert').textContent = `${error.message}${detail}`;
  $('alert').hidden = false;
  log(`错误：${error.message}${detail}`);
}
function clearError() { $('alert').hidden = true; $('alert').textContent = ''; }
function toast(message) { $('toast').textContent = message; $('toast').hidden = false; clearTimeout(toast.timer); toast.timer = setTimeout(() => $('toast').hidden = true, 2600); }
function log(message) { uiLogs.unshift(`[${new Date().toLocaleString()}] ${message}`); uiLogs.length = Math.min(uiLogs.length, 500); renderLogs(); }
async function refreshLogs() {
  try { coreLogText = (await call('log.read')).text || ''; renderLogs(); } catch (error) { showError(error); }
}
function renderLogs() { $('logOutput').textContent = `${coreLogText}${coreLogText ? '\n' : ''}===== Web 控制台 =====\n${uiLogs.join('\n')}`; }
function bytes(value = 0) { const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']; let i = 0; while (value >= 1024 && i < units.length - 1) { value /= 1024; i++; } return `${value.toFixed(i ? 1 : 0)} ${units[i]}`; }
function shortKey(value = '') { return value.slice(0, 10) + (value.length > 10 ? '…' : ''); }
function escapeHTML(value = '') { return String(value).replace(/[&<>'"]/g, char => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;'}[char])); }
function tunnelError(value = '') { return /not found|offline|no route|no known endpoint/i.test(value) ? '对端不在线' : '连接异常'; }

async function copyText(value, message = '已复制') {
  if (!value) return;
  try { await navigator.clipboard.writeText(value); toast(message); }
  catch { const input = document.createElement('textarea'); input.value = value; document.body.append(input); input.select(); document.execCommand('copy'); input.remove(); toast(message); }
}

function showTextDialog(title, help, value, confirm) {
  $('textDialogTitle').textContent = title;
  $('textDialogHelp').textContent = help;
  $('textDialogInput').value = value;
  $('textDialogConfirm').onclick = () => confirm($('textDialogInput').value);
  $('textDialog').showModal();
  $('textDialogInput').focus();
}

document.querySelectorAll('.nav-item').forEach(button => button.onclick = () => {
  document.querySelectorAll('.nav-item').forEach(item => item.classList.toggle('active', item === button));
  document.querySelectorAll('.page').forEach(page => page.classList.toggle('active', page.id === `page-${button.dataset.page}`));
  location.hash = button.dataset.page;
  document.querySelector('.sidebar').classList.remove('open');
  if (button.dataset.page === 'logs') refreshLogs();
});
$('menuButton').onclick = () => document.querySelector('.sidebar').classList.toggle('open');
$('newTunnel').onclick = () => openTunnelDialog();
$('remotePort').oninput = () => { if (!$('localPort').dataset.edited) $('localPort').value = $('remotePort').value; };
$('localPort').oninput = () => $('localPort').dataset.edited = '1';
document.querySelectorAll('.close-dialog').forEach(button => button.onclick = () => button.closest('dialog').close());
$('copyUid').onclick = () => copyText($('localUid').value, 'UID 已复制');
$('shareBandwidth').onchange = async () => {
  const value = Number($('shareBandwidth').value);
  if (!Number.isInteger(value) || value < 0) return showError(new Error('共享带宽必须是非负整数。'));
  config.Network.ShareBandwidth = value;
  try { config = await call('config.replace', {config}); toast('共享带宽已保存'); } catch (error) { showError(error); }
};
$('toggleP2P').onclick = async () => {
  if (p2pBusy) return;
  p2pBusy = state.openP2PRunning ? 'stopping' : 'starting';
  renderTunnels();
  try { await call(state.openP2PRunning ? 'openp2p.stop' : 'openp2p.start'); log(p2pBusy === 'stopping' ? 'OpenP2P 已关闭' : 'OpenP2P 正在启动'); }
  catch (error) { showError(error); }
  finally { p2pBusy = ''; renderTunnels(); }
};
$('disableAll').onclick = () => saveApps(apps.map(app => ({...app, Enabled: 0})));
$('exportCode').onclick = () => {
  const code = apps.filter(app => app.Enabled === 1).map(app => `${app.Protocol === 'tcp' ? 1 : 2}:${app.PeerNode}:${app.DstPort}:${app.SrcPort}`).join(';');
  if (!code) return showError(new Error('没有启用的隧道，无法导出连接码。'));
  copyText(code, '启用隧道的连接码已复制');
};
$('hostCode').onclick = () => showTextDialog('生成联机码', '输入本机游戏或服务端口，生成 UID:端口 的 TCP 快捷联机码。', '', value => { try { copyText(`${config.Network.Node}:${port(value.trim())}`, '快捷联机码已复制'); $('textDialog').close(); } catch (error) { showError(error); } });
$('quickAdd').onclick = async () => {
  let clipboard = '';
  try { clipboard = await navigator.clipboard.readText(); } catch {}
  showTextDialog('快速添加', '支持 UID:端口，或 1/2:UID:远程端口[:本地端口]；多个使用分号分隔。', clipboard, value => {
  try {
    const connections = parseCodes(value);
    let next = apps.map(app => ({...app, Enabled: 0}));
    const used = new Set();
    for (const item of connections) {
      const match = next.findIndex((app, index) => !used.has(index) && app.PeerNode === item.uid && app.Protocol === item.protocol);
      if (match >= 0) { next[match] = {...next[match], DstPort: item.remote, SrcPort: item.local, Enabled: 1}; used.add(match); }
      else next = addWithConflict(next, makeApp('自定义', item.uid, item.protocol, item.remote, item.local));
    }
    $('textDialog').close(); saveApps(next);
  } catch (error) { showError(error); }
  });
};

$('hostToggle').onclick = async () => {
  if (roomBusy) return;
  roomBusy = state.room.running ? 'stopping' : 'starting'; renderNetwork();
  try {
    if (state.room.running) await call('room.stop');
    else { const result = await call('room.create', undefined, 30000); $('roomInvite').value = result.invite; await copyText(result.invite, '网络已创建，邀请码已复制'); }
  } catch (error) { showError(error); }
  finally { roomBusy = ''; renderNetwork(); }
};
$('copyInvite').onclick = () => copyText($('roomInvite').value, '邀请码已复制');
$('joinPermission').onclick = () => run($('joinPermission'), () => call('room.setJoinEnabled', {enabled: !state.room.joinEnabled}));
$('rotateInvite').onclick = async () => { if (!confirm('废除后，当前邀请码不能添加新成员，现有成员不受影响。继续吗？')) return; await run($('rotateInvite'), async () => { const result = await call('room.rotateKey'); $('roomInvite').value = result.invite; await copyText(result.invite, '新邀请码已复制'); }); };
$('joinToggle').onclick = () => {
  const active = ['connecting', 'retrying', 'connected'].includes(state.joinState);
  if (active) { joinBusy = true; renderNetwork(); call('room.leave').catch(showError).finally(() => { joinBusy = false; renderNetwork(); }); return; }
  const invite = $('joinInvite').value.trim();
  if (!invite) return showError(new Error('请输入 OPL2 邀请码。'));
  joinBusy = true; renderNetwork();
  call('room.join', {invite, name: $('deviceName').value.trim()}, 0).catch(error => { if (!['INVALID_STATE'].includes(error.code)) showError(error); }).finally(() => { joinBusy = false; renderNetwork(); });
};
$('copyVirtualIp').onclick = () => copyText($('virtualIp').value, '虚拟 IP 已复制');
$('copyLogs').onclick = () => copyText($('logOutput').textContent, '日志已复制');
$('refreshLogs').onclick = refreshLogs;
$('clearLogs').onclick = () => { uiLogs.length = 0; coreLogText = ''; renderLogs(); };
$('refreshNotices').onclick = loadContent;
$('shutdown').onclick = () => { if (confirm('确定关闭后台 Core 和当前全部网络？')) run($('shutdown'), () => call('core.shutdown'), '关闭中…'); };
$('copyManagementUrl').onclick = () => copyText($('managementUrl').value, '管理地址已复制');
$('saveManagementInterface').onclick = async () => {
  const button = $('saveManagementInterface');
  const address = $('managementInterface').value;
  if (!address || address === management?.address) return toast('当前已经使用该网卡');
  button.disabled = true;
  button.textContent = '切换中…';
  try {
    management = await call('management.setAddress', {address});
    toast('网卡已保存，正在切换管理地址');
    setTimeout(() => location.replace(management.url), 500);
  } catch (error) {
    showError(error);
    button.disabled = false;
    button.textContent = '应用网卡';
  }
};
$('saveAllowedClientIps').onclick = async () => {
  const values = $('allowedClientIps').value.split(/[\s,;，；]+/).map(value => value.trim()).filter(Boolean);
  await run($('saveAllowedClientIps'), async () => {
    management = await call('management.setAllowedIPs', {allowedIPs: values});
    renderManagement();
    toast('访问限制已保存');
  }, '保存中…');
};

$('themeSelect').value = localStorage.getItem('opl-theme') || 'system';
$('accentColor').value = localStorage.getItem('opl-accent') || '#2563eb';
applyAppearance();
$('themeSelect').onchange = () => { localStorage.setItem('opl-theme', $('themeSelect').value); applyAppearance(); };
$('accentColor').oninput = () => { localStorage.setItem('opl-accent', $('accentColor').value); applyAppearance(); };
function applyAppearance() { const theme = $('themeSelect').value; document.documentElement.dataset.theme = theme === 'system' ? '' : theme; document.documentElement.style.setProperty('--accent', $('accentColor').value); }

$('presetSelect').onchange = () => { const preset = $('presetSelect').value === '' ? null : presets[Number($('presetSelect').value)]; $('presetNote').textContent = preset?.note || ''; };
$('serverSelect').onchange = async () => {
  const selected = servers[Number($('serverSelect').value)];
  if (!selected || !config) return;
  config.Network.ServerHost = selected.ServerHost;
  config.Network.Token = String(selected.Token);
  config.Network.User = 'gldoffice';
  try { config = await call('config.replace', {config}); toast('连接节点已保存'); } catch (error) { showError(error); }
};

async function loadContent() {
  const local = location.protocol !== 'file:' && location.port === '26780';
  const source = name => local ? `/content/${name}` : `https://file.gldhn.top/file/json/${name}.json`;
  try {
    const data = await (await fetch(source('notice'))).json();
    $('notices').replaceChildren(...(data.notices || []).reverse().map(item => { const card = document.createElement('article'); card.className = 'notice card'; const header = document.createElement('header'), title = document.createElement('h2'), time = document.createElement('time'), body = document.createElement('p'); title.textContent = item.title || ''; time.textContent = item.time || ''; body.textContent = item.content || ''; header.append(title, time); card.append(header, body); return card; }));
  } catch { $('notices').innerHTML = '<div class="card notice">公告获取失败，请检查网络连接。</div>'; }
  try { const data = await (await fetch(source('update'))).json(); $('updateLog').textContent = data.uplog || '暂无更新日志'; } catch { $('updateLog').textContent = '更新日志获取失败'; }
  try { const data = await (await fetch(source('thank'))).json(); $('thanks').textContent = `afdian.com/@guailoudou\n${(data.list || []).map(item => `${item.name}：${item.num}`).join('\n')}`; } catch { $('thanks').textContent = '鸣谢信息获取失败'; }
  try {
    const data = await (await fetch(source('preset'))).json();
    presets = data.presets || []; servers = data.servers || [];
    $('presetSelect').replaceChildren(new Option('不使用预设', ''), ...presets.map((item, index) => new Option(item.name, index)));
    $('serverSelect').replaceChildren(...servers.map((item, index) => new Option(item.ServerName, index)));
    if (config) $('serverSelect').value = String(Math.max(0, servers.findIndex(item => item.ServerHost === config.Network.ServerHost)));
  } catch { $('serverSelect').replaceChildren(new Option('节点列表获取失败', '')); }
}

const initialPage = location.hash.slice(1);
if (initialPage && document.querySelector(`[data-page="${CSS.escape(initialPage)}"]`)) document.querySelector(`[data-page="${CSS.escape(initialPage)}"]`).click();
connect();
loadContent();

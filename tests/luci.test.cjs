// LuCI contract tests use a fake DOM/RPC; browser and router checks are separate.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = 'luci/htdocs/luci-static/resources/view/wifi-guard/';
class Element {
  constructor(tag, attrs = {}, children = []) {
    this.tag = tag; this.attrs = attrs; Object.assign(this, attrs);
    for (const key of ['disabled', 'checked']) this[key] = attrs[key] != null;
    this.children = []; this.replaceChildren(...children);
  }
  appendChild(child) { this.children.push(child); return child; }
  replaceChildren(...children) { this.children = []; children.flat().forEach(c => this.appendChild(c)); }
  focus() {}
  set textContent(s) { this.children = [s]; }
  get textContent() { return this.children.map(c => c instanceof Element ? c.textContent : String(c)).join(''); }
}
function E(tag, attrs, children) { return new Element(tag, attrs, children || []); }
String.prototype.format = function(...values) { let i = 0; return this.replace(/%[ds]/g, () => values[i++]); };
let writable = true;
const calls = [];
const replies = { devices: { devices: [] }, history: { events: [] }, settings: { enabled: false, chat_id: '', token_set: false } };
const rpc = { declare: schema => (...args) => {
  calls.push([schema.method, args]); const reply = replies[schema.method];
  return reply instanceof Error ? Promise.reject(reply) : reply instanceof Promise ? reply : Promise.resolve(reply || { ok: true });
} };
const ui = { addNotification() {}, showModal(title, nodes) { ui.modal = nodes; }, hideModal() {} };
const poll = { add(fn, interval) { assert.equal(interval, 10); poll.callback = fn; } };
const L = { hasViewPermission: () => writable };
function source(name) { return fs.readFileSync(path + name + '.js', 'utf8'); }
function makeView(name, translate = s => s) { return new Function('view', 'rpc', 'ui', 'poll', 'L', 'E', '_', source(name))({ extend: v => v }, rpc, ui, poll, L, E, translate); }
function all(node) { return node instanceof Element ? [node, ...node.children.flatMap(all)] : []; }
function button(tree, text) { return all(tree).find(e => e.tag === 'button' && e.textContent === text); }
function controls(tree) {
  return { inputs: all(tree).filter(e => e.tag === 'input'), enabled: all(tree).find(e => e.type === 'checkbox'),
    chat: all(tree).find(e => e.type === 'text'), token: all(tree).find(e => e.type === 'password'),
    buttons: all(tree).filter(e => e.tag === 'button') };
}
(async () => {
  const menu = JSON.parse(fs.readFileSync('luci/root/usr/share/luci/menu.d/luci-app-wifi-guard.json'));
  for (const route of ['overview', 'devices', 'history', 'settings']) {
    const item = menu[`admin/services/wifi-guard/${route}`];
    assert.equal(item.action.path, `wifi-guard/${route}`);
    assert.deepEqual(item.depends.acl, ['luci-app-wifi-guard']);
    assert(!/innerHTML/.test(source(route)), 'unsafe HTML rendering introduced');
  }
  assert.equal(menu['admin/services/wifi-guard'].action.type, 'firstchild');
  const overview = makeView('overview');
  const overviewTree = overview.render(await overview.load());
  assert.match(overviewTree.textContent, /0 online \/ 0 known/);
  replies.devices = { devices: [{ mac: '02:00:00:00:00:01', online: true, trust: 'trusted' }, { mac: '02:00:00:00:00:02', online: false, trust: 'unknown', desired_deny: true }] };
  overview.update(replies.devices, { events: [{ at: '2026-01-01T00:00:00Z', kind: 'connected', mac: '02:00:00:00:00:01' }] });
  assert.match(overviewTree.textContent, /1 online \/ 2 known/);
  assert.match(overviewTree.textContent, /Offline: 1 · Trusted: 1 · Deny requested: 1/);
  replies.history = new Error('offline'); await poll.callback();
  assert.match(overviewTree.textContent, /History unavailable/);
  assert.match(overviewTree.textContent, /1 online \/ 2 known/);
  replies.history = { events: [] };

  // Hostnames are untrusted display/search metadata, never manual names or trust.
  const named = { mac: '02:00:00:00:00:03', hostname: 'Laptop-DHCP', trust: 'unknown' };
  replies.devices = { devices: [named] };
  const hostnameDevices = makeView('devices'); hostnameDevices.render(await hostnameDevices.load());
  const displayedName = () => all(hostnameDevices.deviceList).find(e => e.tag === 'strong').textContent;
  assert.equal(displayedName(), 'Laptop-DHCP', 'hostname is the display fallback');
  assert.match(hostnameDevices.deviceList.textContent, /Trust not set/);
  assert(button(hostnameDevices.deviceList, 'Trust'), 'hostname must not imply trust');
  assert(!button(hostnameDevices.deviceList, 'Clear trust'));
  assert.equal(named.trust, 'unknown');
  hostnameDevices.filter.value = 'trusted'; hostnameDevices.filter.change();
  assert.match(hostnameDevices.status.textContent, /0 shown/, 'hostname must not match the trusted filter');
  hostnameDevices.filter.value = 'all'; hostnameDevices.filter.change();
  await button(hostnameDevices.deviceList, 'Rename').click();
  assert.equal(ui.modal.flatMap(all).find(e => e.tag === 'input').value, '', 'do not copy hostname into manual name');
  assert.match(ui.modal.map(e => e.textContent).join(''), /Leave empty to use the hostname/);
  named.friendly_name = 'Manual desk'; hostnameDevices.updateDevices(replies.devices);
  assert.equal(displayedName(), 'Manual desk', 'manual name wins over hostname');
  for (const query of ['lApToP-dHcP', 'MANUAL DESK']) {
    hostnameDevices.search.value = query; hostnameDevices.search.input();
    assert.match(hostnameDevices.status.textContent, /1 shown/, 'search must include both names independently');
    assert.equal(displayedName(), 'Manual desk');
  }
  hostnameDevices.search.value = ''; hostnameDevices.search.input();
  await button(hostnameDevices.deviceList, 'Rename').click();
  const manualInput = ui.modal.flatMap(all).find(e => e.tag === 'input');
  assert.equal(manualInput.value, 'Manual desk');
  manualInput.value = '';
  // Simulate the backend's persisted rename response for refresh.
  replies.rename = Promise.resolve({ ok: true }).then(result => { named.friendly_name = ''; return result; });
  const clearName = ui.modal.flatMap(all).find(e => e.tag === 'button' && e.textContent === 'Save name');
  await clearName.click({ currentTarget: clearName });
  assert.deepEqual(calls.filter(([method]) => method === 'rename').at(-1), ['rename', [named.mac, '']]);
  assert.equal(displayedName(), 'Laptop-DHCP', 'clearing manual name reveals hostname');
  for (const hostname of [undefined, '']) {
    named.hostname = hostname; hostnameDevices.updateDevices(replies.devices);
    assert.equal(displayedName(), 'Unnamed device');
  }
  named.hostname = '<img src=x onerror=alert(1)>'; hostnameDevices.updateDevices(replies.devices);
  assert.equal(displayedName(), named.hostname);
  assert(!all(hostnameDevices.deviceList).some(e => e.tag === 'img'), 'hostname must render as text');
  assert(!calls.some(([method]) => ['trust', 'unblock'].includes(method)), 'display/search/rename must not change access policy or trust');
  replies.rename = { ok: true }; calls.length = 0;
  replies.devices = { devices: [{ mac: '02:00:00:00:00:01', online: true, trust: 'trusted' }, { mac: '02:00:00:00:00:02', online: false, trust: 'unknown', desired_deny: true }] };

  const devices = makeView('devices');
  const deviceTree = devices.render(await devices.load());
  const evil = '<img src=x onerror=alert(1)>';
  replies.devices.devices[0].friendly_name = evil;
  devices.updateDevices(replies.devices);
  assert.match(devices.deviceList.textContent, /<img src=x/);
  assert(!all(devices.deviceList).some(e => e.tag === 'img'));
  devices.search.value = '00:02'; devices.search.input();
  assert.match(devices.status.textContent, /1 shown \/ 2 known/);
  assert(!devices.deviceList.textContent.includes(evil));
  devices.search.value = ''; devices.search.input();
  devices.filter.value = 'online'; devices.filter.change();
  assert.match(devices.deviceList.textContent, /<img src=x/);
  assert(!devices.deviceList.textContent.includes('00:02'));
  devices.filter.value = 'all'; devices.filter.change();
  const renameButton = button(devices.deviceList, 'Rename');
  await renameButton.click();
  const nameInput = ui.modal.flatMap(all).find(e => e.tag === 'input');
  nameInput.value = 'Desk';
  const saveName = ui.modal.flatMap(all).find(e => e.tag === 'button' && e.textContent === 'Save name');
  await saveName.click({ currentTarget: saveName });
  assert.deepEqual(calls.find(([method]) => method === 'rename'), ['rename', ['02:00:00:00:00:01', 'Desk']]);
  await button(devices.deviceList, 'Clear trust').click();
  assert.deepEqual(calls.find(([method]) => method === 'trust'), ['trust', ['02:00:00:00:00:01', 'unknown']]);
  const denied = replies.devices.devices[1]; denied.policy_revision = 2; denied.policy_pending = true;
  devices.updateDevices(replies.devices);
  assert.match(devices.deviceList.textContent, /application pending/);
  assert(all(devices.deviceList).find(e => e.tag === 'button' && e.textContent === 'Trust' && e.disabled));
  await button(devices.deviceList, 'Unblock').click();
  const confirm = ui.modal.flatMap(all).find(e => e.tag === 'button' && e.textContent === 'Remove my deny');
  assert(confirm, 'unblock lacks confirmation');
  await confirm.click({ currentTarget: confirm });
  assert.deepEqual(calls.find(([method]) => method === 'unblock'), ['unblock', [denied.mac, 2]]);
  writable = false;
  const readDevices = makeView('devices'); readDevices.render(await readDevices.load());
  assert(!all(readDevices.deviceList).some(e => e.tag === 'button'));
  writable = true;
  replies.devices = new Error('offline'); await devices.refresh();
  assert.match(devices.status.textContent, /unavailable/);
  devices.search.value = ''; devices.search.input();
  devices.filter.value = 'all'; devices.filter.change();
  assert.match(devices.status.textContent, /unavailable/);
  assert.equal(devices.deviceList.textContent, '');
  assert.deepEqual(devices.currentDevices, []);
  replies.devices = { devices: [] }; await devices.refresh();
  assert.match(devices.status.textContent, /0 shown/);

  const events = Array.from({ length: 60 }, (_, i) => ({ at: '2026-01-01T00:00:00Z', kind: `event-${i}`, mac: `02:00:00:00:00:${String(i).padStart(2, '0')}` }));
  replies.history = { events }; const history = makeView('history'); const historyTree = history.render(await history.load());
  assert.deepEqual(calls.at(-1), ['history', [500]]);
  assert.match(historyTree.textContent, /event-0/);
  assert(!historyTree.textContent.includes('event-25'));
  await button(historyTree, 'Next').click();
  assert.match(historyTree.textContent, /event-25/);
  assert.match(historyTree.textContent, /Page 2 of 3/);
  await button(historyTree, 'Next').click();
  assert(button(historyTree, 'Next').disabled);
  replies.history = new Error('offline'); await history.refresh();
  assert.match(historyTree.textContent, /History unavailable/);
  replies.history = { events: [] }; await history.refresh();
  assert.match(historyTree.textContent, /No history yet/);
  history.updateHistory({ events: Array.from({ length: 600 }, (_, i) => ({ kind: `event-${i}` })) });
  assert.equal(history.events.length, 500);
  history.page = 19; history.showPage();
  assert.match(historyTree.textContent, /Page 20 of 20/);
  assert(button(historyTree, 'Next').disabled);
  history.updateHistory({ events: [{ kind: 'connected' }] });
  assert.equal(history.page, 0, 'shrinking history must clamp the current page');
  assert(button(historyTree, 'Previous').disabled);
  for (const route of ['overview', 'history']) {
    const localized = makeView(route, s => ({ 'Device discovered': 'Обнаружено устройство', Connected: 'Подключено', Disconnected: 'Отключено' }[s] || s));
    const data = { events: ['discovered', 'connected', 'disconnected', '<future>'].map(kind => ({ kind })) };
    const rendered = localized.render(route === 'overview' ? [{ devices: [] }, data] : data);
    assert.match(rendered.textContent, /Обнаружено устройство/);
    assert.match(rendered.textContent, /Подключено/);
    assert.match(rendered.textContent, /Отключено/);
    assert.match(rendered.textContent, /<future>/, 'unknown event names remain visible as text');
  }

  const settings = makeView('settings'); const tree = settings.render(await settings.load());
  let c = controls(tree);
  assert(c.token && c.token.value === '' && c.token.autocomplete === 'new-password');
  assert(!c.token.disabled && !c.enabled.checked && !c.enabled.disabled);
  assert(c.buttons.every(b => !b.disabled));
  await button(tree, 'Save Telegram settings').click();
  assert.deepEqual(calls.find(([method]) => method === 'configure'), ['configure', [false, '', '', false]]);
  await button(tree, 'Send test message').click();
  assert(calls.some(([method]) => method === 'test'));
  const validSettings = { enabled: true, chat_id: '-100123', token_set: true };
  for (const stage of ['configure', 'readback']) {
    replies.settings = validSettings;
    const uncertain = makeView('settings'); const uncertainTree = uncertain.render(await uncertain.load());
    const uc = controls(uncertainTree);
    let finishSave;
    replies.configure = new Promise(resolve => { finishSave = resolve; });
    const saving = button(uncertainTree, 'Save Telegram settings').click();
    assert([...uc.inputs, ...uc.buttons].every(e => e.disabled), 'saving must lock the entire form');
    if (stage === 'readback') replies.settings = new Error('lost readback');
    finishSave(stage === 'configure' ? { error: 'lost reply after possible commit' } : { ok: true });
    await saving;
    assert([...uc.inputs, ...uc.buttons].every(e => e.disabled), 'unconfirmed settings must remain locked');
    const before = calls.length;
    for (const b of uc.buttons) await b.click();
    replies.settings = validSettings; await uncertain.refresh();
    assert.equal(calls.length, before, 'uncertain fields must not be retried or silently repopulated');
    assert.match(uncertainTree.textContent, /settings unavailable/i);
    replies.configure = { ok: true };
  }
  for (const failure of [new Error('offline'), { error: 'RPC unavailable' }]) {
    replies.settings = failure;
    const failed = makeView('settings'); const failedTree = failed.render(await failed.load()); c = controls(failedTree);
    assert.match(failedTree.textContent, /settings unavailable/i);
    assert(c.inputs.every(e => e.disabled) && c.buttons.every(e => e.disabled));
    const mutations = calls.filter(([method]) => ['configure', 'test'].includes(method)).length;
    for (const b of c.buttons) await b.click();
    assert.equal(calls.filter(([method]) => ['configure', 'test'].includes(method)).length, mutations);
    replies.settings = validSettings; await failed.refresh();
    assert(c.enabled.checked && !c.enabled.disabled && c.chat.value === validSettings.chat_id);
    c.chat.value = 'unsaved';
    replies.settings = failure; await failed.refresh();
    assert.equal(c.chat.value, 'unsaved');
  }
  replies.settings = new Error('offline');
  const racing = makeView('settings'); const raceTree = racing.render(await racing.load());
  let firstReply, secondReply;
  replies.settings = new Promise(resolve => { firstReply = resolve; }); const first = racing.refresh();
  replies.settings = new Promise(resolve => { secondReply = resolve; }); const second = racing.refresh();
  firstReply({ enabled: false, chat_id: '', token_set: false }); await first;
  controls(raceTree).chat.value = 'keep edit';
  secondReply(validSettings); await second;
  assert.equal(controls(raceTree).chat.value, 'keep edit');
  writable = false; replies.settings = new Error('offline');
  const readSettings = makeView('settings'); const readTree = readSettings.render(await readSettings.load());
  replies.settings = validSettings; await readSettings.refresh();
  assert([...controls(readTree).inputs, ...controls(readTree).buttons].every(e => e.disabled));
  const acl = JSON.parse(fs.readFileSync('luci/root/usr/share/rpcd/acl.d/luci-app-wifi-guard.json'))['luci-app-wifi-guard'];
  assert.deepEqual(acl.read.ubus['wifi-guard'].sort(), ['devices', 'history', 'settings']);
  for (const action of ['rename', 'trust', 'unblock', 'configure', 'test']) {
    assert(!acl.read.ubus['wifi-guard'].includes(action)); assert(acl.write.ubus['wifi-guard'].includes(action));
  }
  assert(!acl.read.uci && !acl.read.file);
  const po = fs.readFileSync('luci/po/ru/wifi-guard.po', 'utf8');
  const lmo = fs.readFileSync('luci/i18n/wifi-guard.ru.lmo');
  for (const [label, translation] of [['Trust not set', 'Доверие не задано'], ['Clear trust', 'Снять отметку доверия']]) {
    assert(po.includes(`msgid ${JSON.stringify(label)}\nmsgstr ${JSON.stringify(translation)}`));
    assert(lmo.includes(Buffer.from(translation)), `missing compiled translation for ${label}`);
  }
  const ids = [...po.matchAll(/^msgid (".*")$/gm)].map(m => JSON.parse(m[1]));
  for (const match of po.matchAll(/^msgid (".+")\nmsgstr (".*")$/gm)) {
    assert(JSON.parse(match[2]).length > 0, `empty Russian translation for ${match[1]}`);
    assert.deepEqual(JSON.parse(match[1]).match(/%[ds]/g), JSON.parse(match[2]).match(/%[ds]/g), 'translation format placeholders differ');
    assert(lmo.includes(Buffer.from(JSON.parse(match[2]))), `compiled Russian catalog is stale for ${match[1]}`);
  }
  for (const route of ['overview', 'devices', 'history', 'settings']) {
    const labels = [...source(route).matchAll(/_\('((?:\\'|[^'])*)'\)/g)].map(m => m[1]);
    for (const label of labels) assert(ids.includes(label), `missing Russian translation for ${label}`);
  }
  for (const label of ['WiFi Guard', 'Overview', 'Devices', 'History', 'Settings']) assert(ids.includes(label));
  assert(fs.statSync('luci/i18n/wifi-guard.ru.lmo').size > 1000);
  const packageMakefile = fs.readFileSync('packaging/openwrt/luci-app-wifi-guard/Makefile', 'utf8');
  assert(packageMakefile.includes('view/wifi-guard/*.js') && packageMakefile.includes('wifi-guard.ru.lmo'));
  console.log('PASS: LuCI routes, statistics, search/filter, bounded history pagination, independent failures, settings recovery/edit preservation, read-only actions, ACL, Russian catalog');
})().catch(e => { console.error(e); process.exitCode = 1; });

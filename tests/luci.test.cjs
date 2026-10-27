// Lightweight LuCI view contract tests; real browser/rpcd checks remain separate.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const source = fs.readFileSync('luci/htdocs/luci-static/resources/view/wifi-guard/overview.js', 'utf8');
class Element {
  constructor(tag, attrs = {}, children = []) { this.tag = tag; this.attrs = attrs; Object.assign(this, attrs);
    // LuCI E() sets HTML boolean attributes: even attribute="false" means true.
    for (const key of ['disabled', 'checked']) this[key] = attrs[key] != null;
    this.children = []; this.replaceChildren(...children); }
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
const rpc = { declare: schema => (...args) => { calls.push([schema.method, args]); const reply = replies[schema.method]; return reply instanceof Error ? Promise.reject(reply) : Promise.resolve(reply || { ok: true }); } };
const ui = { addNotification() {}, showModal(title, nodes) { ui.modal = nodes; }, hideModal() {} };
const poll = { add(fn, interval) { assert.equal(interval, 10); poll.callback = fn; } };
const L = { hasViewPermission: () => writable };
const makeView = () => new Function('view', 'rpc', 'ui', 'poll', 'L', 'E', '_', source)({ extend: v => v }, rpc, ui, poll, L, E, s => s);
function all(node) { return node instanceof Element ? [node, ...node.children.flatMap(all)] : []; }
(async () => {
  const view = makeView();
  const data = await view.load();
  const tree = view.render(data);
  assert.match(tree.textContent, /No known Wi-Fi devices/);
  const password = all(tree).find(e => e.type === 'password');
  assert(password && password.value === '' && password.autocomplete === 'new-password');
  assert(!password.disabled, 'writable token field incorrectly disabled by HTML boolean attribute');
  const enabled = all(tree).find(e => e.tag === 'input' && e.type === 'checkbox');
  assert(!enabled.checked && !enabled.disabled, 'disabled Telegram was rendered checked/locked');
  assert(all(tree).filter(e => e.tag === 'button').every(e => !e.disabled), 'writable action rendered disabled');
  await all(tree).find(e => e.tag === 'button' && e.textContent === 'Save Telegram settings').click();
  assert.deepEqual(calls.find(([method]) => method === 'configure'), ['configure', [false, '', '', false]]);
  await all(tree).find(e => e.tag === 'button' && e.textContent === 'Send test message').click();
  assert(calls.some(([method]) => method === 'test'));
  const evil = '<img src=x onerror=alert(1)>';
  view.updateDevices({ devices: [{ mac: '02:00:00:00:00:01', friendly_name: evil, online: true, trust: 'unknown', last_interface: 'ap0', first_seen: '2026-01-01T00:00:00Z', last_seen: '2026-01-01T00:00:00Z' }] });
  assert.match(view.deviceList.textContent, /<img src=x/);
  assert(!all(view.deviceList).some(e => e.tag === 'img'), 'dynamic text became HTML');
  const denied = {mac:'02:00:00:00:00:01', trust:'unknown', desired_deny:true, policy_revision:2, policy_pending:true};
  view.updateDevices({devices:[denied]});
  assert.match(view.deviceList.textContent, /application pending/);
  assert(all(view.deviceList).find(e => e.tag==='button' && e.textContent==='Trust').disabled, 'Trust could bypass deny');
  await all(view.deviceList).find(e => e.tag==='button' && e.textContent==='Unblock').click();
  const confirm = ui.modal.flatMap(all).find(e => e.tag==='button' && e.textContent==='Remove my deny');
  assert(confirm, 'native unblock lacks confirmation');
  await confirm.click({currentTarget:confirm});
  assert.deepEqual(calls.find(([method])=>method==='unblock'), ['unblock',[denied.mac,2]]);
  const readDenied = makeView(); readDenied.readonly = true; readDenied.status=E('p');readDenied.deviceList=E('div');
  readDenied.updateDevices({devices:[denied]});
  assert(!all(readDenied.deviceList).some(e=>e.tag==='button'), 'read-only role received unblock controls');
  // Both transport failures and RPC error envelopes must stay isolated.
  const validSettings = { enabled: true, chat_id: '-100123', token_set: true };
  const controls = tree => ({
    inputs: all(tree).filter(e => e.tag === 'input'),
    enabled: all(tree).find(e => e.type === 'checkbox'),
    chat: all(tree).find(e => e.type === 'text'),
    token: all(tree).find(e => e.type === 'password'),
    buttons: all(tree).filter(e => e.tag === 'button')
  });
  for (const failure of [new Error('offline'), { error: 'RPC unavailable' }]) {
    for (const endpoint of ['devices', 'history']) {
      replies.devices = { devices: [] }; replies.history = { events: [] };
      replies.settings = validSettings; replies[endpoint] = failure;
      const partial = makeView();
      const partialTree = partial.render(await partial.load());
      const c = controls(partialTree);
      assert(c.enabled.checked, `${endpoint} failure discarded valid enabled setting`);
      assert.equal(c.chat.value, validSettings.chat_id);
      assert.match(c.token.placeholder, /Configured/);
      assert(c.buttons.every(b => !b.disabled));
      assert.match(partialTree.textContent, /unavailable/i);
      if (endpoint === 'history') assert.match(partial.status.textContent, /0 online/);
      c.chat.value = 'pending edit';
      await partial.refresh();
      assert.equal(c.chat.value, 'pending edit');
      assert(c.enabled.checked && c.buttons.every(b => !b.disabled));
      replies[endpoint] = endpoint === 'devices' ? { devices: [] } : { events: [] };
      await partial.refresh();
      assert(!/unavailable/i.test(partialTree.textContent));
    }
    replies.devices = { devices: [] }; replies.history = { events: [] }; replies.settings = failure;
    const failed = makeView();
    const failedTree = failed.render(await failed.load());
    const c = controls(failedTree);
    assert.match(failedTree.textContent, /settings unavailable/i);
    assert(!/Not configured/.test(c.token.placeholder), 'unknown token rendered as absent');
    assert(c.inputs.every(e => e.disabled), 'unknown settings were editable');
    assert(c.buttons.every(e => e.disabled), 'unknown settings could be saved/tested');
    const mutations = calls.filter(([method]) => ['configure', 'test'].includes(method)).length;
    for (const button of c.buttons) await button.click();
    assert.equal(calls.filter(([method]) => ['configure', 'test'].includes(method)).length, mutations);
    await poll.callback();
    assert(c.buttons.every(e => e.disabled), 'failed retry unlocked controls');
    replies.settings = validSettings;
    await failed.refresh();
    assert(c.enabled.checked);
    assert.equal(c.chat.value, validSettings.chat_id);
    assert.match(c.token.placeholder, /Configured/);
    assert(c.inputs.every(e => !e.disabled) && c.buttons.every(e => !e.disabled));
    assert(!/settings unavailable/i.test(failedTree.textContent));
    c.enabled.checked = false; c.chat.value = 'unsaved chat'; c.token.value = 'unsaved token';
    c.inputs.filter(e => e.type === 'checkbox')[1].checked = true;
    const settingsCalls = calls.filter(([method]) => method === 'settings').length;
    replies.settings = failure;
    await poll.callback();
    assert.equal(calls.filter(([method]) => method === 'settings').length, settingsCalls, 'refresh refetched loaded settings');
    assert(!c.enabled.checked); assert.equal(c.chat.value, 'unsaved chat');
    assert.equal(c.token.value, 'unsaved token');
    assert(c.inputs.filter(e => e.type === 'checkbox')[1].checked);
  }
  // Overlapping retries must not let a late response clobber recovered edits.
  replies.settings = new Error('offline');
  const racing = makeView();
  const racingTree = racing.render(await racing.load());
  let firstReply, secondReply;
  replies.settings = new Promise(resolve => { firstReply = resolve; });
  const firstRefresh = racing.refresh();
  replies.settings = new Promise(resolve => { secondReply = resolve; });
  const secondRefresh = racing.refresh();
  firstReply({ enabled: false, chat_id: '', token_set: false });
  await firstRefresh;
  const recovered = controls(racingTree);
  assert(!recovered.enabled.checked && !recovered.enabled.disabled);
  assert.equal(recovered.chat.value, '');
  assert.equal(recovered.token.placeholder, 'Not configured');
  recovered.chat.value = 'keep edit';
  secondReply(validSettings);
  await secondRefresh;
  assert.equal(recovered.chat.value, 'keep edit', 'late recovery overwrote unsaved edit');
  assert(!recovered.enabled.checked);
  replies.settings = { enabled: false, chat_id: '', token_set: false };
  writable = false;
  // Recovering settings must never unlock a read-only session.
  replies.settings = new Error('offline');
  const recoveringRead = makeView();
  const recoveringTree = recoveringRead.render(await recoveringRead.load());
  replies.settings = validSettings;
  await recoveringRead.refresh();
  assert(controls(recoveringTree).enabled.checked);
  assert([...controls(recoveringTree).inputs, ...controls(recoveringTree).buttons].every(e => e.disabled));
  const readView = makeView();
  const readTree = readView.render(data);
  assert(all(readTree).filter(e => e.tag === 'button' || e.tag === 'input').every(e => e.disabled));
  const acl = JSON.parse(fs.readFileSync('luci/root/usr/share/rpcd/acl.d/luci-app-wifi-guard.json'))['luci-app-wifi-guard'];
  assert.deepEqual(acl.read.ubus['wifi-guard'].sort(), ['devices', 'history', 'settings']);
  for (const action of ['rename', 'trust', 'unblock', 'configure', 'test']) {
    assert(!acl.read.ubus['wifi-guard'].includes(action));
    assert(acl.write.ubus['wifi-guard'].includes(action));
  }
  assert(!acl.read.uci && !acl.read.file, 'generic credential-bearing file/UCI API granted');
  assert(!/innerHTML/.test(source), 'unsafe HTML rendering introduced');
  console.log('PASS: LuCI independent load/refresh failures, settings recovery and edit preservation, empty/disabled state, save/test booleans, text escaping, readonly controls, ACL contract');
})().catch(e => { console.error(e); process.exitCode = 1; });

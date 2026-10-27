'use strict';
'require view';
'require rpc';
'require ui';
'require poll';

var devices = rpc.declare({ object: 'wifi-guard', method: 'devices', expect: { '': {} } });
var history = rpc.declare({ object: 'wifi-guard', method: 'history', params: ['limit'], expect: { '': {} } });
var settings = rpc.declare({ object: 'wifi-guard', method: 'settings', expect: { '': {} } });
var rename = rpc.declare({ object: 'wifi-guard', method: 'rename', params: ['mac', 'name'], expect: { '': {} } });
var trust = rpc.declare({ object: 'wifi-guard', method: 'trust', params: ['mac', 'trust'], expect: { '': {} } });
var unblock = rpc.declare({ object: 'wifi-guard', method: 'unblock', params: ['mac', 'revision'], expect: { '': {} } });
var configure = rpc.declare({ object: 'wifi-guard', method: 'configure', params: ['enabled', 'chat_id', 'token', 'clear_token'], expect: { '': {} } });
var test = rpc.declare({ object: 'wifi-guard', method: 'test', expect: { '': {} } });

function checked(result) {
	if (!result || result.error)
		throw new Error(result && result.error ? result.error : _('WiFi Guard did not respond.'));
	return result;
}
function timestamp(value) {
	var time = new Date(value);
	return isNaN(time.getTime()) ? '—' : time.toLocaleString();
}
function cell(value) { return E('td', {}, [String(value == null ? '' : value)]); }
function loadResult(promise) {
	return promise.then(checked).catch(function() { return { error: true }; });
}

return view.extend({
	load: function() {
		return Promise.all([loadResult(devices()), loadResult(history(100)), loadResult(settings())]);
	},
	notify: function(promise, message) {
		var self = this;
		return promise.then(checked).then(function() {
			ui.addNotification(null, E('p', {}, [message]), 'info');
			return self.refresh();
		}).catch(function(error) {
			ui.addNotification(null, E('p', {}, [String(error.message || error)]), 'error');
		});
	},

	editName: function(device) {
		var self = this;
		var input = E('input', { type: 'text', maxlength: 80, 'class': 'cbi-input-text', value: device.friendly_name || '' });
		ui.showModal(_('Rename Wi-Fi device'), [
			E('p', {}, [device.mac]), input,
			E('div', { 'class': 'right' }, [
				E('button', { 'class': 'btn', click: ui.hideModal }, [_('Cancel')]), ' ',
				E('button', { 'class': 'btn cbi-button-positive', click: function(event) {
					var button = event.currentTarget;
					button.disabled = true;
					return rename(device.mac, input.value).then(checked).then(function() {
						ui.hideModal(); return self.refresh();
					}).catch(function() {
						button.disabled = false;
						ui.addNotification(null, E('p', {}, [_('Rename failed. Use at most 80 characters without control characters.')]), 'error');
					});
				} }, [_('Save name')])
			])
		]);
		input.focus();
	},

	updateDevices: function(data) {
		var self = this;
		var list = data.devices || [];
		var online = list.filter(function(d) { return d.online; }).length;
		this.status.textContent = data.error ? _('Monitor unavailable. Start wifi-guard and refresh this page.') :
			_('%d online / %d known Wi-Fi devices').format(online, list.length);
		var table = E('table', { 'class': 'table cbi-section-table' }, [E('tr', { 'class': 'tr table-titles' },
			[_('Device'), _('Connection'), _('First seen'), _('Last seen'), _('Trust / actions')].map(function(title) { return E('th', {}, [title]); }))]);
		list.sort(function(a, b) { return Number(b.online) - Number(a.online) || a.mac.localeCompare(b.mac); });
		list.forEach(function(d) {
			var denied = !!d.desired_deny || d.trust === 'blocked';
			var label = denied ? (d.policy_pending ? _('Deny requested — application pending') : _('MAC deny applied')) :
				(d.policy_pending ? _('Unblock pending — inspect policy status') : (d.trust === 'trusted' ? _('Trusted') : _('Unknown')));
			var actions = E('td', {}, [E('span', {}, [label])]);
			if (!self.readonly) {
				actions.appendChild(E('div', {}, [
					E('button', { 'class': 'btn cbi-button-edit', click: function() { self.editName(d); } }, [_('Rename')]), ' ',
					E('button', { 'class': 'btn', disabled: denied || d.policy_pending || null, click: function() {
						return self.notify(trust(d.mac, d.trust === 'trusted' ? 'unknown' : 'trusted'), _('Trust updated. No network access policy was changed.'));
					} }, [d.trust === 'trusted' ? _('Mark unknown') : _('Trust')])
				]));
				if (d.desired_deny) {
					actions.appendChild(E('button', { 'class':'btn cbi-button-positive', click:function() {
						ui.showModal(_('Remove WiFi Guard MAC deny'), [
							E('p', {}, [d.mac, ' — ', _('Only WiFi Guard-owned rules will be removed. Independent administrator rules remain in force.')]),
							E('button', { 'class':'btn', click:ui.hideModal }, [_('Cancel')]), ' ',
							E('button', { 'class':'btn cbi-button-positive', click:function(event) {
								event.currentTarget.disabled = true;
								return self.notify(unblock(d.mac, d.policy_revision).then(function(result) {
									checked(result); ui.hideModal();
									if (!result.ok) throw new Error(_('Unblock was recorded, but application is not confirmed. Check policy status.'));
									return result;
								}), _('WiFi Guard deny removed. Independent network rules may still apply.'));
							} }, [_('Remove my deny')])
						]);
					} }, [_('Unblock')]) );
				}
			}
			table.appendChild(E('tr', { 'class': 'tr' }, [
				E('td', {}, [E('strong', {}, [d.friendly_name || _('Unnamed device')]), E('br'), E('code', {}, [d.mac])]),
				E('td', {}, [d.online ? _('Online') : _('Offline'), E('br'), E('small', {}, [d.last_interface || ''])]),
				cell(timestamp(d.first_seen)), cell(timestamp(d.last_seen)), actions
			]));
		});
		if (!list.length)
			table.appendChild(E('tr', {}, [E('td', { colspan: 5 }, [_('No known Wi-Fi devices yet. Authorized clients will appear automatically.')])]));
		this.deviceList.replaceChildren(table);
	},

	updateHistory: function(data) {
		if (data.error) {
			this.historyList.replaceChildren(E('p', {}, [_('History unavailable. Refresh to retry.')]));
			return;
		}
		var table = E('table', { 'class': 'table' }, [E('tr', {}, [_('Time'), _('Event'), _('MAC'), _('Interface')].map(function(title) { return E('th', {}, [title]); }))]);
		(data.events || []).forEach(function(e) {
			table.appendChild(E('tr', {}, [cell(timestamp(e.at)), cell(e.kind), cell(e.mac), cell(e.interface)]));
		});
		this.historyList.replaceChildren(table);
	},

	refresh: function() {
		var self = this;
		return Promise.all([
			loadResult(devices()).then(function(data) { self.updateDevices(data); }),
			loadResult(history(100)).then(function(data) { self.updateHistory(data); }),
			self.settingsLoaded ? Promise.resolve() : loadResult(settings()).then(function(data) { self.updateSettings(data); })
		]);
	},

	render: function(data) {
		var self = this;
		this.readonly = !L.hasViewPermission();
		this.status = E('p', { role: 'status', 'aria-live': 'polite' });
		this.deviceList = E('div', { style: 'overflow-x:auto' });
		this.historyList = E('div', { style: 'max-height:26rem;overflow:auto' });
		this.settingsLoaded = false;
		var locked = this.readonly || !!data[2].error;
		var settingsStatus = E('p', { role: 'status', 'aria-live': 'polite' });
		var enabled = E('input', { type: 'checkbox', disabled: locked || null });
		var chat = E('input', { type: 'text', 'class': 'cbi-input-text', value: '', placeholder: _('Telegram chat ID'), disabled: locked || null });
		var token = E('input', { type: 'password', 'class': 'cbi-input-text', autocomplete: 'new-password', value: '', disabled: locked || null });
		var clear = E('input', { type: 'checkbox', disabled: locked || null });
		var save = E('button', { 'class': 'btn cbi-button-positive', disabled: locked || null, click: function() {
			if (self.readonly || !self.settingsLoaded || save.disabled) return;
			save.disabled = true;
			return configure(enabled.checked, chat.value, token.value, clear.checked).then(checked).then(function() {
				token.value = ''; clear.checked = false;
				return settings().then(checked).then(function(s) { token.placeholder = s.token_set ? _('Configured — leave blank to keep') : _('Not configured'); });
			}).then(function() { ui.addNotification(null, E('p', {}, [_('Telegram settings saved. No network settings were changed.')]), 'info'); })
			.catch(function() { ui.addNotification(null, E('p', {}, [_('Could not confirm settings. Check chat ID, token, enabled state and storage permissions; refresh settings before retrying.')]), 'error'); })
			.finally(function() { save.disabled = self.readonly || !self.settingsLoaded; });
		} }, [_('Save Telegram settings')]);
		var send = E('button', { 'class': 'btn', disabled: locked || null, click: function() {
			if (self.readonly || !self.settingsLoaded || send.disabled) return;
			send.disabled = true;
			return self.notify(test(), _('Telegram accepted the test message.')).finally(function() { send.disabled = self.readonly || !self.settingsLoaded; });
		} }, [_('Send test message')]);
		this.updateSettings = function(s) {
			if (self.settingsLoaded) return;
			if (s.error) {
				settingsStatus.textContent = _('Telegram settings unavailable. Refresh to retry; saving and testing are disabled.');
				token.placeholder = _('Settings unavailable');
				return;
			}
			// Only initial load/recovery populates fields; polling must preserve edits.
			self.settingsLoaded = true;
			enabled.checked = !!s.enabled;
			chat.value = s.chat_id || '';
			token.placeholder = s.token_set ? _('Configured — leave blank to keep') : _('Not configured');
			settingsStatus.textContent = _('Callback polling: %s; new blocks: %s; protected MACs: %d. Advanced controls require an explicit service restart.').format(
				s.callbacks_enabled ? _('enabled') : _('disabled'), s.blocking_enabled ? _('enabled') : _('disabled'), (s.protected_macs || []).length);
			[enabled, chat, token, clear, save, send].forEach(function(control) { control.disabled = self.readonly; });
		};
		this.updateSettings(data[2]);
		this.updateDevices(data[0]); this.updateHistory(data[1]);
		poll.add(function() { return self.refresh(); }, 10);
		return E('div', {}, [
			E('h2', {}, [_('WiFi Guard')]),
			E('p', {}, [_('Only authorized Wi-Fi clients are monitored. Known does not mean trusted; trust is not network authorization. Private MAC rotation may appear as a new device.')]),
			E('div', { 'class': 'cbi-section' }, [E('h3', {}, [_('Wi-Fi devices')]), this.status, this.deviceList]),
			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, [_('Telegram notifications')]),
				settingsStatus,
				E('p', {}, [_('New-device messages contain a MAC address. The saved token is never returned to this page. Blank keeps it; clearing requires disabling notifications or entering a replacement.')]),
				E('p', {}, [E('label', {}, [enabled, ' ', _('Enable Telegram notifications')])]),
				E('p', {}, [E('label', {}, [_('Chat ID'), E('br'), chat])]),
				E('p', {}, [E('label', {}, [_('Bot token'), E('br'), token])]),
				E('p', {}, [E('label', {}, [clear, ' ', _('Clear saved token')])]), save, ' ', send
			]),
			E('div', { 'class': 'cbi-section' }, [E('h3', {}, [_('Recent device history')]), this.historyList])
		]);
	},
	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});

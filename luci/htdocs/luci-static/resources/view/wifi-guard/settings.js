'use strict';
'require view';
'require rpc';
'require ui';
'require poll';
var settings = rpc.declare({ object: 'wifi-guard', method: 'settings', expect: { '': {} } });
var configure = rpc.declare({ object: 'wifi-guard', method: 'configure', params: ['enabled', 'chat_id', 'token', 'clear_token'], expect: { '': {} } });
var test = rpc.declare({ object: 'wifi-guard', method: 'test', expect: { '': {} } });
function checked(result) {
	if (!result || result.error)
		throw new Error(result && result.error ? result.error : _('WiFi Guard did not respond.'));
	return result;
}
function loadResult(promise) {
	return promise.then(checked).catch(function() { return { error: true }; });
}

return view.extend({
	load: function() { return loadResult(settings()); },
	notify: function(promise, message) {
		var self = this;
		return promise.then(checked).then(function() {
			ui.addNotification(null, E('p', {}, [message]), 'info');
			return self.refresh();
		}).catch(function(error) {
			ui.addNotification(null, E('p', {}, [String(error.message || error)]), 'error');
		});
	},
	refresh: function() {
		var self = this;
		return this.settingsLoaded ? Promise.resolve() : loadResult(settings()).then(function(data) { self.updateSettings(data); });
	},
	render: function(data) {
		var self = this;
		this.readonly = !L.hasViewPermission();
		this.settingsLoaded = false;
		this.settingsUncertain = false;
		this.saving = false;
		var locked = this.readonly || !!data.error;
		var settingsStatus = E('p', { role: 'status', 'aria-live': 'polite' });
		var enabled = E('input', { type: 'checkbox', disabled: locked || null });
		var chat = E('input', { type: 'text', 'class': 'cbi-input-text', value: '', placeholder: _('Telegram chat ID'), disabled: locked || null });
		var token = E('input', { type: 'password', 'class': 'cbi-input-text', autocomplete: 'new-password', value: '', disabled: locked || null });
		var clear = E('input', { type: 'checkbox', disabled: locked || null });
		var save = E('button', { 'class': 'btn cbi-button-positive', disabled: locked || null, click: function() {
			if (self.readonly || !self.settingsLoaded || save.disabled) return;
			self.saving = true;
			[enabled, chat, token, clear, save, send].forEach(function(control) { control.disabled = true; });
			return configure(enabled.checked, chat.value, token.value, clear.checked).then(checked).then(function() {
				token.value = ''; clear.checked = false;
				return settings().then(checked).then(function(s) { token.placeholder = s.token_set ? _('Configured — leave blank to keep') : _('Not configured'); });
			}).then(function() { ui.addNotification(null, E('p', {}, [_('Telegram settings saved. No network settings were changed.')]), 'info'); })
			.catch(function() {
				// A failed reply may follow a committed write. Do not retry stale fields.
				self.settingsUncertain = true;
				settingsStatus.textContent = _('Telegram settings unavailable. Refresh to retry; saving and testing are disabled.');
				ui.addNotification(null, E('p', {}, [_('Could not confirm settings. Check chat ID, token, enabled state and storage permissions; refresh settings before retrying.')]), 'error');
			})
			.finally(function() {
				self.saving = false;
				[enabled, chat, token, clear, save, send].forEach(function(control) { control.disabled = self.readonly || !self.settingsLoaded || self.settingsUncertain; });
			});
		} }, [_('Save Telegram settings')]);
		var send = E('button', { 'class': 'btn', disabled: locked || null, click: function() {
			if (self.readonly || !self.settingsLoaded || send.disabled) return;
			send.disabled = true;
			return self.notify(test(), _('Telegram accepted the test message.')).finally(function() { send.disabled = self.readonly || !self.settingsLoaded || self.settingsUncertain || self.saving; });
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
		this.updateSettings(data);
		poll.add(function() { return self.refresh(); }, 10);
		return E('div', {}, [
			E('h2', {}, [_('Telegram settings')]),
			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, [_('Telegram notifications')]),
				settingsStatus,
				E('p', {}, [_('New-device messages contain a MAC address. The saved token is never returned to this page. Blank keeps it; clearing requires disabling notifications or entering a replacement.')]),
				E('p', {}, [E('label', {}, [enabled, ' ', _('Enable Telegram notifications')])]),
				E('p', {}, [E('label', {}, [_('Chat ID'), E('br'), chat])]),
				E('p', {}, [E('label', {}, [_('Bot token'), E('br'), token])]),
				E('p', {}, [E('label', {}, [clear, ' ', _('Clear saved token')])]), save, ' ', send
			])
		]);
	},
	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});

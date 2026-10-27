'use strict';
'require view';
'require rpc';
'require ui';
'require poll';

var devices = rpc.declare({ object: 'wifi-guard', method: 'devices', expect: { '': {} } });
var rename = rpc.declare({ object: 'wifi-guard', method: 'rename', params: ['mac', 'name'], expect: { '': {} } });
var trust = rpc.declare({ object: 'wifi-guard', method: 'trust', params: ['mac', 'trust'], expect: { '': {} } });
var unblock = rpc.declare({ object: 'wifi-guard', method: 'unblock', params: ['mac', 'revision'], expect: { '': {} } });
function checked(result) {
	if (!result || result.error) throw new Error(result && result.error ? result.error : _('WiFi Guard did not respond.'));
	return result;
}
function timestamp(value) { var time = new Date(value); return isNaN(time.getTime()) ? '—' : time.toLocaleString(); }
function cell(value) { return E('td', {}, [String(value == null ? '' : value)]); }

return view.extend({
	load: function() { return devices().then(checked).catch(function() { return { error: true }; }); },
	notify: function(promise, message) {
		var self = this;
		return promise.then(checked).then(function() {
			ui.addNotification(null, E('p', {}, [message]), 'info'); return self.refresh();
		}).catch(function(error) { ui.addNotification(null, E('p', {}, [String(error.message || error)]), 'error'); });
	},
	editName: function(device) {
		var self = this;
		var input = E('input', { type: 'text', maxlength: 80, 'class': 'cbi-input-text', value: device.friendly_name || '' });
		ui.showModal(_('Rename Wi-Fi device'), [E('p', {}, [device.mac]), input,
			E('div', { 'class': 'right' }, [E('button', { 'class': 'btn', click: ui.hideModal }, [_('Cancel')]), ' ',
				E('button', { 'class': 'btn cbi-button-positive', click: function(event) {
					var button = event.currentTarget; button.disabled = true;
					return rename(device.mac, input.value).then(checked).then(function() { ui.hideModal(); return self.refresh(); })
						.catch(function() { button.disabled = false; ui.addNotification(null, E('p', {}, [_('Rename failed. Use at most 80 characters without control characters.')]), 'error'); });
				} }, [_('Save name')])])]);
		input.focus();
	},
	updateDevices: function(data) {
		this.devicesAvailable = !data.error;
		if (data.error) {
			this.currentDevices = [];
			this.status.textContent = _('Monitor unavailable. Start wifi-guard and refresh this page.');
			this.deviceList.replaceChildren(); return;
		}
		this.currentDevices = data.devices || [];
		this.filterDevices();
	},
	filterDevices: function() {
		if (!this.devicesAvailable) return;
		var self = this;
		var all = this.currentDevices || [];
		var query = (this.search.value || '').toLocaleLowerCase();
		var filter = this.filter.value;
		var list = all.filter(function(d) {
			return (!query || [d.mac, d.friendly_name, d.last_interface].some(function(v) { return String(v || '').toLocaleLowerCase().indexOf(query) !== -1; })) &&
				(filter === 'all' || (filter === 'online' && d.online) || (filter === 'offline' && !d.online) ||
				(filter === 'trusted' && d.trust === 'trusted') || (filter === 'denied' && d.desired_deny));
		});
		this.status.textContent = _('%d shown / %d known Wi-Fi devices').format(list.length, all.length);
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
				if (d.desired_deny) actions.appendChild(E('button', { 'class': 'btn cbi-button-positive', click: function() {
				ui.showModal(_('Remove WiFi Guard MAC deny'), [
					E('p', {}, [d.mac, ' — ', _('Only WiFi Guard-owned rules will be removed. Independent administrator rules remain in force.')]),
					E('button', { 'class': 'btn', click: ui.hideModal }, [_('Cancel')]), ' ',
					E('button', { 'class': 'btn cbi-button-positive', click: function(event) {
						event.currentTarget.disabled = true;
						return self.notify(unblock(d.mac, d.policy_revision).then(function(result) {
							checked(result); ui.hideModal();
							if (!result.ok) throw new Error(_('Unblock was recorded, but application is not confirmed. Check policy status.'));
							return result;
						}), _('WiFi Guard deny removed. Independent network rules may still apply.'));
					} }, [_('Remove my deny')])]);
			} }, [_('Unblock')]));
			}
			table.appendChild(E('tr', { 'class': 'tr' }, [
				E('td', {}, [E('strong', {}, [d.friendly_name || _('Unnamed device')]), E('br'), E('code', {}, [d.mac])]),
				E('td', {}, [d.online ? _('Online') : _('Offline'), E('br'), E('small', {}, [d.last_interface || ''])]),
				cell(timestamp(d.first_seen)), cell(timestamp(d.last_seen)), actions]));
		});
		if (!list.length) table.appendChild(E('tr', {}, [E('td', { colspan: 5 }, [all.length ? _('No devices match the search or filter.') : _('No known Wi-Fi devices yet. Authorized clients will appear automatically.')])]));
		this.deviceList.replaceChildren(table);
	},
	refresh: function() { var self = this; return this.load().then(function(data) { self.updateDevices(data); }); },
	render: function(data) {
		var self = this;
		this.readonly = !L.hasViewPermission();
		this.status = E('p', { role: 'status', 'aria-live': 'polite' });
		this.deviceList = E('div', { style: 'overflow-x:auto' });
		this.search = E('input', { type: 'search', 'class': 'cbi-input-text', placeholder: _('Search name, MAC or interface'), input: function() { self.filterDevices(); } });
		this.filter = E('select', { change: function() { self.filterDevices(); } }, [
			['all', _('All')], ['online', _('Online')], ['offline', _('Offline')], ['trusted', _('Trusted')], ['denied', _('Deny requested')]
		].map(function(item) { return E('option', { value: item[0] }, [item[1]]); }));
		this.filter.value = 'all';
		this.updateDevices(data);
		poll.add(function() { return self.refresh(); }, 10);
		return E('div', {}, [E('h2', {}, [_('Wi-Fi devices')]),
			E('p', {}, [_('Search'), ' ', this.search, ' ', _('Filter'), ' ', this.filter]),
			E('div', { 'class': 'cbi-section' }, [this.status, this.deviceList])]);
	},
	handleSaveApply: null, handleSave: null, handleReset: null
});

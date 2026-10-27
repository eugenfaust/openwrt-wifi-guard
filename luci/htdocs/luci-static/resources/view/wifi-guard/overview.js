'use strict';
'require view';
'require rpc';
'require poll';

var devices = rpc.declare({ object: 'wifi-guard', method: 'devices', expect: { '': {} } });
var history = rpc.declare({ object: 'wifi-guard', method: 'history', params: ['limit'], expect: { '': {} } });

function read(promise) { return promise.then(function(r) { return r && !r.error ? r : { error: true }; }).catch(function() { return { error: true }; }); }
function count(list, predicate) { return list.filter(predicate).length; }
function eventLabel(kind) {
	switch (kind) {
	case 'discovered': return _('Device discovered');
	case 'connected': return _('Connected');
	case 'disconnected': return _('Disconnected');
	default: return String(kind || '');
	}
}

return view.extend({
	load: function() { return Promise.all([read(devices()), read(history(100))]); },
	render: function(data) {
		var self = this;
		this.summary = E('div', { 'class': 'cbi-section', role: 'status', 'aria-live': 'polite' });
		this.recent = E('div', { 'class': 'cbi-section' });
		this.update(data[0], data[1]);
		poll.add(function() { return self.load().then(function(result) { self.update(result[0], result[1]); }); }, 10);
		return E('div', {}, [E('h2', {}, [_('WiFi Guard — Overview')]),
			E('p', {}, [_('Only authorized Wi-Fi clients are monitored. Known does not mean trusted; trust is not network authorization. Private MAC rotation may appear as a new device.')]),
			this.summary, this.recent]);
	},
	update: function(deviceData, historyData) {
		if (deviceData.error) {
			this.summary.replaceChildren(E('p', {}, [_('Monitor unavailable. Start wifi-guard and refresh this page.')]));
		} else {
			var list = deviceData.devices || [];
			this.summary.replaceChildren(E('h3', {}, [_('Connection statistics')]),
				E('p', {}, [_('%d online / %d known Wi-Fi devices').format(count(list, function(d) { return d.online; }), list.length)]),
				E('p', {}, [_('Offline: %d · Trusted: %d · Deny requested: %d').format(
					count(list, function(d) { return !d.online; }), count(list, function(d) { return d.trust === 'trusted'; }),
					count(list, function(d) { return !!d.desired_deny; }))]));
		}
		if (historyData.error) {
			this.recent.replaceChildren(E('h3', {}, [_('Recent activity')]), E('p', {}, [_('History unavailable. Refresh to retry.')]));
			return;
		}
		var events = (historyData.events || []).slice(0, 5);
		var rows = events.map(function(event) {
			var date = new Date(event.at);
			return E('tr', { 'class': 'tr' }, [E('td', {}, [isNaN(date.getTime()) ? '—' : date.toLocaleString()]),
				E('td', {}, [eventLabel(event.kind)]), E('td', {}, [String(event.mac || '')])]);
		});
		this.recent.replaceChildren(E('h3', {}, [_('Recent activity')]), events.length ?
			E('table', { 'class': 'table cbi-section-table' }, [E('tr', { 'class': 'tr table-titles' },
				[_('Time'), _('Event'), _('MAC')].map(function(title) { return E('th', {}, [title]); }))].concat(rows)) :
			E('p', {}, [_('No history yet.')]));
	},
	handleSaveApply: null, handleSave: null, handleReset: null
});

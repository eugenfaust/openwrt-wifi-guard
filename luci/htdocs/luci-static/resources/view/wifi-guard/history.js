'use strict';
'require view';
'require rpc';
'require poll';

// The RPC supports only a latest-N limit (maximum 500), not an offset.
// Pagination is local to this bounded window; never imply older events are loaded.
var history = rpc.declare({ object: 'wifi-guard', method: 'history', params: ['limit'], expect: { '': {} } });
var pageSize = 25;
function cell(value) { return E('td', {}, [String(value == null ? '' : value)]); }
function eventLabel(kind) {
	switch (kind) {
	case 'discovered': return _('Device discovered');
	case 'connected': return _('Connected');
	case 'disconnected': return _('Disconnected');
	default: return String(kind || '');
	}
}
return view.extend({
	load: function() { return history(500).then(function(data) { return data && !data.error ? data : { error: true }; }).catch(function() { return { error: true }; }); },
	refresh: function() { var self = this; return this.load().then(function(data) { self.updateHistory(data); }); },
	updateHistory: function(data) {
		if (data.error) {
			this.list.replaceChildren(E('p', {}, [_('History unavailable. Refresh to retry.')]));
			return;
		}
		this.events = (data.events || []).slice(0, 500);
		this.page = Math.min(this.page, Math.max(0, Math.ceil(this.events.length / pageSize) - 1));
		this.showPage();
	},
	showPage: function() {
		var self = this;
		var table = E('table', { 'class': 'table cbi-section-table' }, [E('tr', { 'class': 'tr table-titles' },
			[_('Time'), _('Event'), _('MAC'), _('Interface')].map(function(title) { return E('th', {}, [title]); }))]);
		this.events.slice(this.page * pageSize, (this.page + 1) * pageSize).forEach(function(event) {
			var time = new Date(event.at);
			table.appendChild(E('tr', { 'class': 'tr' }, [cell(isNaN(time.getTime()) ? '—' : time.toLocaleString()),
				cell(eventLabel(event.kind)), cell(event.mac), cell(event.interface)]));
		});
		var pages = Math.max(1, Math.ceil(this.events.length / pageSize));
		this.list.replaceChildren(this.events.length ? table : E('p', {}, [_('No history yet.')]),
			E('div', { 'class': 'cbi-page-actions' }, [
				E('button', { 'class': 'btn', disabled: this.page === 0 || null, click: function() { self.page--; self.showPage(); } }, [_('Previous')]), ' ',
				E('span', { role: 'status' }, [_('Page %d of %d · %d recent events (maximum 500)').format(this.page + 1, pages, this.events.length)]), ' ',
				E('button', { 'class': 'btn', disabled: this.page >= pages - 1 || null, click: function() { self.page++; self.showPage(); } }, [_('Next')])
			]));
	},
	render: function(data) {
		var self = this;
		this.page = 0; this.events = [];
		this.list = E('div', { 'class': 'cbi-section', style: 'overflow-x:auto' });
		this.updateHistory(data);
		poll.add(function() { return self.refresh(); }, 10);
		return E('div', {}, [E('h2', {}, [_('Device history')]), this.list]);
	},
	handleSaveApply: null, handleSave: null, handleReset: null
});

const relay = getApp().relay;
const fmt = require('../../utils/format.js');

Page({
  data: { projects: [], loose: [], stateText: '连接中…', stateClass: 'connecting' },

  onLoad() {
    const t = wx.getStorageSync('caret_token');
    if (!t) { wx.reLaunch({ url: '/pages/connect/connect' }); return; }
    if (relay.state === 'disconnected' || relay.state === 'error') relay.connect(t);
  },
  onShow() {
    this._unsub = relay.subscribe(() => this._render());
    this._render();
    if (relay.state === 'connected') relay.listSessions();
  },
  onHide() { if (this._unsub) this._unsub(); },
  onUnload() { if (this._unsub) this._unsub(); },
  onPullDownRefresh() { relay.listSessions(); setTimeout(() => wx.stopPullDownRefresh(), 600); },

  _render() {
    const g = fmt.groupSessions(relay.sessions || []);
    g.projects.forEach((p) => p.items.forEach((s) => { s._rel = fmt.relTime(s.updatedAt); }));
    g.loose.forEach((s) => { s._rel = fmt.relTime(s.updatedAt); });
    const st = relay.state;
    this.setData({
      projects: g.projects, loose: g.loose,
      stateClass: st,
      stateText: st === 'connected' ? '已连接' : st === 'connecting' ? '连接中…' : st === 'error' ? '连接失败' : '未连接',
    });
  },

  open(e) { relay.openThread(e.currentTarget.dataset.id); wx.navigateTo({ url: '/pages/chat/chat' }); },
  newChat() { relay.newThread(); wx.navigateTo({ url: '/pages/chat/chat' }); },
  repair() { wx.reLaunch({ url: '/pages/connect/connect' }); },
});

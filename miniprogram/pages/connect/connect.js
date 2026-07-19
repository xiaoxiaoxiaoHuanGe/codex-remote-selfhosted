const relay = getApp().relay;
const cfg = require('../../config.js');

Page({
  data: { token: '', connecting: false, error: '', host: cfg.RELAY_HOST },

  onLoad() {
    // 已配对：自动连接并进入会话列表（前台为主，开盒即用）。
    const t = wx.getStorageSync('caret_token');
    if (t) {
      this.setData({ token: t });
      if (relay.state === 'disconnected' || relay.state === 'error') relay.connect(t);
      wx.reLaunch({ url: '/pages/sessions/sessions' });
    }
  },
  onShow() { this._unsub = relay.subscribe(() => this._sync()); this._sync(); },
  onHide() { if (this._unsub) this._unsub(); },
  onUnload() { if (this._unsub) this._unsub(); },

  _sync() {
    this.setData({
      connecting: relay.state === 'connecting',
      error: relay.state === 'error' ? (relay.error || '') : '',
    });
  },

  onInput(e) { this.setData({ token: e.detail.value }); },

  paste() {
    wx.getClipboardData({
      success: (r) => { const t = cfg.tokenFromText(r.data || ''); if (t) this.setData({ token: t }); },
    });
  },

  scan() {
    wx.scanCode({
      onlyFromCamera: false,
      success: (r) => {
        const t = cfg.tokenFromText(r.result || '');
        if (t) { this.setData({ token: t }); this.connect(); }
      },
      fail: () => {},
    });
  },

  connect() {
    const t = (this.data.token || '').trim();
    if (!t) { wx.showToast({ title: '请输入或扫描令牌', icon: 'none' }); return; }
    wx.setStorageSync('caret_token', t);
    relay.connect(t);
    wx.reLaunch({ url: '/pages/sessions/sessions' });
  },
});

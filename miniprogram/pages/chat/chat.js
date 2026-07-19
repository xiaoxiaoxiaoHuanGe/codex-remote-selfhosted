const relay = getApp().relay;

Page({
  data: { items: [], turnRunning: false, approval: null, draft: '', scrollTop: 0 },

  onShow() { this._unsub = relay.subscribe(() => this._render()); this._render(); },
  onHide() { if (this._unsub) this._unsub(); },
  onUnload() { if (this._unsub) this._unsub(); },

  _render() {
    const items = (relay.items || []).map((it, i) => ({
      idx: i, role: it.role, text: it.text, imageB64: it.imageB64 || '',
    }));
    // 每次渲染把 scrollTop 推到一个递增的大值 → 始终滚到底部（含流式追加）。
    this._tick = (this._tick || 0) + 1;
    this.setData({
      items: items,
      turnRunning: relay.turnRunning,
      approval: relay.pendingApproval,
      scrollTop: 100000 + this._tick,
    });
  },

  onInput(e) { this.setData({ draft: e.detail.value }); },
  send() {
    const t = (this.data.draft || '').trim();
    if (!t) return;
    relay.sendPrompt(t);
    this.setData({ draft: '' });
  },
  accept() { relay.respondApproval('accept'); },
  deny() { relay.respondApproval('decline'); },
  interrupt() { relay.interrupt(); },
});

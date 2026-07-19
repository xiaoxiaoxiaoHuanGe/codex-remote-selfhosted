// relay.js —— 小程序的「单一真相源」中继客户端。
// 严格对齐原生 App 的 bridge_client.dart：同一套 JSON-over-WebSocket 协议，
// 同样的事件流式拼接、改名/归档实时同步、断线重连退避。
//
// 协议（与桥一致）：
//   发： {type:'list'} / {type:'read',threadId[,limit,before]} / {type:'prompt',text,threadId?,cwd?,images?}
//        {type:'interrupt',threadId} / {type:'approvalDecision',id,decision:'accept|decline'}
//        {type:'delete',threadId} / {type:'sync'}
//   收： {type:'sessions',data} / {type:'thread',thread[,offset,truncated,total,page]} / {type:'promptAccepted',threadId}
//   注：read 的 limit>0 → 桥只回一个 turn 窗口（带 offset/truncated/total）；before>0 → 窗口截止
//       到该 turn 序号之前（游标向更早翻页,传上一帧的 offset;响应带 page:true 时应前插而非整载）。
//       小程序目前只取尾部窗口（limit=40,不翻页）：微信单帧体积有限,全量大会话会撑断链路。
//   注：thread 内大体积内嵌 base64 图会被桥剥离为绝对路径（imageGeneration.resultPath /
//       image_url 变路径），客户端经 /file 懒加载。
//        {type:'event',method,params} / {type:'approval',id,method,params} / {type:'error',message}
const cfg = require('../config.js');

const relay = {
  // ---- 公开状态（页面 setData 读取）----
  state: 'disconnected', // disconnected | connecting | connected | error
  error: '',
  token: '',
  sessions: [],          // [{id,name,preview,cwd,source,updatedAt,title}]
  currentThreadId: null,
  items: [],             // [{role,text,imageB64?}]  role: user|assistant|reasoning|tool|file|image|media|system
  pendingApproval: null, // {id,method,params,command}
  turnRunning: false,

  // ---- 内部 ----
  _task: null,
  _gen: 0,
  _reconnectTimer: null,
  _reconnectAttempts: 0,
  _suppress: false,
  _asst: -1, _tool: -1, _reason: -1, // 流式拼接中的 items 下标
  _sawSummary: false,
  _listeners: [],

  // ---- 订阅（页面用它驱动 setData）----
  subscribe(fn) {
    this._listeners.push(fn);
    return () => { this._listeners = this._listeners.filter((f) => f !== fn); };
  },
  _emit() { this._listeners.forEach((fn) => { try { fn(); } catch (e) {} }); },

  fileUrl(path) { return cfg.fileUrl(path, this.token); },

  // ---- 连接 ----
  connect(token) {
    if (token) this.token = token;
    if (!this.token) return;
    this._suppress = false;
    const gen = ++this._gen;
    this._teardown();
    this.state = 'connecting'; this.error = ''; this._emit();

    const task = wx.connectSocket({
      url: cfg.wsUrlForToken(this.token),
      fail: () => {
        if (gen !== this._gen) return;
        this.state = 'error'; this.error = '连接失败'; this._emit();
        this._scheduleReconnect();
      },
    });
    this._task = task;

    task.onOpen(() => {
      if (gen !== this._gen) { try { task.close({}); } catch (e) {} return; }
      this.state = 'connected'; this._reconnectAttempts = 0; this._clearReconnect(); this._emit();
      this.listSessions();
      // 断线恢复：重新读当前会话，补回断开期间完成的回复。
      if (this.currentThreadId) this._send({ type: 'read', threadId: this.currentThreadId, limit: 40 });
    });
    task.onMessage((res) => { if (gen === this._gen) this._onMessage(res.data); });
    task.onClose(() => {
      if (gen !== this._gen) return;
      if (this.state !== 'error') this.state = 'disconnected';
      this.turnRunning = false; this._emit(); this._scheduleReconnect();
    });
    task.onError(() => {
      if (gen !== this._gen) return;
      this.state = 'error'; this.error = '连接错误'; this.turnRunning = false;
      this._emit(); this._scheduleReconnect();
    });
  },

  disconnect() {
    this._suppress = true; this._clearReconnect(); this._teardown();
    if (this.state !== 'error') this.state = 'disconnected';
    this._emit();
  },

  _teardown() { if (this._task) { try { this._task.close({}); } catch (e) {} this._task = null; } },
  _send(obj) { if (this._task) { try { this._task.send({ data: JSON.stringify(obj) }); } catch (e) {} } },

  // ---- 命令 ----
  listSessions() { this._send({ type: 'list' }); },
  // 只取最近 40 轮：微信侧单帧体积有限,大会话全量会撑断链路(桥会带 truncated 标记)。
  openThread(id) { this.currentThreadId = id; this._resetChat(); this._send({ type: 'read', threadId: id, limit: 40 }); this._emit(); },
  newThread() { this.currentThreadId = null; this._resetChat(); this._emit(); },
  deleteThread(id) {
    this.sessions = this.sessions.filter((s) => s.id !== id);
    this._send({ type: 'delete', threadId: id }); this._emit();
  },

  sendPrompt(text, opts) {
    opts = opts || {};
    const hasText = (text || '').trim().length > 0;
    const images = opts.images || [];
    if (!hasText && images.length === 0) return;
    if (this.state !== 'connected') {
      this.items.push({ role: 'system', text: '⚠️ 未连接到电脑，正在重连…连上后请重发。' });
      this._emit(); this._scheduleReconnect(); return;
    }
    for (const dataUrl of images) {
      const b64 = dataUrl.indexOf(',') >= 0 ? dataUrl.split(',').pop() : dataUrl;
      this.items.push({ role: 'image', text: '', imageB64: b64 });
    }
    if (hasText) this.items.push({ role: 'user', text: text });
    this._asst = -1; this._tool = -1; this._reason = -1; this._sawSummary = false; this.turnRunning = true;

    const msg = { type: 'prompt', text: text };
    if (images.length) msg.images = images;
    if (this.currentThreadId) msg.threadId = this.currentThreadId;
    if (opts.cwd) msg.cwd = opts.cwd;
    if (opts.effort) msg.effort = opts.effort;
    if (opts.model) msg.model = opts.model;
    if (opts.speed) msg.speed = opts.speed;
    if (opts.approval) msg.approval = opts.approval;
    this._send(msg); this._emit();
  },

  respondApproval(decision) {
    const a = this.pendingApproval;
    if (!a) return;
    this._send({ type: 'approvalDecision', id: a.id, decision: decision });
    this.pendingApproval = null; this._emit();
  },

  interrupt() { if (this.currentThreadId) this._send({ type: 'interrupt', threadId: this.currentThreadId }); },

  _resetChat() {
    this.items = []; this._asst = -1; this._tool = -1; this._reason = -1;
    this._sawSummary = false; this.pendingApproval = null; this.turnRunning = false;
  },

  // ---- 收 ----
  _onMessage(raw) {
    let m;
    try { m = JSON.parse(raw); } catch (e) { return; }
    switch (m.type) {
      case 'sessions': this.sessions = (m.data || []).map(normalizeSession); break;
      case 'thread': this._loadHistory(m.thread); break;
      case 'promptAccepted': this.currentThreadId = m.threadId || this.currentThreadId; break;
      case 'event': this._onEvent(m.method || '', m.params || {}); break;
      case 'approval':
        this.pendingApproval = { id: m.id || '', method: m.method || '', params: m.params || {}, command: approvalCommand(m.params || {}) };
        break;
      case 'error':
        this.error = m.message || 'error'; this.turnRunning = false; this._asst = -1; this._tool = -1;
        this.items.push({ role: 'system', text: '⚠️ ' + this.error });
        break;
    }
    this._emit();
  },

  _onEvent(method, p) {
    p = p || {};
    // —— 会话列表实时同步 ——
    if (method.endsWith('thread/name/updated')) {
      const tid = p.threadId || '', nm = p.threadName || '';
      const idx = this.sessions.findIndex((s) => s.id === tid);
      if (idx >= 0) {
        const next = this.sessions.slice();
        next[idx] = Object.assign({}, next[idx], { name: nm, title: nm || next[idx].preview || tid });
        this.sessions = next;
      } else if (tid) { this.listSessions(); }
      return;
    }
    if (method.endsWith('thread/archived')) {
      const tid = p.threadId || '';
      if (tid) this.sessions = this.sessions.filter((s) => s.id !== tid);
      return;
    }
    if (method.endsWith('thread/unarchived') || method.endsWith('thread/started')) { this.listSessions(); return; }

    // —— 流式 ——
    if (method.endsWith('agentMessage/delta')) {
      this._reason = -1; this._asst = this._ensure(this._asst, 'assistant');
      this.items[this._asst].text += (p.delta || '');
    } else if (method.endsWith('reasoning/summaryTextDelta')) {
      this._sawSummary = true; this._asst = -1; this._reason = this._ensure(this._reason, 'reasoning');
      this.items[this._reason].text += (p.delta || '');
    } else if (method.endsWith('reasoning/textDelta')) {
      if (!this._sawSummary) { this._asst = -1; this._reason = this._ensure(this._reason, 'reasoning'); this.items[this._reason].text += (p.delta || ''); }
    } else if (method.endsWith('reasoning/summaryPartAdded')) {
      if (this._reason >= 0 && this.items[this._reason].text) this.items[this._reason].text += '\n\n';
    } else if (method.endsWith('commandExecution/outputDelta')) {
      this._tool = this._ensure(this._tool, 'tool'); this.items[this._tool].text += (p.delta || '');
    } else if (method === 'turn/started') {
      this.turnRunning = true; this._reason = -1; this._sawSummary = false;
    } else if (method === 'turn/completed') {
      this.turnRunning = false; this._asst = -1; this._tool = -1; this._reason = -1;
    }
  },

  _ensure(idx, role) {
    if (idx >= 0 && this.items[idx]) return idx;
    this.items.push({ role: role, text: '' });
    return this.items.length - 1;
  },

  _loadHistory(threadMsg) {
    this._resetChat();
    try {
      let obj = threadMsg;
      if (obj && obj.thread) obj = obj.thread;
      const turns = obj && obj.turns;
      if (Array.isArray(turns)) for (const t of turns) this._collect(t);
    } catch (e) {}
  },

  _collect(node) {
    if (node && typeof node === 'object' && !Array.isArray(node)) {
      switch (node.type) {
        case 'userMessage': this.items.push({ role: 'user', text: textOf(node) }); return;
        case 'agentMessage': this.items.push({ role: 'assistant', text: textOf(node) }); return;
        case 'fileChange': this.items.push({ role: 'file', text: '📝 ' + textOf(node, 'file change') }); return;
        case 'mcpToolCall': this.items.push({ role: 'tool', text: '🔧 ' + textOf(node, 'tool call') }); return;
        case 'imageGeneration': {
          // 桥会把大体积内嵌 base64 剥离为 resultPath(经 /file 懒加载);小程序
          // 暂以占位文本降级显示,带上 path 供后续接 /file 渲染。
          const b64 = node.result || '', cap = node.revisedPrompt || '';
          if (b64) this.items.push({ role: 'image', text: cap, imageB64: b64 });
          else if (node.resultPath) this.items.push({ role: 'media', text: '🖼️ 生成的图片', path: node.resultPath });
          else this.items.push({ role: 'media', text: '🖼️ 生成的图片（生成中…）' });
          return;
        }
        case 'image': {
          const url = node.image_url || node.imageUrl || '';
          if (url.indexOf('data:image') === 0 && url.indexOf(',') >= 0) this.items.push({ role: 'image', text: '', imageB64: url.split(',').pop() });
          else if (url.indexOf('/') === 0) this.items.push({ role: 'media', text: '🖼️ 图片', path: url });
          else this.items.push({ role: 'media', text: '🖼️ 图片' });
          return;
        }
      }
      for (const k in node) this._collect(node[k]);
    } else if (Array.isArray(node)) {
      for (const v of node) this._collect(v);
    }
  },

  // ---- 断线重连（退避，对齐 App）----
  _scheduleReconnect() {
    if (this._suppress || !this.token) return;
    this._clearReconnect();
    const ms = Math.min(8000, Math.max(500, 500 * (1 << this._reconnectAttempts)));
    if (this._reconnectAttempts < 5) this._reconnectAttempts++;
    this._reconnectTimer = setTimeout(() => {
      if (!this._suppress && this.state !== 'connected') this.connect();
    }, ms);
  },
  _clearReconnect() { if (this._reconnectTimer) { clearTimeout(this._reconnectTimer); this._reconnectTimer = null; } },
};

// ---- 纯函数辅助 ----
function normalizeSession(j) {
  j = j || {};
  const name = j.name || '', preview = j.preview || '', id = j.id || '';
  return {
    id: id, name: name, preview: preview, cwd: j.cwd || '', source: j.source || '',
    updatedAt: j.updatedAt || 0, title: name || preview || id,
  };
}
function approvalCommand(params) {
  const c = params.command;
  if (Array.isArray(c)) return c.join(' ');
  if (typeof c === 'string') return c;
  try { return JSON.stringify(params, null, 2); } catch (e) { return ''; }
}
function textOf(node, fallback) {
  fallback = fallback || '';
  let buf = '';
  (function walk(n, d) {
    if (d > 4) return;
    if (n && typeof n === 'object' && !Array.isArray(n)) {
      if (typeof n.text === 'string') buf += n.text;
      for (const k in n) { if (k !== 'text') walk(n[k], d + 1); }
    } else if (Array.isArray(n)) { for (const e of n) walk(e, d + 1); }
  })(node, 0);
  const s = buf.trim();
  return s || fallback;
}

module.exports = relay;

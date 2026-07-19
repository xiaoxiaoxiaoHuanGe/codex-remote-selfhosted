// Caret 小程序轻量版 —— 全局入口。
// 把单例中继客户端挂到 App 上，各页面用 getApp().relay 访问同一份状态。
const relay = require('./utils/relay.js');

App({
  relay: relay,
  onLaunch() {
    // 前台为主：进入时若已配对则交由首页自动连接（见 pages/connect）。
  },
});

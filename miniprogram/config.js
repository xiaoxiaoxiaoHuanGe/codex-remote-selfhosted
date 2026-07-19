// 与原生 App 保持一致的中继配置。
// 生产环境需把 RELAY_HOST 加入小程序后台的「socket 合法域名」(wss) 与
// 「request 合法域名」(https，用于 /file 图片)。开发期可在开发者工具里
// 勾选「不校验合法域名」(project.config.json 已设 urlCheck:false)。
const RELAY_HOST = 'relay.example.com';

function wsUrlForToken(token) {
  return 'wss://' + RELAY_HOST + '/ws?token=' + (token || '').trim();
}

// 桥的媒体端点：手机无法直接访问桌面文件，经中继按 token 取图。
function fileUrl(path, token) {
  return 'https://' + RELAY_HOST + '/file?path=' +
    encodeURIComponent(path || '') + '&token=' + (token || '').trim();
}

// 二维码里是完整 wss URL（含 ?token=），扫出来取 token；也兼容直接粘贴裸 token。
function tokenFromText(t) {
  if (!t) return '';
  t = String(t).trim();
  const m = t.match(/[?&]token=([^&\s]+)/);
  if (m) {
    try { return decodeURIComponent(m[1]); } catch (e) { return m[1]; }
  }
  return t;
}

module.exports = { RELAY_HOST, wsUrlForToken, fileUrl, tokenFromText };

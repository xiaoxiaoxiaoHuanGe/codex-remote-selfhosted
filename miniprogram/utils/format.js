// format.js —— 列表展示用的纯函数：相对时间、路径取末层、项目/对话两段式分组。
// 分组规则对齐 App 的 sessions_screen（简化版：暂未做同名文件夹消歧，后续可补）。

function relTime(ms) {
  if (!ms) return '';
  let s = Math.floor((Date.now() - ms) / 1000);
  if (s < 0) s = 0;
  if (s < 60) return '刚刚';
  const m = Math.floor(s / 60); if (m < 60) return m + ' 分钟前';
  const h = Math.floor(m / 60); if (h < 24) return h + ' 小时前';
  const d = Math.floor(h / 24); if (d < 7) return d + ' 天前';
  const w = Math.floor(d / 7); if (w < 5) return w + ' 周前';
  return Math.floor(d / 30) + ' 个月前';
}

function baseName(cwd) {
  if (!cwd) return '';
  const parts = String(cwd).split(/[\/\\]/).filter(Boolean);
  return parts.length ? parts[parts.length - 1] : cwd;
}

// 「对话」= 一次性/临时目录（Codex 草稿目录、临时目录、家目录根、盘符根等）。
function isLoose(cwd) {
  if (!cwd) return true;
  const c = String(cwd), lower = c.toLowerCase();
  const segs = c.split(/[\/\\]/).filter(Boolean);
  for (const s of segs) { if (s.toLowerCase() === 'codex') return true; }
  if (lower.indexOf('/tmp/') >= 0 || lower === '/tmp') return true;
  if (lower.indexOf('/private/var') === 0 || lower.indexOf('/var/folders') === 0) return true;
  if (lower.indexOf('\\temp\\') >= 0 || lower.indexOf('/temp/') >= 0) return true;
  if (/^[a-z]:\\windows/i.test(c)) return true;
  if (/^\/users\/[^\/]+\/?$/i.test(lower)) return true;     // /Users/x（无子目录）
  if (/^\/home\/[^\/]+\/?$/i.test(lower)) return true;      // /home/x
  if (/^[a-z]:\\users\\[^\\]+\\?$/i.test(c)) return true;   // C:\Users\x
  if (c === '/' || /^[a-z]:\\?$/i.test(c)) return true;     // 根
  return false;
}

// 返回 { projects:[{cwd,label,items,updatedAt}], loose:[...] }
function groupSessions(sessions) {
  const map = {}, order = [], loose = [];
  for (const s of sessions || []) {
    if (isLoose(s.cwd)) { loose.push(s); continue; }
    const key = s.cwd;
    if (!map[key]) { map[key] = { cwd: key, label: baseName(key), items: [], updatedAt: 0 }; order.push(key); }
    map[key].items.push(s);
    if (s.updatedAt > map[key].updatedAt) map[key].updatedAt = s.updatedAt;
  }
  const projects = order.map((k) => map[k]);
  projects.forEach((g) => g.items.sort((a, b) => b.updatedAt - a.updatedAt));
  projects.sort((a, b) => b.updatedAt - a.updatedAt);
  loose.sort((a, b) => b.updatedAt - a.updatedAt);
  return { projects: projects, loose: loose };
}

module.exports = { relTime, baseName, isLoose, groupSessions };

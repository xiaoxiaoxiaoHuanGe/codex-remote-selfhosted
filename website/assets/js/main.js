// Caret portal — progressive enhancement only; page reads fine without JS.
(function () {
  'use strict';

  var reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  /* ── nav scrolled state ── */
  var nav = document.querySelector('[data-nav]');
  if (nav) {
    var onScroll = function () { nav.classList.toggle('scrolled', window.scrollY > 10); };
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });
  }

  /* ── mobile menu ── */
  var burger = document.querySelector('[data-burger]');
  var mob = document.querySelector('[data-mobmenu]');
  if (burger && mob) {
    burger.addEventListener('click', function () {
      var open = mob.classList.toggle('open');
      burger.setAttribute('aria-expanded', String(open));
    });
    mob.addEventListener('click', function (e) {
      if (e.target.tagName === 'A') { mob.classList.remove('open'); burger.setAttribute('aria-expanded', 'false'); }
    });
  }

  /* ── scroll reveal ── */
  var rvs = document.querySelectorAll('.rv');
  if ('IntersectionObserver' in window && !reduced) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (en.isIntersecting) { en.target.classList.add('in'); io.unobserve(en.target); }
      });
    }, { threshold: 0.12, rootMargin: '0px 0px -40px' });
    rvs.forEach(function (el) { io.observe(el); });
  } else {
    rvs.forEach(function (el) { el.classList.add('in'); });
  }

  /* ── install tabs ── */
  document.querySelectorAll('[data-tabs]').forEach(function (root) {
    root.querySelectorAll('.tab').forEach(function (btn) {
      btn.addEventListener('click', function () {
        root.querySelectorAll('.tab').forEach(function (b) { b.classList.remove('on'); b.setAttribute('aria-selected', 'false'); });
        root.querySelectorAll('.tab-pane').forEach(function (p) { p.classList.remove('on'); });
        btn.classList.add('on');
        btn.setAttribute('aria-selected', 'true');
        var pane = root.querySelector('[data-pane="' + btn.dataset.tab + '"]');
        if (pane) pane.classList.add('on');
      });
    });
  });

  /* ── copy buttons ── */
  document.querySelectorAll('[data-copy]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var done = function () {
        btn.classList.add('done'); btn.textContent = '已复制';
        setTimeout(function () { btn.classList.remove('done'); btn.textContent = '复制'; }, 1800);
      };
      if (navigator.clipboard) navigator.clipboard.writeText(btn.dataset.copy).then(done);
    });
  });

  /* ── footer year ── */
  var y = document.querySelector('[data-year]');
  if (y) y.textContent = String(new Date().getFullYear());

  /* ════════ hero replay：手机 ⇄ 桌面 双窗口回放 ════════ */
  var macEl = document.getElementById('replay-mac');
  var phEl = document.getElementById('replay-phone');
  if (!macEl || !phEl) return;

  // 每步：面板、HTML、与上一步的间隔(ms)。tap 步骤给审批按钮加点击态。
  var SCRIPT = [
    { ph: '<div class="bub bub-user">把登录页改成深色模式，改完跑一遍测试</div>', t: 600 },
    { mac: '<div class="ln"><span class="p">$</span> codex <span class="cursor"></span></div>', t: 700 },
    { mac: '<div class="ln think">▌正在读取 src/pages/Login.tsx…</div>', t: 1100 },
    { mac: '<div class="ln think">▌计划：引入 dark token，替换 6 处硬编码色值</div>', t: 1200 },
    { ph: '<div class="bub bub-sys">正在分析 Login.tsx —— 计划引入 dark token…</div>', t: 900 },
    { mac: '<div class="ln del">- background: #ffffff;</div>', t: 450 },
    { mac: '<div class="ln add">+ background: var(--surface-dark);</div>', t: 450 },
    { mac: '<div class="ln add">+ color: var(--text-on-dark);</div>', t: 900 },
    { ph: '<div class="bub bub-appr"><div class="ap-t">请求批准 · 执行命令</div><code>npm test</code><div class="ap-btns"><span class="ap-btn yes" data-tap>允许</span><span class="ap-btn">拒绝</span></div></div>', t: 1400, tap: true },
    { mac: '<div class="ln"><span class="p">▶</span> npm test</div>', t: 800 },
    { mac: '<div class="ln ok">✓ 12 passed, 0 failed (3.2s)</div>', t: 900 },
    { ph: '<div class="bub bub-done">✓ 已完成 —— 深色模式上线，12 项测试全部通过</div>', t: 2600 }
  ];

  function renderAll() { // reduced-motion / 兜底：直接静态铺满终态
    SCRIPT.forEach(function (s) {
      if (s.mac) macEl.insertAdjacentHTML('beforeend', s.mac);
      if (s.ph) phEl.insertAdjacentHTML('beforeend', s.ph);
    });
    var tapBtn = phEl.querySelector('[data-tap]');
    if (tapBtn) tapBtn.classList.add('tapped');
    var cur = macEl.querySelector('.cursor');
    if (cur) cur.remove();
  }

  if (reduced) { renderAll(); return; }

  var i = 0, timer = null, started = false;

  function step() {
    if (i >= SCRIPT.length) { // 一轮播完，停 2.4s 重来
      timer = setTimeout(function () { macEl.innerHTML = ''; phEl.innerHTML = ''; i = 0; step(); }, 2400);
      return;
    }
    var s = SCRIPT[i++];
    var oldCur = macEl.querySelector('.cursor');
    if (s.mac && oldCur) oldCur.remove();
    if (s.mac) macEl.insertAdjacentHTML('beforeend', s.mac);
    if (s.ph) phEl.insertAdjacentHTML('beforeend', s.ph);
    if (s.tap) {
      setTimeout(function () {
        var b = phEl.querySelector('[data-tap]:not(.tapped)');
        if (b) b.classList.add('tapped');
      }, 850);
    }
    // 面板内容超高时自顶裁剪（保持最新内容可见）
    [macEl, phEl].forEach(function (el) {
      while (el.scrollHeight > el.clientHeight + 4 && el.children.length > 1) el.removeChild(el.firstChild);
    });
    timer = setTimeout(step, s.t);
  }

  // 进入视口才开播，离开即暂停（省电）
  var replay = document.querySelector('.replay');
  if ('IntersectionObserver' in window && replay) {
    new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (en.isIntersecting && !started) { started = true; setTimeout(step, 800); }
        else if (!en.isIntersecting && started && timer) { clearTimeout(timer); started = false; macEl.innerHTML = ''; phEl.innerHTML = ''; i = 0; }
      });
    }, { threshold: 0.25 }).observe(replay);
  } else {
    setTimeout(step, 800);
  }
})();

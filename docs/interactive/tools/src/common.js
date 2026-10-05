/* Shared helpers: theme toggle, step-through diagrams, cast player. No dependencies. */
(function () {
  'use strict';
  var RM = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  function h(tag, attrs, kids) {
    var e = document.createElement(tag);
    if (attrs) for (var k in attrs) {
      if (k === 'text') e.textContent = attrs[k];
      else if (k === 'class') e.className = attrs[k];
      else e.setAttribute(k, attrs[k]);
    }
    (kids || []).forEach(function (c) { e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
    return e;
  }

  /* ---------- theme ---------- */
  function initTheme() {
    var btn = document.getElementById('theme-toggle');
    var saved = null;
    try { saved = localStorage.getItem('rk-theme'); } catch (e) {}
    if (saved) document.documentElement.setAttribute('data-theme', saved);
    function cur() {
      var t = document.documentElement.getAttribute('data-theme');
      if (t) return t;
      return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
    }
    function label() { if (btn) btn.textContent = cur() === 'dark' ? 'Light mode' : 'Dark mode'; }
    if (btn) btn.addEventListener('click', function () {
      var n = cur() === 'dark' ? 'light' : 'dark';
      document.documentElement.setAttribute('data-theme', n);
      try { localStorage.setItem('rk-theme', n); } catch (e) {}
      label();
    });
    label();
  }

  /* ---------- step-through diagram ---------- */
  function Stepper(root, cfg) {
    var actors = cfg.actors, scen = cfg.scenarios, order = Object.keys(scen);
    var sid = order[0], idx = -1, timer = null, speed = 1;
    var tabs = h('div', { class: 'tabs', role: 'tablist', 'aria-label': 'Scenario' });
    var title = h('p', { class: 'caption' });
    var actorBox = h('div', { class: 'actors' });
    var steps = h('ol', { class: 'steps', 'aria-live': 'polite' });
    var state = h('div', { class: 'state', 'aria-label': 'State' });
    var bReset = h('button', { text: 'Reset', type: 'button' });
    var bPrev = h('button', { text: 'Back', type: 'button' });
    var bPlay = h('button', { text: 'Play', type: 'button', class: 'primary' });
    var bNext = h('button', { text: 'Step', type: 'button' });
    var sp = h('select', { 'aria-label': 'Speed' }, [h('option', { value: '1', text: '1x' }), h('option', { value: '2', text: '2x' }), h('option', { value: '0.5', text: '0.5x' })]);
    var pos = h('span', { class: 'small' });
    var controls = h('div', { class: 'controls' }, [bReset, bPrev, bPlay, bNext, sp, pos]);
    root.appendChild(tabs); root.appendChild(title); root.appendChild(actorBox);
    root.appendChild(controls); root.appendChild(steps); root.appendChild(state);
    var actorEls = {};
    actors.forEach(function (a) {
      var e = h('div', { class: 'actor' }, [h('b', { text: a.label }), h('small', { text: a.sub || '' })]);
      actorEls[a.id] = e; actorBox.appendChild(e);
    });
    var tabEls = {};
    function buildTabs() {
      tabs.innerHTML = ''; tabEls = {};
      order.forEach(function (k) {
        var b = h('button', { type: 'button', role: 'tab', text: scen[k].tab, 'aria-selected': 'false' });
        b.addEventListener('click', function () { choose(k); });
        tabEls[k] = b; tabs.appendChild(b);
      });
      tabs.style.display = order.length > 1 ? '' : 'none';
    }
    buildTabs();
    function S() { return scen[sid].steps; }
    function stopTimer() { if (timer) { clearInterval(timer); timer = null; } bPlay.textContent = 'Play'; }
    function render() {
      steps.innerHTML = ''; state.innerHTML = '';
      Object.keys(actorEls).forEach(function (k) { actorEls[k].className = 'actor'; });
      var merged = {};
      for (var i = 0; i <= idx; i++) {
        var s = S()[i];
        var li = h('li', { class: (s.tone || '') + (i === idx ? ' current' : '') }, [
          h('div', { class: 'route', text: (actorName(s.from) + (s.to ? ' -> ' + actorName(s.to) : '')) }),
          h('div', { class: 't', text: s.title }),
          h('div', { class: 'd', text: s.text || '' })
        ]);
        steps.appendChild(li);
        if (s.state) for (var k in s.state) merged[k] = s.state[k];
      }
      if (idx >= 0) {
        var c = S()[idx];
        if (c.from && actorEls[c.from]) actorEls[c.from].classList.add('from');
        if (c.to && actorEls[c.to]) actorEls[c.to].classList.add('to');
        var last = steps.lastChild; if (last && last.scrollIntoView && !RM) last.scrollIntoView({ block: 'nearest' });
      }
      Object.keys(merged).forEach(function (k) {
        state.appendChild(h('span', {}, [h('b', { text: k + ': ' }), String(merged[k])]));
      });
      pos.textContent = (idx + 1) + ' / ' + S().length;
      bPrev.disabled = idx < 0; bNext.disabled = idx >= S().length - 1;
      order.forEach(function (k) { tabEls[k].setAttribute('aria-selected', k === sid ? 'true' : 'false'); tabEls[k].classList.toggle('on', k === sid); });
      title.textContent = scen[sid].title;
    }
    function actorName(id) { var a = actors.filter(function (x) { return x.id === id; })[0]; return a ? a.label : (id || ''); }
    function choose(k) { stopTimer(); sid = k; idx = -1; render(); }
    function next() { if (idx < S().length - 1) { idx++; render(); } else stopTimer(); }
    bNext.addEventListener('click', function () { stopTimer(); next(); });
    bPrev.addEventListener('click', function () { stopTimer(); if (idx >= 0) { idx--; render(); } });
    bReset.addEventListener('click', function () { stopTimer(); idx = -1; render(); });
    sp.addEventListener('change', function () { speed = parseFloat(sp.value); if (timer) { stopTimer(); play(); } });
    function play() {
      if (idx >= S().length - 1) idx = -1;
      bPlay.textContent = 'Pause';
      next();
      timer = setInterval(function () { if (idx >= S().length - 1) stopTimer(); else next(); }, 1800 / speed);
    }
    bPlay.addEventListener('click', function () { if (timer) stopTimer(); else play(); });
    function load(newScen) { stopTimer(); scen = newScen; order = Object.keys(scen); sid = order[0]; idx = -1; buildTabs(); render(); }
    render();
    return { choose: choose, next: next, load: load, last: function () { stopTimer(); idx = S().length - 1; render(); }, state: function () { return { scenario: sid, index: idx }; } };
  }

  /* ---------- cast player ---------- */
  function ansi(text, st) {
    // Tiny SGR parser: 0 reset, 1 bold, 2 dim, 31 red, 32 green, 36 cyan.
    var out = '', re = /\x1b\[([0-9;]*)m/g, last = 0, m;
    function esc(s) { return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'); }
    function open() {
      var c = [];
      if (st.b) c.push('bld'); if (st.d) c.push('dim'); if (st.c) c.push(st.c);
      return c.length ? '<span class="' + c.join(' ') + '">' : '';
    }
    var cur = '';
    function emit(s) { if (!s) return; var o = open(); out += o + esc(s) + (o ? '</span>' : ''); }
    while ((m = re.exec(text))) {
      emit(text.slice(last, m.index)); last = re.lastIndex;
      (m[1] || '0').split(';').forEach(function (n) {
        n = parseInt(n, 10);
        if (n === 0) { st.b = st.d = false; st.c = ''; }
        else if (n === 1) st.b = true;
        else if (n === 2) st.d = true;
        else if (n === 31) st.c = 'red';
        else if (n === 32) st.c = 'grn';
        else if (n === 36) st.c = 'cyn';
      });
    }
    emit(text.slice(last));
    return out;
  }

  function CastPlayer(root, cast) {
    var ev = cast.events, n = ev.length, idx = 0, timer = null, speed = 1, dragging = false;
    // A "chapter" starts at a note line ("# ...") or at a prompt that no note introduces.
    var chap = [];
    ev.forEach(function (e, i) {
      var t = e[2], isNote = t.indexOf('\x1b[2m# ') === 0, isPrompt = t.indexOf('\x1b[32m$ ') === 0;
      var prevNote = i > 0 && ev[i - 1][2].indexOf('\x1b[2m# ') === 0;
      if (isNote || (isPrompt && !prevNote)) chap.push(i);
    });
    var term = h('pre', { class: 'term', tabindex: '0', role: 'log', 'aria-label': 'Terminal recording: ' + (cast.title || '') });
    var bRestart = h('button', { type: 'button', text: 'Restart' });
    var bPrev = h('button', { type: 'button', text: 'Previous command' });
    var bPlay = h('button', { type: 'button', text: 'Play', class: 'primary' });
    var bNext = h('button', { type: 'button', text: 'Next command' });
    var sp = h('select', { 'aria-label': 'Playback speed' }, [h('option', { value: '1', text: '1x' }), h('option', { value: '2', text: '2x' }), h('option', { value: '4', text: '4x' })]);
    var rng = h('input', { type: 'range', min: '0', max: String(n), value: '0', 'aria-label': 'Position' });
    var tl = h('span', { class: 'small' });
    root.appendChild(term);
    root.appendChild(h('div', { class: 'controls' }, [bRestart, bPrev, bPlay, bNext, sp]));
    root.appendChild(h('div', { class: 'controls' }, [rng, tl]));

    function render() {
      var st = { b: false, d: false, c: '' }, html = '';
      for (var i = 0; i < idx; i++) html += ansi(ev[i][2], st);
      term.innerHTML = html + '<span class="cur">&nbsp;</span>';
      term.scrollTop = term.scrollHeight;
      rng.value = String(idx);
      var t = idx > 0 ? ev[Math.min(idx, n) - 1][0] : 0;
      tl.textContent = t.toFixed(1) + ' s of ' + ev[n - 1][0].toFixed(1) + ' s (pauses shortened)';
      bPrev.disabled = idx <= 0; bNext.disabled = idx >= n;
    }
    function stop() { if (timer) { clearTimeout(timer); timer = null; } bPlay.textContent = 'Play'; }
    function tick() {
      if (idx >= n) { stop(); return; }
      var prev = idx > 0 ? ev[idx - 1][0] : 0;
      var gap = Math.min(ev[idx][0] - prev, 1.5);
      var wait = RM ? 0 : gap * 1000 / speed;
      timer = setTimeout(function () { idx++; render(); tick(); }, wait);
    }
    function play() { if (idx >= n) idx = 0; bPlay.textContent = 'Pause'; tick(); }
    bPlay.addEventListener('click', function () { if (timer) stop(); else play(); });
    bRestart.addEventListener('click', function () { stop(); idx = 0; render(); });
    bNext.addEventListener('click', function () {
      stop();
      var target = n;
      for (var i = 0; i < chap.length; i++) if (chap[i] > idx) { target = chap[i]; break; }
      idx = target; render();
    });
    bPrev.addEventListener('click', function () {
      stop();
      var t = 0;
      for (var i = 0; i < chap.length; i++) if (chap[i] < idx) t = chap[i];
      idx = t; render();
    });
    sp.addEventListener('change', function () { speed = parseFloat(sp.value); if (timer) { stop(); play(); } });
    rng.addEventListener('input', function () { stop(); idx = parseInt(rng.value, 10); render(); });
    render();
    return { state: function () { return { index: idx, total: n }; }, seek: function (i) { stop(); idx = i; render(); }, play: play, stop: stop };
  }

  function loadCast(id) { return JSON.parse(document.getElementById(id).textContent); }

  window.RK = { h: h, Stepper: Stepper, CastPlayer: CastPlayer, loadCast: loadCast, initTheme: initTheme, reducedMotion: RM };
  document.addEventListener('DOMContentLoaded', initTheme);
})();

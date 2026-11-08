/* Payments Engineering from Scratch — site behaviour. No dependencies except the pinned
   highlight.js and Mermaid scripts the page may load. Everything the learner does
   (language choice, completed lessons, theme) is stored in localStorage only. */
(function () {
  'use strict';

  var KEYS = { lang: 'pefs:lang', done: 'pefs:done', theme: 'pefs:theme' };
  var LANGS = ['java', 'python', 'go'];

  function store(key, value) {
    try { localStorage.setItem(key, value); } catch (e) { /* private mode etc. */ }
  }
  function load(key) {
    try { return localStorage.getItem(key); } catch (e) { return null; }
  }
  function loadDone() {
    try { return JSON.parse(load(KEYS.done) || '{}') || {}; } catch (e) { return {}; }
  }
  function saveDone(done) { store(KEYS.done, JSON.stringify(done)); }
  function $(sel, root) { return (root || document).querySelector(sel); }
  function $$(sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); }

  /* ---------------- theme ---------------- */
  function effectiveTheme() {
    var forced = document.documentElement.getAttribute('data-theme');
    if (forced) return forced;
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  function applyHljsTheme() {
    var theme = effectiveTheme();
    $$('link[data-hljs-theme]').forEach(function (link) {
      link.media = link.getAttribute('data-hljs-theme') === theme ? 'all' : 'not all';
    });
  }
  function initTheme() {
    var btn = $('.theme-toggle');
    applyHljsTheme();
    if (!btn) return;
    btn.addEventListener('click', function () {
      var next = effectiveTheme() === 'dark' ? 'light' : 'dark';
      document.documentElement.setAttribute('data-theme', next);
      store(KEYS.theme, next);
      applyHljsTheme();
      renderMermaid(true);
    });
  }

  /* ---------------- language ---------------- */
  function currentLang() {
    var lang = load(KEYS.lang);
    return LANGS.indexOf(lang) >= 0 ? lang : 'java';
  }
  function applyLang(lang, scrollTarget) {
    $$('.lang-pick button, .lang-card').forEach(function (b) {
      b.setAttribute('aria-pressed', b.getAttribute('data-lang') === lang ? 'true' : 'false');
    });
    $$('.code-tabs').forEach(function (section) {
      var available = $$('.tabs [role="tab"]', section).map(function (t) { return t.getAttribute('data-lang'); });
      var pick = available.indexOf(lang) >= 0 ? lang : available[0];
      $$('.tabs [role="tab"]', section).forEach(function (tab) {
        tab.setAttribute('aria-selected', tab.getAttribute('data-lang') === pick ? 'true' : 'false');
      });
      $$('.code-panel', section).forEach(function (panel) {
        panel.hidden = panel.getAttribute('data-lang') !== pick;
      });
    });
    highlightVisible();
  }
  function initLang() {
    applyLang(currentLang());
    document.addEventListener('click', function (event) {
      var btn = event.target.closest('[data-lang]');
      if (!btn || !(btn.matches('.lang-pick button') || btn.matches('.lang-card') || btn.matches('.tabs [role="tab"]'))) return;
      var lang = btn.getAttribute('data-lang');
      store(KEYS.lang, lang);
      applyLang(lang);
    });
  }

  /* ---------------- highlighting ---------------- */
  var highlighted = typeof WeakSet === 'function' ? new WeakSet() : null;
  function highlightVisible() {
    if (!window.hljs) return;
    $$('pre code[class*="language-"]').forEach(function (block) {
      if (highlighted && highlighted.has(block)) return;
      if (block.closest('[hidden]')) return;
      try { window.hljs.highlightElement(block); } catch (e) { /* unknown language */ }
      if (highlighted) highlighted.add(block);
    });
  }
  function initCopyButtons() {
    document.addEventListener('click', function (event) {
      var btn = event.target.closest('.copy-btn');
      if (!btn) return;
      var code = $('pre', btn.closest('.codeblock'));
      if (!code) return;
      var text = code.textContent;
      var done = function () { btn.textContent = 'Copied'; setTimeout(function () { btn.textContent = 'Copy'; }, 1500); };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, function () { fallbackCopy(text); done(); });
      } else { fallbackCopy(text); done(); }
    });
  }
  function fallbackCopy(text) {
    var area = document.createElement('textarea');
    area.value = text; area.setAttribute('readonly', ''); area.style.position = 'fixed'; area.style.left = '-9999px';
    document.body.appendChild(area); area.select();
    try { document.execCommand('copy'); } catch (e) { /* ignore */ }
    document.body.removeChild(area);
  }

  /* ---------------- mermaid ---------------- */
  var mermaidSources = null;
  function renderMermaid(rerender) {
    if (!window.mermaid) return;
    var nodes = $$('pre.mermaid');
    if (!nodes.length) return;
    if (!mermaidSources) mermaidSources = nodes.map(function (n) { return n.textContent; });
    if (rerender) {
      nodes.forEach(function (n, i) { n.removeAttribute('data-processed'); n.innerHTML = ''; n.textContent = mermaidSources[i]; });
    }
    try {
      window.mermaid.initialize({ startOnLoad: false, theme: effectiveTheme() === 'dark' ? 'dark' : 'default', securityLevel: 'strict' });
      var run = window.mermaid.run ? window.mermaid.run({ nodes: nodes }) : window.mermaid.init(undefined, nodes);
      if (run && run.catch) run.catch(function (err) { console.warn('mermaid', err); });
    } catch (err) { console.warn('mermaid', err); }
  }

  /* ---------------- progress ---------------- */
  function refreshProgress() {
    var done = loadDone();
    $$('[data-lesson]').forEach(function (el) {
      var id = el.getAttribute('data-lesson');
      var isDone = !!done[id];
      el.classList.toggle('is-done', isDone);
      if (el.matches('.mark-complete')) {
        el.setAttribute('aria-pressed', isDone ? 'true' : 'false');
        el.textContent = isDone ? 'Completed' : 'Mark complete';
      }
      var when = $('.done-when', el);
      if (when) when.textContent = isDone && done[id] > 1 ? new Date(done[id]).toLocaleDateString() : '';
    });
    var total = Object.keys(done).length;
    $$('[data-progress]').forEach(function (bar) {
      var scope = bar.getAttribute('data-progress');
      var count, denom;
      if (scope === 'all') {
        denom = parseInt(bar.getAttribute('data-total'), 10) || 0;
        count = total;
      } else {
        var container = document.querySelector('[data-progress-scope="' + scope + '"]');
        var items = container ? $$('[data-lesson]', container).filter(function (el) { return !el.matches('.mark-complete'); }) : [];
        denom = items.length;
        count = items.filter(function (el) { return done[el.getAttribute('data-lesson')]; }).length;
      }
      var pct = denom ? Math.round(100 * count / denom) : 0;
      var fill = $('.progress-bar', bar);
      if (fill) fill.style.width = pct + '%';
      bar.setAttribute('role', 'progressbar');
      bar.setAttribute('aria-valuenow', String(pct));
      bar.setAttribute('aria-valuemin', '0');
      bar.setAttribute('aria-valuemax', '100');
      $$('[data-progress-text="' + scope + '"]').forEach(function (t) { t.textContent = count + ' of ' + denom; });
      $$('[data-progress-percent="' + scope + '"]').forEach(function (t) { t.textContent = pct + '%'; });
    });
  }
  function initProgress() {
    refreshProgress();
    document.addEventListener('click', function (event) {
      var btn = event.target.closest('.mark-complete');
      if (!btn) return;
      var id = btn.getAttribute('data-lesson');
      var done = loadDone();
      if (done[id]) delete done[id]; else done[id] = Date.now();
      saveDone(done);
      refreshProgress();
    });
    var exportBtn = $('#export-progress'), importBtn = $('#import-progress'), resetBtn = $('#reset-progress');
    if (exportBtn) exportBtn.addEventListener('click', function () {
      var text = JSON.stringify(loadDone());
      if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(text); else fallbackCopy(text);
      exportBtn.textContent = 'Copied'; setTimeout(function () { exportBtn.textContent = 'Copy progress as JSON'; }, 1500);
    });
    if (importBtn) importBtn.addEventListener('click', function () {
      var text = window.prompt('Paste the progress JSON you copied from another browser:');
      if (!text) return;
      try {
        var parsed = JSON.parse(text);
        if (typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('not an object');
        var merged = loadDone();
        Object.keys(parsed).forEach(function (k) { if (/^[\w-]+\/[\w-]+$/.test(k)) merged[k] = parsed[k] || Date.now(); });
        saveDone(merged); refreshProgress();
      } catch (e) { window.alert('That did not look like progress JSON.'); }
    });
    if (resetBtn) resetBtn.addEventListener('click', function () {
      if (window.confirm('Remove every completed-lesson mark from this browser?')) { saveDone({}); refreshProgress(); }
    });
    window.addEventListener('storage', function (event) { if (event.key === KEYS.done) refreshProgress(); });
  }

  /* ---------------- quiz ---------------- */
  var STAGE_LABEL = { pre: 'Pre-check', check: 'Check', post: 'Post-lesson' };
  function initQuiz() {
    var holder = $('.quiz-body');
    var data = $('#quiz-data');
    if (!holder || !data) return;
    var questions;
    try { questions = JSON.parse(data.textContent); } catch (e) { return; }
    var form = document.createElement('form');
    form.addEventListener('submit', function (e) { e.preventDefault(); });
    questions.forEach(function (q, qi) {
      var box = document.createElement('fieldset');
      box.className = 'quiz-q';
      var stage = q.stage || 'check';
      var legend = document.createElement('legend');
      legend.className = 'visually-hidden';
      legend.textContent = 'Question ' + (qi + 1);
      box.appendChild(legend);
      var badge = document.createElement('span');
      badge.className = 'badge stage type-' + (stage === 'pre' ? 'learn' : stage === 'post' ? 'capstone' : 'build');
      badge.textContent = STAGE_LABEL[stage] || stage;
      box.appendChild(badge);
      var qp = document.createElement('p');
      qp.className = 'q';
      qp.textContent = (qi + 1) + '. ' + q.question;
      box.appendChild(qp);
      (q.options || []).forEach(function (opt, oi) {
        var label = document.createElement('label');
        var input = document.createElement('input');
        input.type = 'radio'; input.name = 'q' + qi; input.value = String(oi);
        label.appendChild(input);
        label.appendChild(document.createTextNode(opt));
        box.appendChild(label);
      });
      var explain = document.createElement('div');
      explain.className = 'explain'; explain.hidden = true;
      box.appendChild(explain);
      form.appendChild(box);
    });
    var actions = document.createElement('div');
    actions.className = 'quiz-actions';
    var check = document.createElement('button');
    check.type = 'button'; check.className = 'btn primary'; check.textContent = 'Check answers';
    var reset = document.createElement('button');
    reset.type = 'button'; reset.className = 'btn ghost'; reset.textContent = 'Reset';
    var score = document.createElement('span');
    score.className = 'quiz-score'; score.setAttribute('aria-live', 'polite');
    actions.appendChild(check); actions.appendChild(reset); actions.appendChild(score);
    form.appendChild(actions);
    holder.innerHTML = '';
    holder.appendChild(form);

    check.addEventListener('click', function () {
      var right = 0, answered = 0;
      questions.forEach(function (q, qi) {
        var box = form.children[qi];
        var chosen = form.querySelector('input[name="q' + qi + '"]:checked');
        var labels = $$('label', box);
        labels.forEach(function (l) { l.classList.remove('is-correct', 'is-wrong'); });
        box.classList.remove('correct', 'incorrect');
        var explain = $('.explain', box);
        if (!chosen) { explain.hidden = true; return; }
        answered++;
        var picked = parseInt(chosen.value, 10);
        var ok = picked === q.correct;
        if (ok) right++;
        box.classList.add(ok ? 'correct' : 'incorrect');
        if (labels[q.correct]) labels[q.correct].classList.add('is-correct');
        if (!ok && labels[picked]) labels[picked].classList.add('is-wrong');
        explain.textContent = (ok ? 'Correct. ' : 'Not quite. ') + (q.explanation || '');
        explain.hidden = false;
      });
      score.textContent = answered ? 'Score: ' + right + ' / ' + questions.length + (answered < questions.length ? ' (' + (questions.length - answered) + ' unanswered)' : '') : 'Pick an answer first.';
    });
    reset.addEventListener('click', function () {
      form.reset();
      $$('.quiz-q', form).forEach(function (box) {
        box.classList.remove('correct', 'incorrect');
        $$('label', box).forEach(function (l) { l.classList.remove('is-correct', 'is-wrong'); });
        $('.explain', box).hidden = true;
      });
      score.textContent = '';
    });
  }

  /* ---------------- glossary ---------------- */
  function initGlossary() {
    var input = $('#glossary-filter');
    if (!input) return;
    var terms = $$('.term');
    var letters = $$('.letter');
    var count = $('#glossary-count');
    var empty = $('.glossary-empty');
    var total = terms.length;
    function apply() {
      var q = input.value.trim().toLowerCase();
      var shown = 0;
      terms.forEach(function (t) {
        var hit = !q || t.getAttribute('data-term').indexOf(q) >= 0 || t.getAttribute('data-category').indexOf(q) >= 0 || t.textContent.toLowerCase().indexOf(q) >= 0;
        t.hidden = !hit;
        if (hit) shown++;
      });
      letters.forEach(function (l) { l.hidden = !$$('.term', l).some(function (t) { return !t.hidden; }); });
      if (count) count.textContent = q ? shown + ' of ' + total + ' terms' : total + ' terms';
      if (empty) empty.hidden = shown > 0;
    }
    input.addEventListener('input', apply);
    var initial = new URLSearchParams(location.search).get('q');
    if (initial) { input.value = initial; apply(); }
  }

  /* ---------------- search ---------------- */
  function tokenize(text) {
    return String(text || '').toLowerCase().replace(/[^\w\s-]/g, ' ').split(/[\s-]+/).filter(function (t) { return t.length > 1; });
  }
  function initSearch() {
    var input = $('#search-input');
    var results = $('#search-results');
    var status = $('#search-status');
    if (!input || !results) return;
    var index = window.PEFS_SEARCH_INDEX && window.PEFS_SEARCH_INDEX.lessons;
    if (!index) { status.textContent = 'The search index did not load.'; return; }
    var FIELDS = [['title', 10], ['terms', 6], ['hook', 4], ['headings', 3], ['phase', 2]];
    var docs = index.map(function (lesson) {
      var fields = {};
      FIELDS.forEach(function (f) {
        var value = lesson[f[0]];
        fields[f[0]] = tokenize(Array.isArray(value) ? value.join(' ') : value);
      });
      return { lesson: lesson, fields: fields };
    });
    function escapeHtml(s) { return String(s).replace(/[&<>"]/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]; }); }
    function mark(text, terms) {
      var out = escapeHtml(text);
      terms.forEach(function (t) {
        if (t.length < 2) return;
        out = out.replace(new RegExp('(^|[^\\w])(' + t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\w*)', 'gi'), '$1<mark>$2</mark>');
      });
      return out;
    }
    function run() {
      var terms = tokenize(input.value);
      results.innerHTML = '';
      if (!terms.length) { status.textContent = 'Type to search.'; return; }
      var scored = [];
      docs.forEach(function (doc) {
        var score = 0, matchedAll = true, where = [];
        terms.forEach(function (term) {
          var best = 0, bestField = null;
          FIELDS.forEach(function (f) {
            var hits = doc.fields[f[0]].filter(function (tok) { return tok.indexOf(term) === 0; }).length;
            if (hits) {
              var s = f[1] * (1 + Math.min(hits, 3) * 0.2);
              if (doc.fields[f[0]].indexOf(term) >= 0) s *= 1.5;
              if (s > best) { best = s; bestField = f[0]; }
            }
          });
          if (!best) matchedAll = false; else { score += best; if (where.indexOf(bestField) < 0) where.push(bestField); }
        });
        if (matchedAll) scored.push({ doc: doc, score: score, where: where });
      });
      scored.sort(function (a, b) { return b.score - a.score || a.doc.lesson.phaseNumber - b.doc.lesson.phaseNumber; });
      var top = scored.slice(0, 40);
      status.textContent = scored.length ? scored.length + ' lesson' + (scored.length === 1 ? '' : 's') + ' match' + (scored.length > 40 ? ' (showing 40)' : '') : 'No lessons match.';
      top.forEach(function (hit) {
        var l = hit.doc.lesson;
        var li = document.createElement('li');
        var matchedTerms = (l.terms || []).filter(function (t) { return terms.some(function (q) { return tokenize(t).some(function (tok) { return tok.indexOf(q) === 0; }); }); }).slice(0, 6);
        var matchedHeadings = (l.headings || []).filter(function (h) { return terms.some(function (q) { return tokenize(h).some(function (tok) { return tok.indexOf(q) === 0; }); }); }).slice(0, 4);
        li.innerHTML =
          '<div class="lesson-card-head"><a href="' + escapeHtml(l.url) + '">' + mark(l.title, terms) + '</a></div>' +
          '<div class="where">' + escapeHtml(l.phase) + ' · ' + escapeHtml(l.type) + ' · ~' + l.minutes + ' min</div>' +
          '<p class="hook">' + mark(l.hook, terms) + '</p>' +
          (matchedTerms.length ? '<p class="small">Key terms: ' + matchedTerms.map(function (t) { return '<span class="badge">' + mark(t, terms) + '</span>'; }).join(' ') + '</p>' : '') +
          (matchedHeadings.length ? '<p class="small muted">Sections: ' + matchedHeadings.map(function (h) { return mark(h, terms); }).join(' · ') + '</p>' : '');
        results.appendChild(li);
      });
    }
    input.addEventListener('input', run);
    var initial = new URLSearchParams(location.search).get('q');
    if (initial) { input.value = initial; }
    run();
  }

  /* ---------------- navigation & TOC ---------------- */
  function initNav() {
    var toggle = $('.nav-toggle');
    if (toggle) toggle.addEventListener('click', function () {
      var open = document.body.classList.toggle('nav-open');
      toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
    });
    document.addEventListener('click', function (event) {
      if (document.body.classList.contains('nav-open') && !event.target.closest('.sidebar, .site-nav, .nav-toggle')) {
        document.body.classList.remove('nav-open');
        if (toggle) toggle.setAttribute('aria-expanded', 'false');
      }
    });
    var here = $('.lesson-list li.here');
    if (here && here.scrollIntoView) {
      var inner = $('.sidebar-inner');
      if (inner) inner.scrollTop = Math.max(0, here.offsetTop - inner.clientHeight / 2);
    }
  }
  function initToc() {
    var links = $$('.toc .toc-list a');
    if (!links.length || !('IntersectionObserver' in window)) return;
    var map = {};
    links.forEach(function (a) { map[a.getAttribute('href').slice(1)] = a; });
    var targets = Object.keys(map).map(function (id) { return document.getElementById(id); }).filter(Boolean);
    var active = null;
    var observer = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (entry.isIntersecting) {
          if (active) active.classList.remove('active');
          active = map[entry.target.id];
          if (active) active.classList.add('active');
        }
      });
    }, { rootMargin: '-10% 0px -75% 0px' });
    targets.forEach(function (t) { observer.observe(t); });
  }

  function init() {
    initTheme();
    initNav();
    initLang();
    initCopyButtons();
    initProgress();
    initQuiz();
    initGlossary();
    initSearch();
    initToc();
    highlightVisible();
    renderMermaid(false);
    // CDN scripts are deferred and may finish after this file; try again when the window loads.
    window.addEventListener('load', function () { highlightVisible(); renderMermaid(false); });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init); else init();
})();

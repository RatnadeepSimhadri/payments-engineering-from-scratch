/* Payments Engineering from Scratch — site behaviour. No dependencies except the pinned
   highlight.js and Mermaid scripts the page may load. Everything the learner does
   (language choice, completed lessons, theme) is stored in localStorage only. */
(function () {
  'use strict';

  var KEYS = { lang: 'pefs:lang', done: 'pefs:done', theme: 'pefs:theme', quiz: 'pefs:quiz' };
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
  function loadQuiz() {
    try { return JSON.parse(load(KEYS.quiz) || '{}') || {}; } catch (e) { return {}; }
  }
  function saveQuiz(q) { store(KEYS.quiz, JSON.stringify(q)); }
  var LESSON_ID_RE = /^[\w-]+\/[\w-]+$/;
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
  var LANG_NAME = { java: 'Java', python: 'Python', go: 'Go' };
  function applyWalkthrough(lang) {
    var bar = $('[data-walk-switch]');
    var blocks = $$('[data-walkthrough]');
    if (!bar || !blocks.length) return;
    var langs = (bar.getAttribute('data-langs') || '').split(' ');
    var shown = langs.indexOf(lang) >= 0 ? lang : 'java';
    var all = bar.getAttribute('data-show-all') === 'true';
    blocks.forEach(function (b) { b.hidden = !all && b.getAttribute('data-walkthrough') !== shown; });
    var cur = $('.walk-current', bar); if (cur) cur.textContent = all ? 'all three languages' : (LANG_NAME[shown] || shown);
    $$('[data-lang]', bar).forEach(function (b) { b.hidden = all || b.getAttribute('data-lang') === shown; });
    var links = $('.walk-switch-links', bar); if (links) links.hidden = all;
    var allBtn = $('[data-walk-all]', bar);
    if (allBtn) { allBtn.textContent = all ? 'Show only ' + (LANG_NAME[shown] || shown) : 'Show all three'; allBtn.setAttribute('aria-pressed', all ? 'true' : 'false'); }
    /* keep the table of contents in step with what is visible */
    $$('.toc a[href^="#"]').forEach(function (a) {
      var target = document.getElementById(a.getAttribute('href').slice(1));
      var holder = target && target.closest('[data-walkthrough]');
      var li = a.closest('li');
      if (li) li.hidden = !!(holder && holder.hidden);
    });
  }
  function initWalkthrough() {
    document.addEventListener('click', function (event) {
      var allBtn = event.target.closest('[data-walk-all]');
      if (allBtn) {
        var bar = allBtn.closest('[data-walk-switch]');
        bar.setAttribute('data-show-all', bar.getAttribute('data-show-all') === 'true' ? 'false' : 'true');
        applyWalkthrough(currentLang());
      }
    });
  }
  function applyLang(lang, scrollTarget) {
    applyWalkthrough(lang);
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
      if (!btn || !(btn.matches('.lang-pick button') || btn.matches('.lang-card') || btn.matches('.walk-switch [data-lang]') || btn.matches('.tabs [role="tab"]'))) return;
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
    var quiz = loadQuiz();
    $$('[data-lesson]').forEach(function (el) {
      var id = el.getAttribute('data-lesson');
      var isDone = !!done[id];
      el.classList.toggle('is-done', isDone);
      if (el.matches('.mark-complete')) {
        el.setAttribute('aria-pressed', isDone ? 'true' : 'false');
        el.textContent = isDone ? 'Completed' : 'Mark complete';
      }
      var when = $('.done-when', el);
      if (when) {
        var q = quiz[id];
        when.textContent = (isDone && done[id] > 1 ? new Date(done[id]).toLocaleDateString() : '') + (q ? (isDone ? ' · ' : '') + 'quiz ' + q.score + '/' + q.total : '');
      }
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
    initSync();
    window.addEventListener('storage', function (event) { if (event.key === KEYS.done || event.key === KEYS.quiz) refreshProgress(); });
  }

  /* ---------------- progress sync: file export/import + LEARNING.md ---------------- */
  function exportPayload() {
    return { app: 'payments-engineering-from-scratch', version: 2, exported: new Date().toISOString(),
             lang: load(KEYS.lang) || null, done: loadDone(), quiz: loadQuiz() };
  }
  function normaliseImport(parsed) {
    /* Accepts v2 payloads and the v1 flat {id: timestamp} map. Returns {done, quiz, lang} or null. */
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null;
    var out = { done: {}, quiz: {}, lang: null };
    var src = (parsed.version === 2 && parsed.done && typeof parsed.done === 'object') ? parsed : { done: parsed };
    Object.keys(src.done || {}).forEach(function (k) { if (LESSON_ID_RE.test(k)) out.done[k] = Number(src.done[k]) || Date.now(); });
    Object.keys(src.quiz || {}).forEach(function (k) {
      var v = src.quiz[k];
      if (LESSON_ID_RE.test(k) && v && typeof v === 'object' && isFinite(v.score) && isFinite(v.total)) out.quiz[k] = { score: +v.score, total: +v.total, at: Number(v.at) || Date.now() };
    });
    if (['java', 'python', 'go'].indexOf(src.lang) >= 0) out.lang = src.lang;
    if (!Object.keys(out.done).length && !Object.keys(out.quiz).length) return null;
    return out;
  }
  function mergeImport(data) {
    var done = loadDone(), quiz = loadQuiz(), added = 0;
    Object.keys(data.done).forEach(function (k) { if (!done[k]) added++; done[k] = done[k] || data.done[k]; });
    Object.keys(data.quiz).forEach(function (k) { if (!quiz[k] || data.quiz[k].at > quiz[k].at) quiz[k] = data.quiz[k]; });
    saveDone(done); saveQuiz(quiz);
    if (data.lang && !load(KEYS.lang)) store(KEYS.lang, data.lang);
    refreshProgress();
    return added;
  }
  function downloadText(name, text, type) {
    var blob = new Blob([text], { type: type || 'application/json' });
    var a = document.createElement('a');
    a.href = URL.createObjectURL(blob); a.download = name;
    document.body.appendChild(a); a.click();
    setTimeout(function () { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
  }
  function readFile(input, cb) {
    var file = input.files && input.files[0];
    if (!file) return;
    var reader = new FileReader();
    reader.onload = function () { cb(String(reader.result || '')); input.value = ''; };
    reader.readAsText(file);
  }
  function lessonIndex() {
    var el = $('#lesson-index');
    if (!el) return [];
    try { return JSON.parse(el.textContent) || []; } catch (e) { return []; }
  }
  function parseLearningMd(text) {
    /* Finds lessons in a LEARNING.md (or LEDGER-/CONSENSUS-LEARNING.md): every row of the
       "Progress log" table and every phase marked Done in the "Path" table. Returns
       {done: {id: ts}, quiz: {id: {score,total,at}}, unknown: [strings]}. */
    var index = lessonIndex();
    var byId = {}, byNum = {}, bySlug = {}, byPhase = {};
    index.forEach(function (l) {
      byId[l.id] = l; byNum[l.p + '/' + l.n] = l;
      var parts = l.id.split('/'); bySlug[parts[1]] = l; bySlug[parts[0] + '/' + parts[1]] = l;
      (byPhase[l.p] = byPhase[l.p] || []).push(l);
    });
    function resolve(ref) {
      ref = ref.replace(/`/g, '').trim();
      if (!ref) return null;
      if (byId[ref]) return byId[ref];
      var m = ref.match(/^(?:phase\s*)?(\d{1,2})\s*[\/,-]?\s*(?:lesson\s*)?(\d{1,2})\b/i);
      if (m) { var hit = byNum[parseInt(m[1], 10) + '/' + parseInt(m[2], 10)]; if (hit) return hit; }
      m = ref.match(/([\w-]+)\/([\w-]+)/);
      if (m) {
        if (bySlug[m[1] + '/' + m[2]]) return bySlug[m[1] + '/' + m[2]];
        var pn = m[1].match(/^(\d{1,2})/), ln = m[2].match(/^(\d{1,2})/);
        if (pn && ln && byNum[parseInt(pn[1], 10) + '/' + parseInt(ln[1], 10)]) return byNum[parseInt(pn[1], 10) + '/' + parseInt(ln[1], 10)];
      }
      m = ref.match(/^\d{1,2}-[\w-]+$/);
      if (m && bySlug[ref]) return bySlug[ref];
      var low = ref.toLowerCase();
      var titleHit = index.filter(function (l) { return low === l.t.toLowerCase() || low === l.t.split(':')[0].toLowerCase(); });
      return titleHit.length === 1 ? titleHit[0] : null;
    }
    var out = { done: {}, quiz: {}, unknown: [] };
    var section = '';
    text.split(/\r?\n/).forEach(function (line) {
      var h = line.match(/^##\s+(.*)/);
      if (h) { section = h[1].trim().toLowerCase(); return; }
      if (!/^\|/.test(line) || /^\|\s*-/.test(line)) return;
      var cells = line.split('|').slice(1, -1).map(function (c) { return c.trim(); });
      if (section.indexOf('progress log') === 0) {
        if (cells.length < 2 || /^date$/i.test(cells[0])) return;
        var lesson = resolve(cells[1]);
        if (!lesson) { out.unknown.push(cells[1]); return; }
        var ts = Date.parse(cells[0]); if (!isFinite(ts)) ts = Date.now();
        out.done[lesson.id] = out.done[lesson.id] || ts;
        var q = (cells[3] || '').match(/(\d+)\s*\/\s*(\d+)/);
        if (q) out.quiz[lesson.id] = { score: +q[1], total: +q[2], at: ts };
      } else if (section.indexOf('path') === 0) {
        if (cells.length < 3 || /^phase$/i.test(cells[0])) return;
        if (!/^done$/i.test(cells[2])) return;
        var pn2 = (cells[0].match(/(\d{1,2})/) || [])[1];
        (byPhase[parseInt(pn2, 10)] || []).forEach(function (l) { out.done[l.id] = out.done[l.id] || Date.now(); });
      }
    });
    return out;
  }
  function showPreview(title, parsed, onApply) {
    var box = $('#sync-preview');
    if (!box) return;
    var done = loadDone();
    var ids = Object.keys(parsed.done);
    var fresh = ids.filter(function (k) { return !done[k]; });
    var quizN = Object.keys(parsed.quiz).length;
    box.innerHTML = '';
    var h = document.createElement('h3'); h.textContent = title; box.appendChild(h);
    var p = document.createElement('p');
    p.textContent = ids.length + ' lesson' + (ids.length === 1 ? '' : 's') + ' recognised, ' + fresh.length + ' not yet marked here' + (quizN ? ', ' + quizN + ' quiz score' + (quizN === 1 ? '' : 's') : '') + '.';
    box.appendChild(p);
    if (parsed.unknown && parsed.unknown.length) {
      var u = document.createElement('p'); u.className = 'small muted';
      u.textContent = 'Not recognised (left alone): ' + parsed.unknown.slice(0, 8).join(', ') + (parsed.unknown.length > 8 ? ' …' : '');
      box.appendChild(u);
    }
    if (fresh.length) {
      var ul = document.createElement('ul'); ul.className = 'small';
      var idx = {}; lessonIndex().forEach(function (l) { idx[l.id] = l; });
      fresh.slice(0, 12).forEach(function (k) { var li = document.createElement('li'); li.textContent = idx[k] ? ('Phase ' + idx[k].p + ' · ' + idx[k].t) : k; ul.appendChild(li); });
      if (fresh.length > 12) { var more = document.createElement('li'); more.textContent = '… and ' + (fresh.length - 12) + ' more'; ul.appendChild(more); }
      box.appendChild(ul);
    }
    var actions = document.createElement('p'); actions.className = 'progress-actions';
    var apply = document.createElement('button'); apply.type = 'button'; apply.className = 'btn primary';
    apply.textContent = ids.length ? 'Apply (merge into this browser)' : 'Nothing to apply';
    apply.disabled = !ids.length;
    var cancel = document.createElement('button'); cancel.type = 'button'; cancel.className = 'btn ghost'; cancel.textContent = 'Cancel';
    apply.addEventListener('click', function () { var added = onApply(); box.innerHTML = ''; var ok = document.createElement('p'); ok.className = 'sync-ok'; ok.textContent = 'Done: ' + added + ' lesson' + (added === 1 ? '' : 's') + ' newly marked complete.'; box.appendChild(ok); });
    cancel.addEventListener('click', function () { box.hidden = true; box.innerHTML = ''; });
    actions.appendChild(apply); actions.appendChild(cancel); box.appendChild(actions);
    box.hidden = false;
    box.scrollIntoView({ block: 'nearest' });
  }
  function initSync() {
    var dl = $('#download-progress'), fileBtn = $('#import-file-btn'), fileIn = $('#import-file');
    var copyBtn = $('#export-progress'), pasteBtn = $('#import-progress'), resetBtn = $('#reset-progress');
    var lmBtn = $('#learning-file-btn'), lmIn = $('#learning-file'), lmPaste = $('#learning-paste-btn');
    function importJsonText(text, title) {
      var parsed; try { parsed = JSON.parse(text); } catch (e) { parsed = null; }
      var data = parsed && normaliseImport(parsed);
      if (!data) { window.alert('That did not look like a progress file from this site.'); return; }
      showPreview(title, { done: data.done, quiz: data.quiz, unknown: [] }, function () { return mergeImport(data); });
    }
    if (dl) dl.addEventListener('click', function () {
      downloadText('payments-course-progress-' + new Date().toISOString().slice(0, 10) + '.json', JSON.stringify(exportPayload(), null, 2));
    });
    if (fileBtn && fileIn) {
      fileBtn.addEventListener('click', function () { fileIn.click(); });
      fileIn.addEventListener('change', function () { readFile(fileIn, function (text) { importJsonText(text, 'Import progress file'); }); });
    }
    if (copyBtn) copyBtn.addEventListener('click', function () {
      var text = JSON.stringify(exportPayload());
      if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(text); else fallbackCopy(text);
      copyBtn.textContent = 'Copied'; setTimeout(function () { copyBtn.textContent = 'Copy as text'; }, 1500);
    });
    if (pasteBtn) pasteBtn.addEventListener('click', function () {
      var text = window.prompt('Paste the progress text you copied from another browser:');
      if (text) importJsonText(text, 'Import pasted progress');
    });
    if (lmBtn && lmIn) {
      lmBtn.addEventListener('click', function () { lmIn.click(); });
      lmIn.addEventListener('change', function () { readFile(lmIn, function (text) {
        var parsed = parseLearningMd(text);
        showPreview('Sync from LEARNING.md', parsed, function () { return mergeImport({ done: parsed.done, quiz: parsed.quiz, lang: null }); });
      }); });
    }
    if (lmPaste) lmPaste.addEventListener('click', function () {
      var text = window.prompt('Paste the full contents of LEARNING.md:');
      if (!text) return;
      var parsed = parseLearningMd(text);
      showPreview('Sync from LEARNING.md', parsed, function () { return mergeImport({ done: parsed.done, quiz: parsed.quiz, lang: null }); });
    });
    if (resetBtn) resetBtn.addEventListener('click', function () {
      if (window.confirm('Remove every completed-lesson mark and quiz score from this browser?')) { saveDone({}); saveQuiz({}); refreshProgress(); }
    });
  }

  /* ---------------- quiz ---------------- */
  var STAGE_LABEL = { pre: 'Pre-check', check: 'Check', post: 'Post-lesson' };
  function initQuiz() {
    var holder = $('.quiz-body');
    var data = $('#quiz-data');
    if (!holder || !data) return;
    var questions;
    try { questions = JSON.parse(data.textContent); } catch (e) { return; }
    var article = holder.closest('[data-lesson-id]');
    var lessonId = article ? article.getAttribute('data-lesson-id') : null;
    var last = document.createElement('p'); last.className = 'quiz-last small muted';
    function showLastScore() {
      var rec = lessonId ? loadQuiz()[lessonId] : null;
      last.textContent = rec ? 'Last score in this browser: ' + rec.score + ' / ' + rec.total + ' (' + new Date(rec.at).toLocaleDateString() + ')' : '';
    }
    showLastScore();
    holder.parentNode.insertBefore(last, holder);
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
      if (answered === questions.length && lessonId) {
        var q = loadQuiz(); q[lessonId] = { score: right, total: questions.length, at: Date.now() }; saveQuiz(q); showLastScore();
      }
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
    initWalkthrough();
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

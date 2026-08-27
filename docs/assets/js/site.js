---
# Front matter (even empty) is what makes Jekyll process and copy this file.
layout: null
---
/* Site behaviour: theme, on-this-page TOC, mobile nav, copy buttons, search.
   No framework and no build step — the site is served straight from /docs by
   the branch-based Pages build, so this file ships exactly as written. */
(function () {
  'use strict';

  var root = document.documentElement;

  /* ---------------------------------------------------------------- theme */

  var THEME_KEY = 'servo-theme';

  function prefersDark() {
    return window.matchMedia('(prefers-color-scheme: dark)').matches;
  }

  function effectiveTheme() {
    var explicit = root.getAttribute('data-theme');
    if (explicit === 'dark' || explicit === 'light') return explicit;
    return prefersDark() ? 'dark' : 'light';
  }

  function setTheme(next) {
    root.setAttribute('data-theme', next);
    try { localStorage.setItem(THEME_KEY, next); } catch (e) {}
    // The mermaid module listens for this: an already-rasterised SVG cannot
    // restyle itself from CSS variables.
    window.dispatchEvent(new CustomEvent('servo:themechange', { detail: { theme: next } }));
  }

  var themeBtn = document.querySelector('.theme-toggle');
  if (themeBtn) {
    themeBtn.addEventListener('click', function () {
      setTheme(effectiveTheme() === 'dark' ? 'light' : 'dark');
    });
  }

  /* ------------------------------------------------------------ mobile nav */

  var sidebar = document.getElementById('sidebar');
  var navBtn = document.querySelector('.nav-toggle');
  var scrim = null;

  function closeNav() {
    if (!sidebar) return;
    sidebar.classList.remove('is-open');
    if (navBtn) navBtn.setAttribute('aria-expanded', 'false');
    if (scrim) { scrim.remove(); scrim = null; }
  }

  function openNav() {
    if (!sidebar) return;
    sidebar.classList.add('is-open');
    if (navBtn) navBtn.setAttribute('aria-expanded', 'true');
    scrim = document.createElement('div');
    scrim.className = 'nav-scrim';
    scrim.addEventListener('click', closeNav);
    document.body.appendChild(scrim);
  }

  if (navBtn) {
    navBtn.addEventListener('click', function () {
      if (sidebar && sidebar.classList.contains('is-open')) closeNav();
      else openNav();
    });
  }

  /* -------------------------------------------------- headings, TOC, spy */

  var article = document.querySelector('.content .prose');
  var tocList = document.querySelector('.toc__list');

  if (article) {
    // Give every h2/h3 a click-to-link anchor. kramdown already assigned the
    // ids (auto_ids), so this only adds the affordance.
    article.querySelectorAll('h2[id], h3[id]').forEach(function (h) {
      var a = document.createElement('a');
      a.className = 'heading-anchor';
      a.href = '#' + h.id;
      a.textContent = '#';
      a.setAttribute('aria-label', 'Link to this section');
      h.appendChild(a);
    });

    // Wide markdown tables have no wrapper of their own, so give them one that
    // can scroll instead of forcing the page sideways.
    article.querySelectorAll('table').forEach(function (t) {
      if (t.parentElement && t.parentElement.classList.contains('table-wrap')) return;
      var wrap = document.createElement('div');
      wrap.className = 'table-wrap';
      t.parentNode.insertBefore(wrap, t);
      wrap.appendChild(t);
    });
  }

  if (article && tocList) {
    var headings = Array.prototype.slice.call(article.querySelectorAll('h2[id], h3[id]'));
    headings.forEach(function (h) {
      var li = document.createElement('li');
      if (h.tagName === 'H3') li.className = 'toc-h3';
      var a = document.createElement('a');
      a.href = '#' + h.id;
      // textContent would include the "#" anchor added above.
      a.textContent = (h.firstChild && h.firstChild.textContent
        ? h.firstChild.textContent
        : h.textContent).trim();
      li.appendChild(a);
      tocList.appendChild(li);
    });

    var links = Array.prototype.slice.call(tocList.querySelectorAll('a'));
    if (links.length) {
      var byId = {};
      links.forEach(function (a) { byId[a.getAttribute('href').slice(1)] = a; });
      var visible = new Set();

      var spy = new IntersectionObserver(function (entries) {
        entries.forEach(function (entry) {
          if (entry.isIntersecting) visible.add(entry.target.id);
          else visible.delete(entry.target.id);
        });
        // Highlight the topmost heading currently on screen; if none are,
        // keep the last one we passed rather than clearing the whole TOC.
        var current = null;
        for (var i = 0; i < headings.length; i++) {
          if (visible.has(headings[i].id)) { current = headings[i].id; break; }
        }
        if (!current) {
          for (var j = headings.length - 1; j >= 0; j--) {
            if (headings[j].getBoundingClientRect().top < 120) { current = headings[j].id; break; }
          }
        }
        links.forEach(function (a) { a.classList.remove('is-active'); });
        if (current && byId[current]) byId[current].classList.add('is-active');
      }, { rootMargin: '-80px 0px -70% 0px', threshold: 0 });

      headings.forEach(function (h) { spy.observe(h); });
    }
  }

  /* --------------------------------------------------------- copy buttons */

  function copy(text, btn, label) {
    var done = function () {
      var prev = btn.textContent;
      btn.textContent = label;
      btn.classList.add('is-done');
      setTimeout(function () { btn.textContent = prev; btn.classList.remove('is-done'); }, 1400);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () {});
      return;
    }
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); done(); } catch (e) {}
    ta.remove();
  }

  document.querySelectorAll('[data-copy]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      copy(btn.getAttribute('data-copy'), btn, 'Copied');
    });
  });

  if (article) {
    article.querySelectorAll('pre').forEach(function (pre) {
      // Mermaid blocks are diagrams, not code to copy. This script runs
      // before the mermaid module has converted them, so the figure does not
      // exist yet — match the not-yet-converted markup as well, or the
      // diagram inherits a stray wrapper and a Copy button.
      if (pre.closest('.mermaid-figure')) return;
      if (pre.classList.contains('language-mermaid')) return;
      if (pre.querySelector('.language-mermaid')) return;
      if (pre.closest('.language-mermaid')) return;
      var wrap = document.createElement('div');
      wrap.className = 'code-wrap';
      pre.parentNode.insertBefore(wrap, pre);
      wrap.appendChild(pre);

      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'copy-code';
      btn.textContent = 'Copy';
      btn.setAttribute('aria-label', 'Copy code to clipboard');
      btn.addEventListener('click', function () { copy(pre.innerText, btn, 'Copied'); });
      wrap.appendChild(btn);
    });
  }

  /* --------------------------------------------------------------- search */

  var overlay = document.querySelector('.search-overlay');
  var input = document.querySelector('.search-input');
  var results = document.querySelector('.search-results');
  var empty = document.querySelector('.search-empty');
  var openBtn = document.querySelector('.search-open');
  var index = null;
  var active = -1;

  // Liquid hands one page's `content` to another as raw markdown rather than
  // rendered HTML, so the index arrives full of link syntax, backticks and
  // table pipes. Strip the common markers so snippets read as prose and a
  // phrase is not split by markup. Underscores are left alone — they are far
  // more likely to be part of a Go identifier than emphasis.
  function cleanMarkdown(s) {
    return String(s)
      .replace(/!\[[^\]]*\]\([^)]*\)/g, ' ')
      .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
      .replace(/```+/g, ' ')
      .replace(/`/g, '')
      .replace(/\*{1,3}/g, '')
      .replace(/^\s{0,3}#{1,6}\s*/gm, '')
      .replace(/^\s*[-=]{3,}\s*$/gm, ' ')
      .replace(/\|/g, ' ')
      .replace(/\s+/g, ' ')
      .trim();
  }

  function loadIndex() {
    if (index !== null || !window.SERVO_SEARCH_URL) return Promise.resolve();
    return fetch(window.SERVO_SEARCH_URL)
      .then(function (r) { return r.ok ? r.json() : []; })
      .then(function (data) {
        index = data.map(function (item) {
          return {
            title: item.title,
            section: item.section,
            url: item.url,
            body: cleanMarkdown(item.body)
          };
        });
      })
      .catch(function () { index = []; });
  }

  // The panel declares aria-modal, which promises assistive technology that
  // focus is confined to it. Remember where focus came from so closing can put
  // it back, rather than dumping the user at the top of the document.
  var lastFocused = null;

  function openSearch() {
    if (!overlay) return;
    lastFocused = document.activeElement;
    overlay.hidden = false;
    loadIndex();
    if (input) { input.value = ''; input.focus(); }
    if (results) results.innerHTML = '';
    if (empty) empty.hidden = true;
    active = -1;
  }

  function closeSearch() {
    if (!overlay) return;
    // Restore focus BEFORE hiding: focusing an element inside a hidden subtree
    // is a no-op, and moving focus out of the panel first avoids leaving it on
    // an element that is about to become inert.
    if (lastFocused && document.contains(lastFocused)) lastFocused.focus();
    overlay.hidden = true;
    lastFocused = null;
  }

  // Tab cycles within the panel while it is open. The panel holds the input and
  // whatever result links are currently rendered, so the set is recomputed on
  // each press rather than cached.
  if (overlay) {
    overlay.addEventListener('keydown', function (e) {
      if (e.key !== 'Tab' || overlay.hidden) return;
      var focusable = [].slice.call(
        overlay.querySelectorAll('input, a[href], button:not([disabled])')
      ).filter(function (el) { return el.offsetParent !== null; });
      if (!focusable.length) return;
      var first = focusable[0], last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault(); last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault(); first.focus();
      }
    });
  }

  function escapeHtml(s) {
    return s.replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function snippet(body, query) {
    var i = body.toLowerCase().indexOf(query.toLowerCase());
    if (i < 0) return '';
    var start = Math.max(0, i - 45);
    var raw = body.slice(start, start + 150);
    var html = escapeHtml(raw);
    // Re-find in the escaped string so the mark lands on the right text.
    var at = html.toLowerCase().indexOf(escapeHtml(query).toLowerCase());
    if (at < 0) return (start > 0 ? '…' : '') + html + '…';
    return (start > 0 ? '…' : '') + html.slice(0, at) +
      '<mark>' + html.slice(at, at + query.length) + '</mark>' +
      html.slice(at + query.length) + '…';
  }

  function render(query) {
    if (!results) return;
    results.innerHTML = '';
    active = -1;
    var q = query.trim();
    if (!q || !index) { if (empty) empty.hidden = true; return; }

    var lower = q.toLowerCase();
    var hits = index
      .map(function (item) {
        var inTitle = item.title.toLowerCase().indexOf(lower) >= 0;
        var at = item.body.toLowerCase().indexOf(lower);
        if (!inTitle && at < 0) return null;
        return { item: item, score: inTitle ? 0 : 1 + at / 100000 };
      })
      .filter(Boolean)
      .sort(function (a, b) { return a.score - b.score; })
      .slice(0, 12);

    if (!hits.length) { if (empty) empty.hidden = false; return; }
    if (empty) empty.hidden = true;

    hits.forEach(function (hit) {
      var li = document.createElement('li');
      var a = document.createElement('a');
      a.href = hit.item.url;
      var snip = snippet(hit.item.body, q);
      a.innerHTML = '<span class="r-title">' + escapeHtml(hit.item.title) + '</span>' +
        '<span class="r-crumb">' + escapeHtml(hit.item.section) +
        (snip ? ' — ' + snip : '') + '</span>';
      li.appendChild(a);
      results.appendChild(li);
    });
  }

  function move(delta) {
    if (!results) return;
    var items = Array.prototype.slice.call(results.children);
    if (!items.length) return;
    if (active >= 0 && items[active]) items[active].classList.remove('is-active');
    active = (active + delta + items.length) % items.length;
    items[active].classList.add('is-active');
    items[active].scrollIntoView({ block: 'nearest' });
  }

  if (openBtn) openBtn.addEventListener('click', openSearch);

  if (input) {
    input.addEventListener('input', function () {
      loadIndex().then(function () { render(input.value); });
    });
    input.addEventListener('keydown', function (e) {
      if (e.key === 'ArrowDown') { e.preventDefault(); move(1); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1); }
      else if (e.key === 'Enter') {
        var items = results ? results.children : [];
        var target = active >= 0 ? items[active] : items[0];
        var link = target && target.querySelector('a');
        if (link) { e.preventDefault(); window.location.href = link.href; }
      }
    });
  }

  if (overlay) {
    overlay.addEventListener('click', function (e) {
      if (e.target === overlay) closeSearch();
    });
  }

  document.addEventListener('keydown', function (e) {
    var typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName);
    if (e.key === 'Escape') { closeSearch(); closeNav(); return; }
    if (e.key === '/' && !typing) { e.preventDefault(); openSearch(); return; }
    if ((e.key === 'k' || e.key === 'K') && (e.metaKey || e.ctrlKey)) {
      e.preventDefault(); openSearch();
    }
  });
})();

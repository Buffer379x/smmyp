// SMMYP · Progressive Verbesserung: alles funktioniert auch ohne JS, mit JS schneller.
(() => {
  'use strict';
  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];
  const live = (msg) => { const el = $('#live'); if (el) { el.textContent = ''; setTimeout(() => { el.textContent = msg; }, 30); } };

  // ---------- Live-Suche ----------
  const form = $('#search-form');
  const q = $('#q');
  const scope = $('#scope');
  let timer = null;
  let ctrl = null;
  const runSearch = () => {
    const results = $('#results');
    const params = new URLSearchParams(new FormData(form));
    for (const [k, v] of [...params]) if (!v) params.delete(k);
    const url = '/' + (params.toString() ? '?' + params : '');
    if (!results) { location.href = url; return; }
    ctrl?.abort();
    ctrl = new AbortController();
    params.set('partial', '1');
    fetch('/?' + params, { signal: ctrl.signal, headers: { 'X-SMMYP-Fetch': '1' } })
      .then((r) => (r.ok ? r.text() : Promise.reject(r.status)))
      .then((html) => {
        const tpl = document.createElement('template');
        tpl.innerHTML = html;
        const fresh = tpl.content.querySelector('#results');
        if (fresh) results.replaceWith(fresh);
        history.replaceState(null, '', url);
      })
      .catch(() => {});
  };
  if (form && q) {
    q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(runSearch, 150); });
    scope?.addEventListener('change', runSearch);
    form.addEventListener('submit', (e) => { if ($('#results')) { e.preventDefault(); runSearch(); } });
  }

  // ---------- Kopieren ----------
  const copyText = async (text) => {
    if (navigator.clipboard && window.isSecureContext) {
      try { await navigator.clipboard.writeText(text); return true; } catch (e) { /* Fallback unten */ }
    }
    // Fallback für HTTP im Heimnetz (kein Secure Context): unsichtbares Textfeld + execCommand.
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.className = 'sr';
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
    ta.remove();
    return ok;
  };
  const container = (el) => el.closest('.card, .row, .detail, .invite, .panel');

  // Platzhalter {{name}} / {{name=standard}}; muss zu internal/vars passen.
  const varRe = () => /\\?\{\{\s*([\p{L}\p{N}_][\p{L}\p{N}_ \-]{0,39}?)\s*(?:=([^{}]{0,200}))?\}\}/gu;
  const fillVars = (text, values) => text.replace(varRe(), (m, name, def) => {
    if (m.startsWith('\\')) return m.slice(1);
    const v = values[name.trim()];
    return v ? v : (def || '').trim();
  });
  const askVars = async (id, text) => {
    const names = [];
    for (const m of text.matchAll(varRe())) if (!m[0].startsWith('\\') && !names.includes(m[1].trim())) names.push(m[1].trim());
    if (!names.length) return text;
    let known = [];
    try { known = await (await fetch('/p/' + id + '/vars', { headers: { 'X-SMMYP-Fetch': '1' } })).json(); } catch (e) { known = []; }
    const dlg = $('#vars-dialog');
    const box = $('#vars-fields');
    if (!dlg || !box) return fillVars(text, {});
    box.textContent = '';
    names.forEach((n, i) => {
      const info = known.find((k) => k.name === n) || {};
      const label = document.createElement('label');
      label.textContent = n;
      const input = document.createElement('input');
      input.name = n;
      input.value = info.last || '';
      input.placeholder = info.default || '';
      input.autocomplete = 'off';
      if (i === 0) input.autofocus = true;
      label.appendChild(input);
      box.appendChild(label);
    });
    dlg.showModal();
    return new Promise((resolve) => {
      const done = (ok) => {
        dlg.close();
        if (!ok) { resolve(null); return; }
        const values = {};
        $$('input', box).forEach((i) => { values[i.name] = i.value.trim(); });
        fetch('/p/' + id + '/vars', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-SMMYP-Fetch': '1' }, body: JSON.stringify(values) }).catch(() => {});
        resolve(fillVars(text, values));
      };
      $('#vars-form').onsubmit = (e) => { e.preventDefault(); done(true); };
      dlg.querySelector('[data-close]').onclick = () => done(false);
      dlg.oncancel = () => done(false);
    });
  };

  const doCopy = async (btn) => {
    const box = container(btn);
    if (!box) return;
    const pre = btn.dataset.copy === 'field' ? btn.closest('.field')?.querySelector('pre') : (box.querySelector('pre[data-primary]') || box.querySelector('pre'));
    if (!pre) return;
    let text = pre.textContent;
    if (box.hasAttribute('data-vars') && box.dataset.id && varRe().test(text)) {
      text = await askVars(box.dataset.id, text);
      if (text === null) return;
    }
    const ok = await copyText(text);
    if (!ok) { live('Kopieren nicht möglich. Bitte Text markieren und manuell kopieren.'); return; }
    box.classList.add('is-copied');
    setTimeout(() => box.classList.remove('is-copied'), 1300);
    live('In die Zwischenablage kopiert');
    const id = box.dataset.id;
    if (id) {
      const ctx = box.dataset.ctx ? '?provider=' + encodeURIComponent(box.dataset.ctx) : '';
      fetch('/p/' + id + '/used' + ctx, { method: 'POST', headers: { 'X-SMMYP-Fetch': '1' } }).catch(() => {});
    }
  };
  document.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-copy]');
    if (btn) { e.preventDefault(); doCopy(btn); }
  });

  // ---------- Favorit ohne Neuladen ----------
  document.addEventListener('submit', (e) => {
    const f = e.target;
    if (f.matches('form[data-confirm]') && !confirm(f.dataset.confirm)) { e.preventDefault(); return; }
    if (!f.matches('form.js-fav')) return;
    e.preventDefault();
    fetch(f.action, { method: 'POST', headers: { 'X-SMMYP-Fetch': '1' } })
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((d) => {
        const b = f.querySelector('.star');
        b.classList.toggle('is-on', d.favorite);
        b.setAttribute('aria-pressed', String(d.favorite));
        live(d.favorite ? 'Als Favorit markiert' : 'Favorit entfernt');
      })
      .catch(() => f.submit());
  });

  // ---------- Tastatur ----------
  const typing = (el) => el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName));
  const items = () => $$('[data-nav]');
  const focusItem = (dir) => {
    const list = items();
    if (!list.length) return;
    let i = list.indexOf(document.activeElement.closest('[data-nav]'));
    i = i < 0 ? (dir > 0 ? 0 : list.length - 1) : Math.min(list.length - 1, Math.max(0, i + dir));
    list[i].focus();
    list[i].scrollIntoView({ block: 'nearest' });
  };
  document.addEventListener('keydown', (e) => {
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    if (typing(e.target)) {
      if (e.key === 'Escape' && e.target === q) { q.value = ''; runSearch(); q.blur(); }
      return;
    }
    const cur = document.activeElement?.closest('[data-nav]');
    switch (e.key) {
      case '/': if (q) { e.preventDefault(); q.focus(); q.select(); } break;
      case 'j': case 'ArrowDown': if (items().length) { e.preventDefault(); focusItem(1); } break;
      case 'k': case 'ArrowUp': if (items().length) { e.preventDefault(); focusItem(-1); } break;
      case 'Enter': if (cur && e.target === cur) { location.href = cur.dataset.href; } break;
      case 'c': { const b = (cur || $('.detail'))?.querySelector('[data-copy="primary"]'); if (b) doCopy(b); break; }
      case 'f': { const f = (cur || $('.detail'))?.querySelector('form.js-fav'); if (f) f.requestSubmit(); break; }
      case 'n': location.href = $('[data-key="n"]')?.getAttribute('href') || '/p/new'; break;
      case 'v': { const vf = $('#view-form'); const off = vf?.querySelector('.vt:not(.is-on)'); if (off) vf.requestSubmit(off); break; }
      case 'x': { const c = cur?.querySelector('.row-check'); if (c) { c.checked = !c.checked; c.dispatchEvent(new Event('change', { bubbles: true })); } break; }
    }
  });

  // ---------- Formular: Feld-Union der gewählten Anbieter ----------
  const pf = $('#prompt-form');
  if (pf) {
    const boxes = $$('input[name="provider"]', pf);
    const radios = $$('input[name="primary"]', pf);
    const rows = $$('.field-row', pf);
    const union = () => {
      const keys = new Set();
      boxes.filter((b) => b.checked).forEach((b) => b.dataset.fields.split(',').forEach((k) => keys.add(k)));
      return keys;
    };
    const apply = () => {
      const keys = union();
      rows.forEach((r) => { r.hidden = keys.size > 0 && !keys.has(r.dataset.key); });
      const checked = boxes.filter((b) => b.checked);
      const primary = radios.find((r) => r.checked);
      if (checked.length && (!primary || !boxes.find((b) => b.value === primary.value)?.checked)) {
        radios.find((r) => r.value === checked[0].value).checked = true;
      }
    };
    boxes.forEach((b) => b.addEventListener('change', () => {
      if (!b.checked) {
        const keys = union();
        const lost = rows.filter((r) => !keys.has(r.dataset.key) && keys.size > 0 && r.querySelector('textarea').value.trim());
        if (lost.length) {
          const names = lost.map((r) => r.firstChild.textContent.trim()).join(', ');
          if (!confirm('Folgende Felder gehören nur zu diesem Anbieter und werden beim Speichern entfernt: ' + names + '. Fortfahren?')) { b.checked = true; return; }
        }
      }
      apply();
    }));
    radios.forEach((r) => r.addEventListener('change', () => {
      const b = boxes.find((x) => x.value === r.value);
      if (b && !b.checked) { b.checked = true; }
      apply();
    }));
    apply();
  }
})();

// ---------- Phase 2: Vorschläge im Formular, Befehlspalette, Mehrfachauswahl, PWA ----------
(() => {
  'use strict';
  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];
  const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };

  // Live-Hinweise im Formular
  const pf = $('#prompt-form');
  if (pf) {
    const tagsInput = $('#tags-input');
    const shown = new Set();
    const rejected = new Set();
    const syncHidden = () => { $('#suggested').value = [...shown].join(','); $('#rejected').value = [...rejected].join(','); };
    const currentTags = () => tagsInput.value.split(/[,#;]/).map((t) => t.trim().toLowerCase()).filter(Boolean);
    let timer = null;
    let ctrl = null;
    const run = () => {
      ctrl?.abort();
      ctrl = new AbortController();
      fetch('/api/suggest', { method: 'POST', signal: ctrl.signal, headers: { 'X-SMMYP-Fetch': '1' }, body: new URLSearchParams(new FormData(pf)) })
        .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
        .then(render)
        .catch(() => {});
    };
    const render = (d) => {
      const box = $('#tag-suggest');
      const list = $('#tag-suggest-list');
      list.textContent = '';
      const have = new Set(currentTags());
      (d.tags || []).filter((t) => !have.has(t.slug) && !rejected.has(t.slug)).forEach((t) => {
        shown.add(t.slug);
        const li = el('li', 'auto', '#' + t.slug + ' ');
        li.title = t.reason;
        const add = el('button', '', '+');
        add.type = 'button';
        add.setAttribute('aria-label', '#' + t.slug + ' übernehmen (' + t.reason + ')');
        add.onclick = () => { tagsInput.value = [...currentTags(), t.slug].join(', '); li.remove(); };
        const rej = el('button', 'rej', '×');
        rej.type = 'button';
        rej.setAttribute('aria-label', '#' + t.slug + ' ablehnen');
        rej.onclick = () => { rejected.add(t.slug); li.remove(); syncHidden(); };
        li.append(add, rej);
        list.appendChild(li);
      });
      box.hidden = list.children.length === 0;
      const ps = $('#provider-suggest');
      ps.textContent = '';
      (d.providers || []).forEach((p) => {
        const b = el('button', 'chip', '+ ' + p.name);
        b.type = 'button';
        b.title = p.reason;
        b.onclick = () => {
          const cb = pf.querySelector('input[name="provider"][value="' + p.id + '"]');
          if (cb && !cb.checked) { cb.checked = true; cb.dispatchEvent(new Event('change', { bubbles: true })); }
          b.remove();
          ps.hidden = ps.children.length === 0;
        };
        ps.appendChild(b);
      });
      if (ps.children.length) ps.prepend(el('span', 'hint', 'Passt vermutlich zu: '));
      ps.hidden = ps.children.length === 0;
      const vf = $('#vars-found');
      vf.hidden = !(d.vars && d.vars.length);
      if (d.vars && d.vars.length) vf.textContent = 'Erkannte Platzhalter: ' + d.vars.join(', ');
      let warn = $('#live-warn');
      if (!warn) { warn = el('div', 'flash flash-error'); warn.id = 'live-warn'; pf.prepend(warn); }
      const msgs = [];
      if (d.duplicate) msgs.push('Inhaltsgleich mit „' + d.duplicate.title + '“.');
      else if (d.near) msgs.push('Sehr ähnlich wie „' + d.near.title + '“.');
      if (d.secrets && d.secrets.length) msgs.push('Enthält vermutlich: ' + d.secrets.join(', ') + '.');
      warn.textContent = msgs.join(' ');
      warn.hidden = msgs.length === 0;
      syncHidden();
    };
    pf.addEventListener('input', (e) => { if (e.target.matches('textarea, input[name="title"]')) { clearTimeout(timer); timer = setTimeout(run, 700); } });
    pf.addEventListener('change', (e) => { if (e.target.matches('input[name="provider"]')) { clearTimeout(timer); timer = setTimeout(run, 300); } });
    pf.addEventListener('submit', syncHidden);
    if ([...pf.querySelectorAll('textarea')].some((t) => t.value.trim())) run();
  }

  // Mehrfachauswahl (Listenansicht)
  const bulk = $('#bulk-form');
  if (bulk) {
    const checks = () => $$('.row-check');
    const update = () => {
      const n = checks().filter((c) => c.checked).length;
      $('#bulk-count').textContent = n ? n + ' ausgewählt' : 'Alle';
      bulk.classList.toggle('has-selection', n > 0);
    };
    $('#bulk-all')?.addEventListener('change', (e) => { checks().forEach((c) => { c.checked = e.target.checked; }); update(); });
    document.addEventListener('change', (e) => { if (e.target.matches('.row-check')) update(); });
    const action = bulk.querySelector('select[name="action"]');
    const showArgs = () => $$('.bulk-arg', bulk).forEach((a) => { a.hidden = a.dataset.for !== action.value; });
    action.addEventListener('change', showArgs);
    bulk.addEventListener('submit', (e) => {
      const n = checks().filter((c) => c.checked).length;
      if (!n || !action.value) { e.preventDefault(); return; }
      if (action.value === 'trash' && !confirm(n + ' Prompt(s) in den Papierkorb legen?')) e.preventDefault();
    });
    showArgs();
    update();
  }

  // Befehlspalette (Strg/⌘ K)
  const pal = $('#palette');
  if (pal) {
    const input = $('#palette-q');
    const list = $('#palette-list');
    let items = [];
    let active = 0;
    let timer = null;
    const go = (it) => { pal.close(); if (it.run) it.run(); else location.href = it.url; };
    const statics = () => {
      const out = [
        { label: 'Neuer Prompt', hint: 'n', url: '/p/new' },
        { label: 'Alle Prompts', url: '/' },
        { label: 'Favoriten', url: '/?fav=1' },
        { label: 'Ansicht wechseln (Karten/Liste)', hint: 'v', run: () => { const vf = $('#view-form'); const off = vf?.querySelector('.vt:not(.is-on)'); if (off) vf.requestSubmit(off); } },
        { label: 'Tags & KI-Hilfe', url: '/tags' },
        { label: 'Sammlungen', url: '/c' },
        { label: 'Import', url: '/import' },
        { label: 'Export (JSON)', url: '/export.json' },
        { label: 'Papierkorb', url: '/trash' },
        { label: 'Einstellungen', url: '/settings' },
      ];
      $$('.rail a.drawer').forEach((a) => out.push({ label: a.firstChild ? a.textContent.replace(/\d+$/, '').trim() : a.textContent, hint: 'Schublade', url: a.getAttribute('href') }));
      return out;
    };
    const draw = () => {
      list.textContent = '';
      items.forEach((it, i) => {
        const li = el('li', i === active ? 'is-on' : '', it.label);
        li.setAttribute('role', 'option');
        if (it.hint) li.appendChild(el('span', 'hint', it.hint));
        li.onclick = () => go(it);
        list.appendChild(li);
      });
    };
    const filter = () => {
      const q = input.value.trim().toLowerCase();
      items = statics().filter((it) => !q || it.label.toLowerCase().includes(q)).slice(0, 8);
      active = 0;
      draw();
      clearTimeout(timer);
      if (q.length >= 2) {
        timer = setTimeout(() => {
          fetch('/api/palette?q=' + encodeURIComponent(input.value), { headers: { 'X-SMMYP-Fetch': '1' } })
            .then((r) => r.json())
            .then((ps) => { items = items.concat(ps.map((p) => ({ label: p.title, hint: p.providers, url: p.url }))); draw(); })
            .catch(() => {});
        }, 150);
      }
    };
    const open = () => { input.value = ''; filter(); pal.showModal(); input.focus(); };
    document.addEventListener('keydown', (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); pal.open ? pal.close() : open(); }
    });
    document.addEventListener('click', (e) => { if (e.target.closest('[data-palette]')) { e.preventDefault(); open(); } });
    input.addEventListener('input', filter);
    input.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowDown') { e.preventDefault(); active = Math.min(items.length - 1, active + 1); draw(); }
      if (e.key === 'ArrowUp') { e.preventDefault(); active = Math.max(0, active - 1); draw(); }
      if (e.key === 'Enter' && items[active]) { e.preventDefault(); go(items[active]); }
    });
  }

  // PWA: Service Worker nur im Secure Context (HTTPS oder localhost)
  if ('serviceWorker' in navigator && window.isSecureContext) {
    if (location.pathname === '/login') {
      navigator.serviceWorker.controller?.postMessage('logout'); // Lesecache beim Abmelden leeren
    } else {
      navigator.serviceWorker.register('/sw.js').catch(() => {});
    }
  }
})();

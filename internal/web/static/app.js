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
  const doCopy = async (btn) => {
    const box = container(btn);
    if (!box) return;
    const pre = btn.dataset.copy === 'field' ? btn.closest('.field')?.querySelector('pre') : (box.querySelector('pre[data-primary]') || box.querySelector('pre'));
    if (!pre) return;
    const ok = await copyText(pre.textContent);
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

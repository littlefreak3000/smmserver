(() => {
  const $ = (s) => document.querySelector(s);
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

  async function api(url, body) {
    const res = await fetch(url, body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {});
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || res.statusText);
    return data;
  }

  // ---- tabs ----
  for (const b of document.querySelectorAll('nav button')) {
    b.onclick = () => {
      for (const o of document.querySelectorAll('nav button')) o.classList.toggle('on', o === b);
      for (const s of document.querySelectorAll('main > section')) s.hidden = s.id !== b.dataset.tab;
      if (b.dataset.tab === 'settings') loadSettings();
    };
  }

  api('/api/status').then((s) => {
    const parts = [];
    if (s.levels !== undefined) parts.push(`${s.levels.toLocaleString()} levels on this server`);
    if (s.address) parts.push(`console address ${s.address}`);
    $('#status').textContent = parts.join(' · ');
  }).catch(() => {});

  // ---- levels ----
  const PAGE = 48;
  let offset = 0;

  const band = (rate) => (rate < 0.05 ? ['Super Expert', 0] : rate < 0.2 ? ['Expert', 1] : rate < 0.5 ? ['Normal', 2] : ['Easy', 3]);

  function query() {
    const p = new URLSearchParams({ q: $('#q').value, sort: $('#sort').value, limit: PAGE, offset });
    if ($('#min').value !== '') p.set('min', $('#min').value);
    if ($('#max').value !== '') p.set('max', $('#max').value);
    return p;
  }

  async function loadLevels(append) {
    if (!append) { offset = 0; $('#grid').innerHTML = ''; }
    try {
      const r = await api('/api/levels?' + query());
      for (const l of r.levels) $('#grid').append(card(l));
      offset += r.levels.length;
      $('#count').textContent = r.total ? `${r.total.toLocaleString()} level${r.total === 1 ? '' : 's'}` : 'No levels match. The server fills its pool in the background: look again in a few minutes.';
      $('#more').hidden = offset >= r.total;
    } catch (e) { $('#count').textContent = e.message; }
  }

  function card(l) {
    const b = document.createElement('button');
    b.className = 'card';
    const pill = l.clearRate == null ? '' : (([name, i]) => `<span class="pill d${i}" title="${name}">${(l.clearRate * 100).toFixed(1)}% clear</span>`)(band(l.clearRate));
    b.innerHTML = `<img loading="lazy" src="/thumb/${l.id}" alt="" onerror="this.style.visibility='hidden'">
      <div class="t"><b>${esc(l.name || '(no name)')}</b><small>${esc(l.creator || 'unknown maker')}</small><br>
      ${pill} <small>${l.stars != null ? '★ ' + l.stars : ''} ${l.attempts != null ? '· ' + l.attempts.toLocaleString() + ' plays' : ''}</small></div>`;
    b.onclick = () => openLevel(l.id);
    return b;
  }

  $('#filter').onsubmit = (e) => { e.preventDefault(); loadLevels(false); };
  $('#more').onclick = () => loadLevels(true);

  // ---- level detail ----
  let current = null, area = 'main';

  async function openLevel(id) {
    $('#derror').textContent = '';
    $('#dtitle').textContent = 'Loading…';
    $('#dmeta').textContent = '';
    $('#detail').showModal();
    try {
      current = await api('/api/level/' + id);
    } catch (e) {
      $('#dtitle').textContent = `Level ${id}`;
      $('#derror').textContent = e.message;
      current = null;
      return;
    }
    const l = current.level;
    $('#dtitle').textContent = l.name || '(no name)';
    const bits = [`Course ID ${l.code}`, `by ${l.creator || 'unknown maker'}`];
    if (l.clearRate != null) bits.push(`${(l.clearRate * 100).toFixed(1)}% clear rate (${band(l.clearRate)[0]})`);
    if (l.attempts != null) bits.push(`${l.attempts.toLocaleString()} attempts`);
    if (l.stars != null) bits.push(`${l.stars} stars`);
    const m = current.areas.main;
    if (m) bits.push(`${m.mode} · ${m.themeName} · ${m.timeLimit}s · ${m.widthBlocks} wide · ${m.objects.length} objects`);
    $('#dmeta').textContent = bits.join(' · ');

    const has = Object.keys(current.areas);
    $('#areas').innerHTML = '';
    for (const a of ['main', 'sub'].filter((x) => has.includes(x))) {
      const b = document.createElement('button');
      b.textContent = a === 'main' ? 'Main area' : 'Subworld';
      b.dataset.a = a;
      b.onclick = () => { area = a; render(); };
      $('#areas').append(b);
    }
    area = has.includes('main') ? 'main' : has[0];
    render();
  }

  async function render() {
    if (!current || !current.areas[area]) { $('#derror').textContent = 'The course file could not be read.'; return; }
    for (const b of $('#areas').children) b.classList.toggle('on', b.dataset.a === area);
    const c = $('#canvas');
    await SMMRender.draw(c, current.areas[area], { grid: $('#grid-on').checked, hidden: $('#hidden-on').checked });
    zoom();
    // * the start of a course is at the bottom left
    $('#view').scrollTop = $('#view').scrollHeight;
    $('#view').scrollLeft = 0;
  }

  function zoom() {
    const c = $('#canvas'), z = parseFloat($('#zoom').value);
    c.style.width = c.width * z + 'px';
    c.style.height = c.height * z + 'px';
  }

  $('#zoom').onchange = zoom;
  $('#grid-on').onchange = $('#hidden-on').onchange = render;
  $('#dclose').onclick = () => $('#detail').close();
  $('#detail').addEventListener('click', (e) => { if (e.target === $('#detail')) $('#detail').close(); });

  // ---- settings ----
  let loaded = [];

  async function loadSettings() {
    try {
      const r = await api('/api/settings');
      loaded = r.settings;
      $('#path').textContent = r.path;
      drawSettings();
      $('#settingsError').textContent = '';
    } catch (e) { $('#settingsError').textContent = e.message; }
  }

  function drawSettings() {
    const root = $('#settingsForm');
    root.innerHTML = '';
    let group = null;
    for (const s of loaded) {
      if (s.group !== group) { group = s.group; root.insertAdjacentHTML('beforeend', `<h3 class="group">${esc(group)}</h3>`); }
      const d = document.createElement('div');
      d.className = 'setting' + (s.pending ? ' pending' : '');
      let input;
      if (s.kind === 'toggle') {
        input = `<select data-key="${s.key}"><option value="">Default (${s.default === '0' ? 'off' : 'on'})</option><option value="1">On</option><option value="0">Off</option></select>`;
      } else {
        input = `<input data-key="${s.key}" type="${s.kind === 'int' ? 'number' : 'text'}" ${s.kind === 'int' ? 'min="0"' : ''} placeholder="${esc(s.default)}" value="${esc(s.value)}">`;
      }
      d.innerHTML = `<label class="k">${s.key}</label><div class="row">${input}<span class="def">default: ${esc(s.default || '(automatic / none)')}</span>${s.pending ? '<span class="warn">restart needed</span>' : ''}</div><p>${esc(s.help)}</p>`;
      root.append(d);
      const el = d.querySelector('[data-key]');
      if (s.kind === 'toggle') el.value = s.value;
    }
    root.insertAdjacentHTML('beforeend', '<div class="bar"><button class="primary" id="save">Save changes</button><span id="saved" class="note"></span></div>');
    $('#save').onclick = save;
  }

  async function save() {
    const changes = {};
    for (const el of document.querySelectorAll('#settingsForm [data-key]')) {
      const s = loaded.find((x) => x.key === el.dataset.key);
      if (el.value.trim() !== s.value) changes[s.key] = el.value.trim();
    }
    if (!Object.keys(changes).length) { $('#saved').textContent = 'Nothing changed.'; return; }
    try {
      const r = await api('/api/settings', { changes });
      loaded = r.settings;
      drawSettings();
      $('#settingsError').textContent = '';
      $('#restart').hidden = !loaded.some((s) => s.pending);
      $('#saved').textContent = 'Saved.';
    } catch (e) { $('#settingsError').textContent = e.message; }
  }

  loadLevels(false);
})();

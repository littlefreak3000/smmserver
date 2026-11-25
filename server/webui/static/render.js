// Draws a Super Mario Maker course (JSON from /api/level/<id>) on a canvas.
//
// The sprite tables below are ported from HerobrineTV's SMM1-Level-Downloader (MIT licence, see sprites/LICENSE-sprites.txt):
// which cell of the tile sheet each object type uses, how terrain stretches (the "3x4" templates) and how pipes, bridges
// and similar objects are laid out. Not drawn yet: tracks, rotated (directional) objects and objects inside pipes.
const SMMRender = (() => {
  const T = 16;      // tile size in the sprite sheets and on the canvas
  const ROWS = 27;   // a course is 27 tiles high
  const sheets = new Map();

  function load(url) {
    if (!sheets.has(url)) {
      sheets.set(url, new Promise((resolve) => {
        const img = new Image();
        img.onload = () => resolve(img);
        img.onerror = () => resolve(null);
        img.src = url;
      }));
    }
    return sheets.get(url);
  }

  const BLOCK_SINGLES = {
    4: [0, 0, 1, 0], 5: [0, 0, 2, 0], 6: [0, 0, 6, 0], 21: [0, 0, 0, 4], 22: [0, 0, 6, 6], 29: [0, 0, 3, 0],
    43: [0, 0, 2, 4], 59: [0, 0, 0, 8], 63: [0, 0, 8, 7],
  };
  const SHEET_ONLY_BLOCKS = new Set([7, 9, 14, 16, 17, 26, 37, 49, 53, 59, 64]);

  const cell = (x, y, sx, sy, o = 1) => [x, y, sx, sy, o];

  // Terrain that stretches: a 3x4 template (corners, edges, middle) laid over width x height tiles.
  function extend3x4(x, y, width, height, tpl) {
    const out = [];
    for (let h = 0; h < height; h++) {
      for (let w = 0; w < width; w++) {
        const i = h === height - 1 && w === 0 ? 9 : h === height - 1 && w === width - 1 ? 11 : h === height - 1 ? 10
          : h === 0 && w === 0 ? 0 : h === 0 && w === width - 1 ? 2 : h === 0 ? 1
          : h % 2 === 1 && w === 0 ? 3 : h % 2 === 1 && w === width - 1 ? 5 : h % 2 === 1 ? 4
          : w === 0 ? 6 : w === width - 1 ? 8 : 7;
        const s = tpl[i] || [0, 0];
        out.push(cell(w + x, h + y, s[0], s[1]));
      }
    }
    return out;
  }

  // A w x h block of one sprite taken from the sheet going left/up, as the original's Extend().
  function extend(width, height, sx, sy, x = 0, y = 0) {
    const out = [];
    for (let w = 0; w < width; w++) for (let h = 0; h < height; h++) out.push(cell(-w + x, h + y, sx - w, sy - h));
    return out;
  }

  const row = (width, y, sx, sy) => Array.from({ length: width }, (_, x) => cell(x, y, sx, sy));
  const baseSize = (o) => [Math.max(1, Math.abs(o.w) - 3), Math.max(1, Math.abs(o.h))];

  function pipeCells(o) {
    const dir = Math.floor((o.flags & 0x60) / 0x20), h = Math.max(1, o.h);
    if (dir === 0) return extend3x4(0, -1, h, 2, { 0: [12, 1], 1: [12, 1], 2: [13, 1], 9: [12, 0], 10: [12, 0], 11: [13, 0] });
    if (dir === 1) return extend3x4(-(h - 1), 0, h, 2, { 0: [11, 1], 1: [12, 1], 2: [12, 1], 9: [11, 0], 10: [12, 0], 11: [12, 0] });
    if (dir === 2) return extend3x4(0, 0, 2, h, { 0: [14, 1], 2: [15, 1], 3: [14, 1], 5: [15, 1], 6: [14, 1], 8: [15, 1], 9: [14, 0], 11: [15, 0] });
    return extend3x4(-1, -(h - 1), 2, h, { 0: [14, 2], 2: [15, 2], 3: [14, 1], 5: [15, 1], 6: [14, 1], 8: [15, 1], 9: [14, 1], 11: [15, 1] });
  }

  function groundDecoration(o) {
    switch (o.childType) {
      case 1: return [cell(0, 2, 1, 1), cell(0, 1, 2, 1)];
      case 2: return [cell(0, 3, 3, 1), cell(0, 2, 4, 1), cell(0, 1, 5, 1)];
      case 3: return [cell(0, 1, 13, 8), cell(1, 1, 14, 8), cell(2, 1, 15, 8)];
      case 0: return [cell(0, 1, 0, 1)];
      default: return [];
    }
  }

  function mushroomCells(o) {
    const w = Math.max(1, o.w), h = Math.max(1, o.h), color = (o.flags >> 18) & 3, hy = color === 1 ? 3 : color === 2 ? 4 : 2;
    const cells = extend3x4(0, h - 1, w, 1, { 9: [3, hy], 10: [4, hy], 11: [5, hy] });
    if (w % 2 === 0) {
      for (let y = 0; y < h - 1; y++) { const x = w / 2 - 1; cells.push(cell(x, y, 6, y === 0 ? 2 : 1), cell(x + 1, y, 7, y === 0 ? 2 : 1)); }
    } else {
      for (let y = 0; y < h - 1; y++) cells.push(cell((w - 1) / 2, y, 6, y === 0 ? 4 : 3));
    }
    return cells;
  }

  function boxCells(o) {
    const t = (o.flags >> 18) & 3, l = t === 1 ? 10 : t === 2 ? 13 : 7;
    return extend3x4(0, 0, Math.max(1, o.w), Math.max(1, o.h), {
      0: [l, 6], 1: [l + 1, 6], 2: [l + 2, 6], 3: [l, 5], 4: [l + 1, 5], 5: [l + 2, 5],
      6: [l, 4], 7: [l + 1, 4], 8: [l + 2, 4], 9: [l, 3], 10: [l + 1, 3], 11: [l + 2, 3],
    });
  }

  function blockCells(o) {
    if (BLOCK_SINGLES[o.type]) return [BLOCK_SINGLES[o.type]];
    switch (o.type) {
      case 7: {
        const off = Math.min(Math.max(o.ext, 0), 71);
        return [cell(0, 0, (8 + off) % 16, 11 + Math.floor((8 + off) / 16)), ...groundDecoration(o)];
      }
      case 8: return [((o.flags & 4) >> 2) === 1 ? cell(0, 0, 0, 16) : cell(0, 0, 7, 0)];
      case 9: return pipeCells(o);
      case 14: return mushroomCells(o);
      case 16: return boxCells(o);
      case 17: return extend3x4(0, 0, Math.max(1, o.w), 2, { 0: [0, 3], 1: [1, 3], 2: [2, 3], 9: [0, 2], 10: [1, 2], 11: [2, 2] });
      case 23: return [((o.flags >> 2) & 1) === 1 ? cell(0, 0, 6, 5) : cell(0, 0, 4, 0)];
      case 26: { const [w, h] = baseSize(o); return extend3x4(0, 0, w, h, { 0: [11, 8], 1: [12, 8], 2: [12, 8], 3: [11, 8], 4: [12, 8], 5: [12, 8], 6: [11, 8], 7: [12, 8], 8: [12, 8], 9: [11, 7], 10: [12, 7], 11: [12, 7] }); }
      case 37: { const [w, h] = baseSize(o); return extend3x4(0, 0, w, h, { 0: [9, 8], 1: [9, 8], 2: [10, 8], 3: [9, 8], 4: [9, 8], 5: [10, 8], 6: [9, 8], 7: [9, 8], 8: [10, 8], 9: [9, 7], 10: [9, 7], 11: [10, 7] }); }
      case 49: return row(Math.max(1, o.w), 0, 15, 15);
      case 53: return extend3x4(0, 0, Math.max(1, o.w), 1, { 0: [8, 0], 1: [9, 0], 2: [10, 0], 9: [8, 0], 10: [9, 0], 11: [10, 0] });
      case 64: return Array.from({ length: Math.max(1, o.h) }, (_, y) => cell(0, y, y === 0 ? 13 : y === o.h - 1 ? 15 : 14, 7));
      default: return [];
    }
  }

  function cannonCells(o) {
    const h = Math.max(1, o.h), out = [];
    for (let y = 0; y < h; y++) {
      const pos = y === h - 2 ? 1 : y === h - 1 ? 0 : 2;
      out.push(o.sub === 1 ? cell(0, y, 11, 2 + pos) : cell(0, y, 12, pos));
    }
    return out;
  }

  function booCells(o) {
    const out = [];
    if (o.sub === 1) for (const [x, y] of [[-2, -2], [-3, 0], [-2, 2], [0, 3], [2, 2], [3, 0], [2, -2]]) out.push(cell(x, y, 7, 4, 0.3));
    out.push(((o.flags & 256) >> 8) === 1 ? cell(0, 0, 9, 7) : cell(0, 0, 7, 4));
    return out;
  }

  function monsterCells(o) {
    const s1 = o.sub === 1, door = (o.flags >> 18) & 3;
    switch (o.type) {
      case 0: return extend(1, 2, 1, 1);
      case 1: return extend(1, 2, 2, s1 ? 3 : 1);
      case 2: return extend(1, 2, 3, s1 ? 3 : 1);
      case 3: return o.w > 1 ? extend(2, 2, 5, 3) : extend(1, 2, 4, 1);
      case 10: return [cell(0, 0, 5, s1 ? 1 : 0)];
      case 11: return extend(4, 1, 9, s1 ? 1 : 0, 1);
      case 12: return s1 ? extend(4, 4, 3, 19, 1) : extend(2, 2, 11, 1);
      case 13: return cannonCells(o);
      case 15: return [cell(0, 0, 13, s1 ? 1 : 0)];
      case 18: return [cell(0, 0, s1 ? 4 : 14, s1 ? 16 : 0)];
      case 19: return [cell(0, 0, 14, 1)];
      case 20: return [cell(0, 0, 15, 6)];
      case 24: return [cell(0, 0, 13, 2), cell(1, 0, 14, 2)];
      case 25: return [cell(0, 0, 2, 4)];
      case 27: return [cell(0, 0, 0, 0)];
      case 28: return [cell(0, 0, 4, 4)];
      case 30: return [cell(0, 0, 8, 2)];
      case 31: return [cell(0, 0, 8, 3), cell(1, 0, 9, 3), cell(-1, 0, 9, 2)];
      case 33: return [cell(0, 0, 15, 1)];
      case 34: return [cell(0, 0, 15, 2)];
      case 35: return [cell(0, 0, 15, 3)];
      case 36: { const sx = s1 ? 11 : 10; return [cell(1, 0, sx, 8), cell(0, 0, sx, 8), cell(-1, 0, sx, 8), cell(-2, 0, sx, 8)]; }
      case 38: return [cell(0, 0, 13, 5), ...extend(3, 2, 14, 4, 1, 1)];
      case 39: return extend(1, 2, 0, 6);
      case 40: return [cell(0, 0, s1 ? 6 : 5, 4)];
      case 41: return booCells(o);
      case 42: return s1 ? extend(2, 2, 5, 19) : extend(2, 2, 2, 6);
      case 44: return [cell(0, 0, 15, 5)];
      case 45: return [cell(0, 0, 3, 5)];
      case 46: return extend(1, 2, 4, 6);
      case 47: return [cell(0, 0, 5, 5)];
      case 48: return extend(1, 2, s1 ? 7 : 6, 6);
      case 50: return [cell(0, 0, 15, 4)];
      case 51: return [cell(0, 0, 15, 0)];
      case 52: return [cell(0.2, -0.2, 9, 4), cell(0, 0, s1 ? 9 : 8, 5)];
      case 54: return [cell(0, 0, 9, 6), ...extend(1, 3, 10, 7, 0, 1)];
      case 55: return extend(1, 2, door === 1 ? 5 : door === 2 ? 6 : 11, door === 1 || door === 2 ? 17 : 6);
      case 56: return [cell(0, 0, 8, s1 ? 7 : 6)];
      case 57: return [cell(0, 0, s1 ? 6 : 7, 7)];
      case 58: return s1 ? extend(1, 1, 2, 7) : extend(1, 2, 3, 7);
      case 60: return [cell(0, 0, 1, 7)];
      case 61: return s1 ? [cell(0.3, 0.3, 12, 6), cell(0, 0, 11, 7)] : [cell(0, 0, 11, 7)];
      case 62: return s1 ? extend(2, 2, 4, 9, 1) : extend(3, 3, 2, 10);
      case 66: return s1 ? extend(2, 2, 9, 19) : extend(2, 2, 14, 7);
      case 67: return extend(2, 2, 6, 9);
      case 68: return s1 ? extend(3, 3, 12, 19, 1) : extend(3, 3, 9, 10, 1);
      case 69: return [cell(0, 0, 14, 5)];
      default: return [];
    }
  }

  const cellsOf = (o) => (o.block ? blockCells(o) : monsterCells(o));

  // Single sprites for objects the sheet has no cells for (and that are not terrain).
  function formatCandidates(o) {
    if (o.block && SHEET_ONLY_BLOCKS.has(o.type)) return [];
    const out = [];
    if (o.type === 3 && o.w > 1) out.push('3-1.png');
    if (o.type === 12 && o.sub === 1) out.push('12-1.png');
    if (o.type === 13 && o.sub === 1) out.push('13-1.png');
    if (o.type === 23 && ((o.flags >> 2) & 1) === 1) out.push('23b.png');
    if (o.type === 41 && ((o.flags & 256) >> 8) === 1) out.push('41-x.png');
    if (o.type === 55) out.push(`55-${(o.flags >> 18) & 3}.png`);
    out.push(`${o.type}-${o.sub}.png`, `${o.type}.png`);
    return out;
  }

  const dest = (o, c, H) => {
    const size = Math.max(1, o.size);
    if (o.size === 1) return [(o.x + c[0]) * T, H - (o.y + c[1] + 1) * T, T, T];
    return [(o.x + c[0] * size - (2 - Math.ceil(o.w / 2))) * T, H - (o.y + c[1] * size + 2) * T, T * size, T * size];
  };

  const tileRects = (o, cells) => {
    const size = Math.max(1, o.size);
    return cells.map((c) => (o.size === 1
      ? [o.x + Math.floor(c[0]), o.y + Math.floor(c[1]), size, size]
      : [o.x + Math.floor(c[0] * size) - (2 - Math.ceil(o.w / 2)), o.y + Math.floor(c[1] * size), size, size]));
  };

  function background(ctx, w, h, theme) {
    const dark = theme === 'castle' || theme === 'ghostHouse';
    const g = ctx.createLinearGradient(0, 0, 0, h);
    g.addColorStop(0, dark ? '#2B335E' : '#69C8FF');
    g.addColorStop(1, theme === 'underground' ? '#2E204C' : theme === 'castle' ? '#5A2834' : '#CFF4FF');
    ctx.fillStyle = g;
    ctx.fillRect(0, 0, w, h);
  }

  // Pipes that belong together carry the same number (bits 20-23 of the object flags; 0 = not connected). The two ends can sit
  // in the same area or one in the main area and one in the subworld.
  const pipeNo = (o) => (o.flags >>> 20) & 0xF;
  const AREA = { main: 'Main', sub: 'Sub' };
  const COLORS = ['#e53935', '#1e88e5', '#43a047', '#fb8c00', '#8e24aa', '#00acc1', '#d81b60', '#6d4c41', '#3949ab', '#7cb342', '#f4511e', '#00897b', '#5e35b1', '#c0ca33', '#546e7a'];

  /** Groups the pipes of all areas by number: [{no, ends: [{area, x, y}]}], sorted by number. */
  function pipeLinks(areas) {
    const by = new Map();
    for (const [name, course] of Object.entries(areas)) {
      for (const o of course.objects) {
        const no = o.type === 9 ? pipeNo(o) : 0;
        if (!no) continue;
        if (!by.has(no)) by.set(no, []);
        by.get(no).push({ area: name, x: o.x, y: o.y });
      }
    }
    return [...by].sort((a, b) => a[0] - b[0]).map(([no, ends]) => ({ no, ends }));
  }

  // The label of one pipe: its number and where the other end is.
  function pipeLabel(no, area, links) {
    const link = links.find((l) => l.no === no);
    const here = link ? link.ends.filter((e) => e.area === area).length : 1;
    const other = link ? link.ends.filter((e) => e.area !== area) : [];
    if (other.length) return `${no}→${AREA[other[0].area] || other[0].area}`;
    return link && link.ends.length === 2 && here === 2 ? `${no}↔` : `${no}?`;
  }

  function drawPipeLabel(ctx, o, cells, text, H) {
    const rects = tileRects(o, cells);
    const x0 = Math.min(...rects.map((r) => r[0])), x1 = Math.max(...rects.map((r) => r[0] + r[2]));
    const y0 = Math.min(...rects.map((r) => r[1])), y1 = Math.max(...rects.map((r) => r[1] + r[3]));
    const cx = ((x0 + x1) / 2) * T, cy = H - ((y0 + y1) / 2) * T;
    ctx.font = 'bold 10px sans-serif';
    const w = ctx.measureText(text).width + 6, h = 13;
    ctx.fillStyle = COLORS[(pipeNo(o) - 1) % COLORS.length];
    ctx.strokeStyle = '#fff';
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    ctx.roundRect(cx - w / 2, cy - h / 2, w, h, 4);
    ctx.fill();
    ctx.stroke();
    ctx.fillStyle = '#fff';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(text, cx, cy + 0.5);
    ctx.textAlign = 'start';
    ctx.textBaseline = 'alphabetic';
  }

  /**
   * Draws `course` into `canvas` at 16 px per tile (scale it with CSS). options: {hidden: show clear blocks, grid: draw a grid,
   * pipes: label the pipes (needs area: this area's name and links: pipeLinks of all areas)}.
   * Resolves when drawn.
   */
  async function draw(canvas, course, options = {}) {
    const mode = ['M1', 'M3', 'MW', 'WU'].includes(course.mode) ? course.mode : 'M1';
    const W = Math.max(course.widthBlocks, 1) * T, H = ROWS * T;
    canvas.width = W;
    canvas.height = H;
    const ctx = canvas.getContext('2d');
    ctx.imageSmoothingEnabled = false;

    const objs = course.objects.filter((o) => options.hidden || o.type !== 29).sort((a, b) => a.z - b.z);
    const block = await load(`sprites/titleset/${mode}-${course.themeName}.png`);
    const mon = await load(`sprites/monster/${mode}.png`);

    const prepared = await Promise.all(objs.map(async (o) => {
      const cells = cellsOf(o);
      if (cells.length) return { o, cells };
      const sprite = (await Promise.all(formatCandidates(o).map((f) => load(`sprites/format/${f}`)))).find(Boolean) || null;
      return { o, cells, sprite };
    }));

    background(ctx, W, H, course.themeName);

    if (options.grid) {
      ctx.strokeStyle = 'rgba(124,183,212,.35)';
      ctx.lineWidth = 1;
      ctx.beginPath();
      for (let x = 0; x <= course.widthBlocks; x++) { ctx.moveTo(x * T + .5, 0); ctx.lineTo(x * T + .5, H); }
      for (let y = 0; y <= ROWS; y++) { ctx.moveTo(0, y * T + .5); ctx.lineTo(W, y * T + .5); }
      ctx.stroke();
    }

    // * pipes last, so what sits in a pipe mouth is hidden behind it rather than on top of it
    const order = [...prepared.filter((p) => p.o.type !== 9), ...prepared.filter((p) => p.o.type === 9)];

    for (const { o, cells, sprite } of order) {
      const flip = o.transform === 1 || o.childTransform === 1;
      const img = o.block ? block : mon;

      if (cells.length && img) {
        if (o.wing > 0 && !o.block) drawWings(ctx, mon, o, cells, H);
        for (const c of cells) {
          const [dx, dy, dw, dh] = dest(o, c, H);
          if (dx + dw < 0 || dx > W || dy + dh < 0 || dy > H) continue;
          ctx.globalAlpha = c[4];
          blit(ctx, img, c[2] * T, c[3] * T, dx, dy, dw, dh, flip);
        }
        ctx.globalAlpha = 1;
      } else if (sprite) {
        drawSingle(ctx, sprite, o, H, W);
      } else {
        drawBox(ctx, o, H);
      }
    }

    if (options.pipes && options.links) {
      for (const { o, cells } of prepared) {
        if (o.type === 9 && pipeNo(o) && cells.length) drawPipeLabel(ctx, o, cells, pipeLabel(pipeNo(o), options.area, options.links), H);
      }
    }
  }

  function blit(ctx, img, sx, sy, dx, dy, dw, dh, flip) {
    if (!flip) { ctx.drawImage(img, sx, sy, T, T, dx, dy, dw, dh); return; }
    ctx.save();
    ctx.translate(dx + dw, 0);
    ctx.scale(-1, 1);
    ctx.drawImage(img, sx, sy, T, T, 0, dy, dw, dh);
    ctx.restore();
  }

  function drawWings(ctx, mon, o, cells, H) {
    if (!mon) return;
    const rects = tileRects(o, cells);
    const minX = Math.min(...rects.map((r) => r[0])), maxX = Math.max(...rects.map((r) => r[0] + r[2]));
    const minY = Math.min(...rects.map((r) => r[1])), maxY = Math.max(...rects.map((r) => r[1] + r[3]));
    const y = minY + Math.max(0, maxY - minY - 2);
    ctx.drawImage(mon, 0, 2 * T, T, T, (minX - 1) * T, H - (y + 1) * T, T, T);
    ctx.drawImage(mon, 1 * T, 2 * T, T, T, maxX * T, H - (y + 1) * T, T, T);
  }

  function drawSingle(ctx, img, o, H, W) {
    const wb = Math.max(1, o.w), hb = Math.max(1, o.h);
    const w = o.block ? wb : Math.max(wb, o.size), h = o.block ? hb : Math.max(hb, o.size === 2 ? 2 : 1);
    const maxW = w * T, maxH = Math.max(h * T, T), aspect = img.width / img.height;
    let dh = maxH, dw = dh * aspect;
    if (dw > maxW * 1.8) { dw = maxW * 1.8; dh = dw / aspect; }
    const x = o.x * T + (maxW - dw) / 2, y = H - (o.y + h) * T + (maxH - dh);
    if (x + dw < 0 || x > W || y + dh < 0 || y > H) return;
    ctx.drawImage(img, x, y, dw, dh);
  }

  function drawBox(ctx, o, H) {
    const w = Math.max(1, o.w) * T, h = Math.max(1, o.h) * T, x = o.x * T, y = H - (o.y + Math.max(1, o.h)) * T;
    ctx.fillStyle = o.block ? '#E8B354' : '#D95763';
    ctx.fillRect(x, y, w, h);
    ctx.strokeStyle = '#2C1600';
    ctx.strokeRect(x + .5, y + .5, w - 1, h - 1);
    ctx.fillStyle = '#fff';
    ctx.font = '9px sans-serif';
    ctx.fillText(String(o.type), x + 2, y + 10);
  }

  return { draw, pipeLinks };
})();

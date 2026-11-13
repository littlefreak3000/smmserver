// Literal, label-for-label port of Ash::decompress() from SMMDB/ASH0/Ash.cpp.
// Used ONLY as an independent oracle for tools/ash0.mjs (no cleanup on purpose:
// the structure and register names follow the reference so it can be diffed by eye).

export function refDecompress(inp) {
  if (!(inp.length > 0x10 && inp.toString('latin1', 0, 3) === 'ASH')) throw new Error('not ASH');
  const u32 = (x) => x >>> 0;
  const rdIn = (off) => (off + 4 <= inp.length ? inp.readUInt32BE(off) : 0); // reference reads past the end (UB); treat as 0
  const ram = Buffer.alloc(0x100000 + 0x10000);           // "crap3"
  const rd16 = (a) => ram.readUInt16BE(a);
  const wr16 = (a, v) => ram.writeUInt16BE(v & 0xFFFF, a);
  const r = new Array(32).fill(0);

  r[5] = inp.readUInt32BE(4) & 0x00FFFFFF;
  const size = r[5];
  const out = Buffer.alloc(size);
  r[3] = 0;                         // output index
  r[4] = 0;                         // input base (inDiff folded in)

  r[24] = 0x10;
  r[28] = inp.readUInt32BE(8);
  r[25] = 0; r[29] = 0;
  r[26] = inp.readUInt32BE(0xC);
  r[30] = rdIn(r[28]);
  r[28] = r[28] + 4;

  r[8] = 0; r[9] = r[8] + 0x07FE; r[10] = r[9] + 0x07FE; r[11] = r[10] + 0x1FFE; r[31] = r[11] + 0x1FFE;
  r[23] = 0x200; r[22] = 0x200; r[27] = 0;

  let t, count, pc = 'L2124';
  for (;;) {
    switch (pc) {
      case 'L2124':
        if (r[25] !== 0x1F) { pc = 'L2140'; break; }
        r[0] = r[26] >>> 31; r[26] = rdIn(r[24]); r[25] = 0; r[24] += 4; pc = 'L214C'; break;
      case 'L2140':
        r[0] = r[26] >>> 31; r[25] += 1; r[26] = u32(r[26] << 1); pc = 'L214C'; break;
      case 'L214C':
        if (r[0] === 0) { pc = 'L2174'; break; }
        wr16(r[31], r[23] | 0x8000); wr16(r[31] + 2, r[23] | 0x4000);
        r[31] += 4; r[27] += 2; r[23] += 1; r[22] += 1; pc = 'L2124'; break;
      case 'L2174':
        r[12] = 9; r[21] = r[25] + r[12]; t = r[21];
        if (r[21] > 0x20) { pc = 'L21AC'; break; }
        r[21] = 0x20 - r[12];
        r[6] = r[26] >>> r[21];
        if (t === 0x20) { pc = 'L219C'; break; }
        r[26] = u32(r[26] << r[12]); r[25] += r[12]; pc = 'L21D0'; break;
      case 'L219C':
        r[26] = rdIn(r[24]); r[25] = 0; r[24] += 4; pc = 'L21D0'; break;
      case 'L21AC':
        r[0] = 0x20 - r[12];
        r[6] = r[26] >>> r[0];
        r[26] = rdIn(r[24]);
        r[0] = 0x40 - r[21];
        r[24] += 4;
        r[0] = r[26] >>> r[0];
        r[6] = u32(r[6] | r[0]);
        r[25] = r[21] - 0x20;
        r[26] = u32(r[26] << r[25]); pc = 'L21D0'; break;
      case 'L21D0':
        r[12] = rd16(r[31] - 2); r[31] -= 2; r[27] -= 1;
        r[0] = r[12] & 0x8000; r[12] = (r[12] & 0x1FFF) << 1;
        if (r[0] === 0) { pc = 'L21F8'; break; }
        wr16(r[9] + r[12], r[6]);
        r[6] = (r[12] & 0x3FFF) >>> 1;
        pc = r[27] !== 0 ? 'L21D0' : 'L2204'; break;
      case 'L21F8':
        wr16(r[8] + r[12], r[6]); r[23] = r[22]; pc = 'L2124'; break;
      case 'L2204':
        r[23] = 0x800; r[22] = 0x800; pc = 'L220C'; break;
      case 'L220C':
        if (r[29] !== 0x1F) { pc = 'L2228'; break; }
        r[0] = r[30] >>> 31; r[30] = rdIn(r[28]); r[29] = 0; r[28] += 4; pc = 'L2234'; break;
      case 'L2228':
        r[0] = r[30] >>> 31; r[29] += 1; r[30] = u32(r[30] << 1); pc = 'L2234'; break;
      case 'L2234':
        if (r[0] === 0) { pc = 'L225C'; break; }
        wr16(r[31], r[23] | 0x8000); wr16(r[31] + 2, r[23] | 0x4000);
        r[31] += 4; r[27] += 2; r[23] += 1; r[22] += 1; pc = 'L220C'; break;
      case 'L225C':
        r[12] = 0xB; r[21] = r[29] + r[12]; t = r[21];
        if (r[21] > 0x20) { pc = 'L2294'; break; }
        r[21] = 0x20 - r[12];
        r[7] = r[30] >>> r[21];
        if (t === 0x20) { pc = 'L2284'; break; }
        r[30] = u32(r[30] << r[12]); r[29] += r[12]; pc = 'L22B8'; break;
      case 'L2284':
        r[30] = rdIn(r[28]); r[29] = 0; r[28] += 4; pc = 'L22B8'; break;
      case 'L2294':
        r[0] = 0x20 - r[12];
        r[7] = r[30] >>> r[0];
        r[30] = rdIn(r[28]);
        r[0] = 0x40 - r[21];
        r[28] += 4;
        r[0] = r[30] >>> r[0];
        r[7] = u32(r[7] | r[0]);
        r[29] = r[21] - 0x20;
        r[30] = u32(r[30] << r[29]); pc = 'L22B8'; break;
      case 'L22B8':
        r[12] = rd16(r[31] - 2); r[31] -= 2; r[27] -= 1;
        r[0] = r[12] & 0x8000; r[12] = (r[12] & 0x1FFF) << 1;
        if (r[0] === 0) { pc = 'L22E0'; break; }
        wr16(r[11] + r[12], r[7]);
        r[7] = (r[12] & 0x3FFF) >>> 1;
        pc = r[27] !== 0 ? 'L22B8' : 'L22EC'; break;
      case 'L22E0':
        wr16(r[10] + r[12], r[7]); r[23] = r[22]; pc = 'L220C'; break;
      case 'L22EC':
        r[0] = r[5]; pc = 'L22F0'; break;
      case 'L22F0':
        r[12] = r[6]; pc = 'L22F4'; break;
      case 'L22F4':
        if (r[12] < 0x200) { pc = 'L233C'; break; }
        if (r[25] !== 0x1F) { pc = 'L2318'; break; }
        r[31] = r[26] >>> 31; r[26] = rdIn(r[24]); r[24] += 4; r[25] = 0; pc = 'L2324'; break;
      case 'L2318':
        r[31] = r[26] >>> 31; r[25] += 1; r[26] = u32(r[26] << 1); pc = 'L2324'; break;
      case 'L2324':
        r[27] = r[12] << 1;
        r[12] = r[31] !== 0 ? rd16(r[9] + r[27]) : rd16(r[8] + r[27]);
        pc = 'L22F4'; break;
      case 'L233C':
        if (r[12] >= 0x100) { pc = 'L235C'; break; }
        out[r[3]] = r[12]; r[3] += 1; r[5] -= 1;
        pc = r[5] !== 0 ? 'L22F0' : 'L2434'; break;
      case 'L235C':
        r[23] = r[7]; pc = 'L2360'; break;
      case 'L2360':
        if (r[23] < 0x800) { pc = 'L23A8'; break; }
        if (r[29] !== 0x1F) { pc = 'L2384'; break; }
        r[31] = r[30] >>> 31; r[30] = rdIn(r[28]); r[28] += 4; r[29] = 0; pc = 'L2390'; break;
      case 'L2384':
        r[31] = r[30] >>> 31; r[29] += 1; r[30] = u32(r[30] << 1); pc = 'L2390'; break;
      case 'L2390':
        r[27] = r[23] << 1;
        r[23] = r[31] !== 0 ? rd16(r[11] + r[27]) : rd16(r[10] + r[27]);
        pc = 'L2360'; break;
      case 'L23A8': {
        r[12] = r[12] - 0xFD;
        r[23] = r[3] - r[23];
        r[5] = r[5] - r[12];
        r[31] = r[12] >>> 3;
        if (r[31] === 0) { pc = 'L2414'; break; }
        count = r[31];
        do {
          for (let i = 0; i < 8; i++) out[r[3] + i] = out[r[23] - 1 + i];
          r[23] += 8; r[3] += 8;
        } while (--count);
        r[12] = r[12] & 7;
        pc = r[12] === 0 ? 'L242C' : 'L2414'; break;
      }
      case 'L2414':
        count = r[12];
        do { out[r[3]] = out[r[23] - 1]; r[23] += 1; r[3] += 1; } while (--count);
        pc = 'L242C'; break;
      case 'L242C':
        pc = r[5] !== 0 ? 'L22F0' : 'L2434'; break;
      case 'L2434':
        return out.subarray(0, r[0]);
      default: throw new Error('bad label ' + pc);
    }
    if (r[5] < 0) throw new Error('reference decoder overran output');
  }
}

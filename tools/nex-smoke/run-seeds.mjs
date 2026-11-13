#!/usr/bin/env node
// End-to-end check of the whole-archive index and challenge seeds against a real server process.
//
// Starts the server on a throwaway data folder that holds ONLY an index (no levels), with the HTTP services bound to
// 127.0.0.1, then runs nex_live.py --seeds: two players log in over the real NEX protocol and ask for the same
// difficulty range with and without seeds. Levels in the lists are downloaded for real from the Wayback Machine (a
// few dozen small files at the normal, polite request rate), so this needs internet access.
//
//   node run-seeds.mjs --index ../../samples/live-index.sqlite
//
// Env: SMM_SERVER_EXE (default ../../smm-server.exe), SMM_PYTHON (default C:/Users/<you>/sdk/python312/python.exe)
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { parseArgs } from 'node:util';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { values } = parseArgs({ options: { index: { type: 'string' } } });
if (!values.index) { console.error('usage: run-seeds.mjs --index <index.sqlite>'); process.exit(2); }

const exe = path.resolve(process.env.SMM_SERVER_EXE ?? path.join(here, '..', '..', 'smm-server.exe'));
const python = process.env.SMM_PYTHON ?? path.join(os.homedir(), 'sdk', 'python312', 'python.exe');
for (const f of [exe, python, values.index]) if (!fs.existsSync(f)) { console.error(`missing: ${f}`); process.exit(2); }

const freePort = () => new Promise((ok) => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => ok(p)); }); });
const [account, s3, auth, secure] = [await freePort(), await freePort(), await freePort(), await freePort()];

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'smm-seeds-'));
fs.mkdirSync(path.join(tmp, 'data'));
fs.copyFileSync(values.index, path.join(tmp, 'data', 'index.sqlite'));

const server = spawn(exe, [], {
  cwd: tmp,
  env: {
    ...process.env, SMM_DATA_DIR: path.join(tmp, 'data'), SMM_PUBLIC_HOST: '127.0.0.1', SMM_BIND_HOST: '127.0.0.1',
    SMM_ACCOUNT_PORT: String(account), SMM_S3_PORT: String(s3),
    PN_SMM_AUTHENTICATION_SERVER_PORT: String(auth), PN_SMM_SECURE_SERVER_PORT: String(secure),
  },
});
let log = '';
server.stdout.on('data', (d) => (log += d));
server.stderr.on('data', (d) => (log += d));

const up = async () => { for (let i = 0; i < 100; i++) { try { await fetch(`http://127.0.0.1:${account}/`); return true; } catch { await new Promise((r) => setTimeout(r, 100)); } } return false; };
let code = 1;
try {
  if (!(await up())) throw new Error('server did not start:\n' + log);
  code = await new Promise((ok) => spawn(python, ['-I', path.join(here, 'nex_live.py'), '--account', `http://127.0.0.1:${account}`, '--seeds'], { stdio: 'inherit' }).on('exit', ok));
} finally {
  server.kill();
  await new Promise((r) => setTimeout(r, 500));
  console.log('\n--- server log (last 40 lines) ---\n' + log.replace(/\x1b\[[0-9;]*m/g, '').split('\n').slice(-40).join('\n'));
  fs.rmSync(tmp, { recursive: true, force: true });
}
process.exit(code);

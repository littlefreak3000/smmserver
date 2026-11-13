#!/usr/bin/env node
// Start the server on a throwaway copy of an archive (HTTP services bound to 127.0.0.1) and run nex_smoke.py
// against it. Needs a built server, the Python from sdk/python312 with nintendoclients, and an archive that
// contains the level.
//
//   node run-smoke.mjs --archive ../../archive --level 942697 --name "Level name"
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
const { values } = parseArgs({ options: { archive: { type: 'string' }, level: { type: 'string' }, 'prudp-minor': { type: 'string' }, name: { type: 'string' }, 'ranking-apps': { type: 'string' } } });
if (!values.archive || !values.level || !values.name) { console.error('usage: run-smoke.mjs --archive <dir> --level <id> --name <expected name> [--ranking-apps 0,2400]'); process.exit(2); }

const exe = path.resolve(process.env.SMM_SERVER_EXE ?? path.join(here, '..', '..', 'smm-server.exe'));
const python = process.env.SMM_PYTHON ?? path.join(os.homedir(), 'sdk', 'python312', 'python.exe');
const binFile = path.join(values.archive, 'courses', `${values.level}.bin`);
for (const f of [exe, python, binFile]) if (!fs.existsSync(f)) { console.error(`missing: ${f}`); process.exit(2); }

const freePort = () => new Promise((ok) => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => ok(p)); }); });
const [account, s3, auth, secure] = [await freePort(), await freePort(), await freePort(), await freePort()];

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'smm-smoke-'));
fs.cpSync(values.archive, path.join(tmp, 'data'), { recursive: true, filter: (p) => !/\.sqlite-(wal|shm)$/.test(p) });

const server = spawn(exe, [], {
  cwd: tmp,
  env: {
    ...process.env, SMM_DATA_DIR: path.join(tmp, 'data'), SMM_PUBLIC_HOST: '127.0.0.1', SMM_BIND_HOST: '127.0.0.1', SMM_FETCH: '0', // the smoke test must not touch the public archive services
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
  const args = ['-I', path.join(here, 'nex_smoke.py'), '--account', `http://127.0.0.1:${account}`, '--level', values.level, '--name', values.name, '--bin', binFile];
  if (values['prudp-minor']) args.push('--prudp-minor', values['prudp-minor']);
  if (values['ranking-apps']) args.push('--ranking-apps', ...values['ranking-apps'].split(','));
  code = await new Promise((ok) => spawn(python, args, { stdio: 'inherit' }).on('exit', ok));
} finally {
  server.kill();
  await new Promise((r) => setTimeout(r, 300));
  if (code !== 0) { console.log('\n--- server log (last 60 lines) ---\n' + log.replace(/\x1b\[[0-9;]*m/g, '').split('\n').slice(-60).join('\n')); }
  fs.rmSync(tmp, { recursive: true, force: true });
}
process.exit(code);

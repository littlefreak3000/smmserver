#!/usr/bin/env node
// Build distributable zips of the server: one self-contained executable per platform plus a quickstart.
//
//   node tools/build-release.mjs [windows-amd64 linux-amd64 ...]      (default: all targets)
//
// Output: dist/smm-server-<target>.zip   (and dist/smm-server-<target>/ unpacked)
// Needs only Go (to build). The result needs nothing: no Go, no Python, no Node, no database, no S3.
// Env: GO = path to the go executable (default: ~/sdk/go/bin/go[.exe] if present, else "go on PATH").
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.join(here, '..');
const serverDir = path.join(root, 'server');
const dist = path.join(root, 'dist');

export const TARGETS = {
  'windows-amd64': { goos: 'windows', goarch: 'amd64', exe: 'smm-server.exe' },
  'linux-amd64': { goos: 'linux', goarch: 'amd64', exe: 'smm-server' },
  'linux-arm64': { goos: 'linux', goarch: 'arm64', exe: 'smm-server' }, // Raspberry Pi 4/5, many NAS boxes
  'darwin-arm64': { goos: 'darwin', goarch: 'arm64', exe: 'smm-server' }, // Apple silicon
  'darwin-amd64': { goos: 'darwin', goarch: 'amd64', exe: 'smm-server' },
};

const goBin = (() => {
  if (process.env.GO) return process.env.GO;
  const local = path.join(os.homedir(), 'sdk', 'go', 'bin', process.platform === 'win32' ? 'go.exe' : 'go');

  return fs.existsSync(local) ? local : 'go';
})();

const quickstart = (t) => `Super Mario Maker home server - quick start
==========================================

What this is: a small server that stands in for the old Super Mario Maker (Wii U) online service, with an archive
of old levels. It is a single program: nothing else to install.

1. START IT
   ${t.goos === 'windows' ? 'Double-click smm-server.exe.' : 'In a terminal: chmod +x ./smm-server && ./smm-server'}
${t.goos === 'windows' ? `   - Windows may say "Windows protected your PC" because the program is not code-signed: click "More info", then "Run anyway".
   - Windows Firewall will ask for permission: tick "Private networks" and allow it. Without this, consoles cannot connect.
` : t.goos === 'darwin' ? `   - macOS may block it the first time: System Settings > Privacy & Security > "Open Anyway".
` : `   - If a firewall is active, allow TCP 8080 and 9000 and UDP 60000 and 60001 on your home network.
`}
2. LEAVE THE WINDOW OPEN
   It prints a box saying the server is running and showing this computer's address. The first start downloads about 100
   levels in the background (roughly 5 minutes); a few more arrive slowly while it keeps running.
   Everything it needs is created next to the program in a folder called "data".

3. CONNECT CEMU
   The full step-by-step guide is docs${t.goos === 'windows' ? '\\' : '/'}setup-cemu.md in this folder. In short:
   - Cemu 2.6 with account files dumped from your own Wii U (otp.bin, seeprom.bin, certificates, Mii resources, account).
     That is a Cemu requirement, not this server's. The server never checks them or any password.
   - Copy docs${t.goos === 'windows' ? '\\' : '/'}cemu${t.goos === 'windows' ? '\\' : '/'}SMM_MiiFaceFix into Cemu's graphicPacks folder (without it Miis have no faces in Cemu 2.6).
   - Copy data${t.goos === 'windows' ? '\\' : '/'}network_services.xml next to Cemu's settings.xml.
   - Run "${t.exe} account <path to the account.dat>" (or "${t.exe} account new <name> <path to Cemu's mlc01>") so the account's
     player ID matches this server. Close Cemu first.
   - In Cemu: Options > General settings > Account > Network Service: Custom.
   A modded Wii U (Inkay) is described in docs${t.goos === 'windows' ? '\\' : '/'}setup-wiiu.md; that route has not been tried on a console yet.

4. STOP IT
   Press Ctrl+C in the window. This saves the level list. Copy or move the "data" folder only while the server is stopped.

CHANGING THINGS
   data${t.goos === 'windows' ? '\\' : '/'}settings.env  (commented; remove the # in front of a line, save, restart). Useful settings:
   - SMM_PUBLIC_HOST   if the wrong network address was picked (must be an IPv4 address for Cemu)
   - SMM_FETCH=0       fully offline: only the levels already in the data folder are available
   - SMM_POOL_TARGET   how many levels to keep ready

PLEASE KNOW
   - There are NO passwords. Anyone who can reach this computer on your network can log in. Keep it on your home network;
     do not forward its ports on your router.
   - Levels are downloaded from volunteer-run public services (api.bobac-analytics.com and web.archive.org) at a low rate,
     from this computer's address. SMM_FETCH=0 turns that off.
   - Status: works in Cemu 2.6 (Course World, 100 Mario Challenge, creator Miis and names, clear screens). Not tested on a
     real Wii U yet. Flags are a regional guess: the archive does not say where creators live.
   - Licence: AGPL-3.0 (see LICENSE). If you pass this program on, you must also offer the source code.
`;

export function build(name) {
  const t = TARGETS[name];
  if (!t) throw new Error(`unknown target ${name}; choose from ${Object.keys(TARGETS).join(', ')}`);

  const dir = path.join(dist, `smm-server-${name}`);
  fs.rmSync(dir, { recursive: true, force: true });
  fs.mkdirSync(dir, { recursive: true });

  const started = Date.now();
  const r = spawnSync(goBin, ['build', '-trimpath', '-ldflags', '-s -w', '-o', path.join(dir, t.exe), '.'], {
    cwd: serverDir,
    env: { ...process.env, CGO_ENABLED: '0', GOOS: t.goos, GOARCH: t.goarch, GOTOOLCHAIN: 'local' },
    encoding: 'utf8',
  });

  if (r.status !== 0) throw new Error(`go build failed for ${name}:\n${r.stdout}${r.stderr}`);

  fs.writeFileSync(path.join(dir, 'QUICKSTART.txt'), quickstart(t).replace(/\r?\n/g, t.goos === 'windows' ? '\r\n' : '\n'));
  fs.copyFileSync(path.join(serverDir, 'LICENSE'), path.join(dir, 'LICENSE'));

  // * the Cemu guide and the Mii face fix graphic pack it refers to
  fs.mkdirSync(path.join(dir, 'docs'), { recursive: true });
  fs.copyFileSync(path.join(root, 'docs', 'setup-cemu.md'), path.join(dir, 'docs', 'setup-cemu.md'));
  fs.copyFileSync(path.join(root, 'docs', 'setup-wiiu.md'), path.join(dir, 'docs', 'setup-wiiu.md'));
  fs.copyFileSync(path.join(root, 'docs', 'cache.md'), path.join(dir, 'docs', 'cache.md'));
  fs.cpSync(path.join(root, 'docs', 'cemu'), path.join(dir, 'docs', 'cemu'), { recursive: true });

  const zip = path.join(dist, `smm-server-${name}.zip`);
  fs.rmSync(zip, { force: true });

  const z = process.platform === 'win32'
    ? spawnSync('powershell.exe', ['-NoProfile', '-Command', `Compress-Archive -Path '${dir}\\*' -DestinationPath '${zip}'`], { encoding: 'utf8' })
    : spawnSync('zip', ['-qr', zip, '.'], { cwd: dir, encoding: 'utf8' });

  if (z.status !== 0) throw new Error(`zip failed for ${name}: ${z.stderr}`);

  return { name, seconds: Math.round((Date.now() - started) / 1000), exeMB: (fs.statSync(path.join(dir, t.exe)).size / 1e6).toFixed(1), zipMB: (fs.statSync(zip).size / 1e6).toFixed(1), zip };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const wanted = process.argv.slice(2).length ? process.argv.slice(2) : Object.keys(TARGETS);
  fs.mkdirSync(dist, { recursive: true });

  let failed = 0;

  for (const name of wanted) {
    try {
      const r = build(name);
      console.log(`${r.name.padEnd(14)} exe ${r.exeMB.padStart(5)} MB  zip ${r.zipMB.padStart(5)} MB  (${r.seconds}s)  ${path.relative(root, r.zip)}`);
    } catch (e) {
      failed++;
      console.error(`${name}: ${e.message}`);
    }
  }

  process.exit(failed ? 1 : 0);
}

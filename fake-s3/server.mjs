#!/usr/bin/env node
// Fake S3 for the Super Mario Maker preservation server.
//
// What the SMM server / console actually need from "S3" (see README.md):
//   * GET  presigned object URL   -> console downloads a course (<id>.bin) or preview (<id>.jpg)
//   * HEAD object                 -> server stats size before/after upload
//   * POST presigned form upload  -> console uploads a course / preview image
// Signatures are NOT verified; the point is to serve preserved data to old hardware.
//
// Storage model (flat, by file name):
//   preserved dir  read-only archive      <id>.bin  <id>.jpg
//   upload dir     writable, new uploads  <id>.bin  <id>.jpg   (checked first on reads)
// Only the final path segment of a key is used, so "bucket/prefix/123.bin",
// "prefix/123.bin" and "123.bin" are all the same object. This matters because the
// upstream server builds keys inconsistently (some with PN_SMM_CONFIG_S3_KEY_BASE,
// some without).

import http from 'node:http';
import https from 'node:https';
import fs from 'node:fs';
import fsp from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import { pathToFileURL } from 'node:url';

const MIME = { '.bin': 'application/octet-stream', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg' };

export function loadConfig(env = process.env) {
  const num = (v, d) => (v === undefined || v === '' ? d : Number(v));
  return {
    host: env.FAKE_S3_HOST || '0.0.0.0',
    httpPort: num(env.FAKE_S3_HTTP_PORT, 9000),          // 0 = random, -1 = disabled
    httpsPort: num(env.FAKE_S3_HTTPS_PORT, -1),
    tlsCert: env.FAKE_S3_TLS_CERT || '',
    tlsKey: env.FAKE_S3_TLS_KEY || '',
    preservedDir: path.resolve(env.FAKE_S3_PRESERVED_DIR || './data/preserved'),
    uploadDir: path.resolve(env.FAKE_S3_UPLOAD_DIR || './data/uploads'),
    maxUpload: num(env.FAKE_S3_MAX_UPLOAD, 16 * 1024 * 1024),
    quiet: env.FAKE_S3_QUIET === '1',
  };
}

// ---- helpers ---------------------------------------------------------------

const xmlEsc = (s) => String(s).replace(/[<>&'"]/g, (c) => ({ '<': '&lt;', '>': '&gt;', '&': '&amp;', "'": '&apos;', '"': '&quot;' }[c]));

function sendXmlError(res, status, code, message, resource = '') {
  const body = `<?xml version="1.0" encoding="UTF-8"?>\n<Error><Code>${code}</Code><Message>${xmlEsc(message)}</Message><Resource>${xmlEsc(resource)}</Resource><RequestId>0</RequestId></Error>`;
  res.writeHead(status, { 'Content-Type': 'application/xml', 'Content-Length': Buffer.byteLength(body), Server: 'AmazonS3' });
  res.end(res.req.method === 'HEAD' ? undefined : body);
}

/** Reduce any key to a safe flat file name, or null. */
export function keyToName(key) {
  const base = String(key).split('/').filter(Boolean).pop();
  if (!base || base === '.' || base === '..' || !/^[A-Za-z0-9_.-]{1,128}$/.test(base)) return null;
  return base;
}

/** Works for path-style (/bucket/key) and virtual-host style (/key). */
export function parseTarget(pathname) {
  let segs;
  try { segs = decodeURIComponent(pathname).split('/').filter(Boolean); } catch { return { kind: 'bad' }; }
  if (segs.length === 0) return { kind: 'service' };
  // A lone segment without a dot is a bucket (object names here always have an extension).
  if (segs.length === 1 && !segs[0].includes('.')) return { kind: 'bucket', bucket: segs[0] };
  return { kind: 'object', key: segs.join('/') };
}

async function statFile(cfg, name) {
  for (const dir of [cfg.uploadDir, cfg.preservedDir]) {
    const file = path.join(dir, name);
    try {
      const st = await fsp.stat(file);
      if (st.isFile()) return { file, st };
    } catch { /* try next */ }
  }
  return null;
}

const etagCache = new Map();
async function etagFor(file, st) {
  const k = `${file}:${st.size}:${st.mtimeMs}`;
  if (etagCache.has(k)) return etagCache.get(k);
  const h = crypto.createHash('md5');
  await new Promise((ok, bad) => fs.createReadStream(file).on('data', (d) => h.update(d)).on('end', ok).on('error', bad));
  const e = `"${h.digest('hex')}"`;
  etagCache.set(k, e);
  return e;
}

async function readBody(req, limit) {
  const chunks = []; let n = 0;
  for await (const c of req) {
    n += c.length;
    if (n > limit) throw Object.assign(new Error('too large'), { status: 413 });
    chunks.push(c);
  }
  return Buffer.concat(chunks);
}

async function writeUpload(cfg, name, buf) {
  await fsp.mkdir(cfg.uploadDir, { recursive: true });
  const tmp = path.join(cfg.uploadDir, `.${name}.${process.pid}.tmp`);
  await fsp.writeFile(tmp, buf);
  await fsp.rename(tmp, path.join(cfg.uploadDir, name));
  return `"${crypto.createHash('md5').update(buf).digest('hex')}"`;
}

async function objectHeaders(cfg, name, f) {
  return {
    'Content-Type': MIME[path.extname(name).toLowerCase()] || 'application/octet-stream',
    ETag: await etagFor(f.file, f.st),
    'Last-Modified': f.st.mtime.toUTCString(),
    'Accept-Ranges': 'bytes',
    Server: 'AmazonS3',
    'x-amz-request-id': '0',
  };
}

// ---- request handling ------------------------------------------------------

async function handleObject(cfg, req, res, url, key) {
  const name = keyToName(key);
  if (!name) return sendXmlError(res, 400, 'InvalidArgument', 'Invalid object key', key);

  switch (req.method) {
    case 'GET':
    case 'HEAD': {
      const f = await statFile(cfg, name);
      if (!f) return sendXmlError(res, 404, 'NoSuchKey', 'The specified key does not exist.', '/' + key);
      const headers = await objectHeaders(cfg, name, f);
      let start = 0, end = f.st.size - 1, status = 200;
      const m = /^bytes=(\d*)-(\d*)$/.exec(req.headers.range || '');
      if (m && (m[1] || m[2]) && f.st.size > 0) {
        if (m[1] === '') { start = Math.max(0, f.st.size - Number(m[2])); }
        else { start = Number(m[1]); if (m[2] !== '') end = Math.min(end, Number(m[2])); }
        if (start > end || start >= f.st.size) {
          res.writeHead(416, { 'Content-Range': `bytes */${f.st.size}` });
          return res.end();
        }
        status = 206;
        headers['Content-Range'] = `bytes ${start}-${end}/${f.st.size}`;
      }
      headers['Content-Length'] = f.st.size === 0 ? 0 : end - start + 1;
      res.writeHead(status, headers);
      if (req.method === 'HEAD' || f.st.size === 0) return res.end();
      return fs.createReadStream(f.file, { start, end }).pipe(res);
    }
    case 'PUT': {
      const etag = await writeUpload(cfg, name, await readBody(req, cfg.maxUpload));
      res.writeHead(200, { ETag: etag, Server: 'AmazonS3', 'Content-Length': 0 });
      return res.end();
    }
    case 'DELETE': {
      // Only uploads can be deleted; the preserved archive is read-only. S3 returns 204 either way.
      await fsp.rm(path.join(cfg.uploadDir, name), { force: true });
      res.writeHead(204, { Server: 'AmazonS3' });
      return res.end();
    }
    case 'POST': {
      // Browser-style presigned POST: multipart/form-data with "key", policy fields and "file".
      const ct = req.headers['content-type'] || '';
      if (!/^multipart\/form-data/i.test(ct)) return sendXmlError(res, 400, 'InvalidArgument', 'POST requires multipart/form-data', '/' + key);
      const body = await readBody(req, cfg.maxUpload + 64 * 1024);
      let form;
      try { form = await new Request('http://x/', { method: 'POST', headers: { 'content-type': ct }, body }).formData(); }
      catch { return sendXmlError(res, 400, 'MalformedPOSTRequest', 'Could not parse multipart body', '/' + key); }
      const file = form.get('file');
      if (!file || typeof file === 'string') return sendXmlError(res, 400, 'InvalidArgument', 'POST requires exactly one file upload per request.', '/' + key);
      // The key field is authoritative (it is what the server signed); fall back to the URL.
      const formKey = form.get('key');
      const target = keyToName(typeof formKey === 'string' ? formKey.replace('${filename}', file.name || '') : key);
      if (!target) return sendXmlError(res, 400, 'InvalidArgument', 'Invalid object key', '/' + key);
      const etag = await writeUpload(cfg, target, Buffer.from(await file.arrayBuffer()));
      const status = Number(form.get('success_action_status')) || 204;
      const headers = { ETag: etag, Server: 'AmazonS3', Location: url.origin + '/' + target };
      if (status === 201) {
        const out = `<?xml version="1.0" encoding="UTF-8"?>\n<PostResponse><Location>${xmlEsc(headers.Location)}</Location><Key>${xmlEsc(target)}</Key><ETag>${xmlEsc(etag)}</ETag></PostResponse>`;
        res.writeHead(201, { ...headers, 'Content-Type': 'application/xml', 'Content-Length': Buffer.byteLength(out) });
        return res.end(out);
      }
      res.writeHead(status === 200 ? 200 : 204, { ...headers, 'Content-Length': 0 });
      return res.end();
    }
    default:
      return sendXmlError(res, 405, 'MethodNotAllowed', 'The specified method is not allowed against this resource.', '/' + key);
  }
}

async function handleBucket(cfg, req, res, url, bucket) {
  if (req.method === 'POST') {
    // Presigned POST URLs point at the bucket itself (e.g. https://host/bucket); the key is in the form.
    return handleObject(cfg, req, res, url, `${bucket}/__form__`);
  }
  if (req.method === 'HEAD') { res.writeHead(200, { Server: 'AmazonS3' }); return res.end(); }
  if (req.method !== 'GET') return sendXmlError(res, 405, 'MethodNotAllowed', 'Method not allowed', '/' + bucket);
  if (url.searchParams.has('location')) {
    const x = '<?xml version="1.0" encoding="UTF-8"?>\n<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></LocationConstraint>';
    res.writeHead(200, { 'Content-Type': 'application/xml', 'Content-Length': Buffer.byteLength(x) });
    return res.end(x);
  }
  // Minimal ListObjectsV2 / ListObjects (flat; prefix matched against the file name).
  const prefix = (url.searchParams.get('prefix') || '').split('/').pop();
  const seen = new Map();
  for (const dir of [cfg.preservedDir, cfg.uploadDir]) {
    let ents = [];
    try { ents = await fsp.readdir(dir); } catch { /* dir may not exist */ }
    for (const n of ents) if (!n.startsWith('.') && n.startsWith(prefix)) seen.set(n, path.join(dir, n));
  }
  const max = Math.min(Number(url.searchParams.get('max-keys')) || 1000, 1000);
  const names = [...seen.keys()].sort().slice(0, max);
  let items = '';
  for (const n of names) {
    const st = await fsp.stat(seen.get(n));
    items += `<Contents><Key>${xmlEsc(n)}</Key><LastModified>${st.mtime.toISOString()}</LastModified><ETag>${xmlEsc(await etagFor(seen.get(n), st))}</ETag><Size>${st.size}</Size><StorageClass>STANDARD</StorageClass></Contents>`;
  }
  const out = `<?xml version="1.0" encoding="UTF-8"?>\n<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>${xmlEsc(bucket)}</Name><Prefix>${xmlEsc(url.searchParams.get('prefix') || '')}</Prefix><KeyCount>${names.length}</KeyCount><MaxKeys>${max}</MaxKeys><IsTruncated>false</IsTruncated>${items}</ListBucketResult>`;
  res.writeHead(200, { 'Content-Type': 'application/xml', 'Content-Length': Buffer.byteLength(out) });
  res.end(out);
}

export function createHandler(cfg) {
  return async (req, res) => {
    const t0 = Date.now();
    res.req = req;
    res.on('finish', () => {
      if (!cfg.quiet) console.log(`${new Date().toISOString()} ${req.socket.remoteAddress} ${req.method} ${req.url.split('?')[0]} -> ${res.statusCode} (${Date.now() - t0}ms)`);
    });
    try {
      const url = new URL(req.url, `http://${req.headers.host || 'localhost'}`);
      const t = parseTarget(url.pathname);
      if (t.kind === 'bad') return sendXmlError(res, 400, 'InvalidURI', 'Could not parse the specified URI.');
      if (t.kind === 'service') {
        const x = '<?xml version="1.0" encoding="UTF-8"?>\n<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Buckets></Buckets></ListAllMyBucketsResult>';
        res.writeHead(200, { 'Content-Type': 'application/xml', 'Content-Length': Buffer.byteLength(x) });
        return res.end(req.method === 'HEAD' ? undefined : x);
      }
      if (t.kind === 'bucket') return await handleBucket(cfg, req, res, url, t.bucket);
      return await handleObject(cfg, req, res, url, t.key);
    } catch (e) {
      if (!res.headersSent) sendXmlError(res, e.status || 500, e.status === 413 ? 'EntityTooLarge' : 'InternalError', e.message || 'error');
      else res.destroy();
    }
  };
}

/** Starts the configured listeners and resolves with { servers, ports }. */
export async function start(cfg = loadConfig()) {
  const handler = createHandler(cfg);
  const servers = []; const ports = {};
  const listen = (srv, port, label) => new Promise((ok, bad) => {
    srv.once('error', bad);
    srv.listen(port, cfg.host, () => { ports[label] = srv.address().port; servers.push(srv); ok(); });
  });
  if (cfg.httpPort >= 0) await listen(http.createServer(handler), cfg.httpPort, 'http');
  if (cfg.httpsPort >= 0) {
    if (!cfg.tlsCert || !cfg.tlsKey) throw new Error('FAKE_S3_HTTPS_PORT set but FAKE_S3_TLS_CERT / FAKE_S3_TLS_KEY missing');
    // Wii U / 3DS era TLS: 1.0/1.1 + RSA key exchange. @SECLEVEL=0 lets OpenSSL 3 allow them.
    const opts = {
      cert: fs.readFileSync(cfg.tlsCert), key: fs.readFileSync(cfg.tlsKey),
      minVersion: 'TLSv1', maxVersion: 'TLSv1.2', ciphers: 'ALL:@SECLEVEL=0',
    };
    await listen(https.createServer(opts, handler), cfg.httpsPort, 'https');
  }
  return { servers, ports, close: () => Promise.all(servers.map((s) => new Promise((r) => s.close(r)))) };
}

if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url) {
  const cfg = loadConfig();
  fs.mkdirSync(cfg.preservedDir, { recursive: true });
  fs.mkdirSync(cfg.uploadDir, { recursive: true });
  const { ports } = await start(cfg);
  console.log(`fake-s3 listening: ${Object.entries(ports).map(([k, v]) => `${k}://${cfg.host}:${v}`).join('  ')}`);
  console.log(`preserved (read-only): ${cfg.preservedDir}\nuploads (writable):    ${cfg.uploadDir}`);
}

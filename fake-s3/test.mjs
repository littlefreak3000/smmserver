import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { start, loadConfig, keyToName, parseTarget } from './server.mjs';

const root = fs.mkdtempSync(path.join(os.tmpdir(), 'fake-s3-'));
const preservedDir = path.join(root, 'preserved');
const uploadDir = path.join(root, 'uploads');
fs.mkdirSync(preservedDir);
const course = Buffer.from(Array.from({ length: 5000 }, (_, i) => i % 251));
fs.writeFileSync(path.join(preservedDir, '1029664.bin'), course);
fs.writeFileSync(path.join(preservedDir, '900000.bin'), 'event-metadata');

const cfg = { ...loadConfig({}), httpPort: 0, preservedDir, uploadDir, quiet: true };
const srv = await start(cfg);
const base = `http://127.0.0.1:${srv.ports.http}`;
test.after(() => srv.close());

// minio-go style presigned query string (signature is ignored)
const presign = '?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=a%2F20260101%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20260101T000000Z&X-Amz-Expires=900&X-Amz-SignedHeaders=host&X-Amz-Signature=deadbeef';

test('key/target parsing', () => {
  assert.equal(keyToName('smm/1029664.bin'), '1029664.bin');
  assert.equal(keyToName('../../etc/passwd'), 'passwd');
  assert.equal(keyToName('..'), null);
  assert.equal(keyToName('a/b/..'), null);
  assert.equal(keyToName('bad name.bin'), null);
  assert.deepEqual(parseTarget('/bucket'), { kind: 'bucket', bucket: 'bucket' });
  assert.equal(parseTarget('/1.bin').kind, 'object');
  assert.equal(parseTarget('/b/p/1.bin').kind, 'object');
  assert.equal(parseTarget('/%zz').kind, 'bad');
});

test('GET resolves path-style, prefixed and bare keys to the same object', async () => {
  for (const p of ['/bkt/1029664.bin', '/bkt/some/prefix/1029664.bin', '/1029664.bin']) {
    const r = await fetch(base + p + presign);
    assert.equal(r.status, 200, p);
    assert.deepEqual(Buffer.from(await r.arrayBuffer()), course, p);
    assert.equal(r.headers.get('content-type'), 'application/octet-stream');
  }
});

test('HEAD (StatObject) returns size, ETag and Last-Modified', async () => {
  const r = await fetch(base + '/bkt/1029664.bin', { method: 'HEAD' });
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('content-length'), String(course.length));
  assert.match(r.headers.get('etag'), /^"[0-9a-f]{32}"$/);
  assert.ok(!Number.isNaN(Date.parse(r.headers.get('last-modified'))));
});

test('Range requests', async () => {
  let r = await fetch(base + '/bkt/1029664.bin', { headers: { Range: 'bytes=10-19' } });
  assert.equal(r.status, 206);
  assert.deepEqual(Buffer.from(await r.arrayBuffer()), course.subarray(10, 20));
  assert.equal(r.headers.get('content-range'), `bytes 10-19/${course.length}`);
  r = await fetch(base + '/bkt/1029664.bin', { headers: { Range: 'bytes=-5' } });
  assert.deepEqual(Buffer.from(await r.arrayBuffer()), course.subarray(-5));
  r = await fetch(base + '/bkt/1029664.bin', { headers: { Range: 'bytes=99999-' } });
  assert.equal(r.status, 416);
});

test('missing key -> 404 NoSuchKey XML (GET and HEAD)', async () => {
  let r = await fetch(base + '/bkt/404.bin');
  assert.equal(r.status, 404);
  assert.match(await r.text(), /<Code>NoSuchKey<\/Code>/);
  r = await fetch(base + '/bkt/404.bin', { method: 'HEAD' });
  assert.equal(r.status, 404);
});

test('path traversal cannot escape the data dirs', async () => {
  fs.writeFileSync(path.join(root, 'secret.bin'), 'nope');
  for (const p of ['/bkt/..%2Fsecret.bin', '/bkt/%2e%2e/secret.bin', '/bkt/..%5Csecret.bin']) {
    const r = await fetch(base + p);
    assert.notEqual(await r.text(), 'nope', p);
  }
});

test('presigned POST (multipart) upload, then GET/HEAD it back', async () => {
  const jpg = Buffer.from([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4, 0xff, 0xd9]);
  const form = new FormData();
  form.set('key', 'smm/777.jpg');
  form.set('policy', 'ignored');
  form.set('x-amz-signature', 'ignored');
  form.set('file', new Blob([jpg], { type: 'image/jpeg' }), 'blob');
  let r = await fetch(base + '/bkt', { method: 'POST', body: form });
  assert.equal(r.status, 204);
  r = await fetch(base + '/bkt/smm/777.jpg');
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('content-type'), 'image/jpeg');
  assert.deepEqual(Buffer.from(await r.arrayBuffer()), jpg);
  r = await fetch(base + '/bkt/777.jpg', { method: 'HEAD' });
  assert.equal(r.headers.get('content-length'), String(jpg.length));
  assert.ok(fs.existsSync(path.join(uploadDir, '777.jpg')));
});

test('POST without file is rejected; success_action_status=201 returns XML', async () => {
  let form = new FormData(); form.set('key', 'x/1.bin');
  assert.equal((await fetch(base + '/bkt', { method: 'POST', body: form })).status, 400);
  form = new FormData(); form.set('key', 'x/2.bin'); form.set('success_action_status', '201');
  form.set('file', new Blob(['abc']), 'f');
  const r = await fetch(base + '/bkt', { method: 'POST', body: form });
  assert.equal(r.status, 201);
  assert.match(await r.text(), /<Key>2\.bin<\/Key>/);
});

test('PUT and DELETE only affect uploads; preserved archive is untouched', async () => {
  assert.equal((await fetch(base + '/bkt/55.bin', { method: 'PUT', body: 'hello' })).status, 200);
  assert.equal(await (await fetch(base + '/bkt/55.bin')).text(), 'hello');
  assert.equal((await fetch(base + '/bkt/55.bin', { method: 'DELETE' })).status, 204);
  assert.equal((await fetch(base + '/bkt/55.bin')).status, 404);
  assert.equal((await fetch(base + '/bkt/1029664.bin', { method: 'DELETE' })).status, 204);
  assert.equal((await fetch(base + '/bkt/1029664.bin')).status, 200);
});

test('upload overrides preserved copy for reads; oversize rejected', async () => {
  await fetch(base + '/bkt/900000.bin', { method: 'PUT', body: 'override' });
  assert.equal(await (await fetch(base + '/bkt/900000.bin')).text(), 'override');
  const small = await start({ ...cfg, httpPort: 0, maxUpload: 10 });
  const r = await fetch(`http://127.0.0.1:${small.ports.http}/b/1.bin`, { method: 'PUT', body: 'x'.repeat(100) });
  assert.equal(r.status, 413);
  await small.close();
});

test('bucket list and location', async () => {
  let r = await fetch(base + '/bkt?list-type=2');
  const xml = await r.text();
  assert.match(xml, /<Key>1029664\.bin<\/Key>/);
  assert.match(xml, /<Size>5000<\/Size>/);
  r = await fetch(base + '/bkt?location');
  assert.equal(r.status, 200);
});

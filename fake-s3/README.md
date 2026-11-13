# smm-fake-s3

A tiny S3 stand-in for the Super Mario Maker preservation server. No dependencies, Node 20+.

It serves preserved course files to a Wii U / 3DS, and accepts new uploads. It does **not** verify
S3 signatures, so no real credentials are involved.

```
npm start            # http://0.0.0.0:9000
npm test             # 11 tests
```

## How the SMM server uses S3

| Who | What | Where in `super-mario-maker` |
|---|---|---|
| Console | `GET` presigned URL → downloads a course | `get_object_infos.go`, common `prepare_get_object.go` |
| Console | multipart `POST` to presigned URL → uploads course (`.bin`) or preview (`.jpg`) | `prepare_attach_file.go`, common `prepare_post_object.go` |
| Server | `HEAD` (StatObject) to compare size with the DB row | `complete_attach_file.go`, common `complete_post_object.go` |
| Server | `HEAD 900000.bin` at boot, exits if missing | `database/init_postgres.go` |
| minio-go | `GET /bucket?location` on first use | (client library) |

All of those are implemented. Object names are flat: `<dataID>.bin` (course) and `<dataID>.jpg` (preview).

**Key prefixes are ignored.** Upstream builds keys inconsistently: `GetObjectInfos` and the boot check
use `<id>.bin`, while the common datastore code uses `<KEY_BASE>/<id>.bin`. Only the last path
segment is used here, so every variant hits the same file.

## Files

```
data/preserved/   read-only archive: 1029664.bin, 1029664.jpg, 900000.bin, ...
data/uploads/     writable; new uploads land here and take precedence on reads
```

`DELETE` only removes from `uploads/`; the preserved archive is never modified.

## Wire it to the SMM server

```env
PN_SMM_CONFIG_S3_ENDPOINT=192.168.1.50:9000   # must be reachable BY THE CONSOLE (not 127.0.0.1)
PN_SMM_CONFIG_S3_SECURE=false                 # minio-go then hands out http:// URLs
PN_SMM_CONFIG_S3_ACCESS_KEY=anything
PN_SMM_CONFIG_S3_ACCESS_SECRET=anything
PN_SMM_CONFIG_S3_BUCKET=smm                   # any name
PN_SMM_CONFIG_S3_KEY_BASE=course              # any name
```

The server also needs `data/preserved/900000.bin` (the event-course metadata file). Without it the
server refuses to boot.

## Config (env vars)

| Var | Default | |
|---|---|---|
| `FAKE_S3_HTTP_PORT` | `9000` | `-1` disables, `0` picks a free port |
| `FAKE_S3_HTTPS_PORT` | disabled | needs the two TLS vars below |
| `FAKE_S3_TLS_CERT` / `FAKE_S3_TLS_KEY` | – | PEM files |
| `FAKE_S3_HOST` | `0.0.0.0` | |
| `FAKE_S3_PRESERVED_DIR` | `./data/preserved` | |
| `FAKE_S3_UPLOAD_DIR` | `./data/uploads` | |
| `FAKE_S3_MAX_UPLOAD` | 16 MiB | |

## Plain HTTP vs. legacy TLS

Try plain HTTP first. It avoids the TLS problem entirely, but whether SMM accepts an `http://` URL
from the server is **unverified**: nobody has tested it against real hardware or Cemu here.

If the console insists on HTTPS, the HTTPS listener allows TLS 1.0–1.2 and RSA key-exchange
ciphers (`ALL:@SECLEVEL=0`), which Wii U / 3DS need. Checked with `openssl s_client` for TLS 1.0, 1.1,
1.2 and a TLS 1.0 handshake limited to `AES128-SHA`. The console must also trust the certificate:
either a patched console (Inkay / SSL patch) or a cert from a CA in its store. A self-signed cert alone
will be rejected.

## Not done / limits

* No signature checking, no auth, no versioning, no multipart-upload API. LAN/preservation use only.
* `ListObjects` is flat and unpaginated (max 1000).
* Tested with synthetic requests shaped like minio-go's presigned GET / StatObject / presigned POST,
  not with the actual Go server or a console.

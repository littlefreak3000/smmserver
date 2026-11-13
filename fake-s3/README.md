# smm-fake-s3

> **Superseded.** The server now embeds its own S3 (`server/s3local`), so nothing here is needed to run it. This standalone Node version is kept as a reference and for the tool tests that write course files into `data/preserved`.

A tiny S3 stand-in for the Super Mario Maker server. No dependencies, Node 20+.

It serves preserved course files to a Wii U or 3DS and accepts new uploads. It does **not** verify S3 signatures, so no real credentials are involved.

```
npm start            # http://0.0.0.0:9000
npm test             # 11 tests
```

## How the SMM server uses S3

| Who | What | Where in `super-mario-maker` |
|---|---|---|
| Console | `GET` on a presigned URL downloads a course | `get_object_infos.go`, common `prepare_get_object.go` |
| Console | multipart `POST` to a presigned URL uploads a course (`.bin`) or preview (`.jpg`) | `prepare_attach_file.go`, common `prepare_post_object.go` |
| Server | `HEAD` (StatObject) to compare the size with the database row | `complete_attach_file.go`, common `complete_post_object.go` |
| Server | `HEAD 900000.bin` at boot, exits if missing | `database/init_postgres.go` |
| minio-go | `GET /bucket?location` on first use | (client library) |

All of these are implemented. Object names are flat: `<dataID>.bin` (course) and `<dataID>.jpg` (preview).

**Key prefixes are ignored.** Upstream builds keys inconsistently: `GetObjectInfos` and the boot check use `<id>.bin`, while the common datastore code uses `<KEY_BASE>/<id>.bin`. Only the last path segment is used here, so every variant reaches the same file.

## Files

```
data/preserved/   read-only archive: 1029664.bin, 1029664.jpg, 900000.bin, ...
data/uploads/     writable; new uploads land here and take precedence on reads
```

`DELETE` only removes from `uploads/`; the preserved archive is never modified.

## Using it with the upstream (Postgres) server

```env
PN_SMM_CONFIG_S3_ENDPOINT=192.168.1.50:9000   # must be reachable BY THE CONSOLE (not 127.0.0.1)
PN_SMM_CONFIG_S3_SECURE=false                 # minio-go then hands out http:// URLs
PN_SMM_CONFIG_S3_ACCESS_KEY=anything
PN_SMM_CONFIG_S3_ACCESS_SECRET=anything
PN_SMM_CONFIG_S3_BUCKET=smm                   # any name
PN_SMM_CONFIG_S3_KEY_BASE=course              # any name
```

The server also needs `data/preserved/900000.bin` (the event-course metadata file). Without it the upstream server refuses to boot.

## Settings (environment variables)

| Variable | Default | |
|---|---|---|
| `FAKE_S3_HTTP_PORT` | `9000` | `-1` disables, `0` picks a free port |
| `FAKE_S3_HTTPS_PORT` | disabled | needs the two TLS variables below |
| `FAKE_S3_TLS_CERT` / `FAKE_S3_TLS_KEY` | – | PEM files |
| `FAKE_S3_HOST` | `0.0.0.0` | |
| `FAKE_S3_PRESERVED_DIR` | `./data/preserved` | |
| `FAKE_S3_UPLOAD_DIR` | `./data/uploads` | |
| `FAKE_S3_MAX_UPLOAD` | 16 MiB | |

## Plain HTTP and legacy TLS

Plain HTTP avoids the TLS problem entirely, and the game accepts `http://` download URLs: the built-in S3 serves them over plain HTTP in Cemu. This standalone version was not run against Cemu itself.

If a console insists on HTTPS, the HTTPS listener allows TLS 1.0 to 1.2 and RSA key-exchange ciphers (`ALL:@SECLEVEL=0`), which Wii U and 3DS need. It was checked with `openssl s_client` for TLS 1.0, 1.1 and 1.2 and for a TLS 1.0 handshake limited to `AES128-SHA`. The console must also trust the certificate: either a patched console (Inkay or an SSL patch) or a certificate from a CA in its store. A self-signed certificate alone is rejected.

## Limits

- No signature checking, no authentication, no versioning and no multipart-upload API. Home network and preservation use only.
- `ListObjects` is flat and unpaginated (at most 1000).
- Tested with synthetic requests shaped like minio-go's presigned GET, StatObject and presigned POST, not with the actual Go server or a console.

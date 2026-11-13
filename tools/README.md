# tools

Convert SMMDB level downloads into the `<id>.bin` objects the SMM server hands to the console.

```
node build-bin.mjs "[942697] Marble Garden Zone Act 2 Run.zip" --out ../fake-s3/data/preserved
node --test ash0.test.mjs
```

| File | What |
|---|---|
| `ash0.mjs` | ASH0 compressor + decompressor (SMMDB only ships a decompressor) |
| `ash-ref/ref-decode.mjs` | Line-by-line JS port of `SMMDB/ASH0/Ash.cpp`, used as an independent test oracle |
| `build-bin.mjs` | zip/dir → `<id>.bin` (+ optional `<id>.jpg`), validates CRCs, self-verifies |
| `ash0.test.mjs` | 13 round-trip tests (edge cases, random, deep trees, real sample files) |

## What `<id>.bin` is

Four ASH0 chunks, concatenated with no header, in this order:
`thumbnail0.tnl`, `course_data.cdt`, `course_data_sub.cdt`, `thumbnail1.tnl`
(from SMMDB `indexer.py` and the `mm1-unarchive` / `SmmServer` converters).

## Not verified

* **Never tried on a console or Cemu.** The output decodes correctly with a port of the reference
  decoder, but the game's own parser may expect something the converters don't need, e.g. padding or fixed
  chunk offsets. A real original `.bin` would settle that (compare layouts byte for byte).
* ASH0 byte 4 is written as `0x30`. The reference decoder ignores it.
* `--jpg` is opt-in because which thumbnail becomes the attached `<id>.jpg` is unknown.
* Rebuilt files differ byte-wise from the originals (different compressor, same decompressed content).

## NEX smoke test (`nex-smoke/`)

Behaves like the game against a running server, using Kinnay's `nintendoclients` (the library SMMDB's indexer uses):
account token, PRUDP + Kerberos login, `get_meta`, `prepare_get_object`, download from the built-in S3, a check of
`meta_binary` against the downloaded bytes, `GetCustomRanking`, `LatestCourseSearchObject`.

```
node tools/nex-smoke/run-smoke.mjs --archive archive --level 942697 --name "<level name>"
```

It starts the server on a throwaway copy of the archive with the HTTP services bound to 127.0.0.1.
Python setup used here (no system install; Windows embeddable build):

```
python-3.12.10-embed-amd64.zip -> sdk/python312   (enable "import site" in python312._pth, run get-pip.py)
pip install setuptools wheel
pip install netifaces-plus                       # prebuilt; the original netifaces needs a C compiler
pip install --no-deps --no-build-isolation nintendoclients anynet   # pip would insist on building netifaces
pip install pycryptodome "anyio~=4.0" pyopenssl multidict
```

Settings that matter (the library defaults are not Wii U): `prudp.version = 1`, `nex.struct_header = 1`,
`prudp.minor_version = 0`. The minor version has to be 0 because nex-go v2.1.3 signs the SYN reply
(`prudp_endpoint.go handleSyn`, line ~262) *before* copying the client's negotiated minor version / supported
functions into it (lines ~266-268), so a client that negotiates anything non-zero rejects the reply as
"invalid signature". Wii U games negotiate nothing, so real games are unaffected, but a 3DS or other client
that does negotiate would fail; fixing it needs a patched nex-go (not done).

`nex_live.py` runs two further checks against a **running** server (default server settings make it talk to the public archive
services): `--ondemand ID` (a level the catalog lacks must appear, quickly, with a matching `meta_binary`) and
`--difficulty APP:MIN:MAX` (every level returned by the Mario Challenge request must have a clear rate inside the range).
`run-seeds.mjs --index <index.sqlite>` starts a server that holds only an index (no levels) and runs `nex_live.py --seeds`: two players log in
at the same time and ask for the same difficulty range with no seed, the same seed, a differently typed seed, another seed, a per-player seed and
after clearing; then a listed level is opened (downloaded for real from the Wayback Machine: a few dozen small files at the normal request rate)
and its `meta_binary` is checked against the file.

The search parameter must declare structure version 2 (`use_cache` is revision 1, `data_types` revision 2); with version 0
the server stops reading early and silently ignores the request's extra data.

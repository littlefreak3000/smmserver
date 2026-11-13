# tools

Command-line tools for the Super Mario Maker home server: they turn SMMDB level downloads into the `<id>.bin` objects the server hands to the game, build release packages, and run test clients that talk to a running server the way the game does.

```
node build-bin.mjs "[942697] Marble Garden Zone Act 2 Run.zip" --out ../fake-s3/data/preserved
node --test ash0.test.mjs
```

| File | What it does |
|---|---|
| `ash0.mjs` | ASH0 compressor and decompressor (SMMDB only ships a decompressor) |
| `ash-ref/ref-decode.mjs` | Line-by-line JS port of `SMMDB/ASH0/Ash.cpp`, used as an independent test oracle |
| `build-bin.mjs` | zip or folder → `<id>.bin` (and optional `<id>.jpg`); validates the CRCs and verifies its own output |
| `import-archive.mjs` | Builds a server archive (catalog and course files) from SMMDB downloads |
| `build-release.mjs` | Builds the release zips for every platform (see the main README) |
| `export-catalog-jsonl.mjs`, `inspect-meta.mjs`, `meta.mjs` | Helpers for catalogs and `meta_binary` |
| `ash0.test.mjs`, `meta.test.mjs` | Tests: ASH0 round trips (edge cases, random data, deep trees, real sample files when present) and `meta_binary` |
| `nex-smoke/` | Test clients that behave like the game (below) |

## What `<id>.bin` is

Four ASH0 chunks, concatenated with no header, in this order: `thumbnail0.tnl`, `course_data.cdt`, `course_data_sub.cdt`, `thumbnail1.tnl` (from SMMDB's `indexer.py` and the `mm1-unarchive` and `SmmServer` converters).

## Limits of the converter

- **Rebuilt files have not been tried in Cemu or on a console.** The output decodes correctly with a port of the reference decoder, but the game's own parser may expect something the converters do not need, such as padding or fixed chunk offsets. Levels the server fetches from the Wayback Machine are the original files, byte for byte, and those work in Cemu.
- ASH0 byte 4 is written as `0x30`. The reference decoder ignores it.
- `--jpg` is opt-in because it is not known which thumbnail becomes the attached `<id>.jpg`.
- Rebuilt files differ byte for byte from the originals (a different compressor, the same decompressed content).

## NEX test clients (`nex-smoke/`)

These behave like the game against a running server, using Kinnay's `nintendoclients` (the library SMMDB's indexer uses). The basic run covers the account token, PRUDP and Kerberos login, `get_meta`, `prepare_get_object`, a download from the built-in S3, a check of `meta_binary` against the downloaded bytes, `GetCustomRanking` and `LatestCourseSearchObject`:

```
node tools/nex-smoke/run-smoke.mjs --archive <archive> --level <id> --name "<level name>"
```

It starts the server on a throwaway copy of the archive with its HTTP services bound to 127.0.0.1.

**Python setup.** Python 3.12 with these packages (on Windows the embeddable build works; enable `import site` in `python312._pth` and run `get-pip.py`):

```
pip install setuptools wheel
pip install netifaces-plus                       # prebuilt; the original netifaces needs a C compiler
pip install --no-deps --no-build-isolation nintendoclients anynet   # pip would insist on building netifaces
pip install pycryptodome "anyio~=4.0" pyopenssl multidict
```

**Client settings that matter** (the library defaults are not Wii U): `prudp.version = 1`, `nex.struct_header = 1`, `prudp.minor_version = 0`. The minor version has to be 0 because nex-go v2.1.3 signs the SYN reply (`prudp_endpoint.go handleSyn`, around line 262) *before* copying the client's negotiated minor version and supported functions into it (around lines 266-268). A client that negotiates anything non-zero therefore rejects the reply as "invalid signature". Wii U games negotiate nothing, so real games are unaffected, but a 3DS or another client that does negotiate would fail; fixing it needs a patched nex-go, which has not been done.

**Further checks.** `nex_live.py` runs two checks against a **running** server (default server settings make it talk to the public archive services): `--ondemand ID` (a level the catalog lacks must appear, quickly, with a matching `meta_binary`) and `--difficulty APP:MIN:MAX` (every level returned by the Mario Challenge request must have a clear rate inside the range).

`run-seeds.mjs --index <index.sqlite>` starts a server that holds only an index (no levels) and runs `nex_live.py --seeds`. Two players log in at the same time and ask for the same difficulty range with no seed, the same seed, a differently typed seed, another seed, a per-player seed, and after clearing. Then a listed level is opened (it is downloaded for real from the Wayback Machine: a few dozen small files at the normal request rate) and its `meta_binary` is checked against the file.

The search parameter must declare structure version 2 (`use_cache` is revision 1, `data_types` revision 2). With version 0 the server stops reading early and silently ignores the request's extra data.

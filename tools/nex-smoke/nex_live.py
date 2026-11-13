"""Checks for on-demand fetching, against a RUNNING server (the server may be talking to the public archive).

  --ondemand ID        ask for a level the catalog does not have yet; it must appear (timed), download, and carry a
                       meta_binary that matches the downloaded bytes
  --difficulty A:MIN:MAX   the Mario Challenge request (ScoreRangeCascadedSearchObject) for ranking app A and score
                       range MIN..MAX; every returned level's real clear rate (clears / attempts) must fall in the range
Reuses the login and structure helpers from nex_smoke.py.
"""
import argparse
import asyncio
import contextlib
import json
import os
import sys
import time
import urllib.parse
import urllib.request
import zlib

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # the embeddable Python does not add the script folder

import nex_smoke as ns
from nintendo.nex import backend, datastore_smm, settings, streams
from nintendo.nex.authentication import AuthenticationInfo

check = ns.check


def clear_percent(meta):
    """clears / attempts in percent from the rating slots (2 = clears, 3 = attempts)."""
    clears, attempts = meta.ratings[2].info.total_value, meta.ratings[3].info.total_value
    return (100.0 * clears / attempts) if attempts else None


async def meta_of(store, level_id):
    p = datastore_smm.DataStoreGetMetaParam()
    p.data_id = level_id
    p.persistence_target = datastore_smm.DataStorePersistenceTarget()
    p.persistence_target.owner_id = 0
    p.persistence_target.persistence_id = 0xFFFF
    p.result_option = 6
    p.access_password = 0
    return await store.get_meta(p)


async def ondemand(store, level_id):
    t0 = time.monotonic()
    try:
        meta = await meta_of(store, level_id)
    except Exception as e:
        check(f"on-demand level {level_id} became available", False, f"{type(e).__name__}: {e} after {time.monotonic() - t0:.1f}s")
        return

    took = time.monotonic() - t0
    check(f"on-demand level {level_id} became available", meta.data_id == level_id, f"{meta.name!r} by {meta.owner_id}, {took:.1f}s")
    check("on-demand lookup was quick enough for the game", took < 20, f"{took:.1f}s")

    gp = datastore_smm.DataStorePrepareGetParam()
    gp.data_id = level_id
    req = await store.prepare_get_object(gp)
    status, blob = ns.http("GET", req.url, {h.key: h.value for h in req.headers}, timeout=30)
    check("fetched level downloads from the built-in S3", status == 200 and len(blob) == meta.size == req.size, f"{len(blob)} bytes")

    mb = bytes(meta.meta_binary)
    vals = [int.from_bytes(mb[i:i + 4], "big") for i in range(0, 44, 4)] if len(mb) == 44 else None
    if not check("meta_binary is 44 bytes", vals is not None, f"{len(mb)} bytes"):
        return

    sizes = dict(zip(("cdt", "sub", "t0", "t1"), vals[2:6]))
    crcs = dict(zip(("cdt", "sub", "t0", "t1"), vals[7:11]))
    off, ok = 0, sum(sizes.values()) == len(blob)
    for name in ("t0", "cdt", "sub", "t1"):
        chunk = blob[off:off + sizes[name]]
        off += sizes[name]
        ok &= chunk[:4] == b"ASH0" and zlib.crc32(chunk) == crcs[name]

    check("meta_binary matches the fetched bytes", ok, str(sizes))


class CourseRecordParam(ns.common.Structure):
    def __init__(self, data_id, slot=0):
        super().__init__()
        self.data_id, self.slot = data_id, slot

    def save(self, stream, version):
        stream.u64(self.data_id)
        stream.u8(self.slot)


async def records(a):
    """The archived first clear and world record of levels, fetched on demand when the game asks."""
    ids = [int(x) for x in a.records.split(",")]

    async with logged_in(a.account, "RecordCheck") as (store, client):
        for level_id in ids:
            t0 = time.monotonic()
            stream = streams.StreamOut(store.settings)
            stream.add(CourseRecordParam(level_id, 0))

            try:
                data = await client.request(store.PROTOCOL_ID, 0x48, stream.get())
            except Exception as e:
                check(f"level {level_id} has a course record", False, f"{type(e).__name__}: {e}")
                continue

            took = time.monotonic() - t0
            r = streams.StreamIn(data, store.settings)
            r.u8(); r.u32()  # struct header: version, content length
            data_id, slot, first, best, score = r.u64(), r.u8(), r.u32(), r.u32(), r.s32()
            check(f"level {level_id} has a course record", data_id == level_id and score > 0, f"first pid {first}, best pid {best}, record {score} ms, {took:.1f}s")
            check(f"level {level_id}: someone cleared it first and the record is not the player", first != 0 and best != 0)


async def profiles(a):
    """GetMeta by owner and persistence slot 0: the maker profile (name + Mii) the game shows next to a level or a record.
    Takes PID[=NNID] pairs; the name must match and the meta binary must be a 140-byte Mii blob."""
    async with logged_in(a.account, "ProfileCheck") as (store, client):
        for pair in a.profiles.split(","):
            pid, _, want = pair.partition("=")
            p = datastore_smm.DataStoreGetMetaParam()
            p.data_id = 0
            p.persistence_target = datastore_smm.DataStorePersistenceTarget()
            p.persistence_target.owner_id = int(pid)
            p.persistence_target.persistence_id = 0
            p.result_option = 6
            p.access_password = 0

            t0 = time.monotonic()
            try:
                meta = await store.get_meta(p)
            except Exception as e:
                check(f"profile of {pid} found", False, f"{type(e).__name__}: {e}")
                continue

            mii = bytes(meta.meta_binary)
            check(f"profile of {pid} is a maker profile with a Mii", meta.data_type == 1 and len(mii) == 140 and mii[:4] == b"BPFC",
                  f"name {meta.name!r}, type {meta.data_type}, {len(mii)} bytes, {len(meta.ratings)} rating slots, tags {list(meta.tags)}, period {meta.period}, {time.monotonic() - t0:.1f}s")

            if want:
                check(f"profile of {pid} is named {want}", meta.name == want, repr(meta.name))


async def recommended(a):
    """The 100 Mario Challenge / Course World request (RecommendedCourseSearchObject, 0x42) with the difficulty filter the
    game sends: ["1", MIN, MAX, "0", "0"] where MIN..MAX is a FAILURE rate (Easy 0-34 ... Super Expert 96-100)."""
    lo, hi = (int(x) for x in a.recommended.split(":"))

    async with logged_in(a.account, "RecommendedCheck") as (store, client):
        sp = ns.DataStoreSearchParam()
        sp.result_range.size = 50
        sp.result_option = 6
        stream = streams.StreamOut(store.settings)
        stream.add(sp)
        stream.list(["1", str(lo), str(hi), "0", "0"], stream.string)
        data = await client.request(store.PROTOCOL_ID, 0x42, stream.get())
        rows = streams.StreamIn(data, store.settings).list(ns.DataStoreCustomRankingResult)
        rates = [clear_percent(r.metaInfo) for r in rows]
        want_lo, want_hi = 100 - hi, 100 - lo

        check(f"failure rate {lo}-{hi} returns levels", len(rows) > 0, f"{len(rows)} levels")
        bad = [round(p) for p in rates if p is None or not (want_lo - 0.5 <= p <= want_hi + 0.5)]
        check(f"every level has a clear rate inside {want_lo}-{want_hi}%", not bad, f"clear rates {sorted(round(p) for p in rates if p is not None)}" + (f"  OUT OF RANGE: {bad}" if bad else ""))


async def difficulty(store, client, spec):
    app, lo, hi = (int(x) for x in spec.split(":"))
    seen = []

    for attempt in range(2):
        sp = ns.DataStoreSearchParam()
        sp.result_range.size = 20
        sp.result_option = 6
        stream = streams.StreamOut(store.settings)
        stream.add(sp)
        stream.list([str(app), str(lo), str(hi), "0"], stream.string)
        data = await client.request(store.PROTOCOL_ID, 0x43, stream.get())
        rows = streams.StreamIn(data, store.settings).list(ns.DataStoreCustomRankingResult)
        seen.append([r.metaInfo.data_id for r in rows])

        if attempt == 0:
            rates = [(r.metaInfo.data_id, clear_percent(r.metaInfo)) for r in rows]
            check(f"difficulty {lo}-{hi}% returns levels", len(rows) > 0, f"{len(rows)} levels")
            bad = [(i, p) for i, p in rates if p is None or not (lo - 0.5 <= p <= hi + 0.5)]
            check(f"every returned level's clear rate is inside {lo}-{hi}%", not bad and rates,
                  ", ".join(f"{i}:{p:.1f}%" for i, p in rates[:8]) + ("..." if len(rates) > 8 else "") + (f"  OUT OF RANGE: {bad}" if bad else ""))

    if all(seen) and len(set(seen[0])) > 3:
        check("a second request returns a different random selection", seen[0] != seen[1], f"{len(set(seen[0]) & set(seen[1]))} levels in common")


async def main(a):
    t = ns.account_login(a.account, a.nnid)
    check("account service returns a NEX token", all(t.values()), f"pid {t['pid']}")

    s = settings.default()
    s.configure(ns.ACCESS_KEY, ns.NEX_VERSION)
    s["prudp.version"] = 1
    s["prudp.minor_version"] = 0
    s["nex.struct_header"] = 1

    async with backend.connect(s, t["host"], int(t["port"])) as be:
        info = AuthenticationInfo()
        info.token = t["token"]
        async with be.login(t["pid"], t["nex_password"], info) as client:
            check("login accepted", True)
            store = datastore_smm.DataStoreClientSMM(client)

            if a.ondemand:
                await ondemand(store, a.ondemand)

            if a.difficulty:
                await difficulty(store, client, a.difficulty)


@contextlib.asynccontextmanager
async def logged_in(account, nnid):
    t = ns.account_login(account, nnid)

    s = settings.default()
    s.configure(ns.ACCESS_KEY, ns.NEX_VERSION)
    s["prudp.version"] = 1
    s["prudp.minor_version"] = 0
    s["nex.struct_header"] = 1

    async with backend.connect(s, t["host"], int(t["port"])) as be:
        info = AuthenticationInfo()
        info.token = t["token"]
        async with be.login(t["pid"], t["nex_password"], info) as client:
            yield datastore_smm.DataStoreClientSMM(client), client


async def range_ids(store, client, app, lo, hi, size):
    """The Mario Challenge list request; returns the level IDs in the order the server sent them."""
    sp = ns.DataStoreSearchParam()
    sp.result_range.size = size
    sp.result_option = 6
    stream = streams.StreamOut(store.settings)
    stream.add(sp)
    stream.list([str(app), str(lo), str(hi), "0"], stream.string)
    data = await client.request(store.PROTOCOL_ID, 0x43, stream.get())
    rows = streams.StreamIn(data, store.settings).list(ns.DataStoreCustomRankingResult)

    return [r.metaInfo.data_id for r in rows], rows


def seed_page(account, **form):
    """POST to the seed page the way the browser form does (the server answers with a redirect to the page)."""
    body = urllib.parse.urlencode(form).encode()
    with urllib.request.urlopen(urllib.request.Request(account.rstrip("/") + "/seed", data=body), timeout=10) as r:
        return r.status


async def seeds(a):
    """Challenge seeds, end to end: two players logged in at the same time ask for the same range."""
    app, lo, hi, size = 300000000, 1, 49, 8
    acc = a.account

    async with logged_in(acc, "SeedAlice") as (sa, ca), logged_in(acc, "SeedBob") as (sb, cb):
        async def both():
            ia, _ = await range_ids(sa, ca, app, lo, hi, size)
            ib, _ = await range_ids(sb, cb, app, lo, hi, size)
            return ia, ib

        seed_page(acc, action="clear", who="*")

        r1, _ = await range_ids(sa, ca, app, lo, hi, size)
        r2, _ = await range_ids(sa, ca, app, lo, hi, size)
        check("without a seed the levels are random", len(r1) == size and r1 != r2, f"{len(set(r1) & set(r2))} of {size} in common")

        seed_page(acc, action="set", who="*", seed="Friday Race")
        ia, ib = await both()
        ia2, _ = await range_ids(sa, ca, app, lo, hi, size)
        check("two players with the same seed get the same levels", len(ia) == size and ia == ib, str(ia))
        check("the same player asking again gets them again", ia == ia2)

        seed_page(acc, action="set", who="*", seed="  friday   RACE ")
        ic, _ = await range_ids(sb, cb, app, lo, hi, size)
        check("case and spacing of the seed do not matter", ic == ia)

        seed_page(acc, action="set", who="*", seed="another seed")
        ja, jb = await both()
        check("a different seed gives different levels", ja == jb and ja != ia, str(ja))

        seed_page(acc, action="set", who="nnid:SeedBob", seed="bobs own")
        ka, kb = await both()
        check("a player's own seed wins over the shared one", ka == ja and kb != ja, f"alice {ka[:3]}.. bob {kb[:3]}..")

        seed_page(acc, action="clear", who="nnid:SeedBob")
        la, lb = await both()
        check("clearing the player's seed returns them to the shared one", la == ja and lb == ja)

        # * a seeded level is the one the game then opens: it must download, with metadata that matches the file
        await ondemand(sa, ia[0])

        seed_page(acc, action="clear", who="*")
        ma, _ = await range_ids(sa, ca, app, lo, hi, size)
        mb, _ = await range_ids(sa, ca, app, lo, hi, size)
        check("after clearing every seed the levels are random again", ma != mb)

        with urllib.request.urlopen(acc.rstrip("/") + "/seed.json", timeout=10) as r:
            check("the seed page reports the index fingerprint", len(json.loads(r.read())["fingerprint"]) == 8)


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", required=True)
    ap.add_argument("--nnid", default="LiveCheck1")
    ap.add_argument("--ondemand", type=int)
    ap.add_argument("--difficulty", help="APP:MIN:MAX, e.g. 300000000:1:49")
    ap.add_argument("--profiles", help="PID[=NNID],... : the maker profile (name, Mii) of these players must exist")
    ap.add_argument("--recommended", help="MIN:MAX failure rate, e.g. 0:34 (Easy): levels must have a clear rate in 100-MAX..100-MIN")
    ap.add_argument("--records", help="comma-separated level IDs: GetCourseRecord must return the archived record")
    ap.add_argument("--seeds", action="store_true", help="challenge seed checks (needs an index; two players)")
    args = ap.parse_args()

    try:
        if args.profiles:
            asyncio.run(asyncio.wait_for(profiles(args), timeout=120))
        elif args.recommended:
            asyncio.run(asyncio.wait_for(recommended(args), timeout=120))
        elif args.records:
            asyncio.run(asyncio.wait_for(records(args), timeout=120))
        elif args.seeds:
            asyncio.run(asyncio.wait_for(seeds(args), timeout=300))
        else:
            asyncio.run(asyncio.wait_for(main(args), timeout=180))
    except Exception as e:
        import traceback

        traceback.print_exc()
        check(f"completed without an exception ({type(e).__name__}: {e})", False)

    print(f"\n{'ALL CHECKS PASSED' if not ns.failures else 'FAILED: ' + ', '.join(ns.failures)}")
    sys.exit(1 if ns.failures else 0)

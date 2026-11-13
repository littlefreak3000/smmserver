"""NEX smoke test: behave like the game against a running server.

  1. account service: OAuth + NEX token (what Cemu does)
  2. NEX: PRUDP connect, Kerberos login with PID/password/token (what the game does)
  3. DataStore: get_meta, prepare_get_object -> download the .bin from the built-in S3
  4. check meta_binary (chunk sizes + CRC32s) against the downloaded bytes
  5. ranking list (GetCustomRanking) and newest list (LatestCourseSearchObject)

Structure layouts for the two list calls are taken from the SMMDB indexer (indexer.py), which worked
against Nintendo's real servers. Run with the Python from sdk/python312 (see tools/README.md).
"""
import argparse
import asyncio
import sys
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET
import zlib

from nintendo.nex import backend, common, datastore_smm, settings, streams
from nintendo.nex.authentication import AuthenticationInfo

ACCESS_KEY = "9f2b4678"   # SMM (Wii U) access key
NEX_VERSION = 30803       # 3.8.3
TITLE_ID = "000500001018db00"

failures = []
sys.stdout.reconfigure(encoding="utf-8", errors="replace")  # level names contain characters like ♪


def check(name, ok, detail=""):
    print(("PASS  " if ok else "FAIL  ") + name + (f"  [{detail}]" if detail else ""), flush=True)
    if not ok:
        failures.append(name)
    return ok


# ---- structures copied from SMMDB indexer.py ------------------------------------------------------

class DataStoreCustomRankingResult(common.Structure):
    def __init__(self):
        super().__init__()
        self.order = None
        self.score = None
        self.metaInfo = None

    def load(self, stream, version):
        self.order = stream.u32()
        self.score = stream.u32()
        self.metaInfo = stream.extract(datastore_smm.DataStoreMetaInfo)


class DataStoreSearchParam(common.Structure):
    def max_version(self, settings):
        # use_cache is revision 1 and data_types revision 2 in the server library; this class writes both,
        # so the header must say so or the server stops reading early (and mis-parses what follows)
        return 2

    def __init__(self):
        super().__init__()
        self.search_target = 1
        self.owner_ids = []
        self.owner_type = 0
        self.destination_ids = []
        self.data_type = 65535
        self.created_after = common.DateTime(671076024059)
        self.created_before = common.DateTime(671076024059)
        self.updated_after = common.DateTime(671076024059)
        self.updated_before = common.DateTime(671076024059)
        self.refer_data_id = 0
        self.tags = []
        self.result_order_column = 0
        self.result_order = 0
        self.result_range = common.ResultRange()
        self.result_option = 0
        self.minimal_rating_frequency = 0
        self.use_cache = False
        self.data_types = []

    def save(self, stream, version):
        stream.u8(self.search_target)
        stream.list(self.owner_ids, stream.pid)
        stream.u8(self.owner_type)
        stream.list(self.destination_ids, stream.u64)
        stream.u16(self.data_type)
        stream.datetime(self.created_after)
        stream.datetime(self.created_before)
        stream.datetime(self.updated_after)
        stream.datetime(self.updated_before)
        stream.u32(self.refer_data_id)
        stream.list(self.tags, stream.string)
        stream.u8(self.result_order_column)
        stream.u8(self.result_order)
        stream.add(self.result_range)
        stream.u8(self.result_option)
        stream.u32(self.minimal_rating_frequency)
        stream.bool(self.use_cache)
        stream.list(self.data_types, stream.u16)


class RankingCondition(common.Structure):  # DataStoreCustomRankingRatingCondition (revision 1 has the counts)
    def max_version(self, settings):
        return 1

    def save(self, stream, version):
        stream.s8(0)
        stream.s32(0)
        stream.s32(0)
        if version >= 1:
            stream.s32(0)
            stream.s32(0)


class GetCustomRankingParam(common.Structure):
    def __init__(self, application_id, offset, size, result_option):
        super().__init__()
        self.application_id = application_id
        self.result_range = common.ResultRange(offset, size)
        self.result_option = result_option

    def save(self, stream, version):
        stream.u32(self.application_id)
        stream.add(RankingCondition())
        stream.u8(self.result_option)
        stream.add(self.result_range)


# ---- 1. account service ----------------------------------------------------------------------------

def http(method, url, headers=None, form=None, timeout=15):
    data = urllib.parse.urlencode(form).encode() if form is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers=headers or {})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.status, r.read()


def account_login(base, nnid):
    common_headers = {"X-Nintendo-Title-ID": TITLE_ID, "X-Nintendo-Client-ID": "a2efa818a34fa16b8afbc8a74eba3eda"}
    _, body = http("POST", f"{base}/v1/api/oauth20/access_token/generate", common_headers,
                   {"grant_type": "password", "user_id": nnid, "password": "ab" * 32, "password_type": "hash"})
    access = ET.fromstring(body).findtext("access_token/token")
    auth = {**common_headers, "Authorization": f"Bearer {access}"}
    _, body = http("GET", f"{base}/v1/api/provider/nex_token/@me?game_server_id=1018DB00", auth)
    n = ET.fromstring(body)
    return {k: n.findtext(k) for k in ("host", "port", "pid", "nex_password", "token")}


# ---- main ------------------------------------------------------------------------------------------

async def main(a):
    # 1
    t = account_login(a.account, a.nnid)
    check("account service returns a NEX token", all(t.values()), f"pid {t['pid']} {t['host']}:{t['port']}")

    # 2
    s = settings.default()
    s.configure(ACCESS_KEY, NEX_VERSION)
    # Wii U games use PRUDP v1 with NEX structure headers; the library default is something else
    s["prudp.version"] = 1
    s["prudp.minor_version"] = a.prudp_minor
    s["nex.struct_header"] = 1
    async with backend.connect(s, t["host"], int(t["port"])) as be:
        check("PRUDP connection to the authentication server", True)
        info = AuthenticationInfo()
        info.token = t["token"]
        async with be.login(t["pid"], t["nex_password"], info) as client:
            check("Kerberos login to the secure server accepted", True, f"pid {client.pid()}")
            store = datastore_smm.DataStoreClientSMM(client)

            # 3a. meta
            p = datastore_smm.DataStoreGetMetaParam()
            p.data_id = a.level
            p.persistence_target = datastore_smm.DataStorePersistenceTarget()
            p.persistence_target.owner_id = 0
            p.persistence_target.persistence_id = 0xFFFF
            p.result_option = 6
            p.access_password = 0
            meta = await store.get_meta(p)
            check("get_meta returns the level", meta.data_id == a.level and meta.name == a.name,
                  f"{meta.name!r} owner {meta.owner_id} size {meta.size}")
            check("meta_binary is 44 bytes", len(meta.meta_binary) == 44, f"{len(meta.meta_binary)} bytes")
            check("ratings (plays/clears/attempts/failures) present", len(meta.ratings) == 5,
                  str([r.info.total_value for r in meta.ratings]))

            # 3b. download URL
            gp = datastore_smm.DataStorePrepareGetParam()
            gp.data_id = a.level
            req = await store.prepare_get_object(gp)
            headers = {h.key: h.value for h in req.headers}
            check("prepare_get_object gives a URL", req.url.startswith("http"), req.url)
            status, blob = http("GET", req.url, headers, timeout=30)
            expected = open(a.bin, "rb").read()
            check("downloaded .bin equals the archive file", status == 200 and blob == expected, f"{len(blob)} bytes")
            check("size in metadata matches the download", meta.size == len(blob) == req.size, f"{meta.size}/{len(blob)}/{req.size}")

            # 4. meta_binary against the bytes (this is how a client slices the file)
            mb = bytes(meta.meta_binary)
            vals = [int.from_bytes(mb[i:i + 4], "big") for i in range(0, 44, 4)]
            sizes = dict(zip(("cdt", "sub", "t0", "t1"), vals[2:6]))
            crcs = dict(zip(("cdt", "sub", "t0", "t1"), vals[7:11]))
            check("meta_binary chunk sizes add up to the file", sum(sizes.values()) == len(blob), str(sizes))
            off, ok, notes = 0, True, []
            for name in ("t0", "cdt", "sub", "t1"):  # file order
                chunk = blob[off:off + sizes[name]]
                off += sizes[name]
                good = chunk[:4] == b"ASH0" and zlib.crc32(chunk) == crcs[name]
                ok &= good
                notes.append(f"{name}:{'ok' if good else 'BAD'}")
            check("each chunk starts with ASH0 and matches its CRC32", ok, " ".join(notes))

            # 5a. ranking list: non-empty, best first, numbered from 1; a small archive must also contain our level
            for app in a.ranking_apps:
                stream = streams.StreamOut(store.settings)
                stream.add(GetCustomRankingParam(app, 0, 10, 7))
                data = await client.request(store.PROTOCOL_ID, 0x31, stream.get())
                rs = streams.StreamIn(data, store.settings)
                rows = rs.list(DataStoreCustomRankingResult)
                scores = [r.score for r in rows]
                detail = ", ".join(f"#{r.order} {r.metaInfo.name!r} score {r.score}" for r in rows[:4]) + ("..." if len(rows) > 4 else "")
                check(f"GetCustomRanking app={app} returns a ranked list", len(rows) > 0 and scores == sorted(scores, reverse=True)
                      and [r.order for r in rows] == list(range(1, len(rows) + 1)), detail)
                if len(rows) < 10:  # the whole archive fits in the list
                    check(f"GetCustomRanking app={app} lists the level", any(r.metaInfo.data_id == a.level for r in rows))

            # 5b. newest list: non-empty and newest first; a small archive must also contain our level
            sp = DataStoreSearchParam()
            sp.result_range.size = 10
            stream = streams.StreamOut(store.settings)
            stream.add(sp)
            stream.list(["", "", "", "0", "0"], stream.string)
            data = await client.request(store.PROTOCOL_ID, 0x40, stream.get())
            rows = streams.StreamIn(data, store.settings).list(DataStoreCustomRankingResult)
            times = [r.metaInfo.create_time.timestamp() for r in rows]
            check("LatestCourseSearchObject returns a newest-first list", len(rows) > 0 and times == sorted(times, reverse=True),
                  ", ".join(f"{r.metaInfo.name!r}" for r in rows[:4]) + ("..." if len(rows) > 4 else ""))
            if len(rows) < 10:
                check("LatestCourseSearchObject lists the level", any(r.metaInfo.data_id == a.level for r in rows))


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", required=True, help="account service base URL")
    ap.add_argument("--nnid", default="SmokeTest1")
    ap.add_argument("--level", type=int, required=True)
    ap.add_argument("--name", required=True, help="expected level name")
    ap.add_argument("--bin", required=True, help="the archive's <id>.bin, to compare the download against")
    ap.add_argument("--prudp-minor", type=int, default=0, help="Wii U games negotiate nothing (0); the server library mis-signs the SYN reply otherwise")
    ap.add_argument("--ranking-apps", type=int, nargs="*", default=[0])
    args = ap.parse_args()

    async def run():
        await asyncio.wait_for(main(args), timeout=90)

    try:
        asyncio.run(run())
    except Exception as e:  # report where it stopped, then fail
        import traceback
        traceback.print_exc()
        check(f"completed without an exception ({type(e).__name__}: {e})", False)

    print(f"\n{'ALL CHECKS PASSED' if not failures else 'FAILED: ' + ', '.join(failures)}")
    sys.exit(1 if failures else 0)

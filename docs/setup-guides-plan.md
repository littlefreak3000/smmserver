# Plan: the two setup guides

Two guides, one per way of playing, plus a short troubleshooting page. This file is the plan; nothing here is the guide itself.

| File to write | Audience | State of the facts |
|---|---|---|
| `docs/setup-cemu.md` | Someone with Cemu on a PC | Everything below was tested on this machine (Cemu 2.6, Windows). Ready to write. |
| `docs/setup-wiiu.md` | Someone with a modded Wii U (Aroma) | **Not tested.** Needs the spikes in part 3 first. |
| `docs/troubleshooting.md` | Both | Mostly known; grows as people hit problems. |

The README keeps the one-paragraph "what is this", the ports table and the settings reference, and links to the two guides.

## 1. What both guides share

- **What the player needs to own and dump themselves** (nothing is distributed): the Super Mario Maker base game and the v240 update, and the console's online files. The guide must say plainly that the files come from the player's own Wii U.
- **Starting the server:** unzip the release, run `smm-server` (Windows/Linux/macOS), note the printed LAN address, open ports 8080 and 9000 (TCP) and 60000 and 60001 (UDP) in the firewall.
- **What to expect:** levels are fetched from public archives while playing (a few at first, more as the pool grows), so the first lists are small. Creator names and Miis appear for every level. Flags are a regional guess (see below). No uploading to a public server, no shared online play.
- **Honest limits** (short box near the top): flags are guessed from the creator's language/region, not their real country; the pool starts at 25 levels per difficulty band; only levels the Wayback Machine captured can be played.
- **Where the data lives and how to back it up:** the `archive-live`/`data` folder (catalog, courses, secrets file). Losing `secrets.json` logs everyone out.

## 2. `setup-cemu.md` (verified path)

Order of the guide, with the gotchas we actually hit:

1. **Get Cemu 2.6.** The newest development builds crashed with this game, so say "use 2.6".
2. **Install the game** (base + v240 update) in Cemu as usual; set the game path.
3. **Dump the online files with Dumpling** on the Wii U: "Dump online files" (choose the user), which yields `otp.bin`, `seeprom.bin`, the certificates (`sys/title/0005001b/10054000`), the Mii resources (`.../10056000`, the `FFLRes*.dat` files) and the user's account folder. Copy `otp.bin` and `seeprom.bin` next to Cemu's `settings.xml`, and the `mlc01` contents into Cemu's `mlc01` folder (or the folder set as MLC path).
4. **Install the Mii face fix.** Copy `docs/cemu/SMM_MiiFaceFix` into Cemu's `graphicPacks` folder. Without it Cemu 2.6 draws every Mii without a face (a recompiler bug fixed only in later builds that crash). The pack is already in the repository; the release zip should include it.
5. **Make the account match the server.**
   - The server gives each account name (NNID) a player ID derived from the name. The account's `PrincipalId` in `account.dat` must equal that number in hex, otherwise the login is refused ("Account X expected, got Y").
   - Today this is manual. **Planned helper:** `smm-server account <name>` that prints the ID and, given a Cemu `mlc01` path, writes a ready `account.dat` (or patches `PrincipalId` of a dumped one).
   - Mention both options: the hand-made account (works) and the account from the player's own dump (also works once its `PrincipalId` matches).
6. **Point Cemu at the server.** Copy `network_services.xml` (printed by the server at start-up, and written into the data folder) next to Cemu's `settings.xml`; in Options > General settings > Account choose the account and set Network Service to **Custom**.
   - **Close Cemu before editing `settings.xml`**; Cemu overwrites it on exit.
   - Never leave the same account set to Pretendo in another Cemu folder.
7. **Start the game**, check the title bar says Online-Custom/Home SMM, open 100 Mario Challenge or Course World.
8. **Check list** at the end: creator Miis and names show; levels download; the server window shows requests.

Content to add from what we learned:

- A section "If creators show as grey placeholders": means the data folder came from an older version of the server (before profiles were added to the rankings). Fix: update; the server repairs old data at start-up.
- `SMM_TRACE=1` for a full request log, and where the logs are (`log/`).
- Two Cemu folders on one PC: give each its own settings, do not share `mlc` between a Pretendo setup and this one.

## 3. `setup-wiiu.md` (needs research before writing)

What is known: the project's server already speaks the protocol the console uses after login (NEX over UDP, S3 downloads). What is missing is everything that makes the console talk to this server instead of Nintendo or Pretendo.

### Likely shape of the guide

1. **Prerequisites:** a Wii U running Aroma (Tiramisu is not enough for current plugins); the game and update installed on the console; a PC or a small always-on machine that runs the server on the same network.
2. **Redirect the console to the server.** Two candidate mechanisms:
   - **A. Aroma plugin patches.** Pretendo's Inkay plugin rewrites Nintendo Network addresses and skips SSL checks, but it points at Pretendo. A custom build of Inkay (or a small fork) with this server's address is the documented self-hosting route; Pretendo's self-hosting guides do it with a compile script plus a proxy and the primary DNS set to the server.
   - **B. DNS redirect.** Set the console's DNS to a machine that answers the Nintendo Network host names with the server's address, with Inkay (or SSSL) turning off certificate checks.
3. **Account:** the console needs a network account it can log in with. Open question below.
4. **Check list:** the console boots with the plugin message; the game reaches its title screen online; lists load; levels download from the server's S3.

### Spikes to run first (each answers one question the guide needs)

| # | Question | How to find out |
|---|---|---|
| 1 | Can Inkay be pointed at an arbitrary account server, and what exactly does the custom build change? | Read Inkay's source and Pretendo's self-hosting guide; build once. |
| 2 | Which host names and ports does the console contact at login and in the game? | Run the server with a proxy or DNS log in front of a real console; write the list down. |
| 3 | Does the console need HTTPS on the account server? Today the server speaks plain HTTP on 8080, which is what Cemu's Custom mode uses. | Test with the console; if yes, add TLS on 443 (self-signed is fine when checks are off). |
| 4 | How does the console's own account map to a player ID? A console with an old Nintendo account has that account's real player ID stored; the server hands out its own. A mismatch makes the login fail the same way as in Cemu. | Log what the console sends at login; decide whether the server should adopt the console's ID instead of deriving one. |
| 5 | What happens to the other services the game and system call (shop, Miiverse discovery, friends, boss)? Timeouts could stall menus. | Observe with the proxy log from spike 2. |
| 6 | Does the console clock or region setting matter? | Test. |
| 7 | Can the console create or link an account against this server, or must it already have one? The account-linking screens need many more endpoints than the server has. | Decide: require an existing linked account (simplest) or implement the extra endpoints. |

### Product changes the guide would need (decide after the spikes)

- A **DNS helper** inside the server (answer only the needed names, forward everything else) so the player does not have to run a DNS program; optional and off by default.
- **TLS** on the account port if spike 3 says so.
- **Player ID adoption** (spike 4).
- A **`doctor` command** that checks the ports, the data folder, whether the console or Cemu has contacted the server, and prints what to do next. It would serve both guides.

## 4. Order of work

Update: `docs/setup-wiiu.md` is written from research (Inkay source, Pretendo's self-hosting guide), with the console proxy (`consoleproxy`, port 8888) and `smm-server account adopt` built to match. Spikes 1, 2 (partly), 3, 4 and 7 are answered by that research; 5 and 6 and the real console behaviour remain untested. The two guides' status:

Status: steps 1 and 2 are done (`docs/setup-cemu.md`, the face-fix pack in the release zips, `smm-server account` including `account new`). `account new` has not yet been run against Cemu; the Cemu guide should be followed once from scratch by someone else before it is called finished.

1. Write `setup-cemu.md` now (all steps verified), add the face-fix pack and `network_services.xml` to the release zip (`node tools/build-release.mjs` is not rebuilt since the fixes), and link it from the README.
2. Build the `smm-server account` helper (small) so step 5 stops being manual.
3. Run spikes 1 to 7 with a real console; only then write `setup-wiiu.md`. If spike 7 or 4 shows large work, say so in the guide and list the supported cases.
4. Add `doctor` once both guides exist; use real questions from testers to fill `troubleshooting.md`.

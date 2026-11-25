# Playing on a modded Wii U with the home server

> **Status: working on one console (Aroma + Inkay, USA, v240).** Login, starting the game, Course World, 100 Mario Challenge, level downloads and the played-levels history all work through this server. Not tested since the last changes: a Normal-difficulty challenge once showed a "downloading additional data" popup and error 104-1970 (a SpotPass download); the cause was not found. A second person's reports on older builds (102-2160, then 106-0112) are explained in the table at the end but have not been re-checked on a fixed build. **Use v0.1.2 or later: v0.1.0 and v0.1.1 cannot complete a Wii U login** (see "If something goes wrong").

## How it works

A Wii U finds the Nintendo Network (login, game servers, shop) through addresses built into its software. Those servers are gone. The Pretendo project made a homebrew plugin, **Inkay**, that rewrites those addresses to Pretendo's (`account.pretendo.cc` and so on) and switches off the console's certificate checks. This server uses that:

1. Inkay sends the console's login to `account.pretendo.cc`.
2. You set the console's Internet connection to use an **HTTP proxy**, which is this server (port 8888).
3. The proxy lets **Super Mario Maker** talk to the home server: its login, its game server (this PC, UDP 60000 and 60001) and its level downloads.
4. What happens to the **console's own programs** (the Wii U Menu, the friend list, Miiverse, and so on) depends on the mode below.

Pretendo's self-hosting guide does the same with a general-purpose proxy; this server has a small one built in so you do not have to run anything else. It is not an open proxy: it answers the game and, in pass-through mode, contacts only hosts under `pretendo.cc`.

### Why the console's own programs need handling

The console logs in for its own programs too (the Wii U Menu, the friend list, Miiverse) as soon as it can reach an account server, and then starts background services: friends, the shop, Miiverse. This server provides none of them. If it answers those logins and then cannot answer what follows, **starting a game stalls on its loading screen** (the yellow Mario splash), with no request from the game at all. The console starts the game fine when it has no network at all, when everything works (real Pretendo), and when the server answers nothing. Only the half-working state hangs. So there are two modes:

| Mode | The game | The console's own programs | Needs |
|---|---|---|---|
| **Pretendo pass-through** (default; `SMM_PROXY_PRETENDO=1`) | answered by this server | sent on to the **real Pretendo Network**: their logins go to Pretendo's account server and every other `pretendo.cc` host (friends, Miiverse, SpotPass, shop) is tunnelled through | a Pretendo account on the console |
| **Games only** (`SMM_PROXY_PRETENDO=0`) | answered by this server | **not answered** (the log says `dropping ... from the system title ...`); the console behaves as if offline until the game asks | nothing, but the account's player ID has to be set with `account adopt` |

In games-only mode the Wii U Menu may look signed out of the network (a password prompt, friends offline). That is expected: do not enter anything. In pass-through mode the rest of the console works as if Pretendo were connected directly. The console's login request, including its password hash, then goes to Pretendo through this server, exactly as it would directly; the server does not log it, but it passes through. Keep this on your own network.

## What you need

- A Wii U running **Aroma**, with Inkay installed (Inkay 2.6 or later refreshes SpotPass at every boot, which you will see in the log). Follow <https://wiiu.hacks.guide/> to get Aroma if you do not have it; it carries a small risk of damaging the console, so read its warnings and do it carefully.
- Super Mario Maker (and its v240 update) installed on the console. A game that hangs on its loading screen **with no server at all** is a problem with the game or the console's storage, not this server.
- A network account on the console. The server never checks passwords; it picks a player ID from the account name the console sends.
- The server running on a PC or small always-on machine on the same network (see the first section of [setup-cemu.md](setup-cemu.md)).
- For pass-through mode, a Pretendo account linked to the console (<https://pretendo.network/account>).
- Pretendo's installation page has current pictures of the Inkay steps: <https://pretendo.network/docs/install/wiiu>.

**Do not remove the Nintendo Network ID from your console.** It is not needed and removing it makes you lose access to purchased games.

## 1. Install Inkay

1. On the Wii U open **Aroma Updater**, press A to check for updates, press R to go to page 2, move to **Inkay**, press A, press + and confirm, then let the console restart.
2. When the console restarts you should see "Using Pretendo Network" at the top left for a moment.

Inkay only supports the release version of Aroma set up to start automatically. If you use it differently, SpotPass and other things may misbehave.

## 2. The account and its player ID

The console keeps a **player ID** (PID) for its network account, and it **rejects a login whose profile carries a different one**. The symptom is error 102-2932 on the console, and in the server's log a `login` and a `profile` request from the game with no `nex_token` request after them. The server cannot make the ID up: for a name it has not seen it derives a number from the name, which is almost never the account's real ID. There are two ways to give it the real one:

- **Pass-through mode (the default, `SMM_PROXY_PRETENDO=1`): nothing to do.** The console's own programs log in to the real Pretendo through this server, and the server reads the real player ID out of Pretendo's profile reply and keeps it. The log says `account: learned the real player ID of "<name>" from Pretendo: <number>`. This usually happens at boot, before the game asks. If the game is started first (right after the console), the server makes the game's login to Pretendo itself, with the same credentials the console sent, reads the profile, and learns the ID before it answers (`proxy: Pretendo says ... (read at the game's login)`). It needs a Pretendo account on the console.
- **`smm-server account adopt` with the console's account file**, for games-only mode or a console without a Pretendo account:

  1. Run **Dumpling** on the Wii U and dump the online files (as in the Cemu guide). In the dump, open `<user>/mlc01/usr/save/system/act/<number>/account.dat` for the user you will play as. The line `PrincipalId=` holds the ID in hex.
  2. On the PC, in a terminal next to the server program (stop the server first):

     ```
     smm-server account adopt "path/to/account.dat"
     ```

     It prints the account name and player ID it will use. If it says the file has no network account, that user has no Nintendo Network or Pretendo account.

If the log shows `account: that player ID is made up from the name`, that account has no real ID yet. If the console has already logged in once and the server picked its own ID for that name, either way replaces it.

This server cannot create or link accounts (a console's account screens need many more services than it has), so the account has to exist already.

## 3. Point the console at the server

1. Start the server. The box it prints shows this PC's address and the exact proxy settings to type on the console. If the PC has several network addresses the box lists the others; set `SMM_PUBLIC_HOST` in `data/settings.env` if it picked the wrong one. Allow **TCP 8080, 8888 and 9000** and **UDP 60000 and 60001** in the PC's firewall, **for every network type** (see the table: Windows' own prompt only allows Private networks). `SMM_PROXY_PORT` changes the proxy port; `0` turns it off.
2. Pass-through mode is on by default and needs a Pretendo account on the console. Without one, put `SMM_PROXY_PRETENDO=0` in `data/settings.env`, restart, and use `account adopt` (section 2).
3. On the Wii U open **System Settings > Internet > Connect to the Internet > Connection List**, choose the connection the console uses (the one with the Wii U logo), then **Change Settings**.
4. Go to the page with **Proxy Settings**, choose **Set**, press OK, and enter:
   - proxy server: this PC's address
   - port: **8888**

   Confirm, choose **Don't Use Authentication**, and save the connection. You may be asked to run a connection test: it should pass (the server answers it).
5. Restart the console so Inkay starts ("Using Pretendo Network"), and leave the server window open.

To go back to the real services later (for example to use the eShop), turn the proxy off in the same menu and turn Inkay's network off (press **L + Down + SELECT** on the GamePad, open Inkay, set "Connect to Pretendo network" to false; the console restarts).

## 4. Play

1. Start Super Mario Maker. It should get past the loading screen and reach the main menu.
2. Open **Course World** or **100 Mario Challenge**. Levels are fetched while you play, as in the Cemu guide; the first lists are short. Levels you open are remembered per player (the last 1,000, `SMM_HISTORY_LEVELS`), so later random lists offer others first.

What to expect is the same as on Cemu: creator names and Miis on every level; flags are a regional guess (the archive does not say where a creator lives); no public online play.

If the game ever stalls on its loading screen, close it and start it again. As a last resort the server can be stopped, the game started, and the server started again once the game is running: the game logs in when it first needs the network.

## If something goes wrong

Run the server with `SMM_TRACE=1` (in `data/settings.env`, or as an environment variable), repeat what failed, and look at `log/all.log`. The account service always writes one line per request, and with tracing on the proxy writes the hosts the console asked for.

### Error codes

| Code | What it means | What to do |
|---|---|---|
| **102-2932** | The console got an error it does not know: here almost always **a wrong player ID**. The log shows `login` and `profile` from the game and then no `nex_token` request. | Use pass-through mode (the real ID is learned automatically) or run `smm-server account adopt` on the console's `account.dat`; see section 2. |
| **102-2160** | The console rejected a TLS certificate (`HTTP_SSL_CACERT`). This server's certificate is self-signed and only accepted while Inkay's patch is active. | Check that Inkay is on and **"Connect to Pretendo network" is on** ("Using Pretendo Network" at boot). With it off the console contacts the real `nintendo.net` addresses through the proxy and rejects the certificate. Check the proxy address is this PC. |
| **102-2152** | The server returned nothing (`HTTP_GOT_NOTHING`): the login request never completed. | On **v0.1.0 and v0.1.1 this always happens**: their proxy cut every request at 600 bytes, and a Wii U login is about 1,350. Use v0.1.2 or later. Otherwise look for `ended without a request the proxy understood` in the log. |
| **102-2402** | The server answered a token problem as "bad format". | Fixed long ago; update the server. |
| **106-0112** | The console logged in, then timed out reaching the **game server** (`Core::Timeout`, UDP 60000/60001). | Allow UDP 60000 and 60001 in the PC's firewall for **all network types** (Windows' prompt only allows Private, and Wi-Fi is often marked Public). Make sure the address in the server's box is the one the console can reach (`SMM_PUBLIC_HOST`; with two network adapters the server may pick the wrong one). If the log has no `login as` line from the console, the proxy is not applied to the connection the console uses and it is talking to the real Pretendo. |
| **104-1970** | A SpotPass (BOSS) download failed. Seen once, in the Normal 100 Mario Challenge, with a "downloading additional data" popup. | Not understood yet. In pass-through mode SpotPass goes to Pretendo's real servers. |

### Log lines and symptoms

| What you see | What it likely means |
|---|---|
| Nothing appears in the log when the console connects | The proxy is not set, the address or port is wrong, or the PC's firewall blocks 8888. From another PC `curl http://<address>:8888/` should print a line that mentions Inkay. |
| The game hangs on the yellow Mario loading screen, and the log shows nothing from the game | The console's own programs were answered but cannot be served (see "Why the console's own programs need handling"). Use v0.1.2 or later, or pass-through mode. |
| `account: dropping ... from the system title ...` | Normal in games-only mode: the console's own programs are deliberately not answered. |
| `proxy: refused CONNECT <host>` or `no answer for <host>` | A host the server does not stand in for (shop, Miiverse, SpotPass in games-only mode). The connection is dropped, which is what makes the console give up at once. Normal. |
| `proxy: TLS handshake with the console failed for <host>` | The console would not accept the certificate: see 102-2160. Hosts ending in `nintendo.net` there mean Inkay's network is off. |
| `account … 404` for an address you did not expect | The console asked the account service for something not implemented. Send the line; this is how missing pieces are found. |
| `Account X expected, got Y` in `log/error.log` | The player ID the console sends is not the one the server holds. Run `smm-server account adopt` on the right user's `account.dat`. |
| The game starts but lists are empty | Course World needs the server's data; look for `Level pool` lines in the log, and give it a few minutes after the first start. |
| Levels start downloading but never play | The console cannot reach the server's address on TCP 9000, or `SMM_PUBLIC_HOST` is wrong. |
| The Wii U Menu seems signed out of the network | Expected in games-only mode. Do not enter a password. |

### Diagnostic switches

These are for tracking down problems, set as environment variables or in `data/settings.env`:

- `SMM_PROXY_PRETENDO=0`: games-only mode (above); pass-through is the default.
- `SMM_ACCOUNT_SYSTEM_TITLES=1`: answer the console's own programs like a game (this brings back the launch stall).
- `SMM_PROXY_ANSWER=1`: answer hosts the server does not serve with HTTP errors instead of dropping the connection.
- `SMM_PROXY_SPOTPASS=1`: accept SpotPass connections and log what they ask, instead of dropping them.
- `SMM_PROXY_DROP_ALL=1`: reset every connection to the proxy port, serving nothing (a console that starts the game this way, but not with the server on, tells you the server's answers are the problem).

## Not supported yet

- **SSSL (hackless, DNS-based)**: needs a DNS server and TLS certificates made with a specially patched Nintendo certificate. The server has neither.
- **Creating or linking an account against this server**: needs more account services than exist here.
- **The shop, Miiverse and friends**: unavailable in games-only mode. In pass-through mode they are Pretendo's, not this server's.
- **Nintendo 3DS**: not covered.

## Things to know

- **No passwords, LAN only.** Anyone who can reach the PC can log in as anyone. Do not open its ports on your router.
- **What is and is not checked.** Checked: the proxy (a test client talks to it over HTTPS with an old-console style TLS setup, over plain HTTP, and through the tunnel and the account split), the account service (the same one used by Cemu), and the player-ID helper. Tested on one real console; reports of what happens elsewhere are welcome, and the log lines above are what is needed.
- Backups, licence and the archive services are the same as in the Cemu guide.

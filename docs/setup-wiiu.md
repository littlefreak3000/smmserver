# Playing on a modded Wii U with the home server

> **Status: not tested on a console.** The Cemu setup (see [setup-cemu.md](setup-cemu.md)) is verified. This guide is built from how the Pretendo project's own self-hosting guide connects a console, from Inkay's source code, and from what the server does when it is tested against a stand-in client. Expect to need small fixes on a first real run. The guide says where to look when something fails, and the server logs what the console asks for.

## How it works

A Wii U finds the Nintendo Network (login, game servers, shop) through addresses built into its software. Those servers are gone. The Pretendo project made a homebrew plugin, **Inkay**, that rewrites those addresses to Pretendo's (`account.pretendo.cc` and so on) and switches off the console's certificate checks. This server uses that:

1. Inkay sends the console's login to `account.pretendo.cc`.
2. You set the console's Internet connection to use an **HTTP proxy**, which is this server (port 8888). The proxy answers `account.pretendo.cc`, the console's connection test and the course downloads, and refuses everything else, so it is not an open proxy.
3. The server's account service logs the console in and tells it which game server to use (this PC, UDP 60000 and 60001).
4. The game then talks to the server directly and downloads levels from it.

Pretendo's self-hosting guide does the same with a general-purpose proxy; this server has a small one built in so you do not have to run anything else.

## What you need

- A Wii U running **Aroma**, with Inkay installed. Follow <https://wiiu.hacks.guide/> to get Aroma if you do not have it; it carries a small risk of damaging the console, so read its warnings and do it carefully.
- Super Mario Maker (and its v240 update) on the console.
- A network account on that console that the server can adopt (see "The account"), and the account file from a Dumpling dump.
- The server running on a PC or small always-on machine on the same network (see the first section of [setup-cemu.md](setup-cemu.md)).
- Pretendo's installation page has current pictures of the Inkay steps: <https://pretendo.network/docs/install/wiiu>.

**Do not remove the Nintendo Network ID from your console.** It is not needed and removing it makes you lose access to purchased games.

## 1. Install Inkay

1. On the Wii U open **Aroma Updater**, press A to check for updates, press R to go to page 2, move to **Inkay**, press A, press + and confirm, then let the console restart.
2. When the console restarts you should see "Using Pretendo Network" at the top left for a moment.

Inkay only supports the release version of Aroma set up to start automatically. If you use it differently, SpotPass and other things may misbehave.

## 2. The account

The server never checks passwords, but the console keeps a **player ID** for its network account, and the server has to give out the same one, or the game's login is refused. This server cannot create or link accounts (a console's account screens need many more services than it has), so the account has to exist already. There are two ways:

- **Use the account that is already on the console** (for example your old Nintendo Network ID). Nothing to create.
- **Use a Pretendo account.** Make one on <https://pretendo.network/account> and link it on the console while the console is still using the official Pretendo servers (Inkay on, no proxy set yet). Pretendo's page describes this. The account name must differ from the one already linked to your console.

Then get the account file and tell the server about it:

1. Run **Dumpling** on the Wii U and dump the online files (as in the Cemu guide). In the dump, open `<user>/mlc01/usr/save/system/act/<number>/account.dat` for the user you will play as.
2. On the PC, in a terminal next to the server program:

   ```
   smm-server account adopt "path/to/account.dat"
   ```

   It prints the account name and player ID it will use. If it says the file has no network account, that user has no Nintendo Network or Pretendo account; use a different user or the Pretendo route above.

Do this before the first login. If the console has already logged in once and the server picked its own ID for that name, `adopt` replaces it.

## 3. Point the console at the server

1. Start the server. Note this PC's address in the box it prints (the same one used for Cemu; set `SMM_PUBLIC_HOST` in `data/settings.env` if it picked the wrong one) and allow **TCP 8888 and 9000** and **UDP 60000 and 60001** in the PC's firewall. (`SMM_PROXY_PORT` changes the proxy port; `0` turns it off.)
2. On the Wii U open **System Settings > Internet > Connect to the Internet > Connection List**, choose the connection the console uses (the one with the Wii U logo), then **Change Settings**.
3. Go to the page with **Proxy Settings**, choose **Set**, press OK, and enter:
   - proxy server: this PC's address
   - port: **8888**

   Confirm, choose **Don't Use Authentication**, and save the connection. You may be asked to run a connection test: it should pass (the server answers it).
4. Leave the server window open.

To go back to the real services later (for example to use the eShop), turn the proxy off in the same menu and turn Inkay's network off (press **L + Down + SELECT** on the GamePad, open Inkay, set "Connect to Pretendo network" to false; the console restarts).

## 4. Play

1. Start Super Mario Maker. It should get past the login and reach the main menu.
2. Open **Course World** or **100 Mario Challenge**. Levels are fetched while you play, as in the Cemu guide; the first lists are short.

What to expect is the same as on Cemu: creator names and Miis on every level; flags are a regional guess (the archive does not say where a creator lives); no public online play.

## If something goes wrong

Run the server with `SMM_TRACE=1` (in `data/settings.env`, or as an environment variable), repeat what failed, and look at the newest file in the `log` folder. The account service always writes one line per request, and with tracing on the proxy writes the hosts the console asked for.

| What you see | What it likely means |
|---|---|
| Nothing appears in the log when the console connects | The proxy is not set, the address or port is wrong, or the PC's firewall blocks 8888. Check the Wii U's connection test; from another PC `curl http://<address>:8888/` should print a line that mentions Inkay. |
| `proxy: refused CONNECT <host>` | The console asked for a host the server does not stand in for (shop, Miiverse, and so on). Normal for those. If it is `account.…`, send the line. |
| `proxy: TLS handshake with the console failed` | The console and the server could not agree on a secure connection. This is the most likely first problem. Check that Inkay is on ("Using Pretendo Network" at start-up). Send the log line. |
| `account … 404` lines for an address you did not expect | The console asked the account service for something the server does not implement (`GET /v1/api/people/@me/profile`, for example). Send the line; this is how missing pieces are found. |
| `Account X expected, got Y` in `log/error.log` | The player ID the console sends is not the one the server holds. Run `smm-server account adopt` on the right user's `account.dat`. |
| The console says it cannot connect to the account server | Inkay is not active, or the console is on a connection that has no proxy set. |
| The game starts but lists are empty | Course World needs the server's data; look for `Level pool` lines in the log, and give it a few minutes after the first start. |
| Levels start downloading but never play | The console cannot reach the server's address on TCP 9000, or `SMM_PUBLIC_HOST` is wrong. |

## Not supported yet

- **SSSL (hackless, DNS-based)**: needs a DNS server and TLS certificates made with a specially patched Nintendo certificate. The server has neither.
- **Creating or linking an account against this server**: needs more account services than exist here.
- **The shop, Miiverse and friends**: they stay unavailable while the proxy is on.
- **Nintendo 3DS**: not covered.

## Things to know

- **No passwords, LAN only.** Anyone who can reach the PC can log in as anyone. Do not open its ports on your router.
- **What is and is not checked.** Checked: the proxy (a test client talks to it over HTTPS with an old-console style TLS setup, and over plain HTTP for the connection test and downloads), the account service (same one used by Cemu), and the player-ID helper. Not checked: what a real console sends and how its secure-connection code reacts. Reports of what happens are welcome; the log lines above are what is needed.
- Backups, licence and the archive services are the same as in the Cemu guide.

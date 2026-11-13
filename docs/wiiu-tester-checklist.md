# Wii U test: checklist and what to send back

For someone trying [setup-wiiu.md](setup-wiiu.md) on a real console. It has not been tried yet, so the useful result is either "it works" or the exact place it stopped. Plan on an hour.

## You need

- A Wii U with Aroma and Inkay, and a Nintendo Network ID or Pretendo account linked to the user you will test with (do not remove it).
- Super Mario Maker (USA, v240 was used for everything else) installed on the console. The first test below does not need it.
- A PC on the same network with the server unzipped, and Dumpling on the Wii U.

## Test 1: login only (no game needed)

This checks the part most likely to fail: the console's login through the proxy.

1. On the PC: start the server with `SMM_TRACE=1` set (for example, add `SMM_TRACE=1` as a line in `data/settings.env` first), and run `smm-server account adopt "<account.dat from the Dumpling dump>"`. Allow TCP 8888, 8080, 9000 and UDP 60000, 60001 in the firewall.
2. On the Wii U: set the connection's proxy to the PC's address and port 8888, as in the guide. Restart the console so Inkay starts ("Using Pretendo Network" appears).
3. Make the console log in: open the **Friend List** from the Wii U Menu (it signs in to the network), or run the connection test in System Settings. Wait a minute.
4. In the server's `log` folder (newest file) look for lines starting with `proxy:` and `account`.

Result:

- **Passes:** you see `proxy: CONNECT account…` followed by `account … POST /v1/api/oauth20/access_token/generate -> 200`.
- **Fails at the secure connection:** `proxy: TLS handshake with the console failed`.
- **Fails at the account service:** `account … -> 404` for some address, or no `account` line at all.

## Test 2: the game

1. Start Super Mario Maker. It should pass the login and reach the main menu.
2. Open **Course World** and **100 Mario Challenge**. Open two or three levels and finish or leave them.
3. Look at the creator Mii and name under each level in lists and on the clear screen.

## What to send back

- Which test passed and where the first failure was (the lines above).
- The newest file in the server's `log` folder. It contains your account name, the player ID and the PC's local address; remove those lines first if you do not want to share them. Do not share `data/secrets.json`.
- The console's firmware and Aroma version, and whether the connection is wired or wireless.
- For any problem in the game: what you were doing and what the screen said, including any error code.

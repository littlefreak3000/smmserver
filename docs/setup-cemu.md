# Playing on Cemu with the home server

This guide gets Super Mario Maker (Wii U) running in Cemu against the home server on your own PC, with the old level archive: Course World, 100 Mario Challenge, creator names and Miis, clear screens.

It was written from a working setup: Cemu 2.6 on Windows, game version v240 (USA). Other regions should work but are untested.

## What you need

- A PC that runs the server (Windows, Linux or macOS) and Cemu. They can be the same PC.
- **Your own Wii U** with homebrew (Aroma or Tiramisu), the game, and the game's update. Cemu needs files that only your console has. This guide does not distribute any of them, and they must come from your own console.
- **Cemu 2.6** from <https://cemu.info>. Newer development builds crashed with this game, so use the release.
- The server download from this project (the zip for your system).

## 1. Start the server

1. Unzip the server and run it (on Windows: double-click `smm-server.exe`; the first time Windows asks about the firewall, allow **Private networks**).
2. Leave the window open. It prints a box with this PC's address and starts fetching about 100 levels in the background (roughly five minutes). More arrive slowly as long as it keeps running.
3. Everything it needs is created in a folder called `data` next to the program. `data/network_services.xml` is what Cemu needs in step 5.

If you are not sure the right network address was picked, the box shows it; change it with `SMM_PUBLIC_HOST` in `data/settings.env` (an IPv4 address; Cemu allows at most 15 characters).

## 2. Install the game in Cemu

Install the base game and the v240 update the way Cemu normally does (File > Install game title, update or DLC, or add the folder where the game lives under Options > General settings > Game paths).

## 3. Dump the online files from your Wii U

1. On the Wii U, run **Dumpling** (<https://github.com/emiyl/dumpling>). Choose the option that dumps the files to use Cemu online and pick your user. Put the SD card or USB drive in your PC afterwards.
2. The dump contains a folder called **Online Files** with `otp.bin`, `seeprom.bin` and a folder per user with an `mlc01` folder inside (certificates, Mii resources and your account).
3. Copy `otp.bin` and `seeprom.bin` next to Cemu's `settings.xml` (in Cemu use File > Open Cemu folder if you are unsure where that is).
4. Copy the contents of the user's `mlc01` folder into Cemu's `mlc01` folder (Options > General settings > Paths shows which one), merging with what is there.

Without these files Cemu will not go online at all, and Miis will not draw.

## 4. Install the Mii face fix

Cemu 2.6 has a bug that makes Miis appear without faces in this game. The fix is a small graphic pack that ships with this project:

1. Copy the folder `docs/cemu/SMM_MiiFaceFix` (it is inside the server zip too) into Cemu's `graphicPacks` folder, so you end up with `graphicPacks/SMM_MiiFaceFix/rules.txt`.
2. In Cemu open Options > Graphic packs and check that "Mii face fix" is listed and on. When you start the game, Cemu's log should say `Applying patch group 'SMM_MiiFaceFix'`.

## 5. Connect Cemu to the server

**Close Cemu first.** Cemu rewrites its settings when it closes and would undo your changes.

1. Copy `data/network_services.xml` (from the server folder) next to Cemu's `settings.xml`.
2. Make an account that matches the server (see below).
3. Start Cemu, open Options > General settings > Account, pick the account, and set **Network Service** to **Custom** (not Nintendo, not Pretendo).

### The account must match the server

The game logs in with the player ID stored in the account file, and the server decides which ID belongs to which account name. If they differ, the login fails. Use the helper that comes with the server (run it in a terminal next to the program; close Cemu first):

- **Use the account from your Wii U dump** (it must have a network account name, for example an old Nintendo Network ID):

  ```
  smm-server account "<Cemu folder>/mlc01/usr/save/system/act/<id>/account.dat"
  ```

  This sets the player ID in that file. The original is kept as `account.dat.bak`.

- **Make a new account** (useful if your user has no network account name):

  ```
  smm-server account new MyName "<Cemu folder>/mlc01"
  ```

  It creates a new Cemu account called MyName, with the right player ID. The name is 1 to 16 letters, digits, `-` or `_`. Cemu must have been started once with that `mlc01` folder so its own account exists.

`smm-server account MyName` only prints the player ID for a name.

On Windows the program is called `smm-server.exe`. Set `SMM_DATA_DIR` if your server's data folder is not called `data`.

## 6. Play

Start the game in Cemu. The title bar should show the online network name. Open **Course World** or **100 Mario Challenge**. Levels are downloaded from the server when you open them.

What to expect:

- The first lists are short; the server fetches levels while you play, and the pool grows over time. A level that the public archive never captured is not available.
- Creator Miis and names show for every level.
- **Flags are a regional guess.** The archive does not say where a creator lives. The server guesses from the language/region saved with each creator's Mii: Japanese gets Japan, English gets the USA, and so on. An Australian or Irish creator will show another country.
- There is no public online play and nothing you upload goes anywhere but your own server.

## When something goes wrong

| What you see | What it means and what to do |
|---|---|
| Cemu log: `Account response with error code 106` | The account is set to Nintendo or Pretendo. Set its Network Service to Custom. If you changed it with Cemu open, close Cemu first and set it again. |
| Server window or `log/error.log` says `Account X expected, got Y` | The account's player ID does not match. Run `smm-server account <path to account.dat>`. |
| The game cannot connect | Allow TCP 8080 and 9000 and UDP 60000 and 60001 in the firewall. Check the address in the server box and `SMM_PUBLIC_HOST`. |
| Miis have no faces | The face fix is missing or not on (step 4). |
| Grey placeholder Mii and no name next to every level | The data folder comes from an older version of the server. Update the server; it repairs old data when it starts. |
| Every flag is Japan | Normal for creators whose Mii data has no region (about sixty of the oldest ones), and a guess for the rest (see above). |
| The game crashes right after it starts | Use Cemu 2.6, not a development build. |
| A level will not download | The archive may never have captured that level. Pick another one. With `SMM_FETCH=0` only levels already in the data folder work. |
| You want to see what the game is asking | Run the server with `SMM_TRACE=1` and look at the log in the `log` folder. |

## Things to know

- **No passwords.** The server never checks them, so anyone who can reach your PC on the network can log in. Keep it on your home network and do not forward its ports on your router.
- **Backups.** Copy the `data` folder only while the server is stopped. Keep `data/secrets.json` with it: without that file everyone has to log in again.
- **Licence.** The server is AGPL-3.0. If you pass it on, you must also offer the source.
- Levels come from volunteer-run public services (api.bobac-analytics.com and web.archive.org) at a low request rate. `SMM_FETCH=0` turns that off.

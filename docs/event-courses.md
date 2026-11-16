# The event course list (data ID 900000)

Super Mario Maker has an **event courses** menu: courses Nintendo published for tie-ins and tournaments (2015 to 2017). The game builds the menu from one file on the data store, data ID **900000**. This page describes that file, how to make your own, and what is and is not tested.

## What was found

The original file was saved by the Internet Archive on 2021-04-08 (the same address pattern as the levels: `.../ds/1/data/00000900000-00045`, 450,068 bytes), and so were the 46 event courses it lists (IDs 930000 to 930050). They are Nintendo's files, so this repository does not contain them. A server fetches them on demand like levels.

Until now the server announced data ID 900000 with an empty placeholder, so the game showed no events. With the real list, the game shows a "new event courses" notice and the menu icon becomes active. After the 46 courses were imported as catalog objects, the menu filled (tested in Cemu by a side session; how the courses look and play has not been examined).

## Layout

All numbers are big-endian. All text is UTF-16BE, NUL-terminated inside a fixed-size window. The file is `4 + count × 9,784` bytes:

| Offset | Size | Field |
|---|---|---|
| 0 | 4 | `count` (46 in the original) |
| per record: | | |
| +0 | 8 | data ID of the course the event opens |
| +8 | 12 × 66 | titles, 33 characters per window (32 plus the terminator), one per language |
| +800 | 12 × 742 | descriptions, 371 characters per window, one per language; `\r\n` is a line break |
| +9704 | 1 | 1 if the event has a banner pack, else 0 |
| +9705 | 1 | 0 |
| +9706 | 26 | pack name, 13 characters, like `Boss049.pack` (empty when there is no pack) |
| +9732 | 1 | flag, 1 in every original record, meaning unknown |
| +9733 | 3 | padding |
| +9736 | 24 | start: six u32 numbers, year, month, day, hour, minute, second |
| +9760 | 24 | end: the same |

**Language order** of the 12 windows: Japanese, English (US), French (US), Spanish (US), English (EU), French (EU), Spanish (EU), German, Italian, Dutch, Russian, Portuguese. This comes from the text: windows 2 to 4 and 5 to 7 differ in wording where the US and European releases differ (for example "Mary O." against "Mashiko"). Which window the game shows to which player is inferred from that, not tested.

**Leftover bytes.** After a text's terminator the original file contains pieces of older text (an earlier, longer title was overwritten by a shorter one). They mean nothing. A writer should zero them; the reader in `server/events` ignores them.

**Dates.** In the original, the end is one hundred years after the start for most events (the events never end), with a few real end dates. Time zone is assumed to be UTC; it is not known. Whether the game hides events whose start is in the future or whose end has passed is not tested.

**The list order** in the original is newest start first. Whether the menu shows records in file order is not tested.

**Correction to an earlier note.** The four bytes after the data ID are not a creator ID: they are the first characters of the Japanese title. A record has no creator field. An importer that read those bytes as a creator number got meaningless numbers.

**Banner packs.** The pack names (`Boss000.pack` and up) are files the game fetched over SpotPass; the game's own folder holds only some other packs. Seven of the original records have no pack. What a pack contains, and what the game shows for an event without one, are not tested.

**What was checked.** `go test ./events` writes a list back from the parsed original and compares every byte that has meaning (it ignores only the leftovers), for all 46 records, and checks that the reading and writing code are inverses on synthetic lists. Point `SMM_EVENT_LIST` at a copy of the archived 900000 file to run the original-file test; it is skipped otherwise.

## Course IDs: what the game shows and what the server sees

The game shows a course by a code such as `9007-0000-00BB-FD3B`; the servers (and the log) use a **data ID**, here `12320059`. They are the same number in two forms: the last eight hex digits of the code are the data ID, the second group is `0000`, and the first group is a checksum. The checksum is the HMAC-MD5 of the data ID as 8 little-endian bytes, keyed with the MD5 of the text `9f2b4678` (the game's access key), taking bytes 3 and 2 of the result as the group. The scheme comes from the Pretendo wiki ("datastore codes"); it matched two real codes, the wiki's example and one shown by the game for a level the server served as data ID 12320059 (Cemu).

```
smm-server code 9007-0000-00BB-FD3B     # -> data ID 12320059
smm-server code 12320059                # -> 9007-0000-00BB-FD3B
```

With `SMM_TRACE=1` the log shows both forms for levels (`GetMeta/PrepareGet by data id 12320059 (course ID 9007-0000-00BB-FD3B)`), so a level seen in the game can be found in the log and the other way round. A level can be looked up in the game by typing its code, and the game turns it into the data ID itself. The 46 original event courses have the data IDs 930000 to 930050 (for example 930050 is `FF52-0000-000E-3102`).

## Making your own

```
smm-server events example > events.json     # edit it
smm-server events build events.json         # writes data/courses/900000.bin
smm-server events dump data/courses/900000.bin
```

Each event in `events.json`:

| Key | Meaning |
|---|---|
| `course` | the course the event opens, written as the game shows it: `"9007-0000-00BB-FD3B"` (a data ID such as `12320059` is accepted too). It must exist in the catalog. |
| `courses` | a list of courses instead of one. The menu opens one course per entry, so the list becomes one entry per course, titled `title 1/3`, `title 2/3`, and so on. Give either `course` or `courses`. A course may appear only once in the file. |
| `title`, `description` | the text for every language; titles hold at most 32 characters (with the ` 1/3` numbering included for a list) and descriptions at most 370 |
| `titles`, `descriptions` | per language (`ja`, `en-US`, `fr-US`, `es-US`, `en-EU`, `fr-EU`, `es-EU`, `de`, `it`, `nl`, `ru`, `pt`); these win over the general text |
| `start`, `end` | `2026-10-25` or `2026-10-25T18:00:00Z`; the end defaults to a hundred years after the start |
| `pack` | optional banner pack name |

A text that does not fit is an error, never cut off. **No restart is needed:** every time the game asks about the event list (it does so first whenever it opens Course World, and downloads the file right after), the server looks at `courses/900000.bin`. If the file was replaced, it updates the object's size and update time, logs "Event course list changed" and answers with the new values, so the game gets the new list on the same visit. There is no timer. (A server older than this feature reads the size only at start-up, so it needs a restart unless the new file has exactly the same size.) The game compares the update time with the one in its save data to decide whether to show the "new event courses" notice; the server also sets it at every start.

**The courses an event opens** must be objects in the catalog. In the Cemu test the 46 originals were imported with data type 51. It is not known whether an ordinary archive level (data type 3) works there, or whether the type matters; that is the first thing to try with a custom list.

## Not tested

- A custom list in the game: titles per language, the dates, the order of the menu, and whether an ordinary level can be an event course.
- Whether the game checks start and end dates.
- What the flag byte and the banner packs do.

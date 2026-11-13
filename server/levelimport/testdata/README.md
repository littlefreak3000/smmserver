# Sample level files (not in the repository)

Some tests need one real Super Mario Maker course file. The repository does not carry level files, so they are skipped
unless you add one here:

- `levelimport/testdata/sample.bin`: any complete course file (a `.bin` the game downloads: it starts with the course
  header followed by ASH0-compressed sections).
- `levelimport/testdata/sample.meta`: the `meta_binary` that `node tools/build-bin.mjs` computes for that same file.
- `ash0/testdata/sample.bin`: the same course file.
- `ash0/testdata/course_data.cdt`: its decompressed `course_data.cdt` section (what the ASH0 decoder must produce).

The `.gitignore` keeps these names out of commits.

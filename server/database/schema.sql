-- Catalog schema. Single source of truth: embedded into the Go server (database/sqlite.go) and read by
-- tools/import-archive.mjs. All statements are idempotent. Names are unqualified: the server attaches this
-- file under the schema name "datastore" so the original "datastore.objects" queries work unchanged.
--
-- Array columns are JSON arrays in TEXT; timestamps are UTC text in the form
-- 'YYYY-MM-DD HH:MM:SS[.fff]+00:00' (what the driver reads and writes with _time_format=sqlite).

-- Super Mario Maker has non-standard DataID requirements.
-- DataID 900000 is reserved for the event course metadata file, and official event courses begin at
-- DataID 930010 and end at DataID 930050. To prevent a collision, new course IDs start AFTER 930050
-- (the original server used a Postgres sequence starting at 940000; see the sqlite_sequence seed below).
-- Course share codes are generated from the DataID: 8 hex digits, upper 2 bytes a checksum of the lower
-- 6 bytes, so IDs must fit in 48 bits.

-- "deletion_reason" and "under_review" are specific to SMM. Everything else is stock.
CREATE TABLE IF NOT EXISTS objects (
	data_id INTEGER PRIMARY KEY AUTOINCREMENT,
	upload_completed BOOLEAN NOT NULL DEFAULT 0,
	deleted BOOLEAN NOT NULL DEFAULT 0,
	deletion_reason INTEGER NOT NULL DEFAULT 0,
	under_review BOOLEAN NOT NULL DEFAULT 0,
	owner INTEGER,
	size INTEGER,
	name TEXT,
	data_type INTEGER,
	meta_binary BLOB,
	permission INTEGER,
	permission_recipients TEXT NOT NULL DEFAULT '[]',
	delete_permission INTEGER,
	delete_permission_recipients TEXT NOT NULL DEFAULT '[]',
	flag INTEGER,
	period INTEGER,
	refer_data_id INTEGER,
	tags TEXT NOT NULL DEFAULT '[]',
	persistence_slot_id INTEGER,
	extra_data TEXT NOT NULL DEFAULT '[]',
	access_password INTEGER NOT NULL DEFAULT 0,
	update_password INTEGER NOT NULL DEFAULT 0,
	creation_date TIMESTAMP,
	update_date TIMESTAMP
);

-- The nex QBuffer type cannot scan NULL, so a NULL meta_binary would break every listing.
-- Turn NULL into an empty blob on the way in (Postgres had the same weakness).
CREATE TRIGGER IF NOT EXISTS objects_meta_binary_insert AFTER INSERT ON objects
	WHEN NEW.meta_binary IS NULL
	BEGIN UPDATE objects SET meta_binary = x'' WHERE data_id = NEW.data_id; END;

CREATE TRIGGER IF NOT EXISTS objects_meta_binary_update AFTER UPDATE OF meta_binary ON objects
	WHEN NEW.meta_binary IS NULL
	BEGIN UPDATE objects SET meta_binary = x'' WHERE data_id = NEW.data_id; END;

CREATE INDEX IF NOT EXISTS objects_owner ON objects (owner);
CREATE INDEX IF NOT EXISTS objects_creation ON objects (creation_date);

-- Unsure what like half of this is but the client sends it so we saves it
CREATE TABLE IF NOT EXISTS object_ratings (
	data_id INTEGER,
	slot INTEGER,
	flag INTEGER,
	internal_flag INTEGER,
	lock_type INTEGER,
	initial_value INTEGER,
	range_min INTEGER,
	range_max INTEGER,
	period_hour INTEGER,
	period_duration INTEGER,
	total_value INTEGER,
	count INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(data_id, slot)
);

-- Custom rankings are specific to SMM
CREATE TABLE IF NOT EXISTS object_custom_rankings (
	data_id INTEGER,
	application_id INTEGER,
	value INTEGER,
	PRIMARY KEY(data_id, application_id)
);
CREATE INDEX IF NOT EXISTS custom_rankings_app_value ON object_custom_rankings (application_id, value);

-- BufferQueues are specific to SMM. The real server does not allow duplicate buffers in a given slot for
-- an object, even if uploaded by different users.
CREATE TABLE IF NOT EXISTS buffer_queues (
	data_id INTEGER,
	slot INTEGER,
	creation_date TIMESTAMP,
	buffer BLOB,
	PRIMARY KEY(data_id, slot, buffer)
);

-- Course records are specific to SMM
CREATE TABLE IF NOT EXISTS course_records (
	data_id INTEGER,
	slot INTEGER,
	first_pid INTEGER,
	best_pid INTEGER,
	best_score INTEGER,
	creation_date TIMESTAMP,
	update_date TIMESTAMP,
	PRIMARY KEY(data_id, slot)
);

-- Home account service (server/homeaccount): NNID -> PID registry. A PID is derived from the NNID on first
-- sight and then stays fixed; edit a row to make an NNID "own" an imported creator's PID.
CREATE TABLE IF NOT EXISTS accounts (
	nnid TEXT PRIMARY KEY COLLATE NOCASE,
	pid INTEGER NOT NULL UNIQUE,
	created TIMESTAMP
);

-- Levels pulled from the public archive (server/levelimport, server/pool): where each one came from and its
-- clear rate, so the pool can be stratified by difficulty, a level can be re-fetched, and an archive level is
-- never confused with one a player uploaded to this server.
CREATE TABLE IF NOT EXISTS level_sources (
	data_id INTEGER PRIMARY KEY,
	url TEXT NOT NULL,
	clear_rate REAL,
	attempts INTEGER,
	stars INTEGER,
	fetched TIMESTAMP
);
CREATE INDEX IF NOT EXISTS level_sources_rate ON level_sources (clear_rate);

-- Challenge seeds (server/challenge): with a seed active, a list request returns the same levels every time for the same
-- seed, so several players can run the same random challenge. scope is "*" (everyone) or "nnid:<name>" (one player).
CREATE TABLE IF NOT EXISTS challenge_seeds (
	scope TEXT PRIMARY KEY COLLATE NOCASE,
	seed TEXT NOT NULL,
	updated TIMESTAMP
);

-- Levels whose per-level answer (world record, first clear, creator and record holders) was fetched from the archive
-- API, whether or not it had a record, so the lookup happens once.
CREATE TABLE IF NOT EXISTS level_lookup (
	data_id INTEGER PRIMARY KEY,
	checked TIMESTAMP
);

-- Levels of the whole-archive index (server/index) that turned out to have no usable capture; never picked again.
CREATE TABLE IF NOT EXISTS index_dead (
	data_id INTEGER PRIMARY KEY,
	marked TIMESTAMP
);

-- Emulates `CREATE SEQUENCE ... START 940000`: AUTOINCREMENT hands out max(seq, max rowid)+1
INSERT INTO sqlite_sequence (name, seq)
	SELECT 'objects', 939999
	WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'objects');

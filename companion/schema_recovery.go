package main

// Exact SQL strings extracted from the live ELF; names are reconstruction aids.

// ELF 0xa0d193, 2301 bytes.
const recoveredBaseSchemaSQL = `CREATE TABLE IF NOT EXISTS settings(k TEXT PRIMARY KEY,v TEXT NOT NULL);
 DELETE FROM settings WHERE k IN ('playback_resolver_enabled','playback_resolver_port','playback_resolver_lookup_ips');
 CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,name TEXT UNIQUE NOT NULL ,hash TEXT NOT NULL,admin BIGINT NOT NULL DEFAULT 0,first_admin BIGINT NOT NULL DEFAULT 0,max_devices BIGINT NOT NULL DEFAULT 2);
 CREATE TABLE IF NOT EXISTS tokens(hash TEXT PRIMARY KEY,user_id TEXT REFERENCES users(id) ON DELETE CASCADE,device TEXT NOT NULL,expires BIGINT NOT NULL);
 CREATE TABLE IF NOT EXISTS user_avatars(user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,mime TEXT NOT NULL,data BYTEA NOT NULL,updated BIGINT NOT NULL);
 CREATE TABLE IF NOT EXISTS api_keys(id TEXT PRIMARY KEY,name TEXT NOT NULL,hash TEXT UNIQUE NOT NULL,created BIGINT NOT NULL);
 CREATE TABLE IF NOT EXISTS libraries(id TEXT PRIMARY KEY,name TEXT NOT NULL,path TEXT UNIQUE NOT NULL,kind TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'idle',scanned BIGINT NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',count BIGINT NOT NULL DEFAULT 0,duration DOUBLE PRECISION NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS items(id TEXT PRIMARY KEY,lib TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,parent TEXT NOT NULL,name TEXT NOT NULL,kind TEXT NOT NULL,path TEXT UNIQUE NOT NULL,url TEXT NOT NULL DEFAULT '',overview TEXT NOT NULL DEFAULT '',poster TEXT NOT NULL DEFAULT '',year BIGINT NOT NULL DEFAULT 0,season BIGINT NOT NULL DEFAULT 0,episode BIGINT NOT NULL DEFAULT 0,mtime BIGINT NOT NULL DEFAULT 0,size BIGINT NOT NULL DEFAULT 0,seen TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS items_lib_name ON items(lib,name,id);
 CREATE INDEX IF NOT EXISTS items_parent_name ON items(parent,name,id);
 CREATE INDEX IF NOT EXISTS items_kind ON items(kind);
 CREATE INDEX IF NOT EXISTS items_seen ON items(lib,seen);
 CREATE TABLE IF NOT EXISTS plays(user_id TEXT REFERENCES users(id) ON DELETE CASCADE,device TEXT NOT NULL,item TEXT NOT NULL,updated BIGINT NOT NULL,PRIMARY KEY(user_id,device));
 CREATE TABLE IF NOT EXISTS userdata(user_id TEXT REFERENCES users(id) ON DELETE CASCADE,item TEXT REFERENCES items(id) ON DELETE CASCADE,position BIGINT NOT NULL DEFAULT 0,played BIGINT NOT NULL DEFAULT 0,PRIMARY KEY(user_id,item));`

// ELF 0xa083ed, 494 bytes.
const recoveredUserDataSchemaSQL = `CREATE TABLE IF NOT EXISTS userdata_extra(
 user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
 item TEXT REFERENCES items(id) ON DELETE CASCADE,
 favorite BIGINT NOT NULL DEFAULT 0,
 play_count BIGINT NOT NULL DEFAULT 0,
 last_played TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(user_id,item));
 CREATE TABLE IF NOT EXISTS resume_hidden(
 user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
 item TEXT REFERENCES items(id) ON DELETE CASCADE,
 updated BIGINT NOT NULL,
 PRIMARY KEY(user_id,item));`

// ELF 0xa087f7, 586 bytes.
const recoveredIntroSchemaSQL = `CREATE TABLE IF NOT EXISTS intro_markers(series_id TEXT NOT NULL,season BIGINT NOT NULL,parent_id TEXT NOT NULL,intro_start_ticks BIGINT NOT NULL DEFAULT 0,intro_end_ticks BIGINT NOT NULL DEFAULT 0,credits_start_ticks BIGINT NOT NULL DEFAULT 0,intro_samples BIGINT NOT NULL DEFAULT 0,credits_samples BIGINT NOT NULL DEFAULT 0,sample_count BIGINT NOT NULL DEFAULT 0,confidence DOUBLE PRECISION NOT NULL DEFAULT 0,source TEXT NOT NULL DEFAULT 'behavior',updated_at BIGINT NOT NULL,PRIMARY KEY(series_id,season)); CREATE INDEX IF NOT EXISTS intro_markers_parent ON intro_markers(parent_id)`

// ELF 0xa0ac2c, 1585 bytes.
const recoveredSortSchemaSQL = `
 ALTER TABLE items ADD COLUMN IF NOT EXISTS added_at BIGINT NOT NULL DEFAULT 0;
 ALTER TABLE items ADD COLUMN IF NOT EXISTS premiere_date TEXT NOT NULL DEFAULT '';
 ALTER TABLE items ADD COLUMN IF NOT EXISTS sort_name TEXT NOT NULL DEFAULT '';
 ALTER TABLE items ADD COLUMN IF NOT EXISTS random_key TEXT NOT NULL DEFAULT '';
 CREATE INDEX IF NOT EXISTS items_random_order ON items(random_key,id);
 CREATE INDEX IF NOT EXISTS items_lib_random_order ON items(lib,random_key,id);

 CREATE OR REPLACE FUNCTION preserve_item_sort_fields() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='INSERT' THEN
   IF NEW.added_at=0 THEN
     NEW.added_at=(extract(epoch from clock_timestamp())*1000000000)::bigint;
   END IF;
   IF NEW.random_key='' THEN
     NEW.random_key=md5(random()::text || clock_timestamp()::text || NEW.id);
   END IF;
 ELSE
   IF OLD.added_at<>0 AND NOT (
     COALESCE(current_setting('go_emby.scanner_upsert',true),'')='true'
     AND OLD.kind IN ('Movie','Episode') AND NEW.kind IN ('Movie','Episode')
     AND (OLD.url,OLD.mtime,OLD.size) IS DISTINCT FROM (NEW.url,NEW.mtime,NEW.size)
   ) THEN NEW.added_at=OLD.added_at; END IF;
   IF OLD.random_key<>'' THEN NEW.random_key=OLD.random_key; END IF;
 END IF;

 IF NEW.sort_name='' THEN NEW.sort_name=NEW.name; END IF;

 IF NEW.premiere_date='' AND NEW.year BETWEEN 1 AND 9999 THEN
   NEW.premiere_date=lpad(NEW.year::text,4,'0')||'-01-01';
 END IF;

 RETURN NEW;
 END $$;

 CREATE OR REPLACE TRIGGER item_sort_fields
 BEFORE INSERT OR UPDATE ON items
 FOR EACH ROW EXECUTE FUNCTION preserve_item_sort_fields();
 `

// ELF 0xa092aa, 807 bytes.
const recoveredSortBackfillSQL = `
 WITH batch AS (
   SELECT id
   FROM items
   WHERE added_at=0
      OR random_key=''
      OR sort_name=''
      OR (premiere_date='' AND year BETWEEN 1 AND 9999)
   ORDER BY id
   LIMIT 500
 )
 UPDATE items AS i
 SET
   added_at = CASE
     WHEN i.added_at=0 THEN
       CASE
         WHEN i.mtime>0 THEN i.mtime
         ELSE (extract(epoch from clock_timestamp())*1000000000)::bigint
       END
     ELSE i.added_at
   END,
   random_key = CASE
     WHEN i.random_key='' THEN md5(i.id)
     ELSE i.random_key
   END,
   sort_name = CASE
     WHEN i.sort_name='' THEN i.name
     ELSE i.sort_name
   END,
   premiere_date = CASE
     WHEN i.premiere_date='' AND i.year BETWEEN 1 AND 9999
       THEN lpad(i.year::text,4,'0')||'-01-01'
     ELSE i.premiere_date
   END
 FROM batch
 WHERE i.id=batch.id
 `

// ELF 0xa0996a, 1072 bytes.
const recoveredScannerUpsertSQL = `INSERT INTO items(id,lib,parent,name,kind,path,url,overview,poster,year,season,episode,mtime,size,seen) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET parent=excluded.parent,name=COALESCE((SELECT name FROM media_display_names WHERE item=excluded.id),CASE WHEN excluded.name=? OR (items.name ~ '[一-鿿]' AND excluded.name !~ '[一-鿿]') THEN COALESCE(NULLIF(items.name,''),excluded.name) ELSE excluded.name END),kind=excluded.kind,url=excluded.url,overview=COALESCE(NULLIF(excluded.overview,''),items.overview),poster=COALESCE(NULLIF(excluded.poster,''),items.poster),year=CASE WHEN excluded.year>0 THEN excluded.year ELSE items.year END,season=excluded.season,episode=excluded.episode,mtime=excluded.mtime,size=excluded.size,seen=excluded.seen,added_at=CASE WHEN items.kind IN ('Movie','Episode') AND excluded.kind IN ('Movie','Episode') AND (items.url,items.mtime,items.size) IS DISTINCT FROM (excluded.url,excluded.mtime,excluded.size) THEN (extract(epoch from clock_timestamp())*1000000000)::bigint ELSE items.added_at END RETURNING (xmax = 0)`

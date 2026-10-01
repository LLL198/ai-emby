package main

func (a *App) postgresSchema() error {
	_, e := a.db.Exec(`
 CREATE EXTENSION IF NOT EXISTS pg_trgm;
 CREATE TABLE IF NOT EXISTS scan_snapshots(lib TEXT PRIMARY KEY REFERENCES libraries(id) ON DELETE CASCADE,data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS item_genres(item TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,genre TEXT NOT NULL,PRIMARY KEY(item,genre));
 CREATE INDEX IF NOT EXISTS genres_genre_item ON item_genres(genre,item);
 CREATE OR REPLACE FUNCTION sync_item_genres() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 DELETE FROM item_genres WHERE item=NEW.item;
 INSERT INTO item_genres(item,genre) SELECT NEW.item,value FROM jsonb_array_elements_text(COALESCE(NULLIF(NEW.data::jsonb->'Genres','null'::jsonb),'[]'::jsonb)) AS g(value) ON CONFLICT DO NOTHING;
 RETURN NEW;
 END $$;
 CREATE OR REPLACE TRIGGER metadata_genres_sync AFTER INSERT OR UPDATE OF data ON item_metadata FOR EACH ROW EXECUTE FUNCTION sync_item_genres();
 CREATE UNIQUE INDEX IF NOT EXISTS users_name_ci ON users(lower(name));
 CREATE INDEX IF NOT EXISTS items_name_id ON items(name,id);
 CREATE INDEX IF NOT EXISTS items_kind_name_id ON items(kind,name,id);
 CREATE INDEX IF NOT EXISTS items_lib_kind_name_id ON items(lib,kind,name,id);
 CREATE INDEX IF NOT EXISTS items_parent_kind_name_id ON items(parent,kind,name,id);
 CREATE INDEX IF NOT EXISTS items_mtime_id ON items(mtime,id);
 CREATE INDEX IF NOT EXISTS items_lib_mtime_id ON items(lib,mtime,id);
 CREATE INDEX IF NOT EXISTS items_kind_mtime_id ON items(kind,mtime,id);
 CREATE INDEX IF NOT EXISTS items_lib_kind_mtime_id ON items(lib,kind,mtime,id);
 CREATE INDEX IF NOT EXISTS items_parent_mtime_id ON items(parent,mtime,id);
 CREATE INDEX IF NOT EXISTS items_year_id ON items(year,id);
 CREATE INDEX IF NOT EXISTS items_lib_year_id ON items(lib,year,id);
 CREATE INDEX IF NOT EXISTS items_kind_year_id ON items(kind,year,id);
 CREATE INDEX IF NOT EXISTS items_lib_kind_year_id ON items(lib,kind,year,id);
 CREATE INDEX IF NOT EXISTS items_parent_year_id ON items(parent,year,id);
 CREATE INDEX IF NOT EXISTS items_parent_episode_id ON items(parent,season,episode,id);
 CREATE INDEX IF NOT EXISTS items_lib_episode_id ON items(lib,season,episode,id);
 CREATE INDEX IF NOT EXISTS items_kind_episode_id ON items(kind,season,episode,id);
 CREATE INDEX IF NOT EXISTS items_name_search ON items USING gin(name gin_trgm_ops);
 CREATE INDEX IF NOT EXISTS items_lib_id ON items(lib,id);
 CREATE INDEX IF NOT EXISTS items_lib_poster ON items(lib,name,id) WHERE poster<>'';
 CREATE INDEX IF NOT EXISTS people_name_person ON item_people(name,person,item);
 CREATE INDEX IF NOT EXISTS people_person_item ON item_people(person,item);
 CREATE INDEX IF NOT EXISTS userdata_played ON userdata(user_id,item) WHERE played=1;
 CREATE INDEX IF NOT EXISTS userdata_resume ON userdata(user_id,item) WHERE position>0 AND played=0;
 CREATE INDEX IF NOT EXISTS userdata_item ON userdata(item);
 CREATE INDEX IF NOT EXISTS resume_user_updated ON resume_activity(user_id,updated DESC,item DESC);
 CREATE INDEX IF NOT EXISTS resume_item ON resume_activity(item);
 CREATE INDEX IF NOT EXISTS tokens_user ON tokens(user_id);
 CREATE INDEX IF NOT EXISTS tokens_expires ON tokens(expires);
 CREATE INDEX IF NOT EXISTS plays_updated ON plays(updated);
 CREATE INDEX IF NOT EXISTS metadata_genres ON item_metadata USING gin((data::jsonb->'Genres'));
 CREATE INDEX IF NOT EXISTS metadata_tmdb ON item_metadata((data::jsonb->>'TMDB')) WHERE data::jsonb->>'TMDB' IS NOT NULL;
 ALTER TABLE item_people SET (autovacuum_vacuum_scale_factor=0.05,autovacuum_analyze_scale_factor=0.02);
 ALTER TABLE item_metadata SET (autovacuum_vacuum_scale_factor=0.05,autovacuum_analyze_scale_factor=0.02);
 ALTER TABLE items SET (autovacuum_vacuum_scale_factor=0.02,autovacuum_analyze_scale_factor=0.01);
 `)
	return e
}

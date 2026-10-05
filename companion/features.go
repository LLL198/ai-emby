package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

type featureState struct {
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex
	jobs             map[string]context.CancelFunc
	wg               sync.WaitGroup
	playMu           sync.Mutex
	plays            map[string]featurePlaybackSample
	cacheMu          sync.Mutex
	transcodes       map[string]*featureTranscode
	trackingMu       sync.Mutex
	trackingSearchMu sync.Mutex
	trackingParseMu  sync.Mutex
	trackingBusy     bool
	trackingQueue    chan trackingJob
}

const featureSchema = `
CREATE TABLE IF NOT EXISTS feature_tracking_subscriptions(id TEXT PRIMARY KEY,data TEXT NOT NULL,created BIGINT NOT NULL,last_search BIGINT NOT NULL DEFAULT 0,next_search BIGINT NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'idle',error TEXT NOT NULL DEFAULT '',last_count BIGINT NOT NULL DEFAULT 0);
ALTER TABLE feature_tracking_subscriptions ADD COLUMN IF NOT EXISTS context_kind TEXT NOT NULL DEFAULT 'subscription';
CREATE TABLE IF NOT EXISTS feature_tracking_imports(subscription TEXT PRIMARY KEY REFERENCES feature_tracking_subscriptions(id) ON DELETE CASCADE,data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS feature_tracking_resources(id TEXT PRIMARY KEY,subscription TEXT NOT NULL REFERENCES feature_tracking_subscriptions(id) ON DELETE CASCADE,cloud TEXT NOT NULL,data TEXT NOT NULL,fingerprint TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'new',first_seen BIGINT NOT NULL,updated BIGINT NOT NULL,last_seen BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS feature_tracking_resources_list ON feature_tracking_resources(subscription,status,updated DESC,id);
CREATE TABLE IF NOT EXISTS feature_tracking_resource_parses(resource TEXT PRIMARY KEY REFERENCES feature_tracking_resources(id) ON DELETE CASCADE,data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS feature_file_roots(id TEXT PRIMARY KEY,name TEXT NOT NULL,path TEXT NOT NULL UNIQUE,host_path TEXT NOT NULL DEFAULT '',read_only BOOLEAN NOT NULL DEFAULT false,created BIGINT NOT NULL);
CREATE TABLE IF NOT EXISTS feature_media_issues(id TEXT PRIMARY KEY, source TEXT NOT NULL, path TEXT NOT NULL, status TEXT NOT NULL, reason TEXT NOT NULL, proposed TEXT NOT NULL DEFAULT '', directory BOOLEAN NOT NULL DEFAULT false, kind TEXT NOT NULL DEFAULT '', item_id TEXT NOT NULL DEFAULT '', ignored BOOLEAN NOT NULL DEFAULT false, created BIGINT NOT NULL, updated BIGINT NOT NULL, UNIQUE(source,path));
CREATE INDEX IF NOT EXISTS feature_media_issues_pending ON feature_media_issues(ignored,updated DESC,id);
CREATE TABLE IF NOT EXISTS feature_cloud_mounts(id TEXT PRIMARY KEY, name TEXT NOT NULL, driver TEXT NOT NULL, storage_id BIGINT NOT NULL DEFAULT 0, secret TEXT NOT NULL, enabled BIGINT NOT NULL DEFAULT 1, created BIGINT NOT NULL);
ALTER TABLE feature_cloud_mounts ADD COLUMN IF NOT EXISTS playback_mode TEXT NOT NULL DEFAULT 'redirect';
ALTER TABLE feature_cloud_mounts ADD COLUMN IF NOT EXISTS quark_mobile_url TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS feature_tasks(id TEXT PRIMARY KEY, category TEXT NOT NULL, item TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, data TEXT NOT NULL, started BIGINT NOT NULL, updated BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS feature_tasks_updated ON feature_tasks(updated DESC);
CREATE TABLE IF NOT EXISTS feature_library_policy(lib TEXT PRIMARY KEY REFERENCES libraries(id) ON DELETE CASCADE, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS feature_metadata(item TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE, data TEXT NOT NULL, locked BIGINT NOT NULL DEFAULT 1, updated BIGINT NOT NULL);
CREATE TABLE IF NOT EXISTS feature_artwork(item TEXT REFERENCES items(id) ON DELETE CASCADE, kind TEXT NOT NULL, mime TEXT NOT NULL, data BYTEA NOT NULL, updated BIGINT NOT NULL, PRIMARY KEY(item,kind));
CREATE TABLE IF NOT EXISTS feature_collections(id TEXT PRIMARY KEY, name TEXT NOT NULL, owner TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, public BIGINT NOT NULL DEFAULT 0, created BIGINT NOT NULL);
CREATE TABLE IF NOT EXISTS feature_collection_items(collection TEXT REFERENCES feature_collections(id) ON DELETE CASCADE, item TEXT REFERENCES items(id) ON DELETE CASCADE, added BIGINT NOT NULL, PRIMARY KEY(collection,item));
CREATE TABLE IF NOT EXISTS feature_play_history(user_id TEXT REFERENCES users(id) ON DELETE CASCADE,item TEXT NOT NULL,day TEXT NOT NULL,seconds DOUBLE PRECISION NOT NULL DEFAULT 0,count BIGINT NOT NULL DEFAULT 0,updated BIGINT NOT NULL,PRIMARY KEY(user_id,item,day));
CREATE INDEX IF NOT EXISTS feature_play_history_day ON feature_play_history(day,user_id);
CREATE TABLE IF NOT EXISTS feature_devices(user_id TEXT REFERENCES users(id) ON DELETE CASCADE,device TEXT NOT NULL,name TEXT NOT NULL DEFAULT '',client TEXT NOT NULL DEFAULT '',last_seen BIGINT NOT NULL,PRIMARY KEY(user_id,device));
CREATE TABLE IF NOT EXISTS feature_chapters(item TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,data TEXT NOT NULL,updated BIGINT NOT NULL);
CREATE OR REPLACE FUNCTION feature_preserve_item() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE m jsonb;
BEGIN
IF COALESCE(current_setting('ai_emby.metadata_write',true),'')='true' THEN RETURN NEW; END IF;
SELECT data::jsonb INTO m FROM feature_metadata WHERE item=NEW.id;
IF m IS NOT NULL THEN
 IF m ? 'Title' THEN NEW.name=m->>'Title'; END IF;
 IF m ? 'Plot' THEN NEW.overview=m->>'Plot'; END IF;
 IF m ? 'Year' THEN NEW.year=(m->>'Year')::bigint; END IF;
 IF m ? 'Season' THEN NEW.season=(m->>'Season')::bigint; END IF;
 IF m ? 'Episode' THEN NEW.episode=(m->>'Episode')::bigint; END IF;
END IF;
RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER feature_item_protection BEFORE UPDATE ON items FOR EACH ROW EXECUTE FUNCTION feature_preserve_item();
CREATE OR REPLACE FUNCTION feature_preserve_metadata() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE m jsonb;
BEGIN
IF COALESCE(current_setting('ai_emby.metadata_write',true),'')='true' THEN RETURN NEW; END IF;
SELECT data::jsonb INTO m FROM feature_metadata WHERE item=NEW.item;
IF m IS NOT NULL THEN NEW.data=(NEW.data::jsonb || (m - 'Year' - 'Season' - 'Episode' - 'Tags' - 'Countries'))::text; END IF;
RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER feature_metadata_protection BEFORE INSERT OR UPDATE ON item_metadata FOR EACH ROW EXECUTE FUNCTION feature_preserve_metadata();
`

func (a *App) initFeatures() error {
	for attempt := 0; attempt < 60; attempt++ {
		var ready bool
		_ = a.db.QueryRow("SELECT to_regclass('item_metadata') IS NOT NULL AND to_regclass('libraries') IS NOT NULL").Scan(&ready)
		if ready {
			break
		}
		if attempt == 59 {
			return errors.New("核心服务数据库尚未就绪")
		}
		time.Sleep(time.Second)
	}
	if _, err := a.db.DB.Exec(featureSchema); err != nil {
		return err
	}
	if _, err := a.db.DB.Exec(cloudTransferSchema); err != nil {
		return err
	}
	if err := a.migrateServerName(); err != nil {
		return err
	}
	a.features.ctx, a.features.cancel = context.WithCancel(context.Background())
	a.loadProxySettings()
	a.features.jobs = map[string]context.CancelFunc{}
	a.features.plays = map[string]featurePlaybackSample{}
	a.features.transcodes = map[string]*featureTranscode{}
	a.features.trackingQueue = make(chan trackingJob, 1)
	if _, err := a.db.Exec("UPDATE feature_cloud_transfers SET state='paused',control='' WHERE state='running'"); err != nil {
		return err
	}
	if _, err := a.db.Exec("UPDATE feature_tracking_subscriptions SET state='interrupted',error='服务重启，等待下次搜索' WHERE state='running'"); err != nil {
		return err
	}
	_, err := a.db.Exec("UPDATE feature_tasks SET state='interrupted' WHERE state IN ('waiting','queued','running','counting','cleaning','paused')")
	if err != nil {
		return err
	}
	a.features.wg.Add(1)
	go func() { defer a.features.wg.Done(); a.featureBackground(a.features.ctx) }()
	a.features.wg.Add(1)
	go func() { defer a.features.wg.Done(); a.trackingBackground(a.features.ctx) }()
	a.features.wg.Add(1)
	go func() { defer a.features.wg.Done(); a.cloudTransferBackground(a.features.ctx) }()
	return nil
}

func (a *App) stopFeatures() {
	if a.features.cancel != nil {
		a.features.cancel()
	}
	a.features.mu.Lock()
	for _, cancel := range a.features.jobs {
		cancel()
	}
	a.features.mu.Unlock()
	a.features.cacheMu.Lock()
	for _, job := range a.features.transcodes {
		if job.cancel != nil {
			job.cancel()
		}
	}
	a.features.cacheMu.Unlock()
}

func (a *App) featureSetting(key string, target any) {
	var raw string
	if a.db.QueryRow("SELECT v FROM settings WHERE k=?", "feature:"+key).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), target)
	}
}

func (a *App) saveFeatureSetting(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = a.db.Exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", "feature:"+key, string(data))
	return err
}

func (a *App) featureRoute(w http.ResponseWriter, r *http.Request, user User) bool {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/admin/features/") && !strings.HasPrefix(path, "/features/") {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasPrefix(path, "/admin/") && (!user.Admin || user.API) {
		fail(w, 403, "需要管理员账号")
		return true
	}
	switch {
	case path == "/admin/features/anti-theft" || path == "/admin/features/anti-theft/account":
		a.antiTheftAPI(w, r)
	case strings.HasPrefix(path, "/admin/features/cloud-transfer"):
		a.cloudTransferAPI(w, r)
	case strings.HasPrefix(path, "/admin/features/tracking"):
		a.trackingAPI(w, r)
	case strings.HasPrefix(path, "/admin/features/file-roots"):
		a.managedFileRootsAPI(w, r)
	case strings.HasPrefix(path, "/admin/features/local-files"):
		a.managedFilesAPI(w, r)
	case path == "/admin/features/media-issues":
		a.mediaIssuesAPI(w, r)
	case strings.HasPrefix(path, "/admin/features/naming/"):
		a.namingAPI(w, r, user)
	case strings.HasPrefix(path, "/admin/features/cloud"):
		a.cloudAdmin(w, r)
	case path == "/admin/features/tasks":
		a.featureTasksAPI(w, r)
	case path == "/admin/features/task-action":
		a.featureTaskAction(w, r)
	case path == "/admin/features/task-import":
		a.featureImportActivities(w, r)
	case path == "/admin/features/libraries":
		a.featureLibraryAPI(w, r)
	case path == "/admin/features/schedule":
		a.featureScheduleAPI(w, r)
	case path == "/admin/features/metadata":
		a.featureMetadataAPI(w, r)
	case path == "/admin/features/identify":
		a.featureIdentifyAPI(w, r)
	case path == "/admin/features/artwork":
		a.featureArtworkAPI(w, r)
	case path == "/admin/features/library-action":
		a.featureLibraryAction(w, r)
	case path == "/admin/features/cache":
		a.featureCacheAPI(w, r)
	case path == "/admin/features/devices":
		a.featureDevicesAPI(w, r)
	case path == "/admin/features/playback":
		a.featurePlaybackAdmin(w, r)
	case path == "/admin/features/playback-event":
		a.featurePlaybackEvent(w, r, user)
	case path == "/admin/features/import":
		a.featureServerImport(w, r, user)
	case path == "/admin/features/network":
		a.featureNetworkAPI(w, r)
	case path == "/admin/features/covers":
		a.featureCoverAPI(w, r)
	case path == "/features/catalog":
		a.featureCatalogAPI(w, r, user)
	case path == "/features/facets":
		a.featureFacetsAPI(w, r, user)
	case path == "/features/collections":
		a.featureCollectionsAPI(w, r, user)
	case path == "/features/collection-items":
		a.featureCollectionItems(w, r, user)
	case path == "/features/access":
		a.featureAccessAPI(w, r, user)
	case path == "/features/link":
		if featureMethod(w, r, http.MethodGet) {
			x, err := a.featureAccessibleItem(user, r.URL.Query().Get("ID"))
			if err != nil {
				featureError(w, err)
				return true
			}
			var c featureNetworkSettings
			a.featureSetting("network", &c)
			respond(w, M{"PublicURL": c.PublicURL, "Fragment": "#item/" + x.ID})
		}
	case path == "/features/metadata":
		a.featureItemMetadata(w, r, user)
	case path == "/features/artwork":
		a.featureArtworkImage(w, r, user)
	case path == "/features/playback-settings":
		a.featurePlaybackSettings(w, r, user)
	case path == "/features/playback-event":
		a.featurePlaybackEvent(w, r, user)
	case path == "/features/playback":
		a.featurePlaybackAPI(w, r, user)
	case path == "/features/subtitle":
		a.featureSubtitleAPI(w, r, user)
	case path == "/features/chapters":
		if featureMethod(w, r, http.MethodGet) {
			x, err := a.featureAccessibleItem(user, r.URL.Query().Get("ID"))
			if err != nil {
				featureError(w, err)
				return true
			}
			var raw string
			var list []M
			if a.db.QueryRow("SELECT data FROM feature_chapters WHERE item=?", x.ID).Scan(&raw) == nil {
				_ = json.Unmarshal([]byte(raw), &list)
			}
			if list == nil {
				list = []M{}
			}
			respond(w, M{"Items": list})
		}
	case path == "/features/appearance":
		if featureMethod(w, r, http.MethodGet) {
			style := featureCoverStyle{Font: "system-ui", Size: 20, Wrap: true}
			a.featureSetting("cover-style", &style)
			respond(w, style)
		}
	case path == "/features/library-settings":
		if featureMethod(w, r, http.MethodGet) {
			lib := r.URL.Query().Get("ID")
			if !a.featureLibraryAllowed(user, lib) {
				fail(w, 403, "无权访问这个媒体库")
			} else {
				p := a.featurePolicy(lib)
				respond(w, M{"SortBy": p.SortBy, "SortOrder": p.SortOrder})
			}
		}
	case strings.HasPrefix(path, "/features/stream/"):
		a.featureStream(w, r, user)
	case path == "/features/danmaku":
		a.featureDanmakuAPI(w, r, user)
	default:
		http.NotFound(w, r)
	}
	return true
}

func featureMethod(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	fail(w, 405, "不支持此操作")
	return false
}

func featureError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "内容不存在")
		return
	}
	fail(w, 500, "操作失败，请查看任务记录或服务日志")
}

func featureJSON(value any) string { data, _ := json.Marshal(value); return string(data) }

func featureLimit(r *http.Request, fallback, maxValue int) int {
	var n int
	_ = json.Unmarshal([]byte(r.URL.Query().Get("Limit")), &n)
	if n < 1 {
		n = fallback
	}
	if n > maxValue {
		n = maxValue
	}
	return n
}

func (a *App) featureLibraryAllowed(user User, lib string) bool {
	if user.Admin {
		return true
	}
	policy := a.featurePolicy(lib)
	if policy.RestrictUsers {
		allowed := false
		for _, id := range policy.Users {
			if id == user.ID {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	var hidden int
	if a.db.QueryRow("SELECT hidden FROM libraries WHERE id=?", lib).Scan(&hidden) != nil {
		return false
	}
	return hidden == 0 || a.canViewHiddenLibraries(user.ID)
}

func (a *App) featureAccessibleItem(user User, item string) (Item, error) {
	x, err := a.item(item)
	if err != nil {
		return x, err
	}
	if !a.featureLibraryAllowed(user, x.Lib) {
		return Item{}, sql.ErrNoRows
	}
	return x, nil
}

func (a *App) featureAccessAPI(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet) {
		return
	}
	lib := r.URL.Query().Get("Library")
	if item := r.URL.Query().Get("Item"); item != "" {
		x, err := a.item(item)
		if err != nil {
			featureError(w, err)
			return
		}
		lib = x.Lib
	}
	respond(w, M{"Allowed": a.featureLibraryAllowed(user, lib)})
}

func (a *App) featureItemMetadata(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodGet) {
		return
	}
	x, err := a.featureAccessibleItem(user, r.URL.Query().Get("ID"))
	if err != nil {
		featureError(w, err)
		return
	}
	respond(w, a.featureMetadataView(x))
}

func featureNow() int64 { return time.Now().Unix() }

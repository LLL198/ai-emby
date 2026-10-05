package main

import (
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func catalogFixture(t *testing.T) (*App, string, string) {
	t.Helper()
	a := testApp(t)
	var uid string
	a.db.QueryRow("SELECT id FROM users LIMIT 1").Scan(&uid)
	if _, e := a.db.Exec("INSERT INTO tokens VALUES(?,?,?,?)", digest("browse-test"), uid, "browse-device", time.Now().Unix()+600); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll("/media", 0755); e != nil {
		t.Fatal(e)
	}
	dir, e := os.MkdirTemp("/media", "compat-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if _, e := a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES('l','Films',?,'movies')", dir); e != nil {
		t.Fatal(e)
	}
	for _, x := range []struct{ id, kind, parent string }{{"m", "Movie", "l"}, {"n", "Movie", "l"}, {"o", "Movie", "l"}, {"s", "Series", "l"}, {"s1", "Season", "s"}, {"s2", "Season", "s"}, {"e1", "Episode", "s1"}, {"e2", "Episode", "s2"}} {
		p := filepath.Join(dir, x.id+".strm")
		season := 1
		if x.id == "s2" || x.id == "e2" {
			season = 2
		}
		if _, e := a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,season,episode,seen) VALUES(?,'l',?,?,?,?,?, ?,1,'g')", x.id, x.parent, x.id, x.kind, p, "http://example.invalid/video.mkv", season); e != nil {
			t.Fatal(e)
		}
	}
	nfo := `<movie><plot>Actual plot</plot><genre>Drama</genre><actor><name>Test Actor</name><role>Lead</role></actor><uniqueid type="tmdb">123</uniqueid><fileinfo><streamdetails><video><codec>h264</codec><width>1920</width><height>1080</height><durationinseconds>100</durationinseconds></video><audio><codec>aac</codec><channels>2</channels></audio></streamdetails></fileinfo></movie>`
	os.WriteFile(filepath.Join(dir, "m.nfo"), []byte(nfo), 0600)
	os.WriteFile(filepath.Join(dir, "o.nfo"), []byte(nfo), 0600)
	for _, name := range []string{"poster.png", "backdrop.png"} {
		f, e := os.Create(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		png.Encode(f, image.NewRGBA(image.Rect(0, 0, 20, 30)))
		f.Close()
	}
	if e := a.indexMetadata(""); e != nil {
		t.Fatal(e)
	}
	return a, uid, dir
}
func browseGet(a *App, path string, auth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	if auth {
		r.Header.Set("X-Emby-Token", "browse-test")
	}
	w := httptest.NewRecorder()
	a.serve(w, r)
	return w
}
func decodeM(t *testing.T, w *httptest.ResponseRecorder) M {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var m M
	if e := json.Unmarshal(w.Body.Bytes(), &m); e != nil {
		t.Fatal(e)
	}
	return m
}
func TestBrowseImagesAndMetadata(t *testing.T) {
	a, uid, _ := catalogFixture(t)
	a.db.Exec("INSERT INTO settings VALUES('hide_missing_actor_images','false')")
	for _, p := range []string{"/Users/" + uid + "/Items/Root", "/emby/Items/root"} {
		m := decodeM(t, browseGet(a, p, true))
		if m["IsFolder"] != true {
			t.Fatal(m)
		}
	}
	for _, p := range []string{"/Users/" + uid + "/Views", "/Items?ParentId=root", "/Library/MediaFolders"} {
		m := decodeM(t, browseGet(a, p, true))
		if m["TotalRecordCount"] != float64(1) {
			t.Fatal(m)
		}
	}
	m := decodeM(t, browseGet(a, "/emby/Users/"+uid+"/Items/m", true))
	if m["Overview"] != "Actual plot" || m["ProviderIds"].(map[string]any)["Tmdb"] != "123" || len(m["MediaStreams"].([]any)) != 2 {
		t.Fatal(m)
	}
	tag := m["ImageTags"].(map[string]any)["Primary"].(string)
	for _, p := range []string{"/Items/m/Images/Primary?tag=" + tag, "/emby/items/m/images/primary/0?Tag=" + tag, "/Items/m/Images/Primary/0/" + tag + "/jpg/200/300/0/0"} {
		w := browseGet(a, p, false)
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
			t.Fatal(p, w.Code, w.Body.String())
		}
	}
	if w := browseGet(a, "/Items/m/Images/Primary?tag=bad", false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := browseGet(a, "/Items/m/Images/Backdrop?tag="+tag, false); w.Code != 401 {
		t.Fatal("cross-type tag accepted")
	}
	if w := browseGet(a, "/Items/m/Images/Logo", true); w.Code != 404 {
		t.Fatal("missing logo must not return poster", w.Code)
	}
	if w := browseGet(a, "/Items/m/Images/Backdrop/1", true); w.Code != 404 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("GET", "/Items/m/Images/Primary?tag="+tag, nil)
	r.Header.Set("If-None-Match", `"`+tag+`"`)
	w := httptest.NewRecorder()
	a.serve(w, r)
	if w.Code != 304 {
		t.Fatal(w.Code)
	}
	people := m["People"].([]any)
	pid := people[0].(map[string]any)["Id"].(string)
	p := decodeM(t, browseGet(a, "/Items/"+pid, true))
	if p["Name"] != "Test Actor" {
		t.Fatal(p)
	}
	works := decodeM(t, browseGet(a, "/Items?Recursive=true&PersonIds="+pid, true))
	if works["TotalRecordCount"] != float64(2) {
		t.Fatal(works)
	}
	rec := decodeM(t, browseGet(a, "/Items/m/Similar?Limit=1", true))
	if rec["Items"].([]any)[0].(map[string]any)["Id"] != "o" {
		t.Fatal(rec)
	}
	if w := browseGet(a, "/Users/other/Items/Root", true); w.Code != 200 {
		t.Fatal("fixture admin expected access")
	}
	a.db.Exec("UPDATE users SET admin=0")
	if w := browseGet(a, "/Users/other/Items/Root", true); w.Code != 403 {
		t.Fatal("user isolation", w.Code)
	}
}
func TestSeriesNavigationAndLatest(t *testing.T) {
	a, uid, _ := catalogFixture(t)
	for _, v := range []struct {
		path  string
		count float64
		kind  string
	}{{"/Shows/s/Seasons", 2, "Season"}, {"/Shows/s/Episodes", 2, "Episode"}, {"/Shows/s/Episodes?SeasonId=s2", 1, "Episode"}, {"/Items?ParentId=s&Recursive=True&IncludeItemTypes=Episode", 2, "Episode"}, {"/Shows/NextUp?SeriesId=s", 0, "Episode"}} {
		m := decodeM(t, browseGet(a, v.path, true))
		if m["TotalRecordCount"] != v.count {
			t.Fatal(v.path, m)
		}
		for _, x := range m["Items"].([]any) {
			if x.(map[string]any)["Type"] != v.kind {
				t.Fatal(v.path, x)
			}
		}
	}
	a.db.Exec("INSERT INTO userdata(user_id,item,played) VALUES(?,'e1',1)", uid)
	m := decodeM(t, browseGet(a, "/Shows/NextUp?SeriesId=s", true))
	if len(m["Items"].([]any)) != 0 || m["TotalRecordCount"] != float64(0) {
		t.Fatal(m)
	}
	w := browseGet(a, "/Users/"+uid+"/Items/Latest", true)
	var latest []M
	if e := json.Unmarshal(w.Body.Bytes(), &latest); e != nil {
		t.Fatal(e)
	}
	if len(latest) != 4 {
		t.Fatal(latest)
	}
	decodeM(t, browseGet(a, "/Items?Recursive=true&IncludeItemTypes=Movie&SortBy=SortName&SortOrder=Descending&Limit=1", true))
	m = decodeM(t, browseGet(a, "/Items?Recursive=true&IncludeItemTypes=Movie&SortBy=SortName&SortOrder=Descending&Limit=1&StartIndex=1", true))
	if m["Items"].([]any)[0].(map[string]any)["Id"] != "n" || m["TotalRecordCount"] != float64(3) {
		t.Fatal(m)
	}
}

func TestNativeThumbnailDimensions(t *testing.T) {
	a, _, _ := catalogFixture(t)
	m := decodeM(t, browseGet(a, "/Items/m", true))
	tag := m["ImageTags"].(map[string]any)["Primary"].(string)
	for _, p := range []string{"/Items/m/Images/Primary?MaxWidth=10&tag=" + tag, "/emby/Items/m/Images/Primary/0/" + tag + "/jpg/10/30/0/0"} {
		w := browseGet(a, p, false)
		cfg, _, err := image.DecodeConfig(w.Body)
		if err != nil || cfg.Width != 10 || cfg.Height != 15 {
			t.Fatal(p, w.Code, cfg, err)
		}
	}
}

func TestGenreIDFilter(t *testing.T) {
	a, _, _ := catalogFixture(t)
	m := decodeM(t, browseGet(a, "/Items?Recursive=true&GenreIds=genre-"+digest("Drama")[:32], true))
	if m["TotalRecordCount"] != float64(2) {
		t.Fatal(m)
	}
}

func TestMovieOmitsEpisodeNumbers(t *testing.T) {
	a, _, _ := catalogFixture(t)
	x, err := a.item("m")
	if err != nil {
		t.Fatal(err)
	}
	m := a.dto(x)
	for _, key := range []string{"IndexNumber", "ParentIndexNumber"} {
		if _, exists := m[key]; exists {
			t.Fatalf("movie exposes %s", key)
		}
	}
	e, err := a.item("e1")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := a.dto(e)["IndexNumber"]; !exists {
		t.Fatal("episode lost its number")
	}
}

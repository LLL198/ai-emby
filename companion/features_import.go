package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type featureImportRequest struct {
	URL, Key, RemoteUser, LocalUser string
	Apply, Metadata, Progress       bool
	Mappings                        []featureImportMapping
}
type featureImportMapping struct{ RemoteID, Name, Kind, RemotePath, LocalPath string }
type featureRemoteItem struct {
	Id, Name, Type, Path, Overview, PremiereDate, OriginalTitle, OfficialRating string
	ProductionYear, IndexNumber, ParentIndexNumber                              int
	CommunityRating                                                             float64
	Genres, Tags, ProductionLocations                                           []string
	ProviderIds                                                                 map[string]string
	MediaStreams                                                                []M
	RunTimeTicks                                                                int64
	Chapters                                                                    []struct {
		Name               string
		StartPositionTicks int64
		MarkerType         string
	}
	UserData struct {
		PlaybackPositionTicks int64
		Played, IsFavorite    bool
		PlayCount             int
		LastPlayedDate        string
	}
}

func featureImportGet(ctx context.Context, request featureImportRequest, path string, out any) error {
	base, err := url.Parse(request.URL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return errors.New("服务地址无效")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(strings.Split(path, "?")[0], "/")
	_, query, has := strings.Cut(path, "?")
	if has {
		base.RawQuery = query
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Token", request.Key)
	response, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 2 || req.URL.Host != base.Host {
			return errors.New("导入服务跳转到其他地址")
		}
		return nil
	}}).Do(req)
	if err != nil {
		return errors.New("无法连接导入服务")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("导入服务返回 HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(out)
}
func (a *App) featureServerImport(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodPost) {
		return
	}
	var request featureImportRequest
	if !body(w, r, &request) {
		return
	}
	if request.Key == "" || len(request.Mappings) > 100 {
		fail(w, 400, "请填写连接密钥并检查媒体库映射")
		return
	}
	if !request.Apply {
		var result struct {
			Items []struct {
				Id, ItemId, Name, CollectionType string
				Locations                        []string
			}
		}
		if err := featureImportGet(r.Context(), request, "Library/VirtualFolders", &result.Items); err != nil {
			featureError(w, err)
			return
		}
		var users []M
		_ = featureImportGet(r.Context(), request, "Users", &users)
		respond(w, M{"Libraries": result.Items, "Users": users, "LocalLibraries": a.libraries()})
		return
	}
	if len(request.Mappings) == 0 {
		fail(w, 400, "请选择需要导入的媒体库")
		return
	}
	if request.LocalUser == "" {
		request.LocalUser = user.ID
	}
	var uid string
	if a.db.QueryRow("SELECT id FROM users WHERE id=?", request.LocalUser).Scan(&uid) != nil {
		fail(w, 400, "本地用户不存在")
		return
	}
	if request.Progress && request.RemoteUser == "" {
		fail(w, 400, "导入播放进度需要选择来源用户")
		return
	}
	for _, mapping := range request.Mappings {
		real, err := filepath.EvalSymlinks(mapping.LocalPath)
		if err != nil || !allowedMediaPath(real) {
			fail(w, 400, "映射路径必须位于已挂载媒体目录")
			return
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() || mapping.RemoteID == "" || mapping.RemotePath == "" || (mapping.Kind != "movies" && mapping.Kind != "tvshows") {
			fail(w, 400, "请检查媒体库类型和路径映射")
			return
		}
	}
	job := a.newActivity("import", "", "导入媒体资料")
	ctx, cancel := context.WithCancel(a.features.ctx)
	key := "import:" + job
	a.features.mu.Lock()
	a.features.jobs[key] = cancel
	a.features.mu.Unlock()
	a.features.wg.Add(1)
	go func() {
		defer a.features.wg.Done()
		defer cancel()
		defer func() { a.features.mu.Lock(); delete(a.features.jobs, key); a.features.mu.Unlock() }()
		err := a.featureRunImport(ctx, request, job)
		a.finishActivity(job, err)
	}()
	respond(w, M{"ID": job, "Queued": true})
}
func (a *App) featureRunImport(ctx context.Context, request featureImportRequest, job string) error {
	done, matched := 0, 0
	a.changeActivity(job, func(e *activityEntry) { e.State = "running"; e.Current = "读取媒体库" })
	for _, mapping := range request.Mappings {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		path, _ := filepath.EvalSymlinks(mapping.LocalPath)
		var lib string
		err := a.db.QueryRow("SELECT id FROM libraries WHERE path=?", path).Scan(&lib)
		if err != nil {
			for _, existing := range a.libraries() {
				for _, root := range existing["Locations"].([]string) {
					if pathsOverlap(root, path) {
						return errors.New("映射路径与现有媒体库重叠，请映射到该库的根目录")
					}
				}
			}
			lib = id()
			if _, err = a.db.Exec("INSERT INTO libraries(id,name,path,kind) VALUES(?,?,?,?)", lib, mapping.Name, path, mapping.Kind); err != nil {
				return err
			}
			if _, ok := a.reserveConcurrentScan(lib); !ok {
				return errors.New("媒体库扫描已在运行")
			}
			a.runConcurrentScan(lib, false, false, nil)
		}
		for start := 0; ; start += 200 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			q := url.Values{"ParentId": {mapping.RemoteID}, "Recursive": {"true"}, "StartIndex": {strconv.Itoa(start)}, "Limit": {"200"}, "Fields": {"Path,Overview,Genres,Tags,ProviderIds,MediaStreams,ProductionLocations,Chapters"}}
			endpoint := "Items"
			if request.RemoteUser != "" {
				endpoint = "Users/" + url.PathEscape(request.RemoteUser) + "/Items"
			}
			var result struct {
				Items            []featureRemoteItem
				TotalRecordCount int
			}
			if err = featureImportGet(ctx, request, endpoint+"?"+q.Encode(), &result); err != nil {
				return err
			}
			if len(result.Items) == 0 {
				break
			}
			for _, remote := range result.Items {
				done++
				relative := strings.TrimPrefix(strings.ReplaceAll(remote.Path, "\\", "/"), strings.TrimRight(strings.ReplaceAll(mapping.RemotePath, "\\", "/"), "/")+"/")
				if relative == remote.Path || relative == "" || strings.HasPrefix(relative, "/") {
					continue
				}
				local := filepath.Join(path, filepath.FromSlash(relative))
				if rel, e := filepath.Rel(path, local); e != nil || !filepath.IsLocal(rel) {
					continue
				}
				x, e := a.item(digest(local)[:32])
				if e != nil || x.Lib != lib {
					continue
				}
				matched++
				if request.Metadata {
					m := a.featureMetadataView(x)
					if !m.Locked {
						m.Title = remote.Name
						m.Plot = remote.Overview
						m.Year = remote.ProductionYear
						m.OriginalTitle = remote.OriginalTitle
						m.Genres = remote.Genres
						m.Tags = remote.Tags
						m.Countries = remote.ProductionLocations
						m.MPAA = remote.OfficialRating
						m.Rating = remote.CommunityRating
						m.Premiered = strings.Split(remote.PremiereDate, "T")[0]
						m.TMDB = remote.ProviderIds["Tmdb"]
						m.IMDB = remote.ProviderIds["Imdb"]
						m.CachedAt = featureNow()
						if e = a.featureStoreMetadata(ctx, x, m, false); e != nil {
							return e
						}
						if len(remote.MediaStreams) > 0 {
							_ = a.saveMedia(x, M{"MediaStreams": remote.MediaStreams, "RunTimeTicks": remote.RunTimeTicks})
						}
						if len(remote.Chapters) > 0 {
							chapters := []M{}
							introStart := float64(-1)
							for index, chapter := range remote.Chapters {
								start := float64(chapter.StartPositionTicks) / 1e7
								end := float64(remote.RunTimeTicks) / 1e7
								if index+1 < len(remote.Chapters) {
									end = float64(remote.Chapters[index+1].StartPositionTicks) / 1e7
								}
								switch chapter.MarkerType {
								case "IntroStart":
									introStart = start
								case "IntroEnd":
									if introStart >= 0 {
										chapters = append(chapters, M{"Name": "片头", "Start": introStart, "End": start, "Kind": "intro"})
									}
								case "CreditsStart":
									chapters = append(chapters, M{"Name": "片尾", "Start": start, "End": end, "Kind": "credits"})
								default:
									chapters = append(chapters, M{"Name": chapter.Name, "Start": start, "End": end, "Kind": "chapter"})
								}
							}
							_, _ = a.db.Exec("INSERT INTO feature_chapters(item,data,updated) VALUES(?,?,?) ON CONFLICT(item) DO UPDATE SET data=excluded.data,updated=excluded.updated", x.ID, featureJSON(chapters), featureNow())
						}
					}
				}
				if request.Progress {
					var latest string
					var localCount int
					_ = a.db.QueryRow("SELECT count(*) FROM userdata WHERE user_id=? AND item=?", request.LocalUser, x.ID).Scan(&localCount)
					_ = a.db.QueryRow("SELECT last_played FROM userdata_extra WHERE user_id=? AND item=?", request.LocalUser, x.ID).Scan(&latest)
					if remote.UserData.IsFavorite {
						if _, err = a.db.Exec("INSERT INTO userdata_extra(user_id,item,favorite) VALUES(?,?,1) ON CONFLICT(user_id,item) DO UPDATE SET favorite=1", request.LocalUser, x.ID); err != nil {
							return err
						}
					}
					if featureParseDate(remote.UserData.LastPlayedDate).After(featureParseDate(latest)) || localCount == 0 && (remote.UserData.Played || remote.UserData.PlaybackPositionTicks > 0) {
						tx, e := a.db.Begin()
						if e != nil {
							return e
						}
						_, e = tx.Exec("INSERT INTO userdata(user_id,item,position,played) VALUES(?,?,?,?) ON CONFLICT(user_id,item) DO UPDATE SET position=excluded.position,played=excluded.played", request.LocalUser, x.ID, remote.UserData.PlaybackPositionTicks, boolInt(remote.UserData.Played))
						if e == nil {
							_, e = tx.Exec("INSERT INTO userdata_extra(user_id,item,favorite,play_count,last_played) VALUES(?,?,?,?,?) ON CONFLICT(user_id,item) DO UPDATE SET favorite=GREATEST(userdata_extra.favorite,excluded.favorite),play_count=GREATEST(userdata_extra.play_count,excluded.play_count),last_played=excluded.last_played", request.LocalUser, x.ID, boolInt(remote.UserData.IsFavorite), remote.UserData.PlayCount, remote.UserData.LastPlayedDate)
						}
						if e == nil {
							e = tx.Commit()
						} else {
							tx.Rollback()
						}
						if e != nil {
							return e
						}
					}
				}
			}
			a.changeActivity(job, func(e *activityEntry) {
				e.Done = done
				e.Total = result.TotalRecordCount
				e.Current = fmt.Sprintf("%s · 读取 %d · 匹配 %d", mapping.Name, done, matched)
			})
			if start+len(result.Items) >= result.TotalRecordCount {
				break
			}
		}
	}
	return nil
}

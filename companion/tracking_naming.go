package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type trackingNames struct {
	app                     *App
	root                    *os.Root
	output, mount, activity string
	request                 namingRequest
	rename                  bool
	lookupError             error
	existing, claimed       map[string]string
	issues                  []mediaIssue
	resolved                []string
	observed                int64
}

var errTrackingRenameReview = errors.New("自动命名需核对")

func (a *App) trackingSTRMNames(ctx context.Context, s trackingSubscription, m cloudMount, output, activity string, identity trackingMediaIdentity) (*trackingNames, error) {
	base, err := cloudOutput(output)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(fileRoot())
	if err != nil {
		return nil, err
	}
	n := &trackingNames{app: a, root: root, output: base, mount: m.ID, activity: activity, rename: *s.AutoImport.AutoRename, existing: map[string]string{}, claimed: map[string]string{}, observed: time.Now().UnixNano()}
	success := false
	defer func() {
		if !success {
			root.Close()
		}
	}()
	if n.rename {
		kind := identity.Kind
		for _, lib := range a.libraries() {
			if kind == "" && lib["Id"] == s.AutoImport.Library {
				switch lib["CollectionType"] {
				case "tvshows":
					kind = "tv"
				case "movies":
					kind = "movie"
				}
			}
		}
		n.request = namingRequest{Mode: "auto", Kind: kind, Title: identity.Title, Year: identity.Year, TMDB: identity.TMDBID}
		resolver := namingTMDBResolver{app: a, settings: a.tmdbSettings(), cache: map[string]namingTMDBResult{}}
		n.request, _, n.lookupError = resolver.resolve(ctx, namingSourceInfo{Kind: kind, Identity: MediaRecognition{Title: identity.Title, Year: identity.Year, TMDBID: identity.TMDBID}}, n.request)
		if n.lookupError == nil {
			n.request.Template = "{title} ({year})"
			if n.request.Kind == "tv" {
				n.request.Template += " - S{season:2}E{episode:2}"
			}
		}
	}
	filesMu.RLock()
	defer filesMu.RUnlock()
	if _, err = fileCheck(root, base); errors.Is(err, fs.ErrNotExist) {
		success = true
		return n, nil
	} else if err != nil {
		return nil, err
	}
	scope, err := namingCollect(ctx, root, base, nil, namingRequest{Recursive: true}, func(int, string) {})
	if err != nil {
		return nil, err
	}
	if len(scope.Errors) > 0 {
		return nil, errors.New("无法完整读取已有 STRM 目录，请检查权限后重试")
	}
	for _, local := range scope.Files {
		if !strings.EqualFold(filepath.Ext(local), ".strm") {
			continue
		}
		n.claimed[local] = "existing"
		f, err := root.Open(local)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(f, 65537))
		f.Close()
		if readErr != nil || len(data) > 65536 {
			return nil, errors.New("已有 STRM 内容无法读取，请先检查文件")
		}
		link, err := url.Parse(strings.TrimSpace(string(data)))
		if err != nil || !strings.HasSuffix(link.Path, "/cloud/resolve/"+m.ID) {
			continue
		}
		remote, err := cloudPath(link.Query().Get("path"))
		if err != nil {
			continue
		}
		if previous := n.existing[remote]; previous != "" && previous != local {
			return nil, errors.New("入库目录有指向同一网盘文件的重复 STRM，请先处理重复文件")
		}
		n.existing[remote], n.claimed[local] = local, remote
	}
	success = true
	return n, nil
}

// Called serially by the cloud directory walker before work is sent to writers.
func (n *trackingNames) localPath(ctx context.Context, remote, local string) (string, error) {
	old := n.existing[remote]
	fallback := old
	if fallback == "" {
		fallback = filepath.Join(n.output, filepath.FromSlash(local)+".strm")
	}
	target := fallback
	var namingErr error
	if n.rename {
		namingErr = n.lookupError
		if namingErr == nil {
			source := filepath.Join(n.output, filepath.FromSlash(local)+".strm")
			var name string
			name, _, namingErr = namingDestination(source, false, n.request, nil, 0)
			if namingErr == nil {
				stem, ext := namingSplit(name)
				name, namingErr = cloudLocalName(stem + " {tmdb-" + n.request.TMDB + "}" + ext)
				if namingErr == nil && len(name) > 240 {
					namingErr = errors.New("规范名称超过 240 字节，请缩短作品名称")
				}
				if namingErr == nil {
					target = filepath.Join(filepath.Dir(source), name)
				}
			}
		}
		if namingErr == nil && target != old {
			if owner := n.claimed[target]; owner != "" && owner != remote {
				namingErr = errors.New("规范名称与已有文件或同季同集的其他版本重叠，保留原名")
			} else if _, err := n.root.Lstat(target); err == nil || !errors.Is(err, fs.ErrNotExist) {
				namingErr = errors.New("规范名称已存在或不可访问，保留原名")
			}
		}
		if namingErr == nil && old != "" && old != target {
			if err := n.app.trackingRenameSTRM(ctx, n.root, old, target, remote, n.mount, n.activity); err != nil {
				if !errors.Is(err, errTrackingRenameReview) {
					return "", err
				}
				namingErr = err
			} else {
				delete(n.claimed, old)
				n.existing[remote] = target
			}
		}
		if namingErr != nil {
			target = fallback
			n.issues = append(n.issues, mediaIssue{Path: fallback, Status: "review", Reason: "追新自动命名：" + namingErr.Error()})
		} else {
			n.resolved = append(n.resolved, fallback, target)
		}
	}
	if owner := n.claimed[target]; owner != "" && owner != remote {
		return "", errors.New("STRM 输出名称被其他文件占用，已停止以避免漏入库")
	}
	n.claimed[target] = remote
	relative, err := filepath.Rel(n.output, target)
	if err != nil || !filepath.IsLocal(relative) {
		return "", errors.New("STRM 命名结果超出入库目录")
	}
	return strings.TrimSuffix(relative, filepath.Ext(relative)), nil
}

func (a *App) trackingRenameSTRM(ctx context.Context, root *os.Root, old, next, remote, mount, activity string) error {
	a.naming.mu.Lock()
	defer a.naming.mu.Unlock()
	filesMu.Lock()
	defer filesMu.Unlock()
	a.features.mu.Lock()
	defer a.features.mu.Unlock()
	for job := range a.features.jobs {
		if job != "cloud:"+mount && job != "tracking:"+activity {
			return errors.New("其他文件处理任务正在运行，STRM 已保留，可稍后重试自动命名")
		}
	}
	a.scraper.mu.Lock()
	defer a.scraper.mu.Unlock()
	if a.scraper.running || a.scraper.planning {
		return errors.New("刮削正在运行，请结束后重试自动命名")
	}
	info, err := fileCheck(root, old)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("原 STRM 不可访问，请重新入库")
	}
	f, err := root.Open(old)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, 65537))
	f.Close()
	link, parseErr := url.Parse(strings.TrimSpace(string(data)))
	if readErr != nil || parseErr != nil || len(data) > 65536 || !strings.HasSuffix(link.Path, "/cloud/resolve/"+mount) || link.Query().Get("path") != remote {
		return errors.New("原 STRM 指向的网盘文件已变化，请重新入库")
	}
	entries, err := namingEntries(root, filepath.Dir(old))
	if err != nil {
		return err
	}
	moves := append([]namingMove{{Old: old, New: next, Info: info}}, namingSidecars(root, entries, old, next)...)
	if reason := namingNFOConflict(root, moves); reason != "" {
		return fmt.Errorf("%w：%s", errTrackingRenameReview, reason)
	}
	paths := []string{}
	for _, move := range moves {
		if _, err := root.Lstat(move.New); err == nil || !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w：STRM 或关联资料的新名称已存在，未改动文件", errTrackingRenameReview)
		}
		paths = append(paths, move.Old, move.New)
	}
	if err := a.filesAvailable(ctx, paths); err != nil {
		return err
	}
	tx, err := a.namingCatalog(ctx, moves)
	if err != nil {
		return err
	}
	if tx != nil {
		defer tx.Rollback()
	}
	journal := id()
	if _, err := a.namingJournal(journal, moves, "prepared", ""); err != nil {
		return errors.New("无法保存自动命名记录，未改动文件")
	}
	completed := []namingMove{}
	for _, move := range moves {
		if err = ctx.Err(); err != nil {
			break
		}
		current, checkErr := fileCheck(root, move.Old)
		if checkErr != nil || !namingSameFile(move.Info, current) {
			err = errors.New("执行期间原文件已变化")
			break
		}
		if err = namingRename(root, move, false); err != nil {
			break
		}
		completed = append(completed, move)
	}
	if err == nil && tx != nil {
		err = tx.Commit()
	}
	if err != nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		rollbackError := false
		for i := len(completed) - 1; i >= 0; i-- {
			current, checkErr := fileCheck(root, completed[i].New)
			if checkErr != nil || !namingSameFile(completed[i].Info, current) || namingRename(root, completed[i], true) != nil {
				rollbackError = true
			}
		}
		message := "自动命名失败，已撤回文件改动"
		if rollbackError {
			message = "自动命名失败，部分文件未能撤回，请查看改名记录恢复"
		}
		_, _ = a.namingJournal(journal, moves, "failed", message)
		return fmt.Errorf("%s：%w", message, scraperFilesystemError("改名", err))
	}
	if _, err := a.namingJournal(journal, moves, "complete", ""); err != nil {
		return errors.New("自动命名已完成，但完成记录写入失败，请检查磁盘后重新入库")
	}
	return nil
}

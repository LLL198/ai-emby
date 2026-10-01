package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"golang.org/x/sys/unix"
)

type namingState struct {
	mu    sync.Mutex
	plans map[string]*namingPlan
}
type namingRequest struct {
	Path          string   `json:"path"`
	Paths         []string `json:"paths"`
	Mode          string   `json:"mode"`
	Kind          string   `json:"kind"`
	Title         string   `json:"title"`
	Year          int      `json:"year"`
	TMDB          string   `json:"tmdb"`
	OriginalTitle string   `json:"-"`
	Season        *int     `json:"season"`
	Bare          bool     `json:"bare"`
	Template      string   `json:"template"`
	Pattern       string   `json:"pattern"`
	Replacement   string   `json:"replacement"`
	Start         int      `json:"start"`
	Width         int      `json:"width"`
}
type namingMove struct {
	Old  string      `json:"old"`
	New  string      `json:"new"`
	Info fs.FileInfo `json:"-"`
}
type namingRow struct {
	ID        string       `json:"id"`
	Old       string       `json:"old"`
	New       string       `json:"new"`
	Status    string       `json:"status"`
	Reason    string       `json:"reason"`
	Directory bool         `json:"directory"`
	Moves     []namingMove `json:"moves"`
}
type namingPlan struct {
	ID      string      `json:"id"`
	Rows    []namingRow `json:"rows"`
	Created time.Time   `json:"created"`
	Owner   string      `json:"-"`
}

func (a *App) namingAPI(w http.ResponseWriter, r *http.Request, user User) {
	if !featureMethod(w, r, http.MethodPost) {
		return
	}
	switch r.URL.Path {
	case "/admin/features/naming/preview":
		a.namingPreview(w, r, user)
	case "/admin/features/naming/apply":
		a.namingApply(w, r, user)
	default:
		fail(w, 404, "命名接口不存在")
	}
}

func namingEntries(root *os.Root, dir string) ([]fs.DirEntry, error) {
	f, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(10001)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 10000 {
		return nil, errors.New("当前目录超过 10000 项，请选择更小的目录")
	}
	return entries, nil
}

func namingNatural(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for len(a) > 0 && len(b) > 0 {
		if a[0] >= '0' && a[0] <= '9' && b[0] >= '0' && b[0] <= '9' {
			i, j := 0, 0
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			x, y := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if len(x) != len(y) {
				return len(x) < len(y)
			}
			if x != y {
				return x < y
			}
			a, b = a[i:], b[j:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func (a *App) namingPreview(w http.ResponseWriter, r *http.Request, user User) {
	var b namingRequest
	if !body(w, r, &b) {
		return
	}
	if b.Mode != "auto" && b.Mode != "regex" && b.Mode != "sequence" {
		fail(w, 400, "请选择识别命名、正则替换或顺序编号")
		return
	}
	if b.Kind != "auto" && b.Kind != "tv" && b.Kind != "movie" && b.Kind != "directory" {
		fail(w, 400, "无效作品类型")
		return
	}
	if len(b.Paths) > 500 || len(b.Title) > 512 || len(b.Template) > 512 || len(b.Pattern) > 512 || len(b.Replacement) > 512 {
		fail(w, 400, "一次最多预览 500 项，输入不得过长")
		return
	}
	if b.Year != 0 && (b.Year < 1800 || b.Year > 2199) || b.Season != nil && (*b.Season < 0 || *b.Season > 999) {
		fail(w, 400, "年份或季号无效")
		return
	}
	if b.Mode == "sequence" && (b.Start < 1 || b.Start > 9999 || b.Width < 1 || b.Width > 4 || b.Kind != "tv" || b.Season == nil || strings.TrimSpace(b.Title) == "") {
		fail(w, 400, "顺序编号需要作品名、明确的季号、1–9999 起始编号和 1–4 补位长度")
		return
	}
	var re *regexp.Regexp
	if b.Mode == "regex" {
		var err error
		re, err = regexp.Compile(b.Pattern)
		if b.Pattern == "" || err != nil {
			fail(w, 400, "正则表达式无效")
			return
		}
	}
	dir, err := fileName(b.Path)
	if err != nil {
		fileFail(w, err)
		return
	}
	if b.TMDB != "" {
		n, err := strconv.Atoi(b.TMDB)
		if err != nil || n < 1 || b.Kind != "movie" && b.Kind != "tv" {
			fail(w, 400, "TMDB 补全需要电影或剧集类型以及有效 ID")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var data tmdbData
		if err = a.tmdbGet(ctx, b.Kind+"/"+b.TMDB, url.Values{"language": {"zh-CN"}}, &data, a.tmdbSettings()); err != nil {
			featureError(w, a.scraperSafeError(err))
			return
		}
		date := data.ReleaseDate
		b.Title = data.Title
		b.OriginalTitle = data.OriginalTitle
		if b.Kind == "tv" {
			b.Title = data.Name
			b.OriginalTitle = data.OriginalName
			date = data.FirstAirDate
		}
		if b.Title == "" {
			fail(w, 502, "TMDB 返回的作品名称为空")
			return
		}
		if len(date) >= 4 {
			b.Year, _ = strconv.Atoi(date[:4])
		}
	}
	filesMu.RLock()
	filesLocked := true
	defer func() {
		if filesLocked {
			filesMu.RUnlock()
		}
	}()
	root, err := os.OpenRoot(fileRoot())
	if err != nil {
		fileFail(w, err)
		return
	}
	defer root.Close()
	info, err := fileCheck(root, dir)
	if err != nil {
		fileFail(w, err)
		return
	}
	if !info.IsDir() {
		fail(w, 400, "请选择目录")
		return
	}
	entries, err := namingEntries(root, dir)
	if err != nil {
		featureError(w, err)
		return
	}
	selected := map[string]bool{}
	for _, path := range b.Paths {
		p, e := fileName(path)
		if e != nil {
			fileFail(w, e)
			return
		}
		if p == "." || filepath.Dir(p) != dir {
			fail(w, 400, "只能选择当前目录内的项目")
			return
		}
		selected[p] = true
	}
	files := []string{}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.Type()&os.ModeSymlink != 0 || !e.IsDir() && !featureMediaExtension(e.Name()) {
			continue
		}
		if b.Mode == "sequence" && e.IsDir() {
			continue
		}
		if len(selected) > 0 && !selected[p] {
			continue
		}
		files = append(files, p)
	}
	if len(files) > 500 {
		fail(w, 400, "当前目录超过 500 个媒体项目，请先勾选需要处理的文件")
		return
	}
	sort.SliceStable(files, func(i, j int) bool { return namingNatural(files[i], files[j]) })
	plan := &namingPlan{ID: id(), Owner: user.ID, Created: time.Now(), Rows: []namingRow{}}
	for i, path := range files {
		if err := r.Context().Err(); err != nil {
			return
		}
		row := namingRow{ID: strconv.Itoa(i), Old: path, Status: "review", Moves: []namingMove{}}
		fi, e := fileCheck(root, path)
		if e != nil {
			row.Reason = "源文件不可用"
			plan.Rows = append(plan.Rows, row)
			continue
		}
		row.Directory = fi.IsDir()
		if b.Mode == "sequence" && fi.IsDir() {
			row.Reason = "顺序编号只处理媒体文件"
			plan.Rows = append(plan.Rows, row)
			continue
		}
		next, reason, e := namingDestination(path, fi.IsDir(), b, re, i)
		row.Reason = reason
		if e != nil {
			row.Reason = e.Error()
			plan.Rows = append(plan.Rows, row)
			continue
		}
		if len(next) > 240 || strings.Trim(next, " .") == "" {
			row.Reason = "新名称为空或超过 240 字节，请缩短模板或标题"
			plan.Rows = append(plan.Rows, row)
			continue
		}
		row.New = filepath.Join(dir, next)
		if row.New == path {
			row.Status = "unchanged"
			row.Reason = "名称已符合当前规则"
			if !fi.IsDir() {
				associated := append([]namingMove{{Old: path, New: path, Info: fi}}, namingSidecars(root, entries, path, path)...)
				if reason := namingNFOConflict(root, associated); reason != "" {
					row.Status = "review"
					row.Reason = reason
				}
			}
			plan.Rows = append(plan.Rows, row)
			continue
		}
		if p, e := fileName(row.New); e != nil || filepath.Dir(p) != dir {
			row.Reason = "新名称包含非法路径"
			plan.Rows = append(plan.Rows, row)
			continue
		}
		row.Moves = []namingMove{{Old: path, New: row.New, Info: fi}}
		if !fi.IsDir() {
			row.Moves = append(row.Moves, namingSidecars(root, entries, path, row.New)...)
		}
		row.Status = "ready"
		if reason := namingNFOConflict(root, row.Moves); reason != "" {
			row.Status = "review"
			row.Reason = reason
		}
		plan.Rows = append(plan.Rows, row)
	}
	namingConflicts(root, plan.Rows)
	filesMu.RUnlock()
	filesLocked = false
	a.naming.mu.Lock()
	if a.naming.plans == nil {
		a.naming.plans = map[string]*namingPlan{}
	}
	for key, p := range a.naming.plans {
		if time.Since(p.Created) > time.Hour || p.Owner == user.ID {
			delete(a.naming.plans, key)
		}
	}
	if len(a.naming.plans) >= 32 {
		a.naming.mu.Unlock()
		fail(w, 429, "命名预览过多，请稍后重试")
		return
	}
	a.naming.plans[plan.ID] = plan
	a.naming.mu.Unlock()
	respond(w, plan)
}

func namingDestination(path string, directory bool, b namingRequest, re *regexp.Regexp, index int) (string, string, error) {
	name := filepath.Base(path)
	stem, ext := namingSplit(name)
	if directory {
		stem, ext = name, ""
	}
	if b.Mode == "regex" {
		if !re.MatchString(stem) {
			return name, "未匹配正则", nil
		}
		next := strings.TrimSpace(re.ReplaceAllString(stem, b.Replacement))
		if next == "" {
			return "", "", errors.New("替换后名称为空")
		}
		return namingClean(next) + ext, "正则替换（保留扩展名）", nil
	}
	if directory {
		if b.Template != "" {
			return "", "", errors.New("自定义文件模板不应用于文件夹，请取消勾选文件夹")
		}
		if season, ok := namingSeason(name); ok {
			return fmt.Sprintf("Season %02d", season), "规范季目录", nil
		}
		identity := scraperSearchIdentity(name)
		if b.Title != "" {
			identity.Title = b.Title
		}
		if b.Year > 0 {
			identity.Year = b.Year
		}
		if b.TMDB != "" {
			identity.TMDBID = b.TMDB
		}
		if identity.Title == "" || identity.Year == 0 {
			return "", "", errors.New("作品目录缺少标题或年份，请指定作品信息")
		}
		next := fmt.Sprintf("%s (%d)", namingClean(identity.Title), identity.Year)
		if identity.TMDBID != "" {
			next += " {tmdb-" + identity.TMDBID + "}"
		}
		return next, "规范作品目录（标题、年份和 TMDB 编号）", nil
	}
	if b.Kind == "directory" {
		return "", "", errors.New("当前类型只处理文件夹")
	}
	identity := namingTitle(name)
	parent := filepath.Base(filepath.Dir(path))
	parentIdentity := scraperSearchIdentity(parent)
	parentSeason, hasParentSeason := namingSeason(parent)
	if hasParentSeason {
		parentIdentity = scraperSearchIdentity(filepath.Base(filepath.Dir(filepath.Dir(path))))
	}
	ep, hasEpisode := namingEpisode(stem, b.Bare)
	if b.Mode == "sequence" {
		ep = namingEpisodeInfo{Season: *b.Season, Episode: b.Start + index, HasSeason: true, Reason: "按文件名自然排序，顺序编号"}
		hasEpisode = true
		if ep.Episode > 9999 {
			return "", "", errors.New("顺序编号超过 9999")
		}
	}
	if hasEpisode && b.Mode != "sequence" && namingMulti.MatchString(stem) {
		return "", "", errors.New("多集、特别篇或剧场版需核对编号，请用正则或顺序编号明确指定")
	}
	if b.Kind == "movie" && hasEpisode {
		return "", "", errors.New("文件包含集号，与电影类型冲突")
	}
	if b.Kind == "tv" && !hasEpisode {
		return "", "", errors.New("未找到明确集号；可确认裸集号，或使用顺序编号")
	}
	if !hasEpisode && (hasParentSeason || namingMulti.MatchString(stem)) {
		return "", "", errors.New("季目录或特别篇中的文件不能自动当作电影")
	}
	if hasEpisode {
		if ep.HasSeason && hasParentSeason && ep.Season != parentSeason {
			return "", "", errors.New("文件季号与所在季目录冲突，请先核对季目录")
		}
		if !ep.HasSeason {
			if b.Season != nil {
				ep.Season = *b.Season
				ep.HasSeason = true
			} else if hasParentSeason {
				ep.Season = parentSeason
				ep.HasSeason = true
			}
		}
		if !ep.HasSeason {
			return "", "", errors.New("已找到集号，但季号不明确，请指定季号")
		}
		if b.Season != nil && *b.Season != ep.Season {
			return "", "", errors.New("文件季号与指定季号冲突；需要重排时使用顺序编号")
		}
		identity = scraperSearchIdentity(strings.TrimSpace(stem[:ep.Start]))
		if identity.Title == "" || hasParentSeason {
			identity.Title = parentIdentity.Title
		}
		if identity.Year == 0 {
			identity.Year = parentIdentity.Year
		}
	}
	if b.Title != "" {
		identity.Title = b.Title
	}
	if b.Year > 0 {
		identity.Year = b.Year
	}
	if b.TMDB != "" {
		identity.TMDBID = b.TMDB
	} else if hasEpisode && identity.TMDBID == "" {
		identity.TMDBID = parentIdentity.TMDBID
	}
	if identity.Title == "" {
		return "", "", errors.New("未能确定作品标题，请指定作品或使用 TMDB 搜索")
	}
	if !hasEpisode && identity.Year == 0 {
		return "", "", errors.New("电影缺少年份，请指定年份或选择 TMDB 结果")
	}
	if b.Mode == "auto" && hasEpisode && b.Title == "" && b.Year == 0 && b.TMDB == "" && b.Template == "" && namingCanonicalSE.MatchString(stem) {
		return name, "名称已经包含规范季集编号", nil
	}
	quality := ""
	if m := namingQuality.FindStringSubmatch(stem); len(m) == 2 {
		quality = m[1]
	}
	episodeName := ""
	if hasEpisode && b.Mode != "sequence" {
		episodeName = strings.Trim(scraperTMDBTagRE.ReplaceAllString(stem[ep.End:], ""), " ._-")
		if cutoff := scraperReleaseTokenRE.FindStringIndex(episodeName); len(cutoff) == 2 {
			episodeName = episodeName[:cutoff[0]]
		}
		episodeName = namingClean(strings.Trim(episodeName, " ._-[]()"))
	}
	values := map[string]string{"title": namingClean(identity.Title), "title_original": namingClean(b.OriginalTitle), "year": "", "season": "", "episode": "", "episode_name": episodeName, "quality": quality, "tmdbid": identity.TMDBID, "ext": strings.TrimPrefix(ext, ".")}
	if identity.Year > 0 {
		values["year"] = strconv.Itoa(identity.Year)
	}
	if hasEpisode {
		values["season"] = strconv.Itoa(ep.Season)
		values["episode"] = strconv.Itoa(ep.Episode)
	}
	template := b.Template
	if template == "" {
		template = "{title}"
		if identity.Year > 0 {
			template += " ({year})"
		}
		if hasEpisode {
			width := 2
			if b.Mode == "sequence" {
				width = b.Width
			}
			template += fmt.Sprintf(" - S{season:2}E{episode:%d}", width)
			if episodeName != "" {
				template += " - {episode_name}"
			}
		}
		if identity.TMDBID != "" {
			template += " {tmdb-" + identity.TMDBID + "}"
		}
		if quality != "" {
			template += " [" + quality + "]"
		}
	} else if strings.Contains(template, "{ext}") {
		return "", "", errors.New("扩展名会自动保留，模板中无需填写 {ext}")
	}
	next, err := namingRender(template, values)
	if err != nil {
		return "", "", err
	}
	next += ext
	if len(next) > 240 {
		return "", "", errors.New("新文件名超过 240 字节，请缩短标题或模板")
	}
	if hasEpisode {
		parsed, ok := namingEpisode(next, false)
		if !ok || !parsed.HasSeason || parsed.Season != ep.Season || parsed.Episode != ep.Episode {
			return "", "", errors.New("剧集模板必须保留正确的 SxxExx 季集编号")
		}
		return next, ep.Reason, nil
	}
	if hasParentSeason {
		return "", "", errors.New("无法确定季集编号")
	}
	return next, "识别电影标题和年份", nil
}

func namingSidecars(root *os.Root, entries []fs.DirEntry, old, next string) []namingMove {
	oldStem := strings.TrimSuffix(filepath.Base(old), filepath.Ext(old))
	newStem := strings.TrimSuffix(filepath.Base(next), filepath.Ext(next))
	oldShort, _ := namingSplit(filepath.Base(old))
	newShort, _ := namingSplit(filepath.Base(next))
	moves := []namingMove{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		n := entry.Name()
		ext := strings.ToLower(filepath.Ext(n))
		switch ext {
		case ".nfo", ".jpg", ".jpeg", ".png", ".webp", ".srt", ".ass", ".ssa", ".sub", ".idx", ".vtt", ".sup":
		default:
			continue
		}
		for _, pair := range [][2]string{{oldStem, newStem}, {oldShort, newShort}} {
			if !strings.HasPrefix(n, pair[0]) {
				continue
			}
			suffix := strings.TrimPrefix(n, pair[0])
			if suffix == "" || suffix[0] != '.' && suffix[0] != '-' {
				continue
			}
			p := filepath.Join(filepath.Dir(old), n)
			fi, err := fileCheck(root, p)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			moves = append(moves, namingMove{Old: p, New: filepath.Join(filepath.Dir(next), pair[1]+suffix), Info: fi})
			break
		}
	}
	return moves
}

func namingNFOConflict(root *os.Root, moves []namingMove) string {
	if len(moves) == 0 || moves[0].Info.IsDir() {
		return ""
	}
	stem, _ := namingSplit(filepath.Base(moves[0].New))
	episode, ok := namingEpisode(stem, false)
	if !ok || !episode.HasSeason {
		return ""
	}
	for _, move := range moves[1:] {
		if !strings.EqualFold(filepath.Ext(move.Old), ".nfo") {
			continue
		}
		f, err := root.Open(move.Old)
		if err != nil {
			return "无法读取关联 NFO，请先检查资料"
		}
		var record struct {
			XMLName xml.Name
			Season  *int `xml:"season"`
			Episode *int `xml:"episode"`
		}
		err = xml.NewDecoder(io.LimitReader(f, 2<<20)).Decode(&record)
		f.Close()
		if err != nil {
			return "关联 NFO 无法解析，请先修正资料，避免覆盖季集识别"
		}
		if record.XMLName.Local != "episodedetails" {
			continue
		}
		if record.Season != nil && *record.Season != episode.Season || record.Episode != nil && *record.Episode != episode.Episode {
			return "已有 NFO 的季集编号与新名称不同，请先修正 NFO 后再改名"
		}
	}
	return ""
}

func namingConflicts(root *os.Root, rows []namingRow) {
	claimed := map[string][]int{}
	for i, row := range rows {
		if row.Status != "ready" {
			continue
		}
		for _, m := range row.Moves {
			for _, p := range []string{m.Old, m.New} {
				claimed[strings.ToLower(p)] = append(claimed[strings.ToLower(p)], i)
			}
			if m.Old == m.New {
				rows[i].Status = "conflict"
				rows[i].Reason = "关联文件目标重叠"
				continue
			}
			if _, err := root.Lstat(m.New); err == nil || !errors.Is(err, fs.ErrNotExist) {
				rows[i].Status = "conflict"
				rows[i].Reason = "目标已存在或不可访问，禁止覆盖"
			}
		}
	}
	for _, indices := range claimed {
		if len(indices) > 1 {
			for _, i := range indices {
				rows[i].Status = "conflict"
				rows[i].Reason = "多个项目的源文件或目标名称重叠，请调整模板或选择范围"
			}
		}
	}
}

func namingRename(root *os.Root, m namingMove, reverse bool) error {
	old, next := m.Old, m.New
	if reverse {
		old, next = next, old
	}
	dir, err := root.Open(filepath.Dir(old))
	if err != nil {
		return err
	}
	defer dir.Close()
	err = unix.Renameat2(int(dir.Fd()), filepath.Base(old), int(dir.Fd()), filepath.Base(next), unix.RENAME_NOREPLACE)
	if err == nil || !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EOPNOTSUPP) {
		return err
	}
	if !m.Info.Mode().IsRegular() {
		return errors.New("此挂载不支持禁止覆盖的目录改名")
	}
	// Linking creates the target without replacing an existing name.
	if err = root.Link(old, next); err != nil {
		return fmt.Errorf("此挂载不支持安全文件改名：%w", err)
	}
	if err = root.Remove(old); err != nil {
		_ = root.Remove(next)
		return err
	}
	return nil
}

func (a *App) namingApply(w http.ResponseWriter, r *http.Request, user User) {
	var b struct {
		ID   string   `json:"id"`
		Rows []string `json:"rows"`
	}
	if !body(w, r, &b) {
		return
	}
	a.naming.mu.Lock()
	defer a.naming.mu.Unlock()
	plan, ok := a.naming.plans[b.ID]
	if !ok || plan.Owner != user.ID || time.Since(plan.Created) > time.Hour {
		fail(w, 409, "预览已失效，请重新预览")
		return
	}
	selected := map[string]bool{}
	for _, key := range b.Rows {
		if selected[key] {
			fail(w, 400, "重复的项目")
			return
		}
		selected[key] = true
	}
	if len(selected) == 0 || len(selected) > 500 {
		fail(w, 400, "请选择 1–500 个可改名项目")
		return
	}
	rows := []namingRow{}
	moves := []namingMove{}
	for _, row := range plan.Rows {
		if !selected[row.ID] {
			continue
		}
		if row.Status != "ready" {
			fail(w, 409, "包含不可执行项目，请重新预览")
			return
		}
		rows = append(rows, row)
		moves = append(moves, row.Moves...)
	}
	if len(rows) != len(selected) {
		fail(w, 400, "选择包含未知项目")
		return
	}
	filesMu.Lock()
	defer filesMu.Unlock()
	a.scraper.mu.Lock()
	defer a.scraper.mu.Unlock()
	if a.scraper.running || a.scraper.planning {
		fail(w, 409, "刮削正在进行，请结束后再改名")
		return
	}
	root, err := os.OpenRoot(fileRoot())
	if err != nil {
		fileFail(w, err)
		return
	}
	defer root.Close()
	paths := []string{}
	for _, m := range moves {
		fi, e := fileCheck(root, m.Old)
		if e != nil || !os.SameFile(m.Info, fi) || fi.Size() != m.Info.Size() || !fi.ModTime().Equal(m.Info.ModTime()) {
			fail(w, 409, "预览后源文件已变化，请重新预览")
			return
		}
		if _, e = root.Lstat(m.New); e == nil || !errors.Is(e, fs.ErrNotExist) {
			fail(w, 409, "目标已存在或不可访问，请重新预览")
			return
		}
		paths = append(paths, m.Old, m.New)
	}
	if err = a.filesAvailable(r.Context(), paths); err != nil {
		fail(w, 409, err.Error())
		return
	}
	// A separate context lets an accepted batch finish even if its browser closes.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tx, err := a.namingCatalog(ctx, moves)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	if tx != nil {
		defer tx.Rollback()
	}
	journal, err := a.namingJournal(plan.ID, moves, "prepared", "")
	if err != nil {
		fail(w, 500, "无法保存改名记录，未改动文件")
		return
	}
	job := a.newActivity("rename", plan.ID, "规范命名")
	a.changeActivity(job, func(e *activityEntry) { e.State = "running"; e.Total = len(moves) })
	completed := []namingMove{}
	for _, m := range moves {
		if err = ctx.Err(); err != nil {
			break
		}
		fi, e := fileCheck(root, m.Old)
		if e != nil || !os.SameFile(m.Info, fi) || fi.Size() != m.Info.Size() || !fi.ModTime().Equal(m.Info.ModTime()) {
			err = errors.New("执行期间源文件已变化")
			break
		}
		if err = namingRename(root, m, false); err != nil {
			break
		}
		completed = append(completed, m)
		a.changeActivity(job, func(e *activityEntry) {
			e.Done = len(completed)
			e.Current = filepath.Base(m.Old) + " → " + filepath.Base(m.New)
		})
	}
	if err == nil && tx != nil {
		err = tx.Commit()
	}
	if err != nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		rollbackErrors := []string{}
		for i := len(completed) - 1; i >= 0; i-- {
			fi, e := fileCheck(root, completed[i].New)
			if e != nil || !os.SameFile(completed[i].Info, fi) {
				rollbackErrors = append(rollbackErrors, completed[i].New)
				continue
			}
			if e = namingRename(root, completed[i], true); e != nil {
				rollbackErrors = append(rollbackErrors, completed[i].New)
			}
		}
		message := "改名失败，已撤回本批文件改动：" + scraperFilesystemError("改名", err).Error()
		if len(rollbackErrors) > 0 {
			message = "改名失败，部分文件未能撤回，请按改名记录恢复：" + strings.Join(rollbackErrors, "、")
			a.filesChanged(paths)
		}
		_, _ = a.namingJournal(plan.ID, completed, "failed", message)
		a.finishActivity(job, errors.New(message))
		delete(a.naming.plans, plan.ID)
		fail(w, 500, message)
		return
	}
	_, journalErr := a.namingJournal(plan.ID, moves, "complete", "")
	delete(a.naming.plans, plan.ID)
	a.finishActivity(job, nil)
	a.filesChanged(paths)
	respond(w, M{"ok": true, "renamed": len(rows), "files": len(moves), "journal": journal, "journalWarning": journalErr != nil, "scanQueued": true})
}

func (a *App) namingJournal(key string, moves []namingMove, status, message string) (string, error) {
	base := os.Getenv("MEDIA_INFO_ROOT")
	if base == "" {
		base = "/app/data"
	}
	dir := filepath.Join(base, "rename-history")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(M{"id": key, "updated": time.Now(), "status": status, "moves": moves, "error": message}, "", "  ")
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".rename-*")
	if err != nil {
		return "", err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, key+".json")
	if err = os.Rename(temp, path); err != nil {
		return "", err
	}
	return path, nil
}

func namingRebase(path string, moves []namingMove) string {
	for _, m := range moves {
		old, next := filepath.Join(fileRoot(), m.Old), filepath.Join(fileRoot(), m.New)
		if path == old {
			return next
		}
		if m.Info.IsDir() && strings.HasPrefix(path, old+string(filepath.Separator)) {
			return next + strings.TrimPrefix(path, old)
		}
	}
	return path
}

func (a *App) namingCatalog(ctx context.Context, moves []namingMove) (*sql.Tx, error) {
	if a.db == nil {
		return nil, nil
	}
	tx, err := a.db.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, errors.New("无法锁定媒体库")
	}
	success := false
	defer func() {
		if !success {
			tx.Rollback()
		}
	}()
	libraries, err := tx.QueryContext(ctx, "SELECT id,path,status FROM libraries ORDER BY id FOR NO KEY UPDATE NOWAIT")
	if err != nil {
		return nil, errors.New("媒体库正在扫描或修改，请稍后重试")
	}
	for libraries.Next() {
		var id, path, status string
		if err = libraries.Scan(&id, &path, &status); err != nil {
			libraries.Close()
			return nil, err
		}
		if status == "scanning" {
			libraries.Close()
			return nil, errors.New("媒体库正在扫描，请结束后再改名")
		}
	}
	err = libraries.Err()
	libraries.Close()
	if err != nil {
		return nil, err
	}
	type record struct {
		Data     map[string]any
		Old, New string
	}
	records := []record{}
	ids := map[string]string{}
	seen := map[string]bool{}
	for _, m := range moves {
		old := filepath.Join(fileRoot(), m.Old)
		rows, err := tx.QueryContext(ctx, "SELECT to_jsonb(i) FROM items i WHERE path=$1 OR ($2 AND left(path,length($1)+1)=$1||'/') ORDER BY path LIMIT 2001", old, m.Info.IsDir())
		if err != nil {
			return nil, errors.New("无法读取作品记录")
		}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, err
			}
			data := map[string]any{}
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.UseNumber()
			if err = decoder.Decode(&data); err != nil {
				rows.Close()
				return nil, err
			}
			oldID, _ := data["id"].(string)
			if seen[oldID] {
				continue
			}
			seen[oldID] = true
			path, _ := data["path"].(string)
			path = namingRebase(path, moves)
			newID := digest(path)[:32]
			ids[oldID] = newID
			data["id"], data["path"] = newID, path
			for _, field := range []string{"poster", "url"} {
				if p, ok := data[field].(string); ok {
					data[field] = namingRebase(p, moves)
				}
			}
			records = append(records, record{data, oldID, newID})
			if len(records) > 2000 {
				rows.Close()
				return nil, errors.New("改名涉及超过 2000 条作品记录，请缩小选择范围")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	for _, record := range records {
		if parent, ok := record.Data["parent"].(string); ok {
			if next, ok := ids[parent]; ok {
				record.Data["parent"] = next
			}
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE id=$1 OR path=$2", record.New, record.Data["path"]).Scan(&count); err != nil || count != 0 {
			return nil, errors.New("改名目标已有入库记录，请先处理冲突")
		}
		raw, _ := json.Marshal(record.Data)
		if _, err = tx.ExecContext(ctx, "INSERT INTO items SELECT (jsonb_populate_record(NULL::items,$1::jsonb)).*", string(raw)); err != nil {
			return nil, errors.New("无法迁移作品记录，未改动文件")
		}
	}
	// Discover every declared item reference so collections and playback state follow the new ID.
	refs, err := tx.QueryContext(ctx, `SELECT ns.nspname,t.relname,a.attname FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace ns ON ns.oid=t.relnamespace JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=c.conkey[1] WHERE c.contype='f' AND c.confrelid='items'::regclass AND array_length(c.conkey,1)=1`)
	if err != nil {
		return nil, err
	}
	queries := []string{}
	for refs.Next() {
		var schema, table, column string
		if err = refs.Scan(&schema, &table, &column); err != nil {
			refs.Close()
			return nil, err
		}
		queries = append(queries, "UPDATE "+pq.QuoteIdentifier(schema)+"."+pq.QuoteIdentifier(table)+" SET "+pq.QuoteIdentifier(column)+"=$1 WHERE "+pq.QuoteIdentifier(column)+"=$2")
	}
	err = refs.Err()
	refs.Close()
	if err != nil {
		return nil, err
	}
	queries = append(queries, "UPDATE items SET parent=$1 WHERE parent=$2", "UPDATE plays SET item=$1 WHERE item=$2", "UPDATE intro_markers SET series_id=$1 WHERE series_id=$2", "UPDATE intro_markers SET parent_id=$1 WHERE parent_id=$2")
	for _, record := range records {
		for _, query := range queries {
			if _, err = tx.ExecContext(ctx, query, record.New, record.Old); err != nil {
				return nil, errors.New("无法迁移收藏或观看记录，未改动文件")
			}
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM items WHERE id=$1", record.Old); err != nil {
			return nil, err
		}
	}
	success = true
	return tx, nil
}

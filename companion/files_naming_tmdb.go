package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type namingSourceInfo struct {
	Identity        MediaRecognition
	Kind            string
	Episode         namingEpisodeInfo
	HasEpisode      bool
	HasParentSeason bool
	SeasonDirectory bool
}

func namingSameFile(before, after fs.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func namingDirectoryKind(root *os.Root, path string, allowBare bool) (string, error) {
	entries, err := namingEntries(root, path)
	if err != nil {
		return "auto", err
	}
	media := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if entry.IsDir() {
			if _, ok := namingSeason(entry.Name()); ok {
				return "tv", nil
			}
			continue
		}
		if !featureMediaExtension(entry.Name()) {
			continue
		}
		media++
		stem, _ := namingSplit(entry.Name())
		if _, ok := namingEpisode(stem, allowBare); ok {
			return "tv", nil
		}
	}
	if media == 1 {
		return "movie", nil
	}
	if media == 0 {
		return "auto", errors.New("未找到媒体文件或季目录，请进入具体作品目录")
	}
	return "auto", nil
}

type namingTMDBResult struct {
	Kind, Title, OriginalTitle, ID, Reason string
	Year                                   int
	Err                                    error
}

type namingTMDBResolver struct {
	app      *App
	settings tmdbConfig
	cache    map[string]namingTMDBResult
}

func (resolver *namingTMDBResolver) resolve(ctx context.Context, source namingSourceInfo, request namingRequest) (namingRequest, string, error) {
	identity := source.Identity
	key := fmt.Sprintf("%s|%s|%d|%s", source.Kind, strings.ToLower(identity.Title), identity.Year, identity.TMDBID)
	if identity.TMDBID != "" {
		key = source.Kind + "|id:" + identity.TMDBID
	}
	result, ok := resolver.cache[key]
	if !ok {
		lookupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result = resolver.lookup(lookupCtx, source)
		cancel()
		resolver.cache[key] = result
	}
	if result.Err != nil {
		return request, "", result.Err
	}
	request.Kind = result.Kind
	request.Title = result.Title
	request.OriginalTitle = result.OriginalTitle
	request.Year = result.Year
	request.TMDB = result.ID
	return request, result.Reason, nil
}

func (resolver *namingTMDBResolver) lookup(ctx context.Context, source namingSourceInfo) namingTMDBResult {
	if strings.TrimSpace(resolver.settings.APIKey) == "" {
		return namingTMDBResult{Err: errors.New("请先在 TMDB 管理中配置 API Key 或访问令牌")}
	}
	kinds := []string{source.Kind}
	if source.Kind != "movie" && source.Kind != "tv" {
		kinds = []string{"movie", "tv"}
	}
	identity := source.Identity
	if identity.TMDBID != "" {
		numericID, err := strconv.Atoi(identity.TMDBID)
		if err != nil || numericID < 1 {
			return namingTMDBResult{Err: errors.New("TMDB 编号无效，请修正作品编号")}
		}
		identity.TMDBID = strconv.Itoa(numericID)
		matches := []namingTMDBResult{}
		for _, kind := range kinds {
			result := resolver.details(ctx, kind, identity.TMDBID)
			if result.Err != nil {
				if len(kinds) > 1 && result.Err.Error() == "TMDB HTTP 404" {
					continue
				}
				return result
			}
			matches = append(matches, result)
		}
		if len(matches) != 1 {
			return namingTMDBResult{Err: errors.New("TMDB 编号的作品类型不明确，请指定电影或剧集")}
		}
		result := matches[0]
		result.Reason = fmt.Sprintf("TMDB 编号确认 · %s (%d) · %s %s", result.Title, result.Year, result.Kind, result.ID)
		return result
	}
	if scraperTitleKey(identity.Title) == "" {
		return namingTMDBResult{Err: errors.New("无法确定作品标题，请填写作品名称")}
	}
	candidates, err := resolver.search(ctx, kinds, identity.Title, identity.Year)
	if err != nil {
		return namingTMDBResult{Err: err}
	}
	match, score, err := namingSelectTMDB(candidates, identity.Title, identity.Year, true)
	if err != nil && identity.Year > 0 && len(candidates) == 0 {
		candidates, err = resolver.search(ctx, kinds, identity.Title, 0)
		if err == nil {
			match, score, err = namingSelectTMDB(candidates, identity.Title, identity.Year, false)
		}
	}
	if err != nil {
		return namingTMDBResult{Err: err}
	}
	result := resolver.details(ctx, match.MediaType, strconv.Itoa(match.ID))
	if result.Err == nil {
		result.Reason = fmt.Sprintf("TMDB 自动匹配 · %s (%d) · %s %s · 匹配分 %d", result.Title, result.Year, result.Kind, result.ID, score)
	}
	return result
}

func (resolver *namingTMDBResolver) details(ctx context.Context, kind, id string) namingTMDBResult {
	var data tmdbData
	if err := resolver.app.tmdbGet(ctx, kind+"/"+id, nil, &data, resolver.settings); err != nil {
		return namingTMDBResult{Err: err}
	}
	result := namingTMDBResult{Kind: kind, ID: id, Title: data.Title, OriginalTitle: data.OriginalTitle}
	date := data.ReleaseDate
	if kind == "tv" {
		result.Title, result.OriginalTitle, date = data.Name, data.OriginalName, data.FirstAirDate
	}
	if len(date) >= 4 {
		result.Year, _ = strconv.Atoi(date[:4])
	}
	if strconv.Itoa(data.ID) != id || strings.TrimSpace(result.Title) == "" || result.Year < 1800 || result.Year > 2199 {
		result.Err = errors.New("TMDB 作品资料缺少有效名称、年份或编号，跳过自动命名")
	}
	return result
}

func (resolver *namingTMDBResolver) search(ctx context.Context, kinds []string, title string, year int) ([]scraperTMDBCandidate, error) {
	candidates := []scraperTMDBCandidate{}
	seen := map[string]bool{}
	for _, kind := range kinds {
		for _, variant := range scraperTitleVariants(title) {
			query := url.Values{"query": {variant}, "include_adult": {"false"}}
			if year > 0 {
				yearKey := "year"
				if kind == "tv" {
					yearKey = "first_air_date_year"
				}
				query.Set(yearKey, strconv.Itoa(year))
			}
			pages := 1
			for page := 1; page <= pages; page++ {
				query.Set("page", strconv.Itoa(page))
				var response tmdbSearchResponse
				if err := resolver.app.tmdbGet(ctx, "search/"+kind, query, &response, resolver.settings); err != nil {
					return nil, err
				}
				if response.TotalPages > 3 {
					return nil, errors.New("TMDB 同名搜索结果过多，请补充年份或指定作品")
				}
				if response.TotalPages > pages {
					pages = response.TotalPages
				}
				for _, candidate := range response.Results {
					candidate.MediaType = kind
					key := kind + "/" + strconv.Itoa(candidate.ID)
					if candidate.ID > 0 && !seen[key] {
						seen[key] = true
						candidates = append(candidates, candidate)
					}
				}
			}
		}
	}
	return candidates, nil
}

func namingTitleSimilarity(left, right string) float64 {
	left, right = scraperTitleKey(left), scraperTitleKey(right)
	if left == "" || right == "" {
		return 0
	}
	if left == right {
		return 1
	}
	if strings.Join(namingTitleNumbers.FindAllString(left, -1), ",") != strings.Join(namingTitleNumbers.FindAllString(right, -1), ",") {
		return 0
	}
	a, b := []rune(left), []rune(right)
	if min(len(a), len(b)) < 3 || max(len(a), len(b)) > 256 {
		return 0
	}
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, ch := range a {
		current := make([]int, len(b)+1)
		current[0] = i + 1
		for j, other := range b {
			cost := 1
			if ch == other {
				cost = 0
			}
			current[j+1] = min(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	return 1 - float64(previous[len(b)])/float64(max(len(a), len(b)))
}

func namingSelectTMDB(candidates []scraperTMDBCandidate, title string, year int, filteredYear bool) (scraperTMDBCandidate, int, error) {
	type ranked struct {
		candidate scraperTMDBCandidate
		score     int
	}
	ranking := []ranked{}
	yearAliases := []scraperTMDBCandidate{}
	for _, candidate := range candidates {
		localized, original, date := candidate.Title, candidate.OriginalTitle, candidate.ReleaseDate
		if candidate.MediaType == "tv" {
			localized, original, date = candidate.Name, candidate.OriginalName, candidate.FirstAirDate
		}
		candidateYear := 0
		if len(date) >= 4 {
			candidateYear, _ = strconv.Atoi(date[:4])
		}
		if candidateYear == 0 {
			continue
		}
		similarity := max(namingTitleSimilarity(title, localized), namingTitleSimilarity(title, original))
		score := 0
		switch {
		case year > 0 && year == candidateYear:
			if similarity >= 0.72 {
				score = int(similarity*80) + 20
			}
			if filteredYear {
				yearAliases = append(yearAliases, candidate)
			}
		case year > 0 && (year == candidateYear-1 || year == candidateYear+1):
			if similarity >= 0.94 {
				score = int(similarity*80) + 12
			}
		case year == 0:
			if similarity == 1 || similarity >= 0.94 && len([]rune(scraperTitleKey(title))) >= 6 {
				score = int(similarity*80) + 10
			}
		}
		if score > 0 {
			ranking = append(ranking, ranked{candidate, score})
		}
	}
	if len(ranking) == 0 && len(yearAliases) == 1 {
		return yearAliases[0], 82, nil
	}
	sort.SliceStable(ranking, func(i, j int) bool { return ranking[i].score > ranking[j].score })
	if len(ranking) == 0 || ranking[0].score < 80 {
		return scraperTMDBCandidate{}, 0, fmt.Errorf("TMDB 未找到可靠匹配：%s，年份 %d；请修正名称或手动选作品", title, year)
	}
	if len(ranking) > 1 && ranking[0].score-ranking[1].score < 10 {
		return scraperTMDBCandidate{}, 0, errors.New("TMDB 多个作品匹配接近，跳过自动命名；可手动选作品")
	}
	return ranking[0].candidate, ranking[0].score, nil
}

func namingSource(path string, directory bool, b namingRequest, index int) (namingSourceInfo, error) {
	name := filepath.Base(path)
	if directory {
		if _, ok := namingSeason(name); ok {
			return namingSourceInfo{SeasonDirectory: true}, nil
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
		kind := b.Kind
		if kind == "directory" {
			kind = "auto"
		}
		return namingSourceInfo{Identity: identity, Kind: kind}, nil
	}
	if b.Kind == "directory" {
		return namingSourceInfo{}, errors.New("当前类型只处理文件夹")
	}
	stem, _ := namingSplit(name)
	identity := namingTitle(name)
	fileIdentity := identity
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
			return namingSourceInfo{}, errors.New("顺序编号超过 9999")
		}
	}
	if hasEpisode && b.Mode != "sequence" && namingMulti.MatchString(stem) {
		return namingSourceInfo{}, errors.New("多集、特别篇或剧场版需核对编号，请用正则或顺序编号明确指定")
	}
	if b.Kind == "movie" && hasEpisode {
		return namingSourceInfo{}, errors.New("文件包含集号，与电影类型冲突")
	}
	if b.Kind == "tv" && !hasEpisode {
		return namingSourceInfo{}, errors.New("未找到明确集号；可确认裸集号，或使用顺序编号")
	}
	if !hasEpisode && (hasParentSeason || namingMulti.MatchString(stem)) {
		return namingSourceInfo{}, errors.New("季目录或特别篇中的文件不能自动当作电影")
	}
	if hasEpisode {
		if ep.HasSeason && hasParentSeason && ep.Season != parentSeason {
			return namingSourceInfo{}, errors.New("文件季号与所在季目录冲突，请先核对季目录")
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
			return namingSourceInfo{}, errors.New("已找到集号，但季号不明确，请指定季号")
		}
		if b.Season != nil && *b.Season != ep.Season {
			return namingSourceInfo{}, errors.New("文件季号与指定季号冲突；需要重排时使用顺序编号")
		}
		identity = scraperSearchIdentity(strings.TrimSpace(stem[:ep.Start]))
		if identity.TMDBID == "" {
			identity.TMDBID = fileIdentity.TMDBID
		}
		if identity.Title == "" || hasParentSeason {
			identity.Title = parentIdentity.Title
		}
		if identity.Year == 0 {
			identity.Year = parentIdentity.Year
			if identity.Year == 0 {
				identity.Year = fileIdentity.Year
			}
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

	if !hasEpisode {
		if parentIdentity.Title != "" && parentIdentity.Year > 0 && (identity.Title == "" || scraperTitleKey(identity.Title) == scraperTitleKey(parentIdentity.Title)) {
			if identity.Year == 0 {
				identity.Year = parentIdentity.Year
			}
			if identity.TMDBID == "" {
				identity.TMDBID = parentIdentity.TMDBID
			}
		}
	}
	kind := "movie"
	if hasEpisode {
		kind = "tv"
	}
	return namingSourceInfo{Identity: identity, Kind: kind, Episode: ep, HasEpisode: hasEpisode, HasParentSeason: hasParentSeason}, nil
}

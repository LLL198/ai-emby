package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	namingSE           = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s\s*(\d{1,3})[ ._-]*e(?:p)?\s*(\d{1,4})(?:[^0-9]|$)`)
	namingCanonicalSE  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{2,3})e(\d{2,4})(?:[^0-9]|$)`)
	namingX            = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(\d{1,3})x(\d{1,4})(?:[^0-9]|$)`)
	namingCN           = regexp.MustCompile(`第([0-9零〇一二两三四五六七八九十百千]+)季[ ._-]*第?([0-9零〇一二两三四五六七八九十百千]+)[集话話]`)
	namingEP           = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:ep?|episode)[ ._-]*(\d{1,4})(?:[^0-9]|$)`)
	namingCNEP         = regexp.MustCompile(`第([0-9零〇一二两三四五六七八九十百千]+)[集话話]`)
	namingBare         = regexp.MustCompile(`^\s*(?:\[)?([0-9]{1,4})(?:\])?(?:$|[ ._-]+)`)
	namingSeasonEN     = regexp.MustCompile(`(?i)^(?:season[ ._-]*|s[ ._-]*)(\d{1,3})$`)
	namingSeasonCN     = regexp.MustCompile(`^第([0-9零〇一二两三四五六七八九十百千]+)季$`)
	namingMulti        = regexp.MustCompile(`(?i)(?:e(?:p)?\d+|\d+x\d+|第?[0-9零〇一二两三四五六七八九十百千]+[集话話])\s*(?:[-~～至到]\s*(?:e(?:p)?)?\d+|e(?:p)?\d+)|(?:^|[\[])[0-9]{1,4}\s*[-~～至到]\s*[0-9]{1,4}|(?:合集|全集|特别篇|特別篇|剧场版|劇場版|(?:^|[ ._\[-])(?:ova|oad|sp)(?:$|[ ._\]-]))`)
	namingQuality      = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(2160p|1080p|720p|480p|4k|8k)(?:[^a-z0-9]|$)`)
	namingVariable     = regexp.MustCompile(`\{([a-z_]+)(?::([0-9]{1,2}))?\}`)
	nameUnsupported    = regexp.MustCompile(`\{[^{}]*\}`)
	namingTitleNumbers = regexp.MustCompile(`[0-9]+`)
)

type namingEpisodeInfo struct {
	Season, Episode int
	HasSeason       bool
	Start, End      int
	Reason          string
}

func namingNumber(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, n >= 0 && n <= 9999
	}
	digits := map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	units := map[rune]int{'十': 10, '百': 100, '千': 1000}
	total, current, hasUnit := 0, 0, false
	for _, r := range s {
		if n, ok := digits[r]; ok {
			current = current*10 + n
			continue
		}
		unit, ok := units[r]
		if !ok {
			return 0, false
		}
		if current == 0 {
			current = 1
		}
		total += current * unit
		current = 0
		hasUnit = true
	}
	if !hasUnit {
		return current, current >= 0 && current <= 9999
	}
	return total + current, total+current <= 9999
}

func namingSeason(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "specials") || s == "特别篇" || s == "特別篇" {
		return 0, true
	}
	for _, re := range []*regexp.Regexp{namingSeasonEN, namingSeasonCN} {
		if m := re.FindStringSubmatch(s); len(m) == 2 {
			n, ok := namingNumber(m[1])
			return n, ok && n <= 999
		}
	}
	return 0, false
}

func namingEpisode(s string, allowBare bool) (namingEpisodeInfo, bool) {
	for i, re := range []*regexp.Regexp{namingSE, namingX, namingCN, namingEP, namingCNEP, namingBare} {
		if i == 5 && !allowBare {
			continue
		}
		m := re.FindStringSubmatchIndex(s)
		if len(m) < 4 {
			continue
		}
		info := namingEpisodeInfo{Start: m[0], End: m[1]}
		if len(m) >= 6 {
			season, ok := namingNumber(s[m[2]:m[3]])
			if !ok || season > 999 {
				continue
			}
			episode, ok := namingNumber(s[m[4]:m[5]])
			if !ok || episode < 1 {
				continue
			}
			info.Season, info.Episode, info.HasSeason = season, episode, true
		} else {
			episode, ok := namingNumber(s[m[2]:m[3]])
			if !ok || episode < 1 {
				continue
			}
			if i == 5 && (episode >= 1900 && episode <= 2099 || episode == 480 || episode == 576 || episode == 720 || episode == 1080 || episode == 2160) {
				continue
			}
			info.Episode = episode
		}
		info.Reason = []string{"季集编号", "1x02 季集编号", "中文季集编号", "EP 集号", "中文集号", "已确认的裸集号"}[i]
		return info, true
	}
	return namingEpisodeInfo{}, false
}

func namingSplit(name string) (string, string) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if strings.EqualFold(ext, ".strm") && featureMediaExtension(stem) && !strings.EqualFold(filepath.Ext(stem), ".strm") {
		nested := filepath.Ext(stem)
		stem = strings.TrimSuffix(stem, nested)
		ext = nested + ext
	}
	return stem, ext
}

func namingTitle(name string) MediaRecognition {
	stem, _ := namingSplit(name)
	return scraperSearchIdentity(stem)
}

// A matching movie directory can supply a missing year or provider ID. Do not
// borrow identity from category, collection or differently numbered movies.
func namingMovieIdentity(path string) MediaRecognition {
	identity := namingTitle(filepath.Base(path))
	parent := scraperSearchIdentity(filepath.Base(filepath.Dir(path)))
	sameTitle := scraperTitleKey(identity.Title) == scraperTitleKey(parent.Title)
	if parent.Title != "" && sameTitle {
		if identity.Year == 0 {
			identity.Year = parent.Year
		}
		if identity.TMDBID == "" {
			identity.TMDBID = parent.TMDBID
		}
	}
	return identity
}

func namingClean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.NewReplacer("/", "／", "\\", "＼", ":", "：", "*", "＊", "?", "？", "\"", "＂", "<", "＜", ">", "＞", "|", "｜").Replace(s)
	return strings.Trim(s, " .")
}

func namingRender(template string, values map[string]string) (string, error) {
	var problem error
	out := namingVariable.ReplaceAllStringFunc(template, func(token string) string {
		m := namingVariable.FindStringSubmatch(token)
		v, ok := values[m[1]]
		if !ok {
			problem = fmt.Errorf("未知模板变量：%s", m[1])
			return ""
		}
		if m[2] != "" && v != "" {
			width, _ := strconv.Atoi(m[2])
			if width > 8 {
				problem = errors.New("数字补位最多 8 位")
				return ""
			}
			if n, err := strconv.Atoi(v); err == nil {
				v = fmt.Sprintf("%0*d", width, n)
			} else {
				problem = fmt.Errorf("变量 %s 不能数字补位", m[1])
			}
		}
		return v
	})
	if problem != nil {
		return "", problem
	}
	if nameUnsupported.MatchString(scraperTMDBTagRE.ReplaceAllString(out, "")) {
		return "", errors.New("模板只支持 {变量} 或 {变量:补位长度}")
	}
	out = namingClean(out)
	if out == "" || len(out) > 240 {
		return "", errors.New("新名称为空或超过 240 字节，请缩短模板或标题")
	}
	return out, nil
}

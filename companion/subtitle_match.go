package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const subtitleMaxBytes = 8 << 20

var (
	subtitleDelimiter      = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	subtitleEpisodePattern = regexp.MustCompile(`(?i)s(\d{1,2})[ ._-]*e(\d{1,3})`)
	subtitleEpisodeRange   = regexp.MustCompile(`(?i)s\d+[ ._-]*e\d+\s*(?:[-~]|e\d|至|到)`)
	subtitleYearPattern    = regexp.MustCompile(`\b(?:19|20)\d{2}\b`)
	subtitleReleaseTags    = regexp.MustCompile(`(?i)[ ._\-\[]+(?:2160p|1080p|720p|480p|bluray|blu-ray|web-dl|webrip|hdtv|dvdrip|x264|x265|h264|h265|hevc|remux)\b.*$`)
	subtitleEnglishPattern = regexp.MustCompile(`(?i)(?:^|[^a-z])(english|eng|en)(?:[^a-z]|$)`)
	subtitleMarkup         = regexp.MustCompile(`\{[^}]*\}|<[^>]*>`)
)

func subtitleTraditional(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range []string{"繁", "traditional", "zh-tw", "zh-hk", "zh-hant"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	for _, word := range subtitleDelimiter.Split(text, -1) {
		if word == "cht" || word == "hant" || word == "tc" {
			return true
		}
	}
	return false
}

func subtitleSimplified(text string) bool {
	if subtitleTraditional(text) {
		return false
	}
	text = strings.ToLower(text)
	for _, marker := range []string{"简体", "简中", "簡體", "simplified chinese", "chinese simplified", "zh-cn", "zh-hans"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	for _, word := range subtitleDelimiter.Split(text, -1) {
		if word == "chs" || word == "hans" || word == "sc" {
			return true
		}
	}
	return false
}

func subtitleStreams(value any) []M {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var streams []M
	if json.Unmarshal(data, &streams) != nil {
		return nil
	}
	return streams
}

func subtitleEmbeddedReason(streams any) string {
	for _, stream := range subtitleStreams(streams) {
		if stream["Type"] != "Subtitle" {
			continue
		}
		var labels []string
		for _, key := range []string{"Language", "Title", "DisplayTitle"} {
			if label, ok := stream[key].(string); ok {
				labels = append(labels, label)
			}
		}
		if subtitleSimplified(strings.Join(labels, " ")) {
			if stream["IsExternal"] == true {
				return "跳过：已有外挂简体中文字幕"
			}
			return "跳过：已有内封简体中文字幕"
		}
	}
	return ""
}

func subtitleNorm(text string) string {
	return strings.Join(subtitleDelimiter.Split(strings.ToLower(text), -1), "")
}

func subtitleRelease(name string) string {
	name = filepath.Base(name)
	for {
		ext := filepath.Ext(name)
		switch strings.ToLower(ext) {
		case ".ts", ".ass", ".avi", ".mkv", ".mp4", ".srt", ".strm":
			name = strings.TrimSuffix(name, ext)
		default:
			return name
		}
	}
}

func subtitleTitle(name string) string {
	name = subtitleReleaseTags.ReplaceAllString(subtitleRelease(name), "")
	if match := subtitleEpisodePattern.FindStringIndex(name); match != nil {
		name = name[:match[0]]
	}
	if match := subtitleYearPattern.FindStringIndex(name); match != nil {
		name = name[:match[0]]
	}
	return subtitleNorm(name)
}

func (sub assrtSub) language() int {
	if subtitleSimplified(sub.Lang.Desc) || sub.Lang.List["langchs"] {
		return 2
	}
	if subtitleTraditional(sub.Lang.Desc) || sub.Lang.List["langcht"] {
		return 1
	}
	return 0
}

func subtitleEpisodeOK(text string, item Item, required bool) bool {
	matches := subtitleEpisodePattern.FindAllStringSubmatch(text, -1)
	if item.Kind != "Episode" {
		return len(matches) == 0
	}
	if item.Season < 0 || item.Episode < 1 || required && len(matches) == 0 {
		return false
	}
	for _, match := range matches {
		season, seasonErr := strconv.Atoi(match[1])
		episode, episodeErr := strconv.Atoi(match[2])
		if seasonErr != nil || episodeErr != nil || season != item.Season || episode != item.Episode {
			return false
		}
	}
	return !subtitleEpisodeRange.MatchString(text)
}

func subtitleScore(item Item, release string, aliases []string, sub assrtSub) int {
	if !subtitleEpisodeOK(sub.Video+" "+sub.Native, item, true) {
		return 0
	}
	yearMatches := false
	for _, text := range subtitleYearPattern.FindAllString(sub.Video+" "+sub.Native, -1) {
		year, _ := strconv.Atoi(text)
		if item.Year > 0 && year != item.Year {
			return 0
		}
		yearMatches = year == item.Year
	}
	exact := release != "" && subtitleNorm(subtitleRelease(sub.Video)) == subtitleNorm(release)
	titleMatches := false
	titles := []string{subtitleTitle(sub.Video), subtitleTitle(sub.Native)}
	for _, part := range strings.FieldsFunc(sub.Native, func(r rune) bool { return r == '/' || r == '|' }) {
		titles = append(titles, subtitleTitle(part))
	}
	for _, alias := range aliases {
		alias = subtitleNorm(alias)
		if utf8.RuneCountInString(alias) < 2 {
			continue
		}
		for _, title := range titles {
			if alias == title {
				titleMatches = true
			}
		}
	}
	if !exact && !titleMatches {
		return 0
	}
	score := 0
	if exact {
		score += 45
	}
	if titleMatches {
		score += 45
	}
	if yearMatches {
		score += 20
	}
	switch item.Kind {
	case "Episode":
		score += 30
	case "Movie":
		if !exact {
			return 0
		}
		if yearMatches {
			score += 15
		}
	default:
		return 0
	}
	return score
}

func subtitleRankedCandidates(item Item, release string, aliases []string, candidates []assrtSub) []assrtSub {
	type ranked struct {
		sub   assrtSub
		score int
	}
	seen := make(map[int]bool)
	var matches []ranked
	for _, sub := range candidates {
		if seen[sub.ID] || subtitleEnglishPattern.MatchString(sub.Lang.Desc) || sub.Lang.List["langeng"] {
			continue
		}
		seen[sub.ID] = true
		if score := subtitleScore(item, release, aliases, sub); score >= 80 {
			matches = append(matches, ranked{sub, score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if matches[i].sub.language() != matches[j].sub.language() {
			return matches[i].sub.language() > matches[j].sub.language()
		}
		return matches[i].sub.ID < matches[j].sub.ID
	})
	result := make([]assrtSub, 0, len(matches))
	for _, match := range matches {
		result = append(result, match.sub)
	}
	return result
}

func subtitleFileLanguage(name string) int {
	if subtitleSimplified(name) {
		return 2
	}
	if subtitleTraditional(name) {
		return 1
	}
	return 0
}

func subtitleSelectFile(item Item, sub assrtSub) (assrtFile, bool) {
	files := sub.Files
	if len(files) == 0 {
		files = []assrtFile{{Name: sub.Filename, URL: sub.URL}}
	}
	var selected assrtFile
	best, ties := 0, 0
	for _, file := range files {
		switch strings.ToLower(filepath.Ext(file.Name)) {
		case ".srt", ".ass", ".ssa", ".vtt", ".zip":
		default:
			continue
		}
		if file.URL == "" || !subtitleEpisodeOK(file.Name, item, item.Kind == "Episode") {
			continue
		}
		language := subtitleFileLanguage(file.Name)
		if language == 0 && len(files) == 1 && !subtitleEnglishPattern.MatchString(file.Name) {
			language = sub.language()
		}
		if language == 0 {
			if len(files) != 1 || subtitleEnglishPattern.MatchString(file.Name) {
				continue
			}
			language = 1
		}
		if language > best {
			selected, best, ties = file, language, 1
		} else if language == best {
			ties++
		}
	}
	return selected, ties == 1
}

func subtitleTextValid(data []byte, ext string) bool {
	if len(data) == 0 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	text := strings.TrimSpace(string(data))
	if strings.HasPrefix(text, "<") {
		return false
	}
	switch ext {
	case ".srt":
		return strings.Contains(text, "-->")
	case ".vtt":
		return strings.HasPrefix(text, "WEBVTT") && strings.Contains(text, "-->")
	case ".ass", ".ssa":
		lower := strings.ToLower(text)
		return strings.Contains(lower, "[events]") && strings.Contains(lower, "dialogue:") && strings.IndexFunc(text, unicode.IsLetter) >= 0
	}
	return false
}

func subtitleContentLanguage(data []byte, ext, name string) int {
	if !subtitleTextValid(data, ext) {
		return 0
	}
	var han, simplified, traditional, kana, letters int
	for _, line := range strings.Split(string(data), "\n") {
		if ext == ".ass" || ext == ".ssa" {
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "dialogue:") {
				continue
			}
			fields := strings.SplitN(line, ",", 10)
			if len(fields) != 10 {
				continue
			}
			line = fields[9]
		} else if strings.Contains(line, "-->") {
			continue
		}
		for _, char := range subtitleMarkup.ReplaceAllString(line, "") {
			if unicode.Is(unicode.Han, char) {
				han++
				if strings.ContainsRune("简体国权龙汉语这后发里为时动开东门车书学说电视级爱边过", char) {
					simplified++
				}
				if strings.ContainsRune("簡體國權龍漢語這後發裡為時動開東門車書學說電視級愛邊過", char) {
					traditional++
				}
			}
			if unicode.Is(unicode.Hiragana, char) || unicode.Is(unicode.Katakana, char) {
				kana++
			}
			if unicode.IsLetter(char) {
				letters++
			}
		}
	}
	if han < 2 || kana > 0 || letters > han*20 {
		return -1
	}
	if simplified > 0 && traditional == 0 {
		return 2
	}
	if traditional > 0 && simplified == 0 {
		return 1
	}
	return 0
}

func subtitlePayload(item Item, name string, data []byte) (content []byte, filename, ext string, language int, ok bool) {
	ext = strings.ToLower(filepath.Ext(name))
	if len(data) > subtitleMaxBytes {
		return nil, "", "", 0, false
	}
	if ext != ".zip" {
		language = subtitleContentLanguage(data, ext, name)
		return data, name, ext, language, language > 0
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) > 100 {
		return nil, "", "", 0, false
	}
	total, ties := 0, 0
	for _, file := range archive.File {
		fileExt := strings.ToLower(filepath.Ext(file.Name))
		switch fileExt {
		case ".srt", ".ass", ".ssa", ".vtt":
		default:
			continue
		}
		if !file.Mode().IsRegular() || !subtitleEpisodeOK(file.Name, item, item.Kind == "Episode") {
			continue
		}
		if file.UncompressedSize64 > subtitleMaxBytes {
			return nil, "", "", 0, false
		}
		stream, err := file.Open()
		if err != nil {
			return nil, "", "", 0, false
		}
		payload, readErr := io.ReadAll(io.LimitReader(stream, subtitleMaxBytes+1))
		closeErr := stream.Close()
		total += len(payload)
		if readErr != nil || closeErr != nil || total > subtitleMaxBytes {
			return nil, "", "", 0, false
		}
		rank := subtitleContentLanguage(payload, fileExt, file.Name)
		if rank > language {
			content, filename, ext, language, ties = payload, file.Name, fileExt, rank, 1
		} else if rank > 0 && rank == language {
			ties++
		}
	}
	return content, filename, ext, language, language > 0 && ties == 1
}

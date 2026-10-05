package main

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type MediaRecognition struct {
	Kind       string
	Title      string
	Year       int
	Season     int
	Episode    int
	TMDBID     string
	Confidence float64
	Matched    bool
	Reason     string
	Recognizer string
}

type MediaRecognitionInput struct {
	Library     string
	LibraryType string
	Directory   string
	Name        string
	Entries     []fs.DirEntry
	Parent      *MediaRecognition
}

type MediaMatcher interface {
	Match(MediaRecognitionInput) MediaRecognition
	Name() string
	Priority() int
}

type MediaRecognizer interface {
	Name() string
	Recognize(MediaRecognitionInput) MediaRecognition
}

type RuleBasedRecognizer struct {
	mu       sync.RWMutex
	matchers []MediaMatcher
}

type scraperRule struct {
	name     string
	priority int
	match    func(MediaRecognitionInput) MediaRecognition
}

var (
	mediaRecognizerMu sync.RWMutex
	mediaRecognizers  []MediaRecognizer
	ruleRecognizer    = &RuleBasedRecognizer{}

	scraperSeasonEpisodeRE = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,3})e(?:p)?(\d{1,4})(?:[^0-9]|$)`)
	scraperSeasonRE        = regexp.MustCompile(`(?i)^(?:season[ ._-]*|s)(\d{1,3})$`)
	scraperYearRE          = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d{2})(?:[^0-9]|$)`)
	scraperTMDBTagRE       = regexp.MustCompile(`(?i)[\[{]tmdb(?:id)?[= :_-]+([0-9]+)[\]}]`)
	scraperYearTitleRE     = regexp.MustCompile(`[^0-9a-zA-Z]((?:19|20)\d{2})(?:[^0-9]|$)`)
	scraperEpisodeRE       = regexp.MustCompile(`(?i)(?:^|[ ._\[-])ep?(\d{1,3})(?:$|[^a-z0-9])`)
	scraperReleaseTokenRE  = regexp.MustCompile(`(?i)(?:^|[ ._\[(-])(?:s\d{1,3}(?:e(?:p)?\d{1,4})?|\d{3,4}[pi]|2160p|4k|8k|uhd|bluray|blu-ray|web[ ._-]?dl|webrip|hdtv|remux|x26[45]|h[ .]?26[45]|hevc|av1|hdr(?:10\+?)?|dovi|dv|nf|netflix|amzn|aac|ddp(?:[ .]?\d(?:\.\d)?)?|complete|全集|国语|中字)(?:$|[ ._\])-])`)
)

func RegisterMediaRecognizer(recognizer MediaRecognizer) {
	if recognizer == nil || recognizer.Name() == "" {
		panic("invalid recognizer")
	}

	name := recognizer.Name()
	mediaRecognizerMu.Lock()
	defer mediaRecognizerMu.Unlock()
	for _, registered := range mediaRecognizers {
		if registered.Name() == name {
			panic("duplicate recognizer")
		}
	}
	mediaRecognizers = append(mediaRecognizers, recognizer)
}

func recognizeMedia(input MediaRecognitionInput) MediaRecognition {
	mediaRecognizerMu.RLock()
	registered := append([]MediaRecognizer(nil), mediaRecognizers...)
	mediaRecognizerMu.RUnlock()

	for _, recognizer := range registered {
		result := recognizer.Recognize(input)
		if result.Matched && result.Kind != "Unknown" {
			if result.Recognizer == "" {
				result.Recognizer = recognizer.Name()
			} else {
				result.Recognizer = recognizer.Name() + ":" + result.Recognizer
			}
			return result
		}
	}
	return MediaRecognition{Kind: "Unknown"}
}

func (*RuleBasedRecognizer) Name() string { return "RuleBasedRecognizer" }

func (r *RuleBasedRecognizer) RegisterMatcher(matcher MediaMatcher) {
	if matcher == nil || matcher.Name() == "" {
		panic("invalid media matcher")
	}

	name := matcher.Name()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, registered := range r.matchers {
		if registered.Name() == name {
			panic("duplicate media matcher")
		}
	}
	r.matchers = append(r.matchers, matcher)
	sort.SliceStable(r.matchers, func(i, j int) bool {
		return r.matchers[i].Priority() > r.matchers[j].Priority()
	})
}

func (r *RuleBasedRecognizer) Recognize(input MediaRecognitionInput) MediaRecognition {
	r.mu.RLock()
	matchers := append([]MediaMatcher(nil), r.matchers...)
	r.mu.RUnlock()

	for _, matcher := range matchers {
		result := matcher.Match(input)
		if result.Matched && result.Kind != "Unknown" {
			if result.Recognizer == "" {
				result.Recognizer = matcher.Name()
			} else {
				result.Recognizer = matcher.Name() + ":" + result.Recognizer
			}
			return result
		}
	}
	return MediaRecognition{Kind: "Unknown"}
}

func (r scraperRule) Name() string                                    { return r.name }
func (r scraperRule) Priority() int                                   { return r.priority }
func (r scraperRule) Match(in MediaRecognitionInput) MediaRecognition { return r.match(in) }

func scraperMediaFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ts", ".m4v", ".mkv", ".avi", ".mp4", ".strm":
		return true
	default:
		return false
	}
}

func init() {
	ruleRecognizer.RegisterMatcher(scraperRule{
		name:     "单集编号",
		priority: 100,
		match:    recognizeEpisodeName,
	})
	ruleRecognizer.RegisterMatcher(scraperRule{
		name:     "季目录",
		priority: 90,
		match:    recognizeSeasonDirectory,
	})
	ruleRecognizer.RegisterMatcher(scraperRule{
		name:     "目录结构",
		priority: 80,
		match:    recognizeDirectoryStructure,
	})
	RegisterMediaRecognizer(ruleRecognizer)
}

func recognizeEpisodeName(input MediaRecognitionInput) MediaRecognition {
	result := scraperSearchIdentity(input.Name)
	if result.Episode == 0 || !scraperSeasonEpisodeRE.MatchString(input.Name) {
		return result
	}
	result.Kind = "Episode"
	result.Confidence = 0.98
	result.Matched = true
	result.Reason = "单集编号"
	if input.Parent != nil {
		if result.Year == 0 {
			result.Year = input.Parent.Year
		}
		if result.TMDBID == "" {
			result.TMDBID = input.Parent.TMDBID
		}
		if result.Title == "" {
			result.Title = input.Parent.Title
		}
	}
	return result
}

func recognizeSeasonDirectory(input MediaRecognitionInput) MediaRecognition {
	name := input.Name
	if name == "" {
		name = filepath.Base(input.Directory)
	}
	match := scraperSeasonRE.FindStringSubmatch(strings.TrimSpace(name))
	if len(match) != 2 || (input.LibraryType != "tvshows" && (input.Parent == nil || input.Parent.Kind != "Series")) {
		return scraperSearchIdentity(name)
	}

	season, _ := strconv.Atoi(match[1])
	result := scraperSearchIdentity(name)
	result.Kind = "Season"
	result.Season = season
	result.Confidence = 0.9
	result.Matched = true
	result.Reason = "季目录"
	if input.Parent != nil {
		if result.Title == "" {
			result.Title = input.Parent.Title
		}
		if result.Year == 0 {
			result.Year = input.Parent.Year
		}
		if result.TMDBID == "" {
			result.TMDBID = input.Parent.TMDBID
		}
	}
	return result
}

func recognizeDirectoryStructure(input MediaRecognitionInput) MediaRecognition {
	result := scraperSearchIdentity(input.Name)
	mediaFiles := 0
	episodeFiles := 0
	for _, entry := range input.Entries {
		if entry == nil || entry.IsDir() || !scraperMediaFile(entry.Name()) {
			continue
		}
		mediaFiles++
		fileIdentity := scraperSearchIdentity(entry.Name())
		if fileIdentity.Episode > 0 {
			episodeFiles++
		}
	}

	if episodeFiles > 0 {
		if input.Parent != nil && input.Parent.Kind == "Series" {
			result.Kind = "Season"
			if result.Season == 0 {
				result.Season = 1
			}
		} else {
			result.Kind = "Series"
		}
		result.Confidence = 0.95
		result.Matched = true
		result.Reason = "季目录或单集媒体结构"
		inheritSeries(&result, input.Parent)
		return result
	}

	if mediaFiles == 1 && (input.LibraryType == "movies" || result.Year > 0 || result.TMDBID != "") {
		result.Kind = "Movie"
		result.Confidence = 0.9
		result.Matched = true
		result.Reason = "单媒体文件与年份/编号/电影库类型"
		return result
	}
	if input.LibraryType == "tvshows" && result.TMDBID != "" {
		result.Kind = "Series"
		result.Confidence = 0.8
		result.Matched = true
		result.Reason = "电视剧库类型与 TMDB 编号"
		return result
	}
	return result
}

func inheritSeries(result *MediaRecognition, parent *MediaRecognition) {
	if parent == nil {
		return
	}
	if result.Title == "" {
		result.Title = parent.Title
	}
	if result.Year == 0 {
		result.Year = parent.Year
	}
	if result.TMDBID == "" {
		result.TMDBID = parent.TMDBID
	}
}

func scraperSearchIdentity(name string) MediaRecognition {
	result := MediaRecognition{Kind: "Unknown"}
	if match := scraperTMDBTagRE.FindStringSubmatch(name); len(match) == 2 {
		result.TMDBID = match[1]
	}
	identity := scraperTMDBTagRE.ReplaceAllString(name, "")
	if scraperMediaFile(identity) {
		identity = strings.TrimSuffix(identity, filepath.Ext(identity))
	}

	if match := scraperSeasonEpisodeRE.FindStringSubmatchIndex(identity); len(match) >= 6 {
		result.Season, _ = strconv.Atoi(identity[match[2]:match[3]])
		result.Episode, _ = strconv.Atoi(identity[match[4]:match[5]])
		identity = identity[:match[0]]
	} else if match := scraperSeasonRE.FindStringSubmatch(identity); len(match) == 2 {
		result.Season, _ = strconv.Atoi(match[1])
	}

	if match := scraperEpisodeRE.FindStringSubmatchIndex(identity); len(match) >= 4 && result.Episode == 0 {
		number, _ := strconv.Atoi(identity[match[2]:match[3]])
		if number != 480 && number != 576 && number != 720 {
			result.Episode = number
			identity = identity[:match[0]]
		}
	}

	if match := scraperYearTitleRE.FindStringSubmatchIndex(identity); len(match) >= 4 {
		result.Year, _ = strconv.Atoi(identity[match[2]:match[3]])
		identity = identity[:match[0]]
	} else if match := scraperYearRE.FindStringSubmatch(identity); len(match) == 2 {
		result.Year, _ = strconv.Atoi(match[1])
	}

	if match := scraperReleaseTokenRE.FindStringIndex(identity); len(match) == 2 {
		identity = identity[:match[0]]
	}
	identity = strings.Trim(identity, " ._-()[]{}")
	identity = strings.NewReplacer(".", " ", "_", " ").Replace(identity)
	result.Title = strings.Join(strings.Fields(identity), " ")
	return result
}

package main

import (
	"context"
	"database/sql"
	"io/fs"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type IntroRecord struct {
	Version           int
	ItemID            string
	SeriesID          string
	TMDBSeriesID      string
	TMDBEpisodeID     string
	Season            int
	Episode           int
	IntroStartTicks   int64
	IntroEndTicks     int64
	CreditsStartTicks int64
	Source            string
	Confidence        float64
	UpdatedAt         time.Time
}

type NotifyEvent struct {
	Type         string
	Action       string
	UserName     string
	ItemID       string
	ItemName     string
	MediaType    string
	Year         int
	Rating       float64
	Progress     float64
	IP           string
	Client       string
	Device       string
	TMDBID       string
	IMDBID       string
	Overview     string
	ImageURL     string
	Time         time.Time
	SessionID    string
	UserID       string
	SeriesName   string
	EpisodeCount int
}

type assrtFile struct {
	Name string `json:"f"`
	URL  string `json:"url"`
}

type assrtSub struct {
	ID       int         `json:"id"`
	Native   string      `json:"native_name"`
	Video    string      `json:"videoname"`
	Filename string      `json:"filename"`
	URL      string      `json:"url"`
	Files    []assrtFile `json:"filelist"`
	Lang     struct {
		Desc string          `json:"desc"`
		List map[string]bool `json:"langlist"`
	} `json:"lang"`
}

type bangumiScraper struct{}

type catalogBatchKey struct{}

type catalogCoversKey struct{}

type dashboardPlay struct {
	Username        string  `json:"username"`
	MediaName       string  `json:"mediaName"`
	Device          string  `json:"device"`
	Client          string  `json:"client"`
	ProgressPercent float64 `json:"progressPercent"`
}

type introConfig struct {
	Enabled          bool
	AutoIntro        bool
	AutoCredits      bool
	AutoSkipIntro    bool
	AutoSkipCredits  bool
	Directory        string
	WindowMinutes    int
	MinSamples       int
	ToleranceSeconds int
}

type introMarker struct {
	SeriesID          string
	Season            int
	IntroStartTicks   int64
	IntroEndTicks     int64
	CreditsStartTicks int64
	IntroSamples      int
	CreditsSamples    int
	Source            string
}

type listStateDeferredKey struct{}

type mediaFsEvent struct {
	Name    string
	Op      uint32
	Library string
	Root    string
}

type mediaSubtitleMarker struct {
	Owner       string
	ItemID      string
	Fingerprint string
	File        string
	Hash        string
	LanguageTag string
	Ext         string
}

type probeAutomationSettings struct {
	BatchRoots     []probeRoot
	MonitorEnabled bool
	MonitorRoots   []probeRoot
	Generation     uint64
	Cursor         string
	Done           int64
	Skipped        int64
	Failed         int64
	State          string
}

type probeRoot struct {
	Library string
	Root    string
}

type proxySettings struct {
	Enabled            bool
	Type               string
	URL                string
	Username           string
	Password           string `json:"-"`
	PasswordConfigured bool
	Scopes             map[string]bool
}

type redirectTraceKey struct{}

type scraperCategory struct {
	Enabled bool
	Content []string
}

type scraperFanartImage struct {
	URL    string `json:"url"`
	Lang   string `json:"lang"`
	Season string `json:"season"`
}

type scraperFileScope struct {
	Library   string
	Root      string
	Relative  string
	Path      string
	Directory bool
}

type scraperLanguageKey struct{}

type scraperMonitorRoot struct {
	Library string
	Root    string
	Kind    string
}

type scraperObject struct {
	Item        Item `json:"-"`
	ID          string
	Name        string
	Kind        string
	Error       string
	Targets     []scraperTarget
	Disabled    bool
	Recognition *MediaRecognition `json:",omitempty"`
}

type scraperPlan struct {
	ID            string
	Config        scraperConfig
	Objects       []scraperObject
	Pending       int
	Skipped       int
	Overwrite     int
	Disabled      int
	RetrySelected int `json:",omitempty"`
	RetrySkipped  int `json:",omitempty"`
}

type scraperPoster struct {
	Path     string `json:"file_path"`
	Language string `json:"iso_639_1"`
}

type scraperPreferencesKey struct{}

type scraperScope struct {
	Library  string
	Root     string
	Relative string
	Enabled  bool
}

type scraperTarget struct {
	Content string
	Path    string
	Action  string
}

type subtitleActivityKey struct{}

type subtitleConfig struct {
	Enabled         bool
	AutoClean       bool
	SaveBesideMedia bool
	Directory       string
	Token           string `json:"-"`
	TokenConfigured bool
}

type subtitleMatch struct {
	Detail      assrtSub
	File        assrtFile
	Score       int
	Fingerprint string
	Expires     time.Time
}

type subtitleRecord struct {
	Source       string
	Language     string
	Path         string
	ItemID       string
	Fingerprint  string
	ID           string
	Verified     bool
	LanguageName string
	LanguageTag  string
	FinalName    string
	Owner        string
	Score        int
	Time         time.Time
}

type telegramConfig struct {
	Enabled  bool   `json:"telegram_enabled"`
	Token    string `json:"telegram_token"`
	ChatID   string `json:"telegram_chat_id"`
	Notify   bool   `json:"telegram_notify_enabled"`
	NewMedia bool   `json:"telegram_notify_new_media"`
	Playback bool   `json:"telegram_notify_playback"`
}

type tmdbConfig struct {
	Enabled           bool
	APIBase           string
	APIKey            string
	Directory         string
	RequestsPerSecond int
	HasAPIKey         bool   `json:",omitempty"`
	Valid             bool   `json:",omitempty"`
	Validation        string `json:",omitempty"`
}

type tmdbData struct {
	Title               string `json:"title"`
	Name                string `json:"name"`
	ID                  int
	Overview            string
	OriginalTitle       string  `json:"original_title"`
	OriginalName        string  `json:"original_name"`
	ReleaseDate         string  `json:"release_date"`
	FirstAirDate        string  `json:"first_air_date"`
	AirDate             string  `json:"air_date"`
	Poster              string  `json:"poster_path"`
	Backdrop            string  `json:"backdrop_path"`
	Still               string  `json:"still_path"`
	Rating              float64 `json:"vote_average"`
	Genres              []struct{ Name string }
	Countries           []string `json:"origin_country"`
	ProductionCountries []struct {
		Name string `json:"name"`
	} `json:"production_countries"`
	Credits struct {
		Cast []struct {
			Name      string
			Character string
			Profile   string `json:"profile_path"`
		}
		Crew []struct {
			Name string
			Job  string
		}
	} `json:"credits"`
	Runtime        float64
	EpisodeRuntime []float64 `json:"episode_run_time"`
}

type tmdbRecord struct {
	Key   string
	Until int64
	Data  tmdbData
}

type active struct {
	userID    string
	deviceKey string
	itemID    string
	username  string
	name      string
	kind      string
	parent    string
	series    string
	season    int
	episode   int
}

type browseSortSpec struct {
	expr    string
	kind    string
	userArg bool
}

type catalogBatch struct {
	metadata     map[string]sidecar
	media        map[string]M
	child        map[string]int
	versions     map[string][]Item
	remote       map[string]tmdbData
	items        map[string]Item
	tmdb         tmdbConfig
	nanShareFast bool
}

type catalogGroupedRow struct {
	row   interface{ Scan(...any) error }
	group *string
}

type collageEntry struct {
	data  []byte
	until time.Time
}

type counts struct {
	total    int
	unplayed int
}

type dashboardCPUSample struct {
	mu      sync.Mutex
	at      time.Time
	seconds float64
}

type episodeBatch struct {
	event    NotifyEvent
	deadline time.Time
	seen     map[string]bool
}

type fastNanShareFlight struct {
	done   chan struct{}
	result fastNanShareResult
}

type fastNanShareResult struct {
	source   sourceRedirectResult
	location string
	until    time.Time
}

type fastNanShareState struct {
	mu      sync.Mutex
	cache   map[string]fastNanShareResult
	flights map[string]*fastNanShareFlight
}

type introCandidate struct {
	item       string
	seconds    int64
	credits    bool
	generation uint64
}

type introGroup struct {
	intro   map[string]int64
	credits map[string]int64
}

type introPlayback struct {
	item      string
	seq       uint64
	last      int64
	duration  int64
	candidate int64
	pending   bool
	updated   time.Time
}

type introRecordWrite struct {
	record     IntroRecord
	generation uint64
}

type introSessionSlot struct {
	key    string
	device string
	seq    uint64
}

type introState struct {
	ctx            context.Context
	cancel         context.CancelFunc
	workers        sync.WaitGroup
	learningDone   chan struct{}
	enabled        atomic.Bool
	mu             sync.Mutex
	persistMu      sync.Mutex
	config         introConfig
	sessions       map[string]introPlayback
	active         map[string]string
	ring           [1024]introSessionSlot
	next           uint64
	groups         map[string]*introGroup
	markers        map[string]introMarker
	markerOverflow bool
	queue          chan introCandidate
	recordQueue    chan introRecordWrite
	generation     uint64
}

type mediaEventHub struct {
	mu   sync.RWMutex
	next uint64
	subs map[uint64]chan mediaFsEvent
}

type mediaRefreshGuard struct {
	mu             sync.Mutex
	scraped        map[string]time.Time
	pending        map[string]map[string]bool
	timers         map[string]*time.Timer
	generation     map[string]uint64
	nextGeneration uint64
	manual         map[string]map[string]bool
}

type notificationJob struct {
	app   *App
	event NotifyEvent
}

type probeBatchState struct {
	mu       sync.Mutex
	running  bool
	stopped  bool
	workers  sync.WaitGroup
	cancel   func()
	activity string
}

type probeFileSignature struct {
	size     int64
	modified int64
}

type probeMonitorEntry struct {
	root      probeRoot
	path      string
	signature probeFileSignature
	changed   time.Time
	first     time.Time
	retry     time.Time
}

type proxyState struct {
	mu         sync.RWMutex
	settings   proxySettings
	clients    map[string]*http.Client
	transports []*http.Transport
}

type rankedCatalogRow struct {
	rows *sql.Rows
	rank *int
}

type redirectTrace struct {
	source   []string
	fallback string
}

type scored struct {
	sub   assrtSub
	score int
}

type scraperConfig struct {
	fileScope          *scraperFileScope
	itemID             string
	issueIDs           []string
	manualRecognition  *MediaRecognition
	taskID             string
	Enabled            bool
	ChineseMetadata    bool
	OriginalPosters    bool
	MonitorEnabled     bool
	MonitorAutoRefresh bool
	ManualScopes       []scraperScope
	MonitorScopes      []scraperScope
	Scraper            string
	Scrapers           []string
	FanartAPIKey       string
	Overwrite          bool
	Categories         map[string]scraperCategory
	// Media workers per task; API request rate is configured separately.
	Concurrency int
}

type scraperDiscovery struct {
	scope          scraperScope
	libraryType    string
	signature      string
	stableSince    time.Time
	discovered     time.Time
	nextAttempt    time.Time
	failedAttempts int
	busy           bool
}

type scraperLocalizedData struct {
	tmdbData
	OriginalLanguage string `json:"original_language"`
}

type scraperMonitorResult struct {
	key            string
	signature      string
	configKey      string
	failedAttempts int
	retry          bool
}

type scraperState struct {
	mu         sync.Mutex
	pause      chan struct{}
	taskID     atomic.Pointer[string]
	plan       *scraperPlan
	running    bool
	automatic  bool
	planning   bool
	planError  string
	cancel     func()
	eventMu    sync.Mutex
	suppressed map[string]scraperSuppression
}

type scraperSuppression struct {
	until time.Time
	stamp fs.FileInfo
}

type sourceRedirectResult struct {
	status      int
	location    string
	contentType string
}

type subtitleFlight struct {
	done   chan struct{}
	cancel func()
}

type subtitleState struct {
	storage  sync.Mutex
	matches  map[string][]subtitleMatch
	mu       sync.Mutex
	flights  map[string]*subtitleFlight
	retry    map[string]time.Time
	sessions map[string]map[string]time.Time
	nextAPI  time.Time
	client   *http.Client
}

type telegramState struct {
	mu       sync.Mutex
	configMu sync.Mutex
	running  bool
	wake     chan struct{}
	debounce int64
	queue    []notificationJob
	sessions map[string]time.Time
	client   *http.Client
}

type tmdbState struct {
	once        sync.Once
	gate        chan struct{}
	rate        sync.Mutex
	next        time.Time
	requests    sync.Map
	requestMu   sync.Mutex
	requestRefs map[string]int
}

type xiaoyaFastFlight struct {
	done   chan struct{}
	result xiaoyaFastResult
}

type xiaoyaFastResult struct {
	location string
	until    time.Time
}

type xiaoyaFastState struct {
	mu      sync.Mutex
	cache   map[string]xiaoyaFastResult
	flights map[string]*xiaoyaFastFlight
}

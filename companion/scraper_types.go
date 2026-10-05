package main

import (
	"io/fs"
	"sync"
	"sync/atomic"
	"time"
)

type scraperCategory struct {
	Enabled bool
	Content []string
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

type scraperTarget struct {
	Content string
	Path    string
	Action  string
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

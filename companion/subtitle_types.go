package main

import (
	"net/http"
	"sync"
	"time"
)

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

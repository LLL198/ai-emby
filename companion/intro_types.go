package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

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
	ring           [introSessionCapacity]introSessionSlot
	next           uint64
	groups         map[string]*introGroup
	markers        map[string]introMarker
	markerOverflow bool
	queue          chan introCandidate
	recordQueue    chan introRecordWrite
	generation     uint64
}

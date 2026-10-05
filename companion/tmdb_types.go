package main

import (
	"sync"
	"time"
)

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

type tmdbState struct {
	once        sync.Once
	gate        chan struct{}
	rate        sync.Mutex
	next        time.Time
	requests    sync.Map
	requestMu   sync.Mutex
	requestRefs map[string]int
}

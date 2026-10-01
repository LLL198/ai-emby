package main

import "net/http"

func (a *App) scanControlAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "PUT" {
		fail(w, 405, "GET or PUT required")
		return
	}
	var b struct{ Paused *bool }
	if r.Method == "PUT" && (!body(w, r, &b) || b.Paused == nil) {
		return
	}
	a.scanControlMu.Lock()
	if b.Paused != nil {
		if *b.Paused && a.scanResume == nil {
			a.scanResume = make(chan struct{})
		}
		if !*b.Paused && a.scanResume != nil {
			close(a.scanResume)
			a.scanResume = nil
		}
	}
	paused := a.scanResume != nil
	a.scanControlMu.Unlock()
	respond(w, M{"Paused": paused})
}
func (a *App) waitScan(job string) {
	a.scanControlMu.Lock()
	ch := a.scanResume
	a.scanControlMu.Unlock()
	if ch == nil {
		return
	}
	previous := "running"
	a.changeActivity(job, func(v *activityEntry) { previous = v.State; v.State = "paused" })
	<-ch
	a.changeActivity(job, func(v *activityEntry) { v.State = previous })
}

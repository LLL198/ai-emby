package main

import (
	"log"
	"net"
	"net/http"
	"strings"
)

type errorResponse struct {
	http.ResponseWriter
	status int
	detail strings.Builder
	wrote  bool
}

func (w *errorResponse) WriteHeader(s int) {
	if !w.wrote {
		w.status = s
		w.wrote = true
		w.ResponseWriter.WriteHeader(s)
	}
}
func (w *errorResponse) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(200)
	}
	if w.status >= 400 && w.detail.Len() < 2048 {
		n := 2048 - w.detail.Len()
		if n > len(b) {
			n = len(b)
		}
		w.detail.Write(b[:n])
	}
	return w.ResponseWriter.Write(b)
}
func (w *errorResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (a *App) recordError(r *http.Request, name, detail string) {
	key := a.newActivity("error", "", name)
	a.changeActivity(key, func(v *activityEntry) {
		v.State = "error"
		v.Error = detail
		if r != nil {
			v.IP, _, _ = net.SplitHostPort(r.RemoteAddr)
			v.Client = r.UserAgent()
			v.Current = r.Method + " " + r.URL.Path
		}
	})
	log.Printf("%s: %s", name, detail)
}

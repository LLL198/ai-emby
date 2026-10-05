package main

import "net/http"

type clientResponse struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *clientResponse) WriteHeader(s int) {
	if !w.wrote {
		w.status = s
		w.wrote = true
		w.ResponseWriter.WriteHeader(s)
	}
}
func (w *clientResponse) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *clientResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

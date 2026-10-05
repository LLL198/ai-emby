package main

import "net/http"

// A public request must never borrow the worker's loopback-only task authority.
// This is enforced independently of the optional playback protection setting.
func rejectCloudInternalCredentials(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	_, expiry := q["internal_expires"]
	_, token := q["internal_token"]
	if expiry || token {
		w.Header().Set("Cache-Control", "no-store")
		sessionError(w, http.StatusForbidden, "内部媒体任务凭据不能用于外部播放")
		return true
	}
	return false
}

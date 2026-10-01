package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path"
	"strings"
	"time"
)

// Reconstructed from the exact executable's Ghidra output at
// ../pseudocode/a0/008669a0_main.__App_.webAssetRoute.c and checked against
// the live /web/js/app.js response. The original source is unavailable.
const recoveredBuildVersion = "2026.09.28-124350"

func (a *App) webAssetRoute(w http.ResponseWriter, r *http.Request) bool {
	name := r.URL.Path
	css := strings.HasPrefix(name, "/web/css/")
	js := strings.HasPrefix(name, "/web/js/")
	if !css && !js {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(w, http.StatusMethodNotAllowed, "GET or HEAD required")
		return true
	}
	if path.Clean(name) != name || strings.ContainsRune(name, '\x00') {
		http.NotFound(w, r)
		return true
	}

	contentType := ""
	if css && strings.HasSuffix(name, ".css") {
		contentType = "text/css; charset=utf-8"
	} else if js && strings.HasSuffix(name, ".js") {
		contentType = "text/javascript; charset=utf-8"
	}
	if contentType == "" {
		http.NotFound(w, r)
		return true
	}

	data, err := assets.ReadFile(strings.TrimPrefix(name, "/"))
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	if name == "/web/js/app.js" {
		version, _ := json.Marshal(recoveredBuildVersion)
		data = bytes.ReplaceAll(data, []byte("__GO_EMBY_BUILD_VERSION__"), version)
	}

	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(data))
	return true
}

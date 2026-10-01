package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const coreURL = "http://127.0.0.1:18097"
const scraperURL = "http://127.0.0.1:18098"

// Serve frontend assets while API requests follow their configured service routes.
func serveFrontend(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	path := r.URL.Path
	if path == "/" || path == "/index.html" || path == "/web/index.html" {
		path = "/index.html"
	} else if !strings.HasPrefix(path, "/web/") {
		return false
	}
	root := os.Getenv("FRONTEND_ROOT")
	if root == "" {
		root = "/app/frontend"
	}
	clean := strings.TrimPrefix(filepath.Clean("/"+path), "/")
	file, err := os.Open(filepath.Join(root, clean))
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if path == "/web/js/app.js" {
		data, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "frontend unavailable", 500)
			return true
		}
		version, _ := json.Marshal(currentVersion())
		data = bytes.ReplaceAll(data, []byte(`"__AI_EMBY_VERSION__"`), version)
		http.ServeContent(w, r, info.Name(), info.ModTime(), bytes.NewReader(data))
		return true
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	return true
}

func start(path, listen string, scraper bool) *exec.Cmd {
	command := exec.Command(path)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "LISTEN=") && !strings.HasPrefix(value, "PORT=") && !strings.HasPrefix(value, "SCRAPER_SERVICE_ONLY=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "LISTEN="+listen)
	if scraper {
		command.Env = append(command.Env, "SCRAPER_SERVICE_ONLY=1")
	} else {
		// The media service reads its listening port from PORT.
		command.Env = append(command.Env, "PORT=18097")
	}
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		log.Fatal(err)
	}
	return command
}

func get(r *http.Request, base, path string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header = r.Header.Clone()
	if request.Header.Get("X-Emby-Token") == "" {
		request.Header.Set("X-Emby-Token", featureGatewayToken(r))
	}
	return (&http.Client{Timeout: 15 * time.Second}).Do(request)
}

func relay(w http.ResponseWriter, response *http.Response) {
	defer response.Body.Close()
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	io.Copy(w, response.Body)
}

func main() {
	sessionDB, err := openSessionDatabase()
	if err != nil {
		log.Print("persistent device login database unavailable")
	} else {
		defer sessionDB.Close()
	}
	core := start("/usr/local/bin/ai-emby-core", "127.0.0.1:18097", false)
	companion := start("/usr/local/bin/ai-emby-worker", "127.0.0.1:18098", true)
	coreTarget, _ := url.Parse(coreURL)
	scraperTarget, _ := url.Parse(scraperURL)
	coreProxy := httputil.NewSingleHostReverseProxy(coreTarget)
	coreProxy.ModifyResponse = func(response *http.Response) error { return featureFilterResponse(sessionDB, response) }
	scraperProxy := httputil.NewSingleHostReverseProxy(scraperTarget)
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = ":8097"
	}
	server := &http.Server{Addr: listen, ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = &featureNetworkResponse{ResponseWriter: w}
		path := r.URL.Path
		if featureNetworkGate(sessionDB, w, r) {
			return
		}
		if serveReleaseUpdates(sessionDB, w, r) {
			return
		}
		if path == "/web/session/persist" {
			persistDeviceSession(sessionDB, w, r)
			return
		}
		if serveFrontend(w, r) {
			return
		}
		if serveFeatureRoutes(sessionDB, w, r, scraperProxy) {
			return
		}
		if featureEnforceAccess(sessionDB, w, r) {
			return
		}
		if serveSTRMHead(sessionDB, w, r) {
			return
		}
		if serveFeatureImage(sessionDB, w, r) || serveFeatureLocalStream(sessionDB, w, r) {
			return
		}
		if serveFeaturePlaybackEvent(w, r, coreProxy) {
			return
		}
		if serveScanRoutes(w, r, scraperProxy) {
			return
		}
		if path == "/health" {
			for _, base := range []string{coreURL, scraperURL} {
				response, err := get(r, base, "/health")
				if err != nil {
					http.Error(w, "service unavailable", 503)
					return
				}
				response.Body.Close()
				if response.StatusCode != 200 {
					http.Error(w, "service unavailable", 503)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"status":"ok","scraperConcurrency":true,"scanConcurrency":true}`)
			return
		}
		if path == "/web/js/scraper-admin.js" {
			scraperProxy.ServeHTTP(w, r)
			return
		}
		isScraper := path == "/admin/scraper" || strings.HasPrefix(path, "/admin/scraper/")
		isLogs := path == "/admin/logs" && r.Method == http.MethodGet && r.URL.Query().Get("category") != "proxy"
		if !isScraper && !isLogs {
			coreProxy.ServeHTTP(w, r)
			return
		}
		// Authenticate administrator requests before forwarding them to the worker.
		authorized, err := get(r, coreURL, "/admin/scraper")
		if err != nil {
			http.Error(w, "core service unavailable", 502)
			return
		}
		if authorized.StatusCode != 200 {
			relay(w, authorized)
			return
		}
		var coreState struct {
			Running, Planning bool
			Settings          struct{ MonitorEnabled bool }
		}
		err = json.NewDecoder(io.LimitReader(authorized.Body, 2<<20)).Decode(&coreState)
		authorized.Body.Close()
		if err != nil {
			http.Error(w, "invalid core scraper state", 502)
			return
		}
		if isScraper {
			if r.Method == http.MethodPost && (path == "/admin/scraper/plan" || path == "/admin/scraper/start") {
				if coreState.Running || coreState.Planning {
					http.Error(w, "核心服务刮削任务运行中，请等待完成", 409)
					return
				}
				// Automatic work still belongs to the core process. Block a
				// manual companion task if that watcher could start overlapping work.
				if coreState.Settings.MonitorEnabled {
					http.Error(w, "请先关闭实时刮削监控，再启动手动并发任务", 409)
					return
				}
			}
			scraperProxy.ServeHTTP(w, r)
			return
		}
		responses := make([]map[string]json.RawMessage, 2)
		for i, base := range []string{coreURL, scraperURL} {
			response, err := get(r, base, r.URL.RequestURI())
			if err != nil {
				http.Error(w, "log service unavailable", 502)
				return
			}
			if response.StatusCode != 200 {
				relay(w, response)
				return
			}
			err = json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&responses[i])
			response.Body.Close()
			if err != nil {
				http.Error(w, "invalid log response", 502)
				return
			}
		}
		entries := make([]map[string]json.RawMessage, 0)
		for _, response := range responses {
			var values []map[string]json.RawMessage
			json.Unmarshal(response["Entries"], &values)
			entries = append(entries, values...)
		}
		sort.SliceStable(entries, func(i, j int) bool { return string(entries[i]["Updated"]) > string(entries[j]["Updated"]) })
		if len(entries) > 1000 {
			entries = entries[:1000]
		}
		encoded, _ := json.Marshal(entries)
		responses[0]["Entries"] = encoded
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(responses[0])
	})}
	exited := make(chan error, 2)
	go func() { exited <- core.Wait() }()
	go func() { exited <- companion.Wait() }()
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			exited <- err
		}
	}()
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-stopping:
	case err := <-exited:
		log.Printf("child service exited: %v", err)
	}
	server.Close()
	for _, command := range []*exec.Cmd{core, companion} {
		command.Process.Signal(syscall.SIGTERM)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-exited:
		case <-time.After(35 * time.Second):
			for _, command := range []*exec.Cmd{core, companion} {
				command.Process.Kill()
			}
			fmt.Fprintln(os.Stderr, "forced child shutdown")
			return
		}
	}
}

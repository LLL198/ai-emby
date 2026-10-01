package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Serve scraper and scan requests on the internal port after gateway authorization.
func (a *App) runScraperService() {
	must(a.initConcurrentScanner())
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = "127.0.0.1:18098"
	}
	server := &http.Server{Addr: listen, ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			var count int
			if a.db.QueryRow("SELECT 1").Scan(&count) != nil {
				fail(w, 503, "database unavailable")
				return
			}
			respond(w, M{"status": "ok", "service": "manual-scraper"})
			return
		}
		if r.URL.Path == "/web/js/scraper-admin.js" {
			a.webAssetRoute(w, r)
			return
		}
		user, err := a.auth(r)
		if err != nil {
			fail(w, 401, "unauthorized")
			return
		}
		if !user.Admin {
			fail(w, 403, "administrator required")
			return
		}
		switch r.URL.Path {
		case "/admin/scraper", "/admin/scraper/plan", "/admin/scraper/start", "/admin/scraper/cancel", "/admin/scraper/control", "/admin/scraper/tree":
			a.scraperAdmin(w, r, r.URL.Path)
		case "/admin/logs":
			a.activitySnapshot(w, r)
		case "/admin/scan", "/admin/scan-all":
			a.concurrentScanAPI(w, r)
		case "/admin/scan-settings":
			a.scanSettingsAPI(w, r)
		case "/admin/scan/status":
			a.concurrentScanStatus(w, r)
		case "/admin/scan-control":
			a.scanControlAPI(w, r)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() {
		log.Printf("manual scraper companion listening %s", listen)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGTERM, syscall.SIGINT)
	<-stopping
	a.scraper.mu.Lock()
	if a.scraper.cancel != nil {
		a.scraper.cancel()
	}
	a.scraper.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server.Shutdown(ctx)
	if err := a.stopConcurrentScanner(ctx); err != nil {
		log.Print(err)
	}
	a.db.Close()
}

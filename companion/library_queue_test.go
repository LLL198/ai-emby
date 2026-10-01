package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLibraryQueueLimits(t *testing.T) {
	a := testApp(t)
	if a.jobLimit(false) != 2 || a.jobLimit(true) != 2 || a.displayName() != "go-emby" {
		t.Fatal("incorrect defaults")
	}
	r1 := a.acquireLibraryJob("a", false)
	r2 := a.acquireLibraryJob("b", false)
	u1 := a.acquireLibraryJob("c", true)
	u2 := a.acquireLibraryJob("d", true)
	next := make(chan func(), 1)
	go func() { next <- a.acquireLibraryJob("e", false) }()
	select {
	case <-next:
		t.Fatal("third scan ran")
	case <-time.After(50 * time.Millisecond):
	}
	r1()
	select {
	case release := <-next:
		release()
	case <-time.After(time.Second):
		t.Fatal("queue stalled")
	}
	same := make(chan func(), 1)
	go func() { same <- a.acquireLibraryJob("b", true) }()
	u1()
	select {
	case <-same:
		t.Fatal("same library overlapped")
	case <-time.After(50 * time.Millisecond):
	}
	r2()
	select {
	case release := <-same:
		release()
	case <-time.After(time.Second):
		t.Fatal("library remained blocked")
	}
	u2()
}

func TestQueueSettingsAndKeyMask(t *testing.T) {
	a := testApp(t)
	w := httptest.NewRecorder()
	a.enhancementSettings(w, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(`{"ScanConcurrency":3,"UpdateConcurrency":4,"ServerName":"我的影库"}`)))
	if w.Code != 200 || a.jobLimit(false) != 3 || a.jobLimit(true) != 4 || a.displayName() != "我的影库" {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	a.enhancementSettings(w, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(`{"ScanConcurrency":0}`)))
	if w.Code != 400 {
		t.Fatal("invalid limit accepted")
	}
	w = httptest.NewRecorder()
	a.admin(w, httptest.NewRequest("POST", "/admin/keys", strings.NewReader(`{"Name":"test"}`)), User{Admin: true}, "/admin/keys")
	var result map[string]string
	json.Unmarshal(w.Body.Bytes(), &result)
	secret := result["Token"]
	if w.Code != 200 || len(secret) != 64 {
		t.Fatal("key creation failed")
	}
	w = httptest.NewRecorder()
	a.admin(w, httptest.NewRequest("GET", "/admin/keys", nil), User{Admin: true}, "/admin/keys")
	if strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), secret[:6]) || !strings.Contains(w.Body.String(), secret[60:]) {
		t.Fatal("incorrect masked response")
	}
	var stored string
	a.db.QueryRow("SELECT hash FROM api_keys WHERE name='test'").Scan(&stored)
	if stored != digest(secret) {
		t.Fatal("key must be hashed")
	}
}

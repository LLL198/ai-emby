package licensesdk

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLeaseAndRevocation(t *testing.T) {
	body := `{"valid":true,"mode":"licensed","lease_seconds":120}`
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer s.Close()
	c := &Client{http: s.Client(), url: s.URL, device: "test"}
	c.Refresh(context.Background())
	if ok, _, _ := c.Status(); !ok {
		t.Fatal("expected valid")
	}
	s.Close()
	c.Refresh(context.Background())
	if ok, _, _ := c.Status(); !ok {
		t.Fatal("short outage should retain lease")
	}
	c.until = time.Now().Add(-time.Second)
	if ok, _, _ := c.Status(); ok {
		t.Fatal("expired lease accepted")
	}
	s2 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"valid":false,"reason":"revoked"}`)) }))
	defer s2.Close()
	c.http = s2.Client()
	c.url = s2.URL
	c.until = time.Now().Add(time.Minute)
	c.Refresh(context.Background())
	if ok, _, why := c.Status(); ok || why != "revoked" {
		t.Fatal("revocation must cancel lease")
	}
}
func TestMiddleware(t *testing.T) {
	c := &Client{reason: "invalid_license"}
	h := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for path, expected := range map[string]int{"/Items": 402, "/health": 204, "/license/status": 200, "/emby/Items": 402, "/web/index.html": 402} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != expected {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	c.until = time.Now().Add(time.Minute)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/Items", nil))
	if w.Code != 204 {
		t.Fatal("valid license blocked")
	}
}
func TestRejectMalformedAndOversizeLease(t *testing.T) {
	for _, body := range []string{`{"valid":true,"lease_seconds":99999}`, `{"valid":true,"lease_seconds":0}`, `broken`} {
		s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		c := &Client{http: s.Client(), url: s.URL}
		c.Refresh(context.Background())
		if ok, _, _ := c.Status(); ok {
			t.Fatal("invalid response accepted")
		}
		s.Close()
	}
}

func TestFreeModeEmptyCodeAndLongLease(t *testing.T) {
	free := true
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["license"] != "" || payload["lease_seconds"] != float64(MaxLeaseSeconds) {
			t.Errorf("unexpected payload: %#v", payload)
		}
		if free {
			w.Write([]byte(`{"valid":true,"mode":"free","lease_seconds":43260}`))
		} else {
			w.Write([]byte(`{"valid":false,"reason":"invalid_license"}`))
		}
	}))
	defer s.Close()
	c := &Client{http: s.Client(), url: s.URL}
	c.Refresh(context.Background())
	if ok, mode, _ := c.Status(); !ok || mode != "free" {
		t.Fatal("empty code rejected in free mode")
	}
	if time.Until(c.until) < RefreshInterval {
		t.Fatal("lease expires before scheduled verification")
	}
	free = false
	c.Refresh(context.Background())
	if ok, _, _ := c.Status(); ok {
		t.Fatal("empty code accepted after free mode disabled")
	}
}

func TestEnvironmentConfiguration(t *testing.T) {
	machine := filepath.Join(t.TempDir(), "machine-id")
	if err := os.WriteFile(machine, []byte("0123456789abcdef0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LICENSE_MACHINE_ID_FILE", machine)
	t.Setenv("LICENSE_CA_FILE", "")
	t.Setenv("LICENSE_KEY", "test-only-license")
	for _, endpoint := range []string{"http://license.example", "https://", "https://license.example?secret=value"} {
		t.Setenv("LICENSE_SERVER_URL", endpoint)
		if _, err := NewFromEnv(); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
	t.Setenv("LICENSE_SERVER_URL", "")
	defaultClient, err := NewFromEnv()
	if err != nil || defaultClient.url != DefaultServer {
		t.Fatal("default authorization endpoint not configured")
	}
	credentialsURL := &url.URL{Scheme: "https", Host: "license.example", User: url.UserPassword("user", "pass")}
	t.Setenv("LICENSE_SERVER_URL", credentialsURL.String())
	if _, err := NewFromEnv(); err == nil {
		t.Fatal("URL credentials accepted")
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/check" {
			t.Error("unexpected license path")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["license"] != "test-only-license" || len(body["device"].(string)) != 64 {
			t.Error("invalid authorization payload")
		}
		w.Write([]byte(`{"valid":true,"lease_seconds":60}`))
	}))
	defer srv.Close()
	t.Setenv("LICENSE_SERVER_URL", srv.URL)
	client, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	client.Refresh(context.Background())
	if ok, _, _ := client.Status(); ok {
		t.Fatal("untrusted TLS certificate accepted")
	}
	ca := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LICENSE_CA_FILE", ca)
	client, err = NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	client.Refresh(context.Background())
	if ok, _, _ := client.Status(); !ok {
		t.Fatal("configured CA was not trusted")
	}
	if err := os.WriteFile(ca, []byte("invalid certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFromEnv(); err == nil {
		t.Fatal("invalid CA accepted")
	}
}

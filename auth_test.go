package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testAuth(t *testing.T) *authStore {
	t.Helper()
	a, err := openAuth(filepath.Join(t.TempDir(), "auth.db"), 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })
	if err = a.createUser("reader", "test-only-password-42"); err != nil {
		t.Fatal(err)
	}
	return a
}
func call(a *authStore, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://viewer.test"+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.10:1234"
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}
func signedIn(t *testing.T, a *authStore) *http.Cookie {
	t.Helper()
	w := call(a, "POST", "/api/auth/login", `{"username":"reader","password":"test-only-password-42"}`, nil)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing cookie")
	}
	return cookies[0]
}
func TestAuthLifecycle(t *testing.T) {
	a := testAuth(t)
	for _, p := range []string{"/files", "/file?path=/secret.md", "/stat", "/forward?path=/secret.png", "/api/auth/me"} {
		if w := call(a, "GET", p, "", nil); w.Code != 401 {
			t.Fatalf("%s %d", p, w.Code)
		}
	}
	if w := call(a, "GET", "/private.md", "", nil); w.Code != 303 {
		t.Fatal(w.Code)
	}
	if w := call(a, "GET", "/login", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "login-form") {
		t.Fatal("login page")
	}
	for _, body := range []string{`{"username":"reader","password":"wrong"}`, `{"username":"missing","password":"wrong"}`, `{"username":"' OR 1=1 --","password":"wrong"}`} {
		w := call(a, "POST", "/api/auth/login", body, nil)
		if w.Code != 401 || len(w.Result().Cookies()) != 0 {
			t.Fatal("bad login accepted")
		}
	}
	cookie := signedIn(t, a)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 28800 || len(cookie.Value) != 43 {
		t.Fatal("cookie contract")
	}
	if w := call(a, "GET", "/api/auth/me", "", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	var hash string
	a.db.QueryRow("SELECT password_hash FROM users").Scan(&hash)
	if strings.Contains(hash, "test-only") || !strings.HasPrefix(hash, "$2a$") {
		t.Fatal("plaintext password")
	}
	var stored string
	a.db.QueryRow("SELECT token_hash FROM sessions").Scan(&stored)
	if stored == cookie.Value || stored != tokenDigest(cookie.Value) {
		t.Fatal("plaintext token")
	}
	if w := call(a, "POST", "/api/auth/logout", "", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := call(a, "GET", "/api/auth/me", "", cookie); w.Code != 401 {
		t.Fatal("logout not revoked")
	}
}
func TestExpiryAndRotation(t *testing.T) {
	a := testAuth(t)
	now := time.Date(2026, 9, 17, 0, 0, 0, 123000000, time.UTC)
	a.now = func() time.Time { return now }
	first := signedIn(t, a)
	w := call(a, "POST", "/api/auth/login", `{"username":"reader","password":"test-only-password-42"}`, first)
	second := w.Result().Cookies()[0]
	if call(a, "GET", "/api/auth/me", "", first).Code != 401 {
		t.Fatal("old token not rotated")
	}
	now = now.Add(8*time.Hour - time.Millisecond)
	if call(a, "GET", "/api/auth/me", "", second).Code != 200 {
		t.Fatal("expired early")
	}
	now = now.Add(time.Millisecond)
	if call(a, "GET", "/api/auth/me", "", second).Code != 401 {
		t.Fatal("expiry boundary failed")
	}
}
func TestPersistenceAndDisable(t *testing.T) {
	a := testAuth(t)
	cookie := signedIn(t, a)
	a.db.Close()
	b, err := openAuth(a.dbPath, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer b.db.Close()
	if call(b, "GET", "/api/auth/me", "", cookie).Code != 200 {
		t.Fatal("session lost across restart")
	}
	b.db.Exec("UPDATE users SET disabled=1")
	if call(b, "GET", "/api/auth/me", "", cookie).Code != 401 {
		t.Fatal("disabled user retained access")
	}
}
func TestOriginAndRateLimit(t *testing.T) {
	a := testAuth(t)
	r := httptest.NewRequest("POST", "http://viewer.test/api/auth/login", bytes.NewBufferString(`{}`))
	r.Header.Set("Origin", "https://attacker.test")
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin login")
	}
	for i := 0; i < 10; i++ {
		if call(a, "POST", "/api/auth/login", "bad json", nil).Code != 400 {
			t.Fatal("rate early")
		}
	}
	if call(a, "POST", "/api/auth/login", "bad json", nil).Code != 429 {
		t.Fatal("rate limit missing")
	}
	a.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if call(a, "POST", "/api/auth/login", "bad json", nil).Code != 400 {
		t.Fatal("rate limit stuck")
	}
}
func TestSQLiteAndIntegrity(t *testing.T) {
	a := testAuth(t)
	var version string
	if err := a.db.QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "3.53.4" {
		t.Fatalf("expected SQLite 3.53.4, got %s", version)
	}
	if err := a.createUser("READER", "test-only-password-42"); err == nil {
		t.Fatal("duplicate user accepted")
	}
	for _, table := range []string{"users", "sessions"} {
		rows, err := a.db.Query("PRAGMA foreign_key_list(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		exists := rows.Next()
		rows.Close()
		if exists {
			t.Fatal("foreign key")
		}
	}
	var utc, shanghai string
	var ms int64
	if err := a.db.QueryRow("SELECT created_at_utc_iso,created_at_shanghai_iso,created_at_ms FROM users_operations").Scan(&utc, &shanghai, &ms); err != nil {
		t.Fatal(err)
	}
	u, err := time.Parse(time.RFC3339Nano, utc)
	if err != nil {
		t.Fatal(err)
	}
	s, err := time.Parse(time.RFC3339Nano, shanghai)
	if err != nil {
		t.Fatal(err)
	}
	if u.UnixMilli() != ms || !u.Equal(s) {
		t.Fatal("time projection drift")
	}
	if err := validateDocumentRoot(filepath.Dir(a.dbPath), a.dbPath); err == nil {
		t.Fatal("database readable from document root")
	}
	if err := validateDocumentRoot(t.TempDir(), a.dbPath); err != nil {
		t.Fatal(err)
	}
	var plan string
	var x, y, z int
	if err := a.db.QueryRow("EXPLAIN QUERY PLAN SELECT token_hash FROM sessions WHERE expires_at_ms<=? ORDER BY expires_at_ms LIMIT 1000", time.Now().UnixMilli()).Scan(&x, &y, &z, &plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "sessions_expiry") {
		t.Fatal(plan)
	}
	t.Logf("SQLite %s; expiry query %s", version, plan)
}
func TestMethodsAndBounds(t *testing.T) {
	a := testAuth(t)
	if call(a, "GET", "/api/auth/login", "", nil).Code != 405 {
		t.Fatal("method")
	}
	if call(a, "POST", "/api/auth/login", strings.Repeat("a", 3000), nil).Code != 400 {
		t.Fatal("body size")
	}
	a.secure = true
	c := signedIn(t, a)
	if !c.Secure {
		t.Fatal("secure cookie")
	}
	if w := call(a, "GET", "/api/auth/me", "", c); w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cache")
	}
}
func TestConcurrentSessions(t *testing.T) {
	a := testAuth(t)
	c := signedIn(t, a)
	errs := make(chan string, 20)
	start := time.Now()
	for i := 0; i < 20; i++ {
		go func() {
			w := call(a, "GET", "/api/auth/me", "", c)
			if w.Code != 200 {
				errs <- fmt.Sprint(w.Code)
				return
			}
			var d map[string]interface{}
			if json.Unmarshal(w.Body.Bytes(), &d) != nil {
				errs <- "JSON"
				return
			}
			errs <- ""
		}()
	}
	for i := 0; i < 20; i++ {
		if e := <-errs; e != "" {
			t.Fatal(e)
		}
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("20 local session reads exceed 5s")
	}
	t.Logf("20 concurrent authenticated reads: %s", time.Since(start))
}

func TestPasswordResetRevokesSessions(t *testing.T) {
	a := testAuth(t)
	cookie := signedIn(t, a)
	f, err := os.CreateTemp(t.TempDir(), "password")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString("replacement-test-password\n")
	f.Seek(0, 0)
	previous := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = previous }()
	if err = a.resetPasswordFromStdin("reader"); err != nil {
		t.Fatal(err)
	}
	if call(a, "GET", "/api/auth/me", "", cookie).Code != 401 {
		t.Fatal("old session survived password reset")
	}
	if call(a, "POST", "/api/auth/login", `{"username":"reader","password":"test-only-password-42"}`, nil).Code != 401 {
		t.Fatal("old password survived")
	}
	if call(a, "POST", "/api/auth/login", `{"username":"reader","password":"replacement-test-password"}`, nil).Code != 200 {
		t.Fatal("new password rejected")
	}
}

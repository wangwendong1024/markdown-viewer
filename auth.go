package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

//go:embed auth_schema.sql
var authSchema string

//go:embed ui/login.html
var loginPage string

const sessionCookie = "markdown_session"

var errMissingSession = errors.New("missing session")

type loginWindow struct {
	count int
	until time.Time
}
type authStore struct {
	db        *sql.DB
	dbPath    string
	ttl       time.Duration
	secure    bool
	now       func() time.Time
	mu        sync.Mutex
	attempts  map[string]loginWindow
	hashSlots chan struct{}
	dummyHash []byte
}

func openAuthFromEnv() (*authStore, error) {
	p := os.Getenv("AUTH_DB_PATH")
	if p == "" {
		p = "data/auth.db"
	}
	ttl := 8 * time.Hour
	if s := os.Getenv("AUTH_TOKEN_TTL"); s != "" {
		var err error
		ttl, err = time.ParseDuration(s)
		if err != nil || ttl < time.Minute || ttl > 30*24*time.Hour {
			return nil, errors.New("AUTH_TOKEN_TTL must be between 1m and 720h")
		}
	}
	secure := false
	if s := os.Getenv("AUTH_COOKIE_SECURE"); s != "" {
		var err error
		secure, err = strconv.ParseBool(s)
		if err != nil {
			return nil, errors.New("invalid AUTH_COOKIE_SECURE")
		}
	}
	a, err := openAuth(p, ttl)
	if err == nil {
		a.secure = secure
	}
	return a, err
}

func openAuth(p string, ttl time.Duration) (*authStore, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;" + authSchema); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(abs, 0600); err != nil {
		db.Close()
		return nil, err
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte("non-user-placeholder-password"), 12)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &authStore{db: db, dbPath: abs, ttl: ttl, now: time.Now, attempts: map[string]loginWindow{}, hashSlots: make(chan struct{}, 4), dummyHash: dummy}, nil
}

func validateDocumentRoot(root, dbPath string) error {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	d, err := filepath.EvalSymlinks(dbPath)
	if err != nil {
		return err
	}
	r, _ = filepath.Abs(r)
	rel, err := filepath.Rel(r, d)
	if err != nil {
		return err
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("AUTH_DB_PATH must be outside the document directory")
	}
	return nil
}

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{2,63}$`)

func (a *authStore) createUser(username, password string) error {
	if !usernamePattern.MatchString(username) {
		return errors.New("username must contain 3-64 ASCII letters, numbers, dots, underscores or hyphens")
	}
	if len(password) < 12 || len(password) > 72 {
		return errors.New("password must contain 12-72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	_, err = a.db.Exec("INSERT INTO users(username,password_hash,created_at_ms) VALUES(?,?,?)", strings.ToLower(username), string(hash), a.now().UnixMilli())
	return err
}

func (a *authStore) createUserFromStdin(username string) error {
	password, err := io.ReadAll(io.LimitReader(os.Stdin, 74))
	if err != nil {
		return err
	}
	return a.createUser(username, strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"))
}

func (a *authStore) resetPasswordFromStdin(username string) error {
	password, err := io.ReadAll(io.LimitReader(os.Stdin, 74))
	if err != nil {
		return err
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r")
	if len(value) < 12 || len(value) > 72 {
		return errors.New("password must contain 12-72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(value), 12)
	if err != nil {
		return err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE users SET password_hash=? WHERE username=?", string(hash), strings.ToLower(username))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("user not found")
	}
	if _, err = tx.Exec("DELETE FROM sessions WHERE user_id=(SELECT id FROM users WHERE username=?)", strings.ToLower(username)); err != nil {
		return err
	}
	return tx.Commit()
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func respond(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}

// The client cannot choose a forwarded origin. Configure the reverse proxy to
// preserve Host; only enable secure cookies when the public site uses HTTPS.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	} // Non-browser API clients.
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
}

func (a *authStore) allowLogin(r *http.Request) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, v := range a.attempts {
		if !now.Before(v.until) {
			delete(a.attempts, key)
		}
	}
	v, exists := a.attempts[ip]
	if !exists {
		if len(a.attempts) >= 4096 {
			return false
		}
		v.until = now.Add(5 * time.Minute)
	}
	if v.count >= 10 {
		return false
	}
	v.count++
	a.attempts[ip] = v
	return true
}

func (a *authStore) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		fail(w, 405, "请使用 POST")
		return
	}
	if !sameOrigin(r) {
		fail(w, 403, "请求来源不匹配")
		return
	}
	if !a.allowLogin(r) {
		w.Header().Set("Retry-After", "300")
		fail(w, 429, "尝试次数过多，请五分钟后再试")
		return
	}
	select {
	case a.hashSlots <- struct{}{}:
		defer func() { <-a.hashSlots }()
	default:
		fail(w, 429, "登录繁忙，请稍后再试")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "登录参数格式不正确")
		return
	}
	var id int64
	var username, hash string
	err := a.db.QueryRow("SELECT id,username,password_hash FROM users WHERE username=? AND disabled=0", strings.ToLower(strings.TrimSpace(input.Username))).Scan(&id, &username, &hash)
	if err != nil && err != sql.ErrNoRows {
		fail(w, 503, "暂时无法登录，请稍后重试")
		return
	}
	check := []byte(hash)
	if err == sql.ErrNoRows {
		check = a.dummyHash
	}
	valid := bcrypt.CompareHashAndPassword(check, []byte(input.Password)) == nil
	if err != nil || !valid {
		fail(w, 401, "用户名或密码错误")
		return
	}
	bytes := make([]byte, 32)
	if _, err = rand.Read(bytes); err != nil {
		fail(w, 500, "暂时无法登录")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	now := a.now()
	expires := now.Add(a.ttl)
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 503, "暂时无法登录")
		return
	}
	defer tx.Rollback()
	// Fixed-size cleanup keeps expired sessions bounded without scanning all rows.
	_, err = tx.Exec("DELETE FROM sessions WHERE token_hash IN (SELECT token_hash FROM sessions WHERE expires_at_ms<=? ORDER BY expires_at_ms LIMIT 1000)", now.UnixMilli())
	if err == nil {
		var result sql.Result
		result, err = tx.Exec("INSERT INTO sessions(token_hash,user_id,created_at_ms,expires_at_ms) SELECT ?,id,?,? FROM users WHERE id=? AND disabled=0 AND password_hash=?", tokenDigest(token), now.UnixMilli(), expires.UnixMilli(), id, hash)
		if err == nil {
			var n int64
			n, err = result.RowsAffected()
			if err == nil && n != 1 {
				err = errors.New("account changed during login")
			}
		}
	}
	if old, e := r.Cookie(sessionCookie); err == nil && e == nil {
		_, err = tx.Exec("DELETE FROM sessions WHERE token_hash=?", tokenDigest(old.Value))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 503, "暂时无法登录")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(a.ttl.Seconds())})
	respond(w, 200, map[string]interface{}{"username": username, "expires_at": expires.UTC().Format(time.RFC3339), "expires_in": int(a.ttl.Seconds())})
}

func (a *authStore) session(r *http.Request) (string, time.Time, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || len(c.Value) != 43 {
		return "", time.Time{}, errMissingSession
	}
	var username string
	var expires int64
	err = a.db.QueryRowContext(r.Context(), "SELECT u.username,s.expires_at_ms FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at_ms>? AND u.disabled=0", tokenDigest(c.Value), a.now().UnixMilli()).Scan(&username, &expires)
	return username, time.UnixMilli(expires), err
}

func (a *authStore) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		fail(w, 405, "请使用 POST")
		return
	}
	if !sameOrigin(r) {
		fail(w, 403, "请求来源不匹配")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, err = a.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE token_hash=?", tokenDigest(c.Value)); err != nil {
			fail(w, 503, "退出失败，请重试")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	respond(w, 200, map[string]bool{"ok": true})
}

func (a *authStore) protect(next http.HandlerFunc, page bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			fail(w, 403, "请求来源不匹配")
			return
		}
		_, _, err := a.session(r)
		if err != nil {
			if err != sql.ErrNoRows && !errors.Is(err, errMissingSession) {
				fail(w, 503, "会话服务暂时不可用")
				return
			}
			if page {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			} else {
				fail(w, 401, "登录已过期，请重新登录")
			}
			return
		}
		next(w, r)
	}
}

func (a *authStore) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			fail(w, 405, "请使用 GET")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, loginPage)
	})
	mux.HandleFunc("/api/auth/login", a.login)
	mux.HandleFunc("/api/auth/logout", a.logout)
	mux.HandleFunc("/api/auth/me", a.protect(func(w http.ResponseWriter, r *http.Request) {
		u, e, err := a.session(r)
		if err != nil {
			fail(w, 401, "登录已过期")
			return
		}
		respond(w, 200, map[string]string{"username": u, "expires_at": e.UTC().Format(time.RFC3339)})
	}, false))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.PingContext(r.Context()); err != nil {
			fail(w, 503, "unavailable")
			return
		}
		respond(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/files", a.protect(files, false))
	mux.HandleFunc("/file", a.protect(file, false))
	mux.HandleFunc("/stat", a.protect(stat, false))
	mux.HandleFunc("/forward", a.protect(forward, false))
	mux.HandleFunc("/", a.protect(home, true))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		mux.ServeHTTP(w, r)
	})
}

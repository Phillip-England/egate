package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed EGATE.md
var agentGuide string

type config struct {
	DB, Username, Password, ResendKey, Host, Listen string
	Port, MaxAttempts                               int
}

const defaultPort = 8119

type app struct {
	db   *sql.DB
	cfg  config
	tpl  *template.Template
	http *http.Client
}

type appView struct {
	ID                    int64
	Name, Prefix, Created string
}
type pageData struct {
	Error, Message, NewKey, CSRF string
	BaseURL, EmailEndpoint       string
	Apps                         []appView
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "teach" {
		if len(os.Args) != 4 && len(os.Args) != 6 {
			fmt.Fprintln(os.Stderr, "usage: egate teach HOST_URL SENDER_EMAIL [APP_API_KEY ENV_PATH]")
			os.Exit(2)
		}
		if err := teach(os.Args[2], os.Args[3], os.Args[4:]...); err != nil {
			log.Fatal(err)
		}
		if len(os.Args) == 6 {
			log.Printf("wrote egate integration guide to ./EGATE.md and EGATE_API_KEY to %s", os.Args[5])
		} else {
			log.Printf("wrote egate integration guide to ./EGATE.md")
		}
		return
	}
	flag.Usage = func() { printHelp(flag.CommandLine.Output()) }
	envPath := flag.String("env", "", "optional environment file for local runs")
	initEnvPath := flag.String("init-env", "", "create a starter environment file and exit")
	flag.Parse()
	if flag.NArg() == 1 && flag.Arg(0) == "help" {
		printHelp(os.Stdout)
		return
	}
	if flag.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "egate: unknown command or argument %q\n\n", flag.Arg(0))
		printHelp(os.Stderr)
		os.Exit(2)
	}
	if *initEnvPath != "" {
		if err := initEnv(*initEnvPath); err != nil {
			log.Fatal(err)
		}
		log.Printf("created %s", *initEnvPath)
		return
	}
	if *envPath != "" {
		if err := loadEnv(*envPath); err != nil {
			log.Fatal(err)
		}
	}
	cfg, err := readConfig()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DB), 0700); err != nil {
		log.Fatal(err)
	}
	if f, err := os.OpenFile(cfg.DB, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
		if err := f.Close(); err != nil {
			log.Fatal(err)
		}
	} else if !errors.Is(err, os.ErrExist) {
		log.Fatal(err)
	}

	db, err := sql.Open("sqlite", cfg.DB)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := migrate(db, cfg); err != nil {
		log.Fatal(err)
	}

	tpl := template.Must(template.ParseFS(templateFiles, "templates/*.html"))
	a := &app{db: db, cfg: cfg, tpl: tpl, http: &http.Client{Timeout: 15 * time.Second}}
	srv := &http.Server{Addr: cfg.Listen, Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("egate listening on %s", cfg.Listen)
	log.Fatal(srv.ListenAndServe())
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `egate - a small application-to-application email gateway

USAGE
  egate help
  egate teach HOST_URL SENDER_EMAIL [APP_API_KEY ENV_PATH]
  egate --init-env PATH
  egate --env PATH

QUICK START
  1. Create a starter configuration file:
       egate --init-env config/.env

  2. Edit config/.env and replace the starter admin password and Resend API key.

  3. Start the server with the local file:
       egate --env config/.env

  4. Open http://127.0.0.1:8119 in a browser to sign in, register apps,
     and find their keys and email endpoint.

TEACH AN AGENT
  Write the egate integration guide into the current directory:
       egate teach https://egate.example.com noreply@example.com eg_APP_KEY .env

  The sender email must be on a domain verified in Resend. The app key comes
  from a registered app in the egate admin page. The command writes ./EGATE.md
  and sets EGATE_API_KEY in ENV_PATH, creating the file if needed. Existing
  environment entries are preserved; an existing EGATE_API_KEY is replaced.
  Running teach without APP_API_KEY and ENV_PATH only writes the guide.

OPTIONS
  --init-env PATH  Create a starter environment file and exit.
                   Existing files are never overwritten.
  --env PATH       Load a local configuration file. By default, read process environment.
  -h, --help       Show this help and exit.

REQUIRED CONFIGURATION
  EGATE_ADMIN_USERNAME      Administrator sign-in username
  EGATE_ADMIN_PASSWORD      Administrator sign-in password
  EGATE_RESEND_API_KEY    Resend API key used to deliver email

OPTIONAL CONFIGURATION
  EGATE_DB                  SQLite database path (default: ./data/main.sqlite)
  EGATE_HOST                Listen address (default: 0.0.0.0)
  EGATE_PORT                Listen port (default: 8119)
  EGATE_LOGIN_MAX_ATTEMPTS  Failed logins allowed per IP in 24 hours (default: 5)

SEND AN EMAIL
  First register an app in the admin page and copy its key, then send:

  curl http://127.0.0.1:8119/v1/email \
    -H 'Authorization: Bearer eg_YOUR_KEY' \
    -H 'Content-Type: application/json' \
    -d '{"from":"sender@example.com","to":"recipient@example.com","subject":"Hello","text_body":"Sent through egate"}'

NOTES
  The SQLite database and tables are created automatically on first startup.
  Put egate behind a TLS reverse proxy before exposing it in production.
`)
}

func teach(host, sender string, credentials ...string) error {
	if len(credentials) != 0 && len(credentials) != 2 {
		return errors.New("teach requires both APP_API_KEY and ENV_PATH")
	}
	u, err := url.Parse(host)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("host must be an absolute http or https base URL without a path, query, or fragment")
	}
	address, err := mail.ParseAddress(sender)
	if err != nil || address.Address != sender {
		return errors.New("sender must be a single email address without a display name")
	}
	host = strings.TrimRight(u.String(), "/")
	guide := strings.ReplaceAll(agentGuide, "{EGATE_HOST}", host)
	guide = strings.ReplaceAll(guide, "{EGATE_SENDER_EMAIL}", sender)
	keyStatus := "Set `EGATE_API_KEY` to the registered app key in the calling application's server-side secret storage."
	if len(credentials) == 2 {
		if err := writeAppKeyEnv(credentials[1], credentials[0]); err != nil {
			return err
		}
		keyStatus = "`egate teach` has saved `EGATE_API_KEY` in the environment file you supplied. Load that file in the calling application's server process."
	}
	guide = strings.ReplaceAll(guide, "{EGATE_KEY_STATUS}", keyStatus)
	if err := os.WriteFile("EGATE.md", []byte(guide), 0644); err != nil {
		return fmt.Errorf("write EGATE.md: %w", err)
	}
	return nil
}

func writeAppKeyEnv(path, key string) error {
	if !strings.HasPrefix(key, "eg_") || len(key) <= len("eg_") || strings.Trim(key[len("eg_"):], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return errors.New("APP_API_KEY must be a single eg_ app key")
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("ENV_PATH is required")
	}
	var contents string
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("environment path must be a regular file: %s", path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read environment file: %w", err)
		}
		contents = string(b)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect environment file: %w", err)
	}
	var lines []string
	for _, line := range strings.Split(contents, "\n") {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "export ")
		name, _, hasValue := strings.Cut(trimmed, "=")
		if hasValue && strings.TrimSpace(name) == "EGATE_API_KEY" {
			continue
		}
		lines = append(lines, line)
	}
	contents = strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if contents != "" {
		contents += "\n"
	}
	contents += "EGATE_API_KEY=" + key + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create environment directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".egate-env-*")
	if err != nil {
		return fmt.Errorf("create temporary environment file: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := io.WriteString(f, contents); err != nil {
		f.Close()
		return fmt.Errorf("write environment file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close environment file: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace environment file: %w", err)
	}
	return nil
}

const starterEnv = `EGATE_DB=./data/main.sqlite
EGATE_ADMIN_USERNAME=admin
EGATE_ADMIN_PASSWORD=replace-with-a-long-random-password
EGATE_RESEND_API_KEY=invalid-replace-with-resend-api-key
EGATE_PORT=8119
EGATE_LOGIN_MAX_ATTEMPTS=5
`

func initEnv(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("environment file path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create env directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create env file: %w", err)
	}
	if _, err := io.WriteString(f, starterEnv); err != nil {
		f.Close()
		return fmt.Errorf("write env file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close env file: %w", err)
	}
	return nil
}

func loadEnv(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read env file: %w", err)
	}
	for n, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return fmt.Errorf("env file line %d is invalid", n+1)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"')) {
			v = v[1 : len(v)-1]
		}
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

func readConfig() (config, error) {
	c := config{DB: os.Getenv("EGATE_DB"), Username: os.Getenv("EGATE_ADMIN_USERNAME"), Password: os.Getenv("EGATE_ADMIN_PASSWORD"), ResendKey: os.Getenv("EGATE_RESEND_API_KEY"), Host: "0.0.0.0", Port: defaultPort, MaxAttempts: 5}
	if c.DB == "" {
		c.DB = "./data/main.sqlite"
	}
	for k, v := range map[string]string{"EGATE_ADMIN_USERNAME": c.Username, "EGATE_ADMIN_PASSWORD": c.Password, "EGATE_RESEND_API_KEY": c.ResendKey} {
		if v == "" {
			return c, fmt.Errorf("%s is required", k)
		}
	}
	if c.Password == "replace-with-a-long-random-password" {
		return c, errors.New("EGATE_ADMIN_PASSWORD must be changed from the starter value")
	}
	if c.ResendKey == "invalid-replace-with-resend-api-key" {
		return c, errors.New("EGATE_RESEND_API_KEY must be changed from the starter value")
	}
	if v := os.Getenv("EGATE_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return c, errors.New("EGATE_PORT must be an integer from 1 to 65535")
		}
		c.Port = n
	}
	if v := os.Getenv("EGATE_HOST"); v != "" {
		c.Host = v
	}
	c.Listen = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	if v := os.Getenv("EGATE_LOGIN_MAX_ATTEMPTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return c, errors.New("EGATE_LOGIN_MAX_ATTEMPTS must be a positive integer")
		}
		c.MaxAttempts = n
	}
	return c, nil
}

func migrate(db *sql.DB, cfg config) error {
	_, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS admin (id INTEGER PRIMARY KEY CHECK(id=1), username TEXT NOT NULL, password_hash TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS login_failures (id INTEGER PRIMARY KEY, ip TEXT NOT NULL, attempted_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS login_failures_ip_time ON login_failures(ip, attempted_at);
CREATE TABLE IF NOT EXISTS login_bans (ip TEXT PRIMARY KEY, banned_until INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, csrf_token TEXT NOT NULL, expires_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS api_keys (id INTEGER PRIMARY KEY, name TEXT NOT NULL, key_hash TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL, created_at INTEGER NOT NULL, revoked_at INTEGER);
CREATE TABLE IF NOT EXISTS apps (id INTEGER PRIMARY KEY, name TEXT NOT NULL, key_hash TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL, created_at INTEGER NOT NULL, revoked_at INTEGER);
CREATE INDEX IF NOT EXISTS apps_key_hash ON apps(key_hash);
INSERT OR IGNORE INTO apps(id,name,key_hash,prefix,created_at,revoked_at) SELECT id,name,key_hash,prefix,created_at,revoked_at FROM api_keys;`)
	if err != nil {
		return err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(cfg.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO admin(id,username,password_hash,updated_at) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET username=excluded.username,password_hash=excluded.password_hash,updated_at=excluded.updated_at`, cfg.Username, h, time.Now().Unix())
	return err
}

func (a *app) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	})
	m.HandleFunc("GET /login", a.loginPage)
	m.HandleFunc("POST /login", a.login)
	m.HandleFunc("POST /logout", a.requireAdmin(a.logout))
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})
	m.HandleFunc("GET /admin", a.requireAdmin(a.dashboard))
	m.HandleFunc("POST /apps", a.requireAdmin(a.createApp))
	m.HandleFunc("POST /apps/{id}/revoke", a.requireAdmin(a.revokeApp))
	m.HandleFunc("POST /v1/email", a.requireAPIKey(a.sendEmail))
	return securityHeaders(m)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/admin" || r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/apps") {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func hash(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
func token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func (a *app) cleanup() {
	now := time.Now().Unix()
	a.db.Exec(`DELETE FROM login_failures WHERE attempted_at < ?`, now-24*3600)
	a.db.Exec(`DELETE FROM login_bans WHERE banned_until < ?`, now)
	a.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now)
}

func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, "login.html", pageData{})
}
func gatewayURLs(r *http.Request) (string, string) {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	base := scheme + "://" + r.Host
	return base, base + "/v1/email"
}
func (a *app) login(w http.ResponseWriter, r *http.Request) {
	a.cleanup()
	ip := clientIP(r)
	now := time.Now()
	var bannedUntil int64
	if err := a.db.QueryRow(`SELECT banned_until FROM login_bans WHERE ip=?`, ip).Scan(&bannedUntil); err == nil && bannedUntil > now.Unix() {
		w.Header().Set("Retry-After", strconv.FormatInt(bannedUntil-now.Unix(), 10))
		http.Error(w, "too many failed attempts; try again later", http.StatusTooManyRequests)
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	var failures int
	a.db.QueryRow(`SELECT COUNT(*) FROM login_failures WHERE ip=? AND attempted_at>=?`, ip, cutoff).Scan(&failures)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	var user, passhash string
	err := a.db.QueryRow(`SELECT username,password_hash FROM admin WHERE id=1`).Scan(&user, &passhash)
	if err != nil || r.FormValue("username") != user || bcrypt.CompareHashAndPassword([]byte(passhash), []byte(r.FormValue("password"))) != nil {
		a.db.Exec(`INSERT INTO login_failures(ip,attempted_at) VALUES(?,?)`, ip, now.Unix())
		failures++
		if failures >= a.cfg.MaxAttempts {
			until := now.Add(24 * time.Hour).Unix()
			a.db.Exec(`INSERT INTO login_bans(ip,banned_until) VALUES(?,?) ON CONFLICT(ip) DO UPDATE SET banned_until=excluded.banned_until`, ip, until)
			w.Header().Set("Retry-After", "86400")
			http.Error(w, "too many failed attempts; banned for 24 hours", http.StatusTooManyRequests)
			return
		}
		a.renderStatus(w, "login.html", pageData{Error: "Invalid username or password."}, http.StatusUnauthorized)
		return
	}
	a.db.Exec(`DELETE FROM login_failures WHERE ip=?`, ip)
	raw, csrf := token(32), token(24)
	a.db.Exec(`INSERT INTO sessions(token_hash,csrf_token,expires_at) VALUES(?,?,?)`, hash(raw), csrf, time.Now().Add(12*time.Hour).Unix())
	http.SetCookie(w, &http.Cookie{Name: "egate_session", Value: raw, Path: "/", HttpOnly: true, Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"), SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *app) session(r *http.Request) (string, bool) {
	c, e := r.Cookie("egate_session")
	if e != nil {
		return "", false
	}
	var csrf string
	e = a.db.QueryRow(`SELECT csrf_token FROM sessions WHERE token_hash=? AND expires_at>?`, hash(c.Value), time.Now().Unix()).Scan(&csrf)
	return csrf, e == nil
}
func (a *app) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrf, ok := a.session(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method == "POST" && r.FormValue("csrf") != csrf {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		next(w, r)
	}
}
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("egate_session"); e == nil {
		a.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "egate_session", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", 303)
}
func (a *app) dashboard(w http.ResponseWriter, r *http.Request) {
	csrf, _ := a.session(r)
	rows, err := a.db.Query(`SELECT id,name,prefix,datetime(created_at,'unixepoch') FROM apps WHERE revoked_at IS NULL ORDER BY id DESC`)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	base, endpoint := gatewayURLs(r)
	d := pageData{CSRF: csrf, BaseURL: base, EmailEndpoint: endpoint}
	for rows.Next() {
		var k appView
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Created); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		d.Apps = append(d.Apps, k)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "database error", 500)
		return
	}
	a.render(w, "dashboard.html", d)
}
func (a *app) createApp(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 100 {
		http.Error(w, "app name must be 1 to 100 characters", 400)
		return
	}
	raw := "eg_" + token(32)
	prefix := raw[:11]
	_, err := a.db.Exec(`INSERT INTO apps(name,key_hash,prefix,created_at) VALUES(?,?,?,?)`, name, hash(raw), prefix, time.Now().Unix())
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	csrf, _ := a.session(r)
	base, endpoint := gatewayURLs(r)
	d := pageData{CSRF: csrf, BaseURL: base, EmailEndpoint: endpoint, NewKey: raw, Message: "Copy this app key now. It cannot be shown again."}
	rows, err := a.db.Query(`SELECT id,name,prefix,datetime(created_at,'unixepoch') FROM apps WHERE revoked_at IS NULL ORDER BY id DESC`)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var k appView
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Created); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		d.Apps = append(d.Apps, k)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "database error", 500)
		return
	}
	a.render(w, "dashboard.html", d)
}
func (a *app) revokeApp(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad app id", 400)
		return
	}
	if _, err := a.db.Exec(`UPDATE apps SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, time.Now().Unix(), id); err != nil {
		http.Error(w, "database error", 500)
		return
	}
	http.Redirect(w, r, "/admin", 303)
}

func (a *app) requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := r.Header.Get("Authorization")
		raw, ok := strings.CutPrefix(v, "Bearer ")
		if !ok {
			http.Error(w, "missing bearer token", 401)
			return
		}
		var one int
		err := a.db.QueryRow(`SELECT 1 FROM apps WHERE key_hash=? AND revoked_at IS NULL`, hash(raw)).Scan(&one)
		if err != nil {
			http.Error(w, "invalid API key", 401)
			return
		}
		next(w, r)
	}
}

type emailRequest struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Subject  string `json:"subject"`
	TextBody string `json:"text_body"`
	HTMLBody string `json:"html_body"`
	ReplyTo  string `json:"reply_to,omitempty"`
}

func (a *app) sendEmail(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var in emailRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&in); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if strings.TrimSpace(in.From) == "" || strings.TrimSpace(in.To) == "" || strings.TrimSpace(in.Subject) == "" || (strings.TrimSpace(in.TextBody) == "" && strings.TrimSpace(in.HTMLBody) == "") {
		http.Error(w, "from, to, subject, and a body are required", 400)
		return
	}
	payload := map[string]any{"from": in.From, "to": []string{in.To}, "subject": in.Subject}
	if in.TextBody != "" {
		payload["text"] = in.TextBody
	}
	if in.HTMLBody != "" {
		payload["html"] = in.HTMLBody
	}
	if in.ReplyTo != "" {
		payload["reply_to"] = in.ReplyTo
	}
	b, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "could not encode email", 500)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), "POST", "https://api.resend.com/emails", strings.NewReader(string(b)))
	if err != nil {
		http.Error(w, "could not create provider request", 500)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.ResendKey)
	resp, err := a.http.Do(req)
	if err != nil {
		http.Error(w, "email provider unavailable", 502)
		return
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(out)
}
func (a *app) render(w http.ResponseWriter, name string, d pageData) { a.renderStatus(w, name, d, 200) }
func (a *app) renderStatus(w http.ResponseWriter, name string, d pageData, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := a.tpl.ExecuteTemplate(w, name, d); err != nil {
		log.Printf("template: %v", err)
	}
}

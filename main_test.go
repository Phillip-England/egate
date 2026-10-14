package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHelpIsClearAndActionable(t *testing.T) {
	var out bytes.Buffer
	printHelp(&out)
	help := out.String()
	for _, want := range []string{
		"egate --init-env config/.env",
		"egate teach https://egate.example.com noreply@example.com",
		"writes ./EGATE.md",
		"By default, read process environment",
		"EGATE_RESEND_API_KEY",
		"http://127.0.0.1:8119 in a browser",
		"Authorization: Bearer eg_YOUR_KEY",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help is missing %q", want)
		}
	}
}

func TestTeachWritesAgentGuide(t *testing.T) {
	t.Chdir(t.TempDir())
	host := "https://egate.englandsoftware.com"
	sender := "noreply@englandsoftware.com"
	if err := teach(host+"/", sender); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("EGATE.md")
	if err != nil {
		t.Fatal(err)
	}
	guide := string(b)
	for _, want := range []string{
		"POST " + host + "/v1/email",
		"EGATE_HOST=" + host,
		"Sender email: `" + sender + "`",
		"\"from\": \"" + sender + "\"",
		"Set the `from` field to `" + sender + "`",
		"EGATE_API_KEY",
		"Authorization: Bearer <EGATE_API_KEY>",
		"text_body",
		"html_body",
		"401",
		"502",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("guide is missing %q", want)
		}
	}
	if strings.Contains(guide, "{EGATE_HOST}") || strings.Contains(guide, "{EGATE_SENDER_EMAIL}") || strings.Contains(guide, "github.com/englandsystems/egate/sdk") {
		t.Fatal("guide contains an unresolved placeholder or SDK dependency")
	}

	if err := os.WriteFile("EGATE.md", []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := teach(host, sender); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile("EGATE.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != guide {
		t.Fatal("teach did not replace an existing guide")
	}
}

func TestTeachRejectsInvalidHostWithoutWriting(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, host := range []string{"", "egate.example.com", "ftp://egate.example.com", "https://egate.example.com/v1/email", "https://egate.example.com?key=secret", "https://user:pass@egate.example.com"} {
		if err := teach(host, "noreply@example.com"); err == nil {
			t.Errorf("teach accepted invalid host %q", host)
		}
	}
	if _, err := os.Stat("EGATE.md"); !os.IsNotExist(err) {
		t.Fatalf("invalid host created EGATE.md: %v", err)
	}
}

func TestTeachRejectsInvalidSenderWithoutWriting(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, sender := range []string{"", "noreply", "Example <noreply@example.com>", "first@example.com, second@example.com", "noreply@example.com\nInjected: value"} {
		if err := teach("https://egate.example.com", sender); err == nil {
			t.Errorf("teach accepted invalid sender %q", sender)
		}
	}
	if _, err := os.Stat("EGATE.md"); !os.IsNotExist(err) {
		t.Fatalf("invalid sender created EGATE.md: %v", err)
	}
}

func TestInitEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", ".env")
	if err := initEnv(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "EGATE_RESEND_API_KEY=invalid-") {
		t.Fatalf("starter env has no invalid Resend key: %s", b)
	}
	if !strings.Contains(string(b), "EGATE_PORT=8119") {
		t.Fatalf("starter env has no standard egate port: %s", b)
	}
	if !strings.Contains(string(b), "EGATE_DB=./data/main.sqlite") {
		t.Fatal("starter env does not use the persistent database path")
	}
	if err := initEnv(path); err == nil {
		t.Fatal("initEnv overwrote an existing file")
	}
}

func TestReadConfigPort(t *testing.T) {
	t.Setenv("EGATE_DB", "test.sqlite3")
	t.Setenv("EGATE_ADMIN_USERNAME", "admin")
	t.Setenv("EGATE_ADMIN_PASSWORD", "password")
	t.Setenv("EGATE_RESEND_API_KEY", "test")
	t.Setenv("EGATE_PORT", "54321")

	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 54321 || cfg.Listen != "0.0.0.0:54321" {
		t.Fatalf("port=%d listen=%q", cfg.Port, cfg.Listen)
	}
}

func TestReadConfigDefaultsToPersistentDatabase(t *testing.T) {
	t.Setenv("EGATE_DB", "")
	t.Setenv("EGATE_ADMIN_USERNAME", "admin")
	t.Setenv("EGATE_ADMIN_PASSWORD", "password")
	t.Setenv("EGATE_RESEND_API_KEY", "test")
	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DB != "./data/main.sqlite" {
		t.Fatalf("database = %q", cfg.DB)
	}
}

func TestReadConfigRejectsStarterCredentials(t *testing.T) {
	t.Setenv("EGATE_ADMIN_USERNAME", "admin")
	t.Setenv("EGATE_ADMIN_PASSWORD", "replace-with-a-long-random-password")
	t.Setenv("EGATE_RESEND_API_KEY", "test")
	if _, err := readConfig(); err == nil {
		t.Fatal("starter administrator password was accepted")
	}
	t.Setenv("EGATE_ADMIN_PASSWORD", "a unique password")
	t.Setenv("EGATE_RESEND_API_KEY", "invalid-replace-with-resend-api-key")
	if _, err := readConfig(); err == nil {
		t.Fatal("starter Resend key was accepted")
	}
}

func TestReadConfigHost(t *testing.T) {
	t.Setenv("EGATE_DB", "test.sqlite3")
	t.Setenv("EGATE_ADMIN_USERNAME", "admin")
	t.Setenv("EGATE_ADMIN_PASSWORD", "password")
	t.Setenv("EGATE_RESEND_API_KEY", "test")
	t.Setenv("EGATE_HOST", "0.0.0.0")

	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "0.0.0.0:8119" {
		t.Fatalf("listen=%q", cfg.Listen)
	}
}

func TestReadConfigRejectsInvalidPort(t *testing.T) {
	t.Setenv("EGATE_DB", "test.sqlite3")
	t.Setenv("EGATE_ADMIN_USERNAME", "admin")
	t.Setenv("EGATE_ADMIN_PASSWORD", "password")
	t.Setenv("EGATE_RESEND_API_KEY", "test")
	t.Setenv("EGATE_PORT", "65536")

	if _, err := readConfig(); err == nil {
		t.Fatal("readConfig accepted an invalid port")
	}
}

func testApp(t *testing.T, max int) *app {
	t.Helper()
	db, err := sqlOpenForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{Username: "admin", Password: "correct horse battery staple", ResendKey: "test", MaxAttempts: max}
	if err := migrate(db, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &app{db: db, cfg: cfg, tpl: templateForTest(t), http: &http.Client{Timeout: time.Second}}
}

func sqlOpenForTest(t *testing.T) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/test.sqlite3")
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func templateForTest(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.ParseFS(templateFiles, "templates/*.html"))
}

func TestLoginCreatesSession(t *testing.T) {
	a := testApp(t, 5)
	form := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) == 0 || w.Result().Cookies()[0].Name != "egate_session" {
		t.Fatal("session cookie not set")
	}
	if location := w.Header().Get("Location"); location != "/admin" {
		t.Fatalf("location = %q, want /admin", location)
	}
}

func TestLoginCookieIsSecureBehindTLSProxy(t *testing.T) {
	a := testApp(t, 5)
	form := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || len(w.Result().Cookies()) == 0 || !w.Result().Cookies()[0].Secure {
		t.Fatalf("status = %d, session cookie is not Secure", w.Code)
	}
}

func TestRootLeadsToAdmin(t *testing.T) {
	a := testApp(t, 5)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin" {
		t.Fatalf("status = %d, location = %q", w.Code, w.Header().Get("Location"))
	}
}

func TestAdminShowsConnectionURLs(t *testing.T) {
	a := testApp(t, 5)
	rawSession, csrf := token(32), token(24)
	if _, err := a.db.Exec(`INSERT INTO sessions(token_hash,csrf_token,expires_at) VALUES(?,?,?)`, hash(rawSession), csrf, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://egate.example.com/admin", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.AddCookie(&http.Cookie{Name: "egate_session", Value: rawSession})
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"Host (EGATE_HOST)", "https://egate.example.com", "POST https://egate.example.com/v1/email", "EGATE_API_KEY"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("admin page is missing %q", want)
		}
	}
}

func TestAdminRequiresLogin(t *testing.T) {
	a := testApp(t, 5)
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("status = %d, location = %q", w.Code, w.Header().Get("Location"))
	}
}

func TestLoginBanAtThreshold(t *testing.T) {
	a := testApp(t, 2)
	for i, want := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		form := url.Values{"username": {"admin"}, "password": {"wrong"}}
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		r.RemoteAddr = "203.0.113.9:1234"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, w.Code, want)
		}
	}
}

func TestAPIKeyMiddleware(t *testing.T) {
	a := testApp(t, 5)
	raw := "eg_test_key"
	if _, err := a.db.Exec(`INSERT INTO apps(name,key_hash,prefix,created_at) VALUES(?,?,?,?)`, "test", hash(raw), "eg_test", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	hit := false
	h := a.requireAPIKey(func(w http.ResponseWriter, r *http.Request) { hit = true; w.WriteHeader(204) })
	r := httptest.NewRequest(http.MethodPost, "/v1/email", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != 204 || !hit {
		t.Fatalf("status=%d hit=%v", w.Code, hit)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRegisterAppAndRevokeKey(t *testing.T) {
	a := testApp(t, 5)
	rawSession, csrf := token(32), token(24)
	if _, err := a.db.Exec(`INSERT INTO sessions(token_hash,csrf_token,expires_at) VALUES(?,?,?)`, hash(rawSession), csrf, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf": {csrf}, "name": {"My App"}}
	req := httptest.NewRequest("POST", "/apps", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "egate_session", Value: rawSession})
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("register status=%d body=%s", w.Code, w.Body.String())
	}
	var id int64
	var keyHash string
	if err := a.db.QueryRow(`SELECT id,key_hash FROM apps WHERE name='My App'`).Scan(&id, &keyHash); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), "eg_") || strings.Contains(w.Body.String(), keyHash) {
		t.Fatal("registration did not show a raw key safely")
	}
	if !strings.Contains(w.Body.String(), "POST http://example.com/v1/email") {
		t.Fatal("registration response did not show the email endpoint with the new key")
	}
	revoke := httptest.NewRequest("POST", "/apps/"+strconv.FormatInt(id, 10)+"/revoke", strings.NewReader(url.Values{"csrf": {csrf}}.Encode()))
	revoke.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	revoke.AddCookie(&http.Cookie{Name: "egate_session", Value: rawSession})
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, revoke)
	if w.Code != 303 {
		t.Fatalf("revoke status=%d", w.Code)
	}
	var revoked sql.NullInt64
	if err := a.db.QueryRow(`SELECT revoked_at FROM apps WHERE id=?`, id).Scan(&revoked); err != nil || !revoked.Valid {
		t.Fatalf("revocation=%v err=%v", revoked, err)
	}
}

func TestSendEmailMapsToResend(t *testing.T) {
	a := testApp(t, 5)
	raw := "eg_test_key"
	if _, err := a.db.Exec(`INSERT INTO apps(name,key_hash,prefix,created_at) VALUES(?,?,?,?)`, "test", hash(raw), "eg_test", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	called := false
	a.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		if r.URL.String() != "https://api.resend.com/emails" || r.Header.Get("Authorization") != "Bearer test" {
			t.Fatalf("provider request: %s %s", r.URL, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["from"] != "sender@example.com" || body["subject"] != "Hello" || body["text"] != "Hi" || body["html"] != "<p>Hi</p>" || body["reply_to"] != "reply@example.com" {
			t.Fatalf("provider payload: %#v", body)
		}
		to, ok := body["to"].([]any)
		if !ok || len(to) != 1 || to[0] != "recipient@example.com" {
			t.Fatalf("provider recipients: %#v", body["to"])
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"email_123"}`))}, nil
	})}
	r := httptest.NewRequest("POST", "/v1/email", strings.NewReader(`{"from":"sender@example.com","to":"recipient@example.com","subject":"Hello","text_body":"Hi","html_body":"<p>Hi</p>","reply_to":"reply@example.com"}`))
	r.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if !called || w.Code != 200 || w.Body.String() != `{"id":"email_123"}` {
		t.Fatalf("called=%v status=%d body=%s", called, w.Code, w.Body.String())
	}
}

func TestLegacyKeyMigratesToApp(t *testing.T) {
	a := testApp(t, 5)
	raw := "eg_legacy_key"
	if _, err := a.db.Exec(`INSERT INTO api_keys(name,key_hash,prefix,created_at) VALUES(?,?,?,?)`, "legacy", hash(raw), "eg_legacy", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := migrate(a.db, a.cfg); err != nil {
		t.Fatal(err)
	}
	hit := false
	h := a.requireAPIKey(func(w http.ResponseWriter, r *http.Request) { hit = true; w.WriteHeader(204) })
	r := httptest.NewRequest("POST", "/v1/email", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != 204 || !hit {
		t.Fatalf("migrated key status=%d hit=%v", w.Code, hit)
	}
	if _, err := a.db.Exec(`UPDATE apps SET revoked_at=? WHERE key_hash=?`, time.Now().Unix(), hash(raw)); err != nil {
		t.Fatal(err)
	}
	if err := migrate(a.db, a.cfg); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h(w, r)
	if w.Code != 401 {
		t.Fatalf("revoked key status=%d", w.Code)
	}
}

package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// A valid client IP supplied by a registered app is used for that app's limit.
// The bearer key authenticates the application making this assertion.
func submissionIP(r *http.Request) string {
	if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Egate-Client-IP"))); err == nil {
		return ip.Unmap().String()
	}
	if ip, err := netip.ParseAddr(clientIP(r)); err == nil {
		return ip.Unmap().String()
	}
	return "unknown"
}

func cleanupEmailData(db *sql.DB, now int64) error {
	for _, q := range []string{
		`DELETE FROM email_submissions WHERE created_at <= ?`,
		`DELETE FROM email_jobs WHERE created_at <= ?`,
	} {
		if _, err := db.Exec(q, now-24*3600); err != nil {
			return err
		}
	}
	_, err := db.Exec(`DELETE FROM email_jail WHERE banned_until <= ?`, now)
	return err
}

func (a *app) acceptEmail(r *http.Request, id string, payload []byte) error {
	now := time.Now().Unix()
	appID := r.Context().Value(appIDKey{}).(int64)
	ip := submissionIP(r)
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Keep the 24-hour delivery and abuse data bounded even under sustained traffic.
	for _, q := range []string{`DELETE FROM email_submissions WHERE created_at <= ?`, `DELETE FROM email_jobs WHERE created_at <= ?`} {
		if _, err = tx.Exec(q, now-24*3600); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DELETE FROM email_jail WHERE banned_until <= ?`, now); err != nil {
		return err
	}
	var limit int
	if err = tx.QueryRow(`SELECT daily_limit FROM apps WHERE id=?`, appID).Scan(&limit); err != nil {
		return err
	}
	if limit > 0 {
		var until int64
		err = tx.QueryRow(`SELECT banned_until FROM email_jail WHERE app_id=? AND ip=?`, appID, ip).Scan(&until)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if until > now {
			return tx.Commit()
		}
		var used int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM email_submissions WHERE app_id=? AND ip=? AND created_at>?`, appID, ip, now-24*3600).Scan(&used); err != nil {
			return err
		}
		if used >= limit {
			if _, err = tx.Exec(`INSERT INTO email_jail(app_id,ip,banned_until) VALUES(?,?,?) ON CONFLICT(app_id,ip) DO UPDATE SET banned_until=excluded.banned_until`, appID, ip, now+24*3600); err != nil {
				return err
			}
			return tx.Commit()
		}
		if _, err = tx.Exec(`INSERT INTO email_submissions(app_id,ip,created_at) VALUES(?,?,?)`, appID, ip, now); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO email_jobs(id,app_id,payload,status,next_attempt_at,created_at,updated_at) VALUES(?,?,?,'queued',?,?,?)`, id, appID, payload, now, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *app) setAppLimit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.Error(w, "bad app id", 400)
		return
	}
	n, err := strconv.Atoi(r.FormValue("daily_limit"))
	if err != nil || n < 0 || n > 100000 {
		http.Error(w, "daily limit must be 0 to 100000", 400)
		return
	}
	result, err := a.db.Exec(`UPDATE apps SET daily_limit=? WHERE id=?`, n, id)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		http.Error(w, "app not found", 404)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *app) releaseJail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.FormValue("app_id"), 10, 64)
	ip, parseErr := netip.ParseAddr(strings.TrimSpace(r.FormValue("ip")))
	if err != nil || id < 1 || parseErr != nil {
		http.Error(w, "bad jail entry", 400)
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer tx.Rollback()
	canonical := ip.Unmap().String()
	for _, q := range []string{`DELETE FROM email_jail WHERE app_id=? AND ip=?`, `DELETE FROM email_submissions WHERE app_id=? AND ip=?`} {
		if _, err = tx.Exec(q, id, canonical); err != nil {
			http.Error(w, "database error", 500)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "database error", 500)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

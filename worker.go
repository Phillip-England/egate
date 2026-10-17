package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type emailJob struct {
	id       string
	payload  []byte
	attempts int
}

// The server and worker are separate processes. Stdin is a fast wake-up signal;
// the periodic database scan also recovers work after either process restarts.
func superviseWorker(notify <-chan struct{}) {
	for {
		bin, err := os.Executable()
		if err != nil {
			log.Printf("worker executable: %v", err)
			time.Sleep(time.Second)
			continue
		}
		cmd := exec.Command(bin, "--worker")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.Env = append(os.Environ(), "EGATE_MANAGED_WORKER=1")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			log.Printf("worker pipe: %v", err)
			time.Sleep(time.Second)
			continue
		}
		if err := cmd.Start(); err != nil {
			log.Printf("worker start: %v", err)
			stdin.Close()
			time.Sleep(time.Second)
			continue
		}
		log.Printf("email worker started (pid %d)", cmd.Process.Pid)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
	loop:
		for {
			select {
			case <-notify:
				if _, err := stdin.Write([]byte{1}); err != nil {
					break loop
				}
			case err := <-done:
				log.Printf("email worker exited: %v; restarting", err)
				break loop
			}
		}
		stdin.Close()
		time.Sleep(time.Second)
	}
}

func runWorker(ctx context.Context, db *sql.DB, key string, client *http.Client, wake io.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wakes := make(chan struct{}, 1)
	if wake != nil {
		go func() {
			buf := make([]byte, 1)
			for {
				if _, err := wake.Read(buf); err != nil {
					if os.Getenv("EGATE_MANAGED_WORKER") == "1" {
						cancel()
					}
					return
				}
				select {
				case wakes <- struct{}{}:
				default:
				}
			}
		}()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastCleanup := time.Now()
	for {
		if time.Since(lastCleanup) >= time.Hour {
			if err := cleanupEmailData(db, time.Now().Unix()); err != nil {
				log.Printf("email cleanup: %v", err)
			}
			lastCleanup = time.Now()
		}
		job, err := claimEmail(ctx, db)
		if err != nil {
			log.Printf("email claim: %v", err)
		}
		if job != nil {
			if err := deliverEmail(ctx, db, key, client, *job); err != nil {
				log.Printf("email %s: %v", job.id, err)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wakes:
		case <-ticker.C:
		}
	}
}

func claimEmail(ctx context.Context, db *sql.DB) (*emailJob, error) {
	now := time.Now().Unix()
	var j emailJob
	err := db.QueryRowContext(ctx, `UPDATE email_jobs SET status='processing',attempts=attempts+1,lease_until=?,updated_at=? WHERE id=(
	 SELECT id FROM email_jobs WHERE (status='queued' AND next_attempt_at<=?) OR (status='processing' AND lease_until<=?) ORDER BY created_at,id LIMIT 1
	) RETURNING id,payload,attempts`, now+60, now, now, now).Scan(&j.id, &j.payload, &j.attempts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func deliverEmail(ctx context.Context, db *sql.DB, key string, client *http.Client, j emailJob) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", strings.NewReader(string(j.payload)))
	if err != nil {
		return finishEmail(db, j, false, false, "invalid provider request", "")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return finishEmail(db, j, false, true, "provider unavailable", "")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return finishEmail(db, j, false, true, "provider response unreadable", "")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var result struct {
			ID string `json:"id"`
		}
		json.Unmarshal(body, &result)
		return finishEmail(db, j, true, false, "", result.ID)
	}
	retry := resp.StatusCode == 429 || resp.StatusCode >= 500
	return finishEmail(db, j, false, retry, fmt.Sprintf("provider returned HTTP %d", resp.StatusCode), "")
}

func finishEmail(db *sql.DB, j emailJob, sent, retry bool, message, providerID string) error {
	now := time.Now().Unix()
	if sent {
		_, err := db.Exec(`UPDATE email_jobs SET status='sent',provider_id=?,last_error=NULL,lease_until=NULL,updated_at=? WHERE id=? AND status='processing' AND attempts=?`, providerID, now, j.id, j.attempts)
		return err
	}
	if retry && j.attempts < 5 {
		delay := int64(1 << min(j.attempts, 6))
		_, err := db.Exec(`UPDATE email_jobs SET status='queued',next_attempt_at=?,last_error=?,lease_until=NULL,updated_at=? WHERE id=? AND status='processing' AND attempts=?`, now+delay, message, now, j.id, j.attempts)
		return err
	}
	_, err := db.Exec(`UPDATE email_jobs SET status='failed',last_error=?,lease_until=NULL,updated_at=? WHERE id=? AND status='processing' AND attempts=?`, message, now, j.id, j.attempts)
	return err
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ScheduleOf returns the minute after local midnight of the user's daily
// job search; ok is false when they have none yet.
func (s *Store) ScheduleOf(ctx context.Context, userID string) (minute int, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT at_minute FROM job_schedules WHERE user_id = ?`, userID).Scan(&minute)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return minute, err == nil, err
}

// ScheduleMinutes returns every user's search minute, in order.
func (s *Store) ScheduleMinutes(ctx context.Context) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at_minute FROM job_schedules ORDER BY at_minute`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var m int
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveSchedule gives the user a daily search at minute; ok is false when
// the minute is another user's or the user has a schedule already.
func (s *Store) SaveSchedule(ctx context.Context, userID string, minute int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO job_schedules (user_id, at_minute) VALUES (?, ?) ON CONFLICT (user_id) DO NOTHING`, userID, minute)
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DueSchedules returns the users whose search minute is at or before
// minute and who have not searched on date, a local YYYY-MM-DD.
func (s *Store) DueSchedules(ctx context.Context, minute int, date string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id FROM job_schedules
		WHERE at_minute <= ? AND (last_run_on IS NULL OR last_run_on < ?) ORDER BY at_minute`, minute, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MarkRan records that the user's search of date, a local YYYY-MM-DD, has
// started.
func (s *Store) MarkRan(ctx context.Context, userID, date string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE job_schedules SET last_run_on = ? WHERE user_id = ?`, date, userID)
	return err
}

// JobPosting is a job posting found for a user.
type JobPosting struct {
	UserID  string
	URLHash string // URLHash(URL), the id of its application
	URL     string
	Title   string
	Company string
	Source  string
	Status  string
	FoundAt time.Time
}

// Statuses of a JobPosting. Picked and published postings are open: the
// user has not dealt with them yet. Passed postings were judged not a fit
// and never shown.
const (
	PostingPicked    = "picked"
	PostingPublished = "published"
	PostingFailed    = "failed"
	PostingClosed    = "closed"
	PostingPassed    = "passed"
)

// JobPostingOf returns the user's posting with url hash h, or nil.
func (s *Store) JobPostingOf(ctx context.Context, userID, h string) (*JobPosting, error) {
	var p JobPosting
	var found int64
	err := s.db.QueryRowContext(ctx, `
		SELECT user_id, url_hash, url, title, company, source, status, found_at FROM job_postings
		WHERE user_id = ? AND url_hash = ?`, userID, h).
		Scan(&p.UserID, &p.URLHash, &p.URL, &p.Title, &p.Company, &p.Source, &p.Status, &found)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	p.FoundAt = time.Unix(found, 0)
	return &p, err
}

// OpenPostings returns the user's picked and published postings, oldest
// first.
func (s *Store) OpenPostings(ctx context.Context, userID string) ([]JobPosting, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, url_hash, url, title, company, source, status, found_at FROM job_postings
		WHERE user_id = ? AND status IN ('picked', 'published') ORDER BY found_at, url_hash`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobPosting
	for rows.Next() {
		var p JobPosting
		var found int64
		if err := rows.Scan(&p.UserID, &p.URLHash, &p.URL, &p.Title, &p.Company, &p.Source, &p.Status, &found); err != nil {
			return nil, err
		}
		p.FoundAt = time.Unix(found, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

// UnseenPostings returns the urls not found for the user before, in order.
func (s *Store) UnseenPostings(ctx context.Context, userID string, urls []string) ([]string, error) {
	var out []string
	for _, u := range urls {
		var one int
		err := s.db.QueryRowContext(ctx,
			`SELECT 1 FROM job_postings WHERE user_id = ? AND url_hash = ?`, userID, URLHash(u)).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			out = append(out, u)
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PickPosting records p as picked for its user, before its application is
// written, and returns it with its URLHash; ok is false when the posting
// was found for them before.
func (s *Store) PickPosting(ctx context.Context, p JobPosting) (JobPosting, bool, error) {
	p.URLHash, p.Status, p.FoundAt = URLHash(p.URL), PostingPicked, time.Unix(time.Now().Unix(), 0)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO job_postings (user_id, url_hash, url, title, company, source, status, found_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, url_hash) DO NOTHING`,
		p.UserID, p.URLHash, p.URL, p.Title, p.Company, p.Source, p.Status, p.FoundAt.Unix())
	if err != nil {
		return p, false, err
	}
	n, err := res.RowsAffected()
	return p, n == 1, err
}

// RepickPosting records as picked again a posting of p's user that was
// judged not a fit or whose application failed, when the user asks for it
// themselves; ok is false when the posting is neither.
func (s *Store) RepickPosting(ctx context.Context, p JobPosting) (JobPosting, bool, error) {
	p.URLHash, p.Status, p.FoundAt = URLHash(p.URL), PostingPicked, time.Unix(time.Now().Unix(), 0)
	res, err := s.db.ExecContext(ctx, `
		UPDATE job_postings SET status = 'picked', found_at = ?, title = ?, company = ?, source = ?
		WHERE user_id = ? AND url_hash = ? AND status IN ('passed', 'failed')`,
		p.FoundAt.Unix(), p.Title, p.Company, p.Source, p.UserID, p.URLHash)
	if err != nil {
		return p, false, err
	}
	n, err := res.RowsAffected()
	return p, n == 1, err
}

// PassPosting records p as judged not a fit for its user, so it is not
// judged again; a posting found before is left as it is.
func (s *Store) PassPosting(ctx context.Context, p JobPosting) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO job_postings (user_id, url_hash, url, title, company, source, status, found_at)
		VALUES (?, ?, ?, ?, ?, ?, 'passed', ?)
		ON CONFLICT (user_id, url_hash) DO NOTHING`,
		p.UserID, URLHash(p.URL), p.URL, p.Title, p.Company, p.Source, time.Now().Unix())
	return err
}

// MarkPublished records that the posting's application is in the web app.
func (s *Store) MarkPublished(ctx context.Context, userID, h string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE job_postings SET status = 'published', published_at = ?
		WHERE user_id = ? AND url_hash = ? AND status = 'picked'`, time.Now().Unix(), userID, h)
	return err
}

// MarkFailed records that the posting's application could not be written.
func (s *Store) MarkFailed(ctx context.Context, userID, h string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE job_postings SET status = 'failed' WHERE user_id = ? AND url_hash = ? AND status = 'picked'`, userID, h)
	return err
}

// ClosePostings records that the user is done with the postings of the
// given url hashes.
func (s *Store) ClosePostings(ctx context.Context, userID string, hashes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `
			UPDATE job_postings SET status = 'closed', closed_at = ?
			WHERE user_id = ? AND url_hash = ? AND status != 'closed'`, now, userID, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveWebProfile records uid as the user's web app profile.
func (s *Store) SaveWebProfile(ctx context.Context, userID, uid string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO web_profiles (user_id, uid) VALUES (?, ?)
		ON CONFLICT (user_id) DO UPDATE SET uid = excluded.uid`, userID, uid)
	return err
}

// WebProfileOf returns the uid of the user's web app profile, or "" when
// they have not signed in to it.
func (s *Store) WebProfileOf(ctx context.Context, userID string) (string, error) {
	var uid string
	err := s.db.QueryRowContext(ctx, `SELECT uid FROM web_profiles WHERE user_id = ?`, userID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return uid, err
}

// AddLinkRequests records posting links the user asked for; a link asked
// for again is kept once.
func (s *Store) AddLinkRequests(ctx context.Context, userID string, urls []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, u := range urls {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO job_link_requests (user_id, url, requested_at) VALUES (?, ?, ?)
			ON CONFLICT (user_id, url) DO NOTHING`, userID, u, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TakeLinkRequests removes and returns the user's link requests, oldest
// first.
func (s *Store) TakeLinkRequests(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		DELETE FROM job_link_requests WHERE user_id = ? RETURNING url, requested_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type req struct {
		url string
		at  int64
	}
	var reqs []req
	for rows.Next() {
		var r req
		if err := rows.Scan(&r.url, &r.at); err != nil {
			return nil, err
		}
		reqs = append(reqs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(reqs, func(i, j int) bool { return reqs[i].at < reqs[j].at })
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.url
	}
	return out, nil
}

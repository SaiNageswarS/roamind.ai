package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// User is a person known to the agent, keyed by email hash.
type User struct {
	ID          string
	Email       string
	DisplayName string
	// Channel is where the user came from: ChannelSlack, or ChannelWeb for
	// users of the job assistant only.
	Channel     string
	SlackTeamID string
	SlackUserID string
	// MemoryProject is the user's Basic Memory project, assigned once by
	// AssignMemoryProject; empty until then.
	MemoryProject string
}

// Channels for User.Channel.
const (
	// ChannelSlack users get every feature: chat, goals, exercises, news
	// shorts, and the job assistant when they also use the web app.
	ChannelSlack = "slack"
	// ChannelWeb users signed up in the web app and get the job assistant
	// only.
	ChannelWeb = "web"
)

// UserBySlack finds the user linked to a Slack identity, or nil.
func (s *Store) UserBySlack(ctx context.Context, teamID, slackUserID string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, display_name, channel, slack_team_id, slack_user_id, COALESCE(memory_project, '') FROM users
		 WHERE slack_team_id = ? AND slack_user_id = ?`, teamID, slackUserID)
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Channel, &u.SlackTeamID, &u.SlackUserID, &u.MemoryProject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

// UserByID finds a user by id, or nil.
func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, display_name, channel, COALESCE(slack_team_id, ''), COALESCE(slack_user_id, ''), COALESCE(memory_project, '') FROM users
		 WHERE id = ?`, id)
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Channel, &u.SlackTeamID, &u.SlackUserID, &u.MemoryProject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

// UpsertSlackUser creates the user for u.ID or links the Slack identity to
// the existing user with that email; a web user who joins Slack becomes a
// Slack user.
func (s *Store) UpsertSlackUser(ctx context.Context, u User) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, slack_team_id, slack_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			display_name = excluded.display_name,
			channel = 'slack',
			slack_team_id = excluded.slack_team_id,
			slack_user_id = excluded.slack_user_id,
			updated_at = excluded.updated_at`,
		u.ID, u.Email, u.DisplayName, u.SlackTeamID, u.SlackUserID, now, now)
	return err
}

// UpsertWebUser creates a web user for u.ID, the hash of u.Email, unless a
// user with that email exists already; an existing user, from Slack or the
// web, is left as it is.
func (s *Store) UpsertWebUser(ctx context.Context, u User) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, channel, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		u.ID, u.Email, u.DisplayName, ChannelWeb, now, now)
	return err
}

// AssignMemoryProject gives the user a Basic Memory project named base-<n>,
// with the smallest n no other user has, and returns it; a user who has one
// keeps it. user-setup's memory-project task calls it; afterwards the name
// is read with the user row, and it sticks even if the user is renamed.
func (s *Store) AssignMemoryProject(ctx context.Context, userID, base string) (string, error) {
	for n := 1; n < 1000; n++ {
		// Assigned already, by this call's last pass or a concurrent one.
		var cur sql.NullString
		if err := s.db.QueryRowContext(ctx, `SELECT memory_project FROM users WHERE id = ?`, userID).Scan(&cur); err != nil {
			return "", err
		}
		if cur.String != "" {
			return cur.String, nil
		}
		_, err := s.db.ExecContext(ctx,
			`UPDATE users SET memory_project = ? WHERE id = ? AND memory_project IS NULL`, fmt.Sprintf("%s-%d", base, n), userID)
		var se *sqlite.Error
		if err != nil && !(errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE) {
			return "", err
		}
		// On a unique error the name is another user's; try the next.
	}
	return "", fmt.Errorf("no free memory project name for %s", base)
}

// Users returns every Slack user with a Slack identity: the users of goals,
// exercises and news shorts.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, email, display_name, channel, slack_team_id, slack_user_id, COALESCE(memory_project, '') FROM users
		WHERE channel = 'slack' AND slack_user_id IS NOT NULL AND slack_user_id != '' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var us []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Channel, &u.SlackTeamID, &u.SlackUserID, &u.MemoryProject); err != nil {
			return nil, err
		}
		us = append(us, u)
	}
	return us, rows.Err()
}

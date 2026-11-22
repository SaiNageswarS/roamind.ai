package store

import (
	"strings"

	"github.com/SaiNageswarS/go-api-boot/odm"
)

// The tables below are read and written with go-api-boot's ORM: the Store
// holds a postgres.Table of each (see NewStore). The
// ORM keys a row by one id, so each table has an id column made from its
// natural key, here joined with "|" (the key's own columns stay, unique), and
// created_on and updated_on, which Save sets. Tables with conditional
// writes, serial ids or nullable unique columns are queried with SQL
// instead (see store.go).

// rowID joins the parts of a natural key into a row's id; no part but the
// last may contain "|".
func rowID(parts ...string) string { return strings.Join(parts, "|") }

type threadRow struct {
	ID              string `db:"id"`
	ChannelID       string `db:"channel_id"`
	ThreadTS        string `db:"thread_ts"`
	UserID          string `db:"user_id"`
	ClaudeSessionID string `db:"claude_session_id"`
	odm.Timestamps
}

func (r threadRow) Id() string           { return rowID(r.ChannelID, r.ThreadTS) }
func (threadRow) CollectionName() string { return "threads" }

type channelRow struct {
	ID        string `db:"id"`
	UserID    string `db:"user_id"`
	Kind      string `db:"kind"`
	Ref       string `db:"ref"`
	Title     string `db:"title"`
	ChannelID string `db:"channel_id"`
	odm.Timestamps
}

func (r channelRow) Id() string           { return rowID(r.UserID, r.Kind, r.Ref) }
func (channelRow) CollectionName() string { return "channels" }

type exerciseRow struct {
	ID            string `db:"id"`
	UserID        string `db:"user_id"`
	GoalPermalink string `db:"goal_permalink"`
	ChannelID     string `db:"channel_id"`
	ThreadTS      string `db:"thread_ts"`
	NotePermalink string `db:"note_permalink"`
	PostedAt      int64  `db:"posted_at"`
	AnsweredAt    *int64 `db:"answered_at"`
	odm.Timestamps
}

func (r exerciseRow) Id() string           { return rowID(r.ChannelID, r.ThreadTS) }
func (exerciseRow) CollectionName() string { return "exercises" }

type mailAccountRow struct {
	ID          string `db:"id"`
	Address     string `db:"address"`
	UserID      string `db:"user_id"`
	Secret      string `db:"secret"`
	ConnectedAt int64  `db:"connected_at"`
	SeenAt      *int64 `db:"seen_at"`
	odm.Timestamps
}

// Id is the lowercase address: addresses match without case.
func (r mailAccountRow) Id() string           { return strings.ToLower(r.Address) }
func (mailAccountRow) CollectionName() string { return "mail_accounts" }

type financeUploadRow struct {
	ID         string `db:"id"`
	UserID     string `db:"user_id"`
	SHA256     string `db:"sha256"`
	File       string `db:"file"`
	Account    string `db:"account"`
	Doc        string `db:"doc"`
	PeriodFrom string `db:"period_from"`
	PeriodTo   string `db:"period_to"`
	Rows       int64  `db:"rows"`
	ImportedAt int64  `db:"imported_at"`
	odm.Timestamps
}

func (r financeUploadRow) Id() string           { return rowID(r.UserID, r.SHA256) }
func (financeUploadRow) CollectionName() string { return "finance_uploads" }

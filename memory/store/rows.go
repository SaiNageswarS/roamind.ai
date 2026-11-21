package store

import (
	"context"
	"errors"
	"strings"

	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-api-boot/odm/postgres"
	"github.com/SaiNageswarS/go-collection-boot/async"
)

// The tables below are read and written with go-api-boot's ORM (TableOf). The
// ORM keys a row by one id, so each table has an id column made from its
// natural key, here joined with "|" (the key's own columns stay, unique), and
// created_on and updated_on, which Save sets. Tables with conditional
// writes, serial ids or nullable unique columns are queried with SQL
// instead (see store.go).

// rowID joins the parts of a natural key into a row's id; no part but the
// last may contain "|".
func rowID(parts ...string) string { return strings.Join(parts, "|") }

// table is the ORM table of T in the store's schema.
func table[T odm.Model](s *Store) postgres.Table[T] { return postgres.TableOf[T](s.pg, s.schema) }

// find returns the row of T with id, or nil.
func find[T odm.Model](ctx context.Context, s *Store, id string) (*T, error) {
	row, err := async.Await(table[T](s).FindByID(ctx, id))
	if errors.Is(err, odm.ErrNotFound) {
		return nil, nil
	}
	return row, err
}

// save inserts or replaces the row, by id.
func save[T odm.Model](ctx context.Context, s *Store, row T) error {
	_, err := async.Await(table[T](s).Save(ctx, row))
	return err
}

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

type financeDirtyRow struct {
	ID     string `db:"id"`
	UserID string `db:"user_id"`
	Since  int64  `db:"since"`
	odm.Timestamps
}

func (r financeDirtyRow) Id() string           { return r.UserID }
func (financeDirtyRow) CollectionName() string { return "finance_dirty" }

type financeSettingsRow struct {
	ID           string `db:"id"`
	UserID       string `db:"user_id"`
	BaseCurrency string `db:"base_currency"`
	odm.Timestamps
}

func (r financeSettingsRow) Id() string           { return r.UserID }
func (financeSettingsRow) CollectionName() string { return "finance_settings" }

type stockNameRow struct {
	ID     string `db:"id"`
	UserID string `db:"user_id"`
	Key    string `db:"key"`
	ISIN   string `db:"isin"`
	odm.Timestamps
}

// Id lowercases the key, which matches without case.
func (r stockNameRow) Id() string           { return rowID(r.UserID, strings.ToLower(r.Key)) }
func (stockNameRow) CollectionName() string { return "finance_stock_names" }

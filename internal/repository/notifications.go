package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourteam/crawler-lite/internal/notify"
)

type NotificationRepo struct{ pool *pgxpool.Pool }

func NewNotificationRepo(pool *pgxpool.Pool) *NotificationRepo {
	return &NotificationRepo{pool: pool}
}

const notificationColumns = `id, name, kind, url, events,
	enabled, created_by, created_at, updated_at`

func (r *NotificationRepo) scanOne(row pgx.Row) (*notify.Channel, error) {
	var (
		c         notify.Channel
		eventsB   []byte
		createdBy *int64
	)
	err := row.Scan(
		&c.ID, &c.Name, &c.Kind, &c.URL, &eventsB,
		&c.Enabled, &createdBy, &c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(eventsB) > 0 {
		_ = json.Unmarshal(eventsB, &c.Events)
	}
	if c.Events == nil {
		c.Events = []string{}
	}
	c.CreatedBy = createdBy
	return &c, nil
}

func (r *NotificationRepo) Insert(ctx context.Context, in notify.CreateInput) (*notify.Channel, error) {
	eventsJSON, err := json.Marshal(in.Events)
	if err != nil {
		return nil, err
	}
	if len(eventsJSON) == 0 || string(eventsJSON) == "null" {
		eventsJSON = []byte(`["failed","timeout","captcha_blocked"]`)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO notification_channels (name, kind, url, events, enabled, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+notificationColumns,
		in.Name, in.Kind, in.URL, eventsJSON, in.Enabled,
		nullableInt64(in.CreatedBy),
	)
	return r.scanOne(row)
}

func (r *NotificationRepo) Get(ctx context.Context, id int64) (*notify.Channel, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+notificationColumns+`
		FROM notification_channels WHERE id = $1
	`, id)
	return r.scanOne(row)
}

func (r *NotificationRepo) List(ctx context.Context) ([]*notify.Channel, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+notificationColumns+`
		FROM notification_channels
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*notify.Channel
	for rows.Next() {
		c, err := r.scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *NotificationRepo) ListEnabled(ctx context.Context) ([]*notify.Channel, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+notificationColumns+`
		FROM notification_channels
		WHERE enabled
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*notify.Channel
	for rows.Next() {
		c, err := r.scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *NotificationRepo) Update(ctx context.Context, id int64, in notify.UpdateInput) (*notify.Channel, error) {
	eventsJSON, err := json.Marshal(in.Events)
	if err != nil {
		return nil, err
	}
	if len(eventsJSON) == 0 || string(eventsJSON) == "null" {
		eventsJSON = []byte("[]")
	}
	row := r.pool.QueryRow(ctx, `
		UPDATE notification_channels
		SET name = $2, kind = $3, url = $4, events = $5, enabled = $6,
		    updated_at = now()
		WHERE id = $1
		RETURNING `+notificationColumns,
		id, in.Name, in.Kind, in.URL, eventsJSON, in.Enabled,
	)
	return r.scanOne(row)
}

func (r *NotificationRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM notification_channels WHERE id = $1`, id)
	return err
}
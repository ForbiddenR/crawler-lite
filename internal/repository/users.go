package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourteam/crawler-lite/internal/auth"
)

type UserRepo struct{ pool *pgxpool.Pool }

func NewUserRepo(pool *pgxpool.Pool) *UserRepo { return &UserRepo{pool: pool} }

// userRow holds every user column from the DB. We keep this private and expose
// auth.User to the rest of the app so timestamps and soft-delete flags don't
// leak into business logic.
type userRow struct {
	ID           int64
	Email        string
	PasswordHash string
	Role         auth.Role
	DisplayName  *string
	IsActive     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

func (r *UserRepo) scanOne(row pgx.Row) (*auth.User, error) {
	var u userRow
	err := row.Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.DisplayName,
		&u.IsActive, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	display := ""
	if u.DisplayName != nil {
		display = *u.DisplayName
	}
	return &auth.User{
		ID: u.ID, Email: u.Email, PasswordHash: u.PasswordHash,
		Role: u.Role, DisplayName: display, IsActive: u.IsActive,
	}, nil
}

const userColumns = `id, email, password_hash, role, display_name,
                     is_active, created_at, updated_at, deleted_at`

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*auth.User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+userColumns+`
		FROM users
		WHERE email = $1 AND deleted_at IS NULL AND is_active = TRUE
	`, email)
	return r.scanOne(row)
}

func (r *UserRepo) GetByID(ctx context.Context, id int64) (*auth.User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+userColumns+`
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
	`, id)
	return r.scanOne(row)
}

// Package repository contains the Postgres data-access layer.
//
// Week 1 uses raw pgx queries. Once the SQL surface grows, migrate to sqlc:
// the queries are already authored in db/queries/*.sql and the config in
// sqlc.yaml. Each Repo's public method signatures are designed to remain
// stable across that switch.
package repository

import "github.com/jackc/pgx/v5/pgxpool"

// Repos is the bag of repositories the master uses. It exists so the
// composition root doesn't have to construct each one separately and the rest
// of the app can grab repos through a single struct.
type Repos struct {
	Users   *UserRepo
	Spiders *SpiderRepo
	Tasks   *TaskRepo
}

// New constructs every repository against the same pool. The pool is owned by
// the composition root, not by the repos.
func New(pool *pgxpool.Pool) *Repos {
	return &Repos{
		Users:   NewUserRepo(pool),
		Spiders: NewSpiderRepo(pool),
		Tasks:   NewTaskRepo(pool),
	}
}

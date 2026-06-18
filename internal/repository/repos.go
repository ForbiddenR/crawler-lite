// Package repository contains the Postgres data-access layer.
//
// Week 1 used raw pgx queries; week 2 keeps that pattern. Once the SQL surface
// grows enough that hand-rolling scans hurts, migrate to sqlc — the queries
// are already authored in db/queries/*.sql and the config in sqlc.yaml. Each
// repo's public method signatures are designed to remain stable across that
// switch.
package repository

import "github.com/jackc/pgx/v5/pgxpool"

// Repos is the bag of repositories the master uses.
type Repos struct {
	Users     *UserRepo
	Spiders   *SpiderRepo
	Tasks     *TaskRepo
	Items     *ItemRepo
	Artifacts *ArtifactsRepo
}

func New(pool *pgxpool.Pool) *Repos {
	return &Repos{
		Users:     NewUserRepo(pool),
		Spiders:   NewSpiderRepo(pool),
		Tasks:     NewTaskRepo(pool),
		Items:     NewItemRepo(pool),
		Artifacts: NewArtifactsRepo(pool),
	}
}

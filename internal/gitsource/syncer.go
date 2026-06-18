// Package gitsource implements spider.SourceSyncer over Git: clones the
// spider's repository at the configured branch into a temp dir, zips the
// working tree (excluding .git/), and uploads the zip to MinIO under a
// versioned key.
//
// Auth is intentionally simple — we honor URL-embedded credentials
// (https://user:token@host/repo.git). HTTPS-only for now; SSH adds key
// management complexity we don't need yet.
package gitsource

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/yourteam/crawler-lite/internal/spider"
	"github.com/yourteam/crawler-lite/internal/storage"
)

type Syncer struct {
	store *storage.MinIOClient
	log   *slog.Logger
}

func New(store *storage.MinIOClient, log *slog.Logger) *Syncer {
	return &Syncer{store: store, log: log}
}

// Sync implements spider.SourceSyncer.
func (s *Syncer) Sync(ctx context.Context, sp *spider.Spider) (string, string, int32, error) {
	if sp.GitURL == "" {
		return "", "", 0, fmt.Errorf("spider has no git_url")
	}

	tmp, err := os.MkdirTemp("", fmt.Sprintf("crawler-spider-%d-*", sp.ID))
	if err != nil {
		return "", "", 0, fmt.Errorf("mktmp: %w", err)
	}
	defer os.RemoveAll(tmp)

	branch := sp.GitBranch
	if branch == "" {
		branch = "main"
	}

	// Shallow single-branch clone — we don't need history.
	repo, err := git.PlainCloneContext(ctx, tmp, false, &git.CloneOptions{
		URL:           sp.GitURL,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
		Depth:         1,
	})
	if err != nil {
		return "", "", 0, fmt.Errorf("clone: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return "", "", 0, fmt.Errorf("head: %w", err)
	}
	commit := head.Hash().String()

	zipBytes, err := zipDirExcludingGit(tmp)
	if err != nil {
		return "", "", 0, fmt.Errorf("zip: %w", err)
	}

	newVer := sp.SourceVersion + 1
	key := fmt.Sprintf("spiders/%d/source/v%d.zip", sp.ID, newVer)
	if err := s.store.Upload(ctx, key, zipBytes, "application/zip"); err != nil {
		return "", "", 0, fmt.Errorf("upload: %w", err)
	}

	return key, commit, newVer, nil
}

// zipDirExcludingGit walks `root` and returns a zip of every regular file
// under it, with `.git/` excluded. Symlinks become regular files (we
// dereference them) — this matches what users want: a runnable copy.
func zipDirExcludingGit(root string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// Normalize path separators in the zip — readers expect forward slashes.
		rel = strings.ReplaceAll(rel, string(os.PathSeparator), "/")

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		w, err := zw.Create(rel)
		if err != nil {
			return err
		}
		if _, err := w.Write(body); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

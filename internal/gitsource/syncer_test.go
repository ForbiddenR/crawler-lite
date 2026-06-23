package gitsource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZipDirExcludingGitRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spider.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatalf("write spider: %v", err)
	}
	if err := os.Symlink("/proc/self/environ", filepath.Join(root, "leak-env")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, err := zipDirExcludingGit(root)
	if err == nil {
		t.Fatal("expected symlink rejection error")
	}
	if !strings.Contains(err.Error(), "symlinks are not allowed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "leak-env") {
		t.Fatalf("error does not name symlink path: %v", err)
	}
}

package push

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Local writes payload to a local filesystem path. The directory is created
// if missing; the write is atomic via tmpfile + rename within the same
// directory (so on POSIX it crosses no filesystem boundary).
type Local struct {
	Path string
}

func (l Local) Push(_ context.Context, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	tmp := l.Path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, l.Path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (l Local) Describe() string { return "local:" + l.Path }

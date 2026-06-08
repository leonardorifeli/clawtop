// Package history persists a compact per-poll record of the rate-limit windows
// so the daemon can report how today compares to the recent past. It is local
// daemon state: the derived deltas travel to viewers inside the status payload,
// so the file itself never needs to be shipped or read by the viewer.
package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Sample is one observation of the account-scoped rate-limit windows.
type Sample struct {
	TS   int64   `json:"ts"`
	Sess float64 `json:"session"`
	Week float64 `json:"week"`
}

// Store is an append-only JSONL file of samples for one machine.
type Store struct{ path string }

// New creates (or opens) the store under dir. An empty dir disables history
// (returns nil, nil) so callers can treat the feature as opt-out cleanly.
func New(dir, machine string) (*Store, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, "history-"+machine+".jsonl")}, nil
}

// Append writes one sample. O(1) via O_APPEND; safe to call every poll.
func (s *Store) Append(ts int64, sess, week float64) error {
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(Sample{TS: ts, Sess: sess, Week: week})
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// load reads and time-sorts every sample. Missing file yields an empty slice.
func (s *Store) load() []Sample {
	f, err := os.Open(s.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Sample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var sm Sample
		if json.Unmarshal(line, &sm) == nil {
			out = append(out, sm)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

// At returns the most recent sample at or before (now - ago). ok is false when
// no sample is old enough yet (e.g. the daemon just started).
func (s *Store) At(now time.Time, ago time.Duration) (Sample, bool) {
	target := now.Add(-ago).Unix()
	var best Sample
	found := false
	for _, sm := range s.load() {
		if sm.TS <= target {
			best, found = sm, true
			continue
		}
		break
	}
	return best, found
}

// Prune drops samples older than maxAge and rewrites the file atomically. Meant
// to be called once at startup so the file stays bounded across restarts.
func (s *Store) Prune(now time.Time, maxAge time.Duration) error {
	all := s.load()
	cutoff := now.Add(-maxAge).Unix()
	kept := all[:0]
	for _, sm := range all {
		if sm.TS >= cutoff {
			kept = append(kept, sm)
		}
	}
	if len(kept) == len(all) {
		return nil // nothing to drop
	}
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, sm := range kept {
		b, _ := json.Marshal(sm)
		w.Write(append(b, '\n'))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

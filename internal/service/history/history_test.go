package history

import (
	"testing"
	"time"
)

func TestAppendAndAt(t *testing.T) {
	s, err := New(t.TempDir(), "omen")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	// Samples at -48h, -24h, -1h relative to now.
	must(t, s.Append(now.Add(-48*time.Hour).Unix(), 10, 20))
	must(t, s.Append(now.Add(-24*time.Hour).Unix(), 30, 40))
	must(t, s.Append(now.Add(-1*time.Hour).Unix(), 50, 60))

	// At ~24h ago: nearest sample at or before is the -24h one.
	got, ok := s.At(now, 24*time.Hour)
	if !ok {
		t.Fatal("expected a sample at -24h")
	}
	if got.Sess != 30 || got.Week != 40 {
		t.Errorf("At(24h) = %+v, want sess=30 week=40", got)
	}
}

func TestAtNoneOldEnough(t *testing.T) {
	s, _ := New(t.TempDir(), "omen")
	now := time.Unix(1_000_000, 0)
	must(t, s.Append(now.Add(-1*time.Hour).Unix(), 5, 5)) // only recent
	if _, ok := s.At(now, 24*time.Hour); ok {
		t.Error("no sample is 24h old; At should report not-found")
	}
}

func TestPruneDropsOld(t *testing.T) {
	s, _ := New(t.TempDir(), "omen")
	now := time.Unix(1_000_000, 0)
	must(t, s.Append(now.Add(-40*24*time.Hour).Unix(), 1, 1)) // older than 30d
	must(t, s.Append(now.Add(-1*time.Hour).Unix(), 2, 2))     // recent
	must(t, s.Prune(now, 30*24*time.Hour))

	all := s.load()
	if len(all) != 1 || all[0].Sess != 2 {
		t.Fatalf("prune should keep only the recent sample, got %+v", all)
	}
}

func TestDisabledStore(t *testing.T) {
	s, err := New("", "omen")
	if err != nil || s != nil {
		t.Fatalf("empty dir should disable cleanly, got store=%v err=%v", s, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

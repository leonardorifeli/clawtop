package alert

import (
	"testing"
	"time"
)

func TestEvaluateThreshold(t *testing.T) {
	cfg := Config{SessionPct: 80, WeekPct: 90}
	got := Evaluate(cfg, []Window{
		{Name: "session", Pct: 85, ResetIn: time.Hour},
		{Name: "week", Pct: 50, ResetIn: 48 * time.Hour},
	})
	if len(got) != 1 || got[0].Key != "session:threshold" {
		t.Fatalf("want one session threshold alert, got %+v", got)
	}
	if got[0].Level != "warning" {
		t.Errorf("85%% should be warning, got %q", got[0].Level)
	}
}

func TestEvaluateUrgentAtHighPct(t *testing.T) {
	got := Evaluate(Config{SessionPct: 80}, []Window{{Name: "session", Pct: 97, ResetIn: time.Hour}})
	if len(got) != 1 || got[0].Level != "urgent" {
		t.Fatalf("97%% should be urgent, got %+v", got)
	}
}

func TestEvaluateProjection(t *testing.T) {
	// 50%, burning 30%/h → hits 100% in ~1.67h, before a 3h reset → alert.
	got := Evaluate(Config{Project: true}, []Window{{Name: "week", Pct: 50, Rate: 30, ResetIn: 3 * time.Hour}})
	if len(got) != 1 || got[0].Key != "week:projection" {
		t.Fatalf("want week projection alert, got %+v", got)
	}
	// Same burn but reset is sooner than the cap → no alert.
	got = Evaluate(Config{Project: true}, []Window{{Name: "week", Pct: 50, Rate: 30, ResetIn: time.Hour}})
	if len(got) != 0 {
		t.Fatalf("reset before cap should not alert, got %+v", got)
	}
	// Projection disabled.
	got = Evaluate(Config{}, []Window{{Name: "week", Pct: 50, Rate: 30, ResetIn: 3 * time.Hour}})
	if len(got) != 0 {
		t.Fatalf("projection off should not alert, got %+v", got)
	}
}

func TestNotifierEdgeTrigger(t *testing.T) {
	n := New(Config{SessionPct: 80}, "", "", nil)
	var fired []string
	n.emit = func(a Alert) { fired = append(fired, a.Key) }

	win := []Window{{Name: "session", Pct: 85, ResetIn: time.Hour}}
	n.Process(win) // crosses → fires
	n.Process(win) // still firing → must NOT fire again
	if len(fired) != 1 {
		t.Fatalf("edge-trigger should fire once while firing, got %d", len(fired))
	}

	n.Process([]Window{{Name: "session", Pct: 10, ResetIn: time.Hour}}) // drops → re-arm
	n.Process(win)                                                      // crosses again → fires
	if len(fired) != 2 {
		t.Fatalf("should re-fire after re-arming, got %d", len(fired))
	}
}

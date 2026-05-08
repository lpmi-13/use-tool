package main

import "testing"

func TestPickUniqueDistractorsBasic(t *testing.T) {
	got := pickUniqueDistractors("foo", []string{"a", "b", "c", "d"}, 3)
	want := []string{"a", "b", "c"}
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d (%v)", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestPickUniqueDistractorsExcludesCorrect(t *testing.T) {
	got := pickUniqueDistractors("a", []string{"a", "b", "c"}, 3)
	for _, s := range got {
		if s == "a" {
			t.Errorf("correct answer leaked into distractors: %v", got)
		}
	}
}

func TestPickUniqueDistractorsDeduplicates(t *testing.T) {
	got := pickUniqueDistractors("x", []string{"a", "a", "b", "b", "c"}, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3 distinct, got %d (%v)", len(got), got)
	}
	seen := map[string]bool{}
	for _, s := range got {
		if seen[s] {
			t.Errorf("duplicate distractor %q in %v", s, got)
		}
		seen[s] = true
	}
}

func TestPickUniqueDistractorsReturnsFewerWhenPoolThin(t *testing.T) {
	got := pickUniqueDistractors("x", []string{"a"}, 3)
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d (%v)", len(got), got)
	}
}

func TestPickUniqueDistractorsAllPoolEqualsCorrect(t *testing.T) {
	got := pickUniqueDistractors("x", []string{"x", "x", "x"}, 3)
	if len(got) != 0 {
		t.Errorf("expected no distractors when whole pool equals correct, got %v", got)
	}
}

func TestMakeRecallQuestionSkipsWhenTooFewDistractors(t *testing.T) {
	// Pool is "x, x, x" against correct "x" → 0 unique distractors → skip
	got := makeRecallQuestion("stem?", "x", []string{"x", "x"})
	if got != nil {
		t.Errorf("expected nil when pool yields no distractors, got %v", got)
	}
}

func TestMakeRecallQuestionAcceptsTwoDistractors(t *testing.T) {
	got := makeRecallQuestion("stem?", "x", []string{"a", "b"})
	if len(got) != 1 {
		t.Fatalf("expected 1 question, got %d", len(got))
	}
	if len(got[0].Distractors) != 2 {
		t.Errorf("expected 2 distractors, got %d", len(got[0].Distractors))
	}
}

func TestMakeRecallQuestionSkipsWhenOnlyOneDistractor(t *testing.T) {
	// Pool that yields just 1 unique distractor → skip (not enough for a quiz)
	got := makeRecallQuestion("stem?", "x", []string{"x", "x", "a"})
	if got != nil {
		t.Errorf("expected nil for 1-distractor pool, got %v", got)
	}
}

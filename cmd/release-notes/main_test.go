package main

import "testing"

func TestValidateRevisionRangeRejectsOptionAndWhitespace(t *testing.T) {
	for _, revisionRange := range []string{"", "--help", "v1.2.3..HEAD with-space", "v1.2.3..HEAD\n"} {
		if err := validateRevisionRange(revisionRange); err == nil {
			t.Fatalf("validateRevisionRange(%q) succeeded, want error", revisionRange)
		}
	}

	if err := validateRevisionRange("v1.2.3..abcdef1234567890"); err != nil {
		t.Fatalf("validateRevisionRange() error = %v", err)
	}
}

func TestParseGitLog(t *testing.T) {
	input := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\x00feat: add thing\x00feat: add thing\n\nBody\n\x00\x1e\nbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\x00fix: patch thing\x00fix: patch thing\n\x00\x1e")

	got := parseGitLog(input)
	if len(got) != 2 {
		t.Fatalf("got %d commits, want 2", len(got))
	}
	if got[0].SHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("first SHA = %q", got[0].SHA)
	}
	if got[0].Subject != "feat: add thing" {
		t.Fatalf("first subject = %q", got[0].Subject)
	}
	if got[0].Body != "feat: add thing\n\nBody" {
		t.Fatalf("first body = %q", got[0].Body)
	}
	if got[1].Subject != "fix: patch thing" {
		t.Fatalf("second subject = %q", got[1].Subject)
	}
}

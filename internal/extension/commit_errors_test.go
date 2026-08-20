package extension

import "testing"

func TestSanitizeCommitItemError_AllowlistAndLeaks(t *testing.T) {
	t.Parallel()

	if got := SanitizeCommitItemError(CommitItemErrorNotAllowed); got != CommitItemErrorNotAllowed {
		t.Fatalf("SanitizeCommitItemError(not allowed) = %q", got)
	}
	if got := SanitizeCommitItemError(CommitItemErrorAddFailed); got != CommitItemErrorAddFailed {
		t.Fatalf("SanitizeCommitItemError(add failed) = %q", got)
	}
	if got := SanitizeCommitItemError("engine failed https://download.fixture.invalid/?token=leak apr-x r-9"); got != CommitItemErrorAddFailed {
		t.Fatalf("SanitizeCommitItemError(leaky) = %q, want %q", got, CommitItemErrorAddFailed)
	}
	if got := SanitizeCommitItemError("retry-later"); got != CommitItemErrorAddFailed {
		t.Fatalf("SanitizeCommitItemError(retry-later) = %q, want allowlist remap not substring r-", got)
	}
}

func TestSanitizeCommitItemErrors_MapsEveryValue(t *testing.T) {
	t.Parallel()

	got := SanitizeCommitItemErrors(map[string]string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": "engine failed https://download.fixture.invalid/?token=leak",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb": CommitItemErrorNotAllowed,
	})
	if got["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"] != CommitItemErrorAddFailed {
		t.Fatalf("leaky mapped to %q", got["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"])
	}
	if got["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"] != CommitItemErrorNotAllowed {
		t.Fatalf("policy mapped to %q", got["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"])
	}
}

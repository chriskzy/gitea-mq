package forge

import (
	"slices"
	"testing"
)

func TestPullRequestOnlyContexts(t *testing.T) {
	got := PullRequestOnlyContexts([]string{
		"ci / build (pull_request)",
		"ci / build (push)",
		"lint / lint (pull_request_target)",
		"ci/woodpecker",
		"ci / *",
	})
	want := []string{"ci / build (pull_request)", "lint / lint (pull_request_target)"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

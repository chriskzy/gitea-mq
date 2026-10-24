package forge

import (
	"slices"
	"testing"
)

func ids(c []ReviewRef) []int64 {
	out := make([]int64, len(c))
	for i, r := range c {
		out[i] = r.ID
	}
	return out
}

func TestFoldAtHead(t *testing.T) {
	approve := func(id int64, u, sha string) ReviewVerdict { return ReviewVerdict{id, u, sha, VerdictApprove, false} }
	reject := func(id int64, u, sha string) ReviewVerdict {
		return ReviewVerdict{id, u, sha, VerdictRequestChanges, false}
	}
	for _, tc := range []struct {
		name      string
		reviews   []ReviewVerdict
		approvals int
		blocked   bool
		conflicts []int64
	}{
		{"older head ignored", []ReviewVerdict{approve(1, "a", "old"), reject(2, "b", "old")}, 0, false, nil},
		{"same user flips", []ReviewVerdict{approve(1, "bot", "h"), reject(2, "bot", "h")}, 0, true, []int64{1, 2}},
		{"re-approval clears", []ReviewVerdict{approve(1, "bot", "h"), reject(2, "bot", "h"), approve(3, "bot", "h")}, 1, false, nil},
		{"users disagree", []ReviewVerdict{approve(1, "a", "h"), reject(2, "b", "h")}, 1, true, []int64{1, 2}},
		{"reject only", []ReviewVerdict{reject(2, "b", "h")}, 0, true, nil},
		{"approval on old head does not conflict", []ReviewVerdict{approve(1, "a", "old"), reject(2, "b", "h")}, 0, true, nil},
		{"dismiss clears", []ReviewVerdict{approve(1, "a", "h"), reject(2, "a", "h"), {3, "a", "", VerdictDismiss, false}}, 0, false, nil},
		{"superseded approval shows flip", []ReviewVerdict{{1, "bot", "h", VerdictApprove, true}, reject(2, "bot", "h")}, 0, true, []int64{1, 2}},
		{"history alone never counts", []ReviewVerdict{{1, "a", "h", VerdictApprove, true}}, 0, false, nil},
		{"same user counts once", []ReviewVerdict{approve(1, "a", "h"), approve(2, "a", "h")}, 1, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rv := FoldAtHead(tc.reviews, "h")
			if rv.Approvals != tc.approvals || rv.ChangesRequested != tc.blocked || !slices.Equal(ids(rv.Conflicts), tc.conflicts) {
				t.Fatalf("got %+v", rv)
			}
		})
	}
}

func TestWaitingStatus(t *testing.T) {
	rv := FoldAtHead([]ReviewVerdict{{7, "a", "h", VerdictApprove, false}, {9, "a", "h", VerdictRequestChanges, false}}, "h")
	if got, want := WaitingStatus(rv, 1), "Conflicting reviews at head: #7 approved, #9 changes requested"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := WaitingStatus(Reviews{Approvals: 1}, 2); got != "Waiting for approval (1/2)" {
		t.Fatal(got)
	}
	if got := (ApprovalPolicy{Count: 1, DistinctReviewers: 2}).Needed(t.Context(), nil, "", "", ""); got != 2 {
		t.Fatalf("distinct reviewer floor ignored: %d", got)
	}
}

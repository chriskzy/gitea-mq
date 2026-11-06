package notify

import (
	"testing"

	"github.com/Mic92/gitea-mq/internal/queue"
)

func TestHeadDiagnosisRequiresExplicitOutboundSubscription(t *testing.T) {
	for _, tc := range []struct {
		kinds []string
		want  int
	}{
		{nil, 0},
		{[]string{"ejected"}, 0},
		{[]string{"pr_head_failed"}, 1},
	} {
		n := New("https://receiver.invalid", nil, tc.kinds)
		n.Observe("gitea:org/app", queue.Event{Kind: queue.EventPRHeadFailed})
		if len(n.ch) != tc.want {
			t.Fatalf("kinds=%v deliveries=%d", tc.kinds, len(n.ch))
		}
	}
}

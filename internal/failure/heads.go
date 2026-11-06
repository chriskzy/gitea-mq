package failure

import (
	"sync"

	"github.com/Mic92/gitea-mq/internal/forge"
)

// HeadTracker restricts diagnostic wakeups to observed eligible PR heads.
// It contains no log content. A nil tracker disables diagnostic wakeups.
type HeadTracker struct {
	mu    sync.RWMutex
	heads map[string]bool
}

func (t *HeadTracker) Update(prs []forge.PR) {
	if t == nil {
		return
	}
	heads := make(map[string]bool, len(prs))
	for _, pr := range prs {
		heads[pr.HeadSHA] = true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.heads = heads
}

func (t *HeadTracker) Contains(sha string) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.heads[sha]
}

package forge

import (
	"context"
	"time"
)

// BatchInput is grouping advice, never CI qualification or review evidence.
// Nil durations mean unavailable, not zero-cost work.
type BatchInput struct {
	RoutingBaseSHA string
	WorkflowPaths  map[string]string
	TimingSources  map[string]string
	HeadSHA        string
	Files          []string
	Suites         []string
	Seconds        map[string]float64
	Isolate        bool
	Blocked        bool
}

type BatchCandidate struct {
	Number  int64
	HeadSHA string
}

// BatchAdvisor reads routing from the target tree, never executes PR source.
type BatchAdvisor interface {
	BatchInputs(context.Context, string, string, string, string, []BatchCandidate) ([]BatchInput, error)
}

const BatchHardMax = 10
const BatchMaxWait = 90 * time.Minute

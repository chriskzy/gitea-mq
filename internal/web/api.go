package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/Mic92/gitea-mq/internal/queue"
)

// RepoIDLookup resolves "<forge>:<owner>/<name>" to the repo's DB id.
type RepoIDLookup func(key string) (int64, bool)

const (
	defaultEventLimit = 100
	maxEventLimit     = 1000
)

// apiEvent is the public shape of a queue event.
type apiEvent struct {
	ID           int64           `json:"id"`
	Time         time.Time       `json:"time"`
	Repo         string          `json:"repo"`
	Kind         string          `json:"kind"`
	Reason       string          `json:"reason,omitempty"`
	PR           *int64          `json:"pr,omitempty"`
	Batch        *int64          `json:"batch,omitempty"`
	TargetBranch string          `json:"target_branch,omitempty"`
	HeadSHA      string          `json:"head_sha,omitempty"`
	Detail       json.RawMessage `json:"detail,omitempty"`
}

// EventsHandler serves GET /api/events and
// GET /api/v1/repos/{forge}/{owner}/{name}/events: queue events in id order.
//
// Query parameters (all optional): repo=<forge>:<owner>/<name> (on the
// unscoped route), kind,
// after=<id> (cursor: pass the last id seen), since=<duration or RFC3339>,
// limit (default 100, max 1000). The response carries next_after so
// consumers can poll for new events, e.g. kind=ejected.
func EventsHandler(svc *queue.Service, repos RepoIDLookup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := queue.EventFilter{Kind: queue.EventKind(q.Get("kind")), Limit: defaultEventLimit}

		var ok bool
		if f.RepoID, ok = repoFilter(w, repoKey(r), repos); !ok {
			return
		}
		if s := q.Get("after"); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n < 0 {
				http.Error(w, "after: invalid id", http.StatusBadRequest)
				return
			}
			f.AfterID = n
		}
		if s := q.Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n <= 0 {
				http.Error(w, "limit: must be a positive integer", http.StatusBadRequest)
				return
			}
			f.Limit = int32(min(n, maxEventLimit))
		}
		since, err := parseSince(q.Get("since"), time.Time{})
		if err != nil {
			http.Error(w, "since: "+err.Error(), http.StatusBadRequest)
			return
		}
		f.Since = since

		rows, err := svc.ListEvents(r.Context(), f)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		out := struct {
			Events    []apiEvent `json:"events"`
			NextAfter int64      `json:"next_after"`
		}{Events: make([]apiEvent, 0, len(rows)), NextAfter: f.AfterID}
		for _, row := range rows {
			ev := apiEvent{
				ID: row.ID, Time: row.CreatedAt.Time, Repo: row.Forge + ":" + row.Owner + "/" + row.Name,
				Kind: row.Kind, Reason: row.Reason, TargetBranch: row.TargetBranch, HeadSHA: row.HeadSha,
			}
			if row.PrNumber.Valid {
				ev.PR = &row.PrNumber.Int64
			}
			if row.BatchID.Valid {
				ev.Batch = &row.BatchID.Int64
			}
			if len(row.Detail) > 0 && string(row.Detail) != "{}" {
				ev.Detail = row.Detail
			}
			out.Events = append(out.Events, ev)
			out.NextAfter = row.ID
		}
		writeAPIJSON(w, out)
	}
}

// EventSummaryHandler serves GET /api/events/summary: event counts per repo,
// kind and reason over a window (since, default 24h; optional repo).
func EventSummaryHandler(svc *queue.Service, repos RepoIDLookup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		repoID, ok := repoFilter(w, repoKey(r), repos)
		if !ok {
			return
		}
		since, err := parseSince(q.Get("since"), time.Now().Add(-24*time.Hour))
		if err != nil {
			http.Error(w, "since: "+err.Error(), http.StatusBadRequest)
			return
		}
		rows, err := svc.SummarizeEvents(r.Context(), since, repoID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		type count struct {
			Repo   string `json:"repo"`
			Kind   string `json:"kind"`
			Reason string `json:"reason,omitempty"`
			Count  int64  `json:"count"`
		}
		out := struct {
			Since  time.Time `json:"since"`
			Counts []count   `json:"counts"`
		}{Since: since, Counts: make([]count, 0, len(rows))}
		for _, row := range rows {
			out.Counts = append(out.Counts, count{
				Repo: row.Forge + ":" + row.Owner + "/" + row.Name, Kind: row.Kind, Reason: row.Reason, Count: row.Count,
			})
		}
		writeAPIJSON(w, out)
	}
}

// repoKey is the repo the request is scoped to: the path segments of the
// /api/v1/repos/{forge}/{owner}/{name}/... routes, else ?repo=.
func repoKey(r *http.Request) string {
	if f := r.PathValue("forge"); f != "" {
		return f + ":" + r.PathValue("owner") + "/" + r.PathValue("name")
	}
	return r.URL.Query().Get("repo")
}

// apiQueueEntry is the public shape of a queued PR.
type apiQueueEntry struct {
	PR               int64      `json:"pr"`
	Position         int        `json:"position"`
	State            string     `json:"state"`
	HeadSHA          string     `json:"head_sha"`
	Priority         bool       `json:"priority"`
	EnqueuedAt       time.Time  `json:"enqueued_at"`
	TestingStartedAt *time.Time `json:"testing_started_at,omitempty"`
	MergeBranch      string     `json:"merge_branch,omitempty"`
	MergeBranchSHA   string     `json:"merge_branch_sha,omitempty"`
	Batch            *int64     `json:"batch,omitempty"`
}

// QueueHandler serves GET /api/v1/repos/{forge}/{owner}/{name}/queue: the
// repo's live queue per target branch, in queue order.
func QueueHandler(svc *queue.Service, repos RepoIDLookup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := repoKey(r)
		repoID, ok := repos(key)
		if !ok {
			http.Error(w, "repo: not managed", http.StatusNotFound)
			return
		}
		entries, err := svc.ListActiveEntries(r.Context(), repoID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		out := struct {
			Repo     string                     `json:"repo"`
			Branches map[string][]apiQueueEntry `json:"branches"`
		}{Repo: key, Branches: map[string][]apiQueueEntry{}}
		for _, e := range entries {
			list := out.Branches[e.TargetBranch]
			ae := apiQueueEntry{
				PR: e.PrNumber, Position: len(list) + 1, State: string(e.State), HeadSHA: e.PrHeadSha,
				Priority: e.Priority, EnqueuedAt: e.EnqueuedAt.Time,
				MergeBranch: e.MergeBranchName.String, MergeBranchSHA: e.MergeBranchSha.String,
			}
			if e.TestingStartedAt.Valid {
				ae.TestingStartedAt = &e.TestingStartedAt.Time
			}
			if e.ActiveBatchID.Valid {
				ae.Batch = &e.ActiveBatchID.Int64
			}
			out.Branches[e.TargetBranch] = append(list, ae)
		}
		writeAPIJSON(w, out)
	}
}

func repoFilter(w http.ResponseWriter, key string, repos RepoIDLookup) (int64, bool) {
	if key == "" {
		return 0, true
	}
	id, ok := repos(key)
	if !ok {
		http.Error(w, "repo: not managed", http.StatusNotFound)
		return 0, false
	}
	return id, true
}

// parseSince accepts a Go duration ("24h", meaning that long ago) or an
// RFC3339 timestamp.
func parseSince(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Parse(time.RFC3339, s)
}

func writeAPIJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

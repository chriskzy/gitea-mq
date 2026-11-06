package web

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Mic92/gitea-mq/internal/failure"
	"github.com/Mic92/gitea-mq/internal/queue"
)

type diagnosisMember struct {
	PR               int64     `json:"pr"`
	HeadSHA          string    `json:"head_sha"`
	ObservedAt       time.Time `json:"observed_at"`
	ObservationError string    `json:"observation_error,omitempty"`
	Stale            bool      `json:"stale"`
}

type diagnosisGroup struct {
	failure.Diagnosis
	Count   int               `json:"count"`
	Members []diagnosisMember `json:"members"`
}

func groupDiagnoses(heads []queue.HeadDiagnosis) []diagnosisGroup {
	groups := make([]diagnosisGroup, 0)
	byFingerprint := map[string]int{}
	for _, head := range heads {
		for _, d := range head.Failures {
			i, ok := byFingerprint[d.Fingerprint]
			if !ok {
				i = len(groups)
				byFingerprint[d.Fingerprint] = i
				groups = append(groups, diagnosisGroup{Diagnosis: d})
			}
			groups[i].Members = append(groups[i].Members, diagnosisMember{PR: head.PR, HeadSHA: head.HeadSHA, ObservedAt: head.ObservedAt, ObservationError: head.ObservationError, Stale: head.ObservationError != "" || time.Since(head.ObservedAt) > 15*time.Minute})
			groups[i].Count++
		}
	}
	slices.SortFunc(groups, func(a, b diagnosisGroup) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Fingerprint, b.Fingerprint)
	})
	return groups
}

// HeadFailuresHandler serves current observed failures, including PRs that
// have no merge intent. Historical observations remain in /api/events.
func HeadFailuresHandler(svc *queue.Service, repos RepoIDLookup, enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !enabled {
			http.NotFound(w, r)
			return
		}
		key := repoKey(r)
		id, ok := repos(key)
		if !ok {
			http.Error(w, "repo: not managed", http.StatusNotFound)
			return
		}
		heads, err := svc.HeadDiagnoses(r.Context(), id)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeAPIJSON(w, struct {
			Repo         string           `json:"repo"`
			Groups       []diagnosisGroup `json:"groups"`
			Limit        int              `json:"head_limit"`
			LimitReached bool             `json:"head_limit_reached"`
		}{key, groupDiagnoses(heads), 1000, len(heads) == 1000})
	}
}

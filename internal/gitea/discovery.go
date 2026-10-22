package gitea

import (
	"context"
	"log/slog"

	"github.com/Mic92/gitea-mq/internal/forge"
)

// TopicSource lists repos carrying the given topic that the token can manage.
// With auto-setup on that means admin access, because EnsureRepoSetup mutates
// branch protection and webhooks; without it push access is enough.
func TopicSource(c Client, topic string, requireAdmin bool) func(context.Context) ([]forge.RepoRef, error) {
	return func(ctx context.Context) ([]forge.RepoRef, error) {
		repos, err := c.SearchReposByTopic(ctx, topic)
		if err != nil {
			return nil, err
		}
		var out []forge.RepoRef
		for _, r := range repos {
			switch {
			case requireAdmin && !r.Permissions.Admin:
				slog.Debug("discovery: skipping repo without admin access", "repo", r.FullName)
				continue
			case !requireAdmin && !r.Permissions.Push && !r.Permissions.Admin:
				slog.Debug("discovery: skipping repo without push access", "repo", r.FullName)
				continue
			}
			out = append(out, forge.RepoRef{Forge: forge.KindGitea, Owner: r.Owner.Login, Name: r.Name})
		}
		return out, nil
	}
}

package mcpgithub

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// This file holds the mcp-github surface the multi-phase orchestration engine
// drives on-behalf-of a run's owner: creating the run's integration branch and
// opening the branch-to-branch PRs the engine merges (a phase's integration
// target, the merge-forward of main into integration, and the final
// integration->main promotion). The merge itself reuses MergePR. Like the rest
// of this package the client is a thin transport; the engine owns the policy.

// DefaultBranch resolves a repo's default branch (the line main-target phases
// land on and the run's integration branch forks from) via mcp-github's
// get_repo on-behalf-of userEmail. "main" is a logical label in a plan; the
// repo's actual default may be main/master/etc., so the engine resolves it once
// at kickoff rather than assuming.
func (c *Client) DefaultBranch(ctx context.Context, userEmail, owner, name string) (string, error) {
	if c == nil {
		return "", errors.New("mcpgithub: client unavailable")
	}
	userEmail = strings.ToLower(strings.TrimSpace(userEmail))
	if userEmail == "" {
		return "", errors.New("mcpgithub: user email is empty")
	}
	token, err := c.tokenFor(ctx, userEmail)
	if err != nil {
		return "", fmt.Errorf("mint on-behalf-of token: %w", err)
	}
	res, err := c.callTool(ctx, token, "get_repo", map[string]any{"owner": owner, "name": name})
	if err != nil {
		return "", err
	}
	if sc, ok := res["structuredContent"].(map[string]any); ok {
		if b, _ := sc["default_branch"].(string); strings.TrimSpace(b) != "" {
			return b, nil
		}
	}
	return "", fmt.Errorf("mcp-github get_repo: no default_branch for %s/%s", owner, name)
}

// CreateBranch creates `branch` off `base` (default branch when base is empty)
// via mcp-github's create_branch on-behalf-of userEmail. The base SHA is
// resolved server-side at call time (the tool takes no caller SHA). It is
// idempotent: an "already exists" ref is treated as success so creating a run's
// integration branch is safe to retry and safe under the reconcile backstop.
func (c *Client) CreateBranch(ctx context.Context, userEmail, owner, name, branch, base string) error {
	if c == nil {
		return errors.New("mcpgithub: client unavailable")
	}
	userEmail = strings.ToLower(strings.TrimSpace(userEmail))
	if userEmail == "" {
		return errors.New("mcpgithub: user email is empty")
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return errors.New("mcpgithub: branch is empty")
	}
	token, err := c.tokenFor(ctx, userEmail)
	if err != nil {
		return fmt.Errorf("mint on-behalf-of token: %w", err)
	}
	args := map[string]any{"owner": owner, "name": name, "branch": branch}
	if base = strings.TrimSpace(base); base != "" {
		args["base"] = base
	}
	if _, err := c.callTool(ctx, token, "create_branch", args); err != nil {
		if isAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

// CreatedPR is the narrowed create_pull_request result the engine needs to then
// merge the PR (number) and surface it (URL/state).
type CreatedPR struct {
	Number int
	URL    string
	State  string
	// NoDiff is set when the PR could not be opened because head and base carry
	// no different commits (GitHub's "No commits between" 422). For a
	// branch-sync PR that means the branches are already in sync — a clean
	// no-op, not a failure.
	NoDiff bool
}

// CreatePR opens a PR head->base via mcp-github's create_pull_request
// on-behalf-of userEmail. The engine uses it only for branch-to-branch
// promotions (merge-forward of main into integration, final integration->main);
// phase spokes open their own governed PRs. A "No commits between" 422 is
// returned as a non-error CreatedPR with NoDiff=true so the caller can treat an
// already-synced branch as nothing-to-do rather than a hard error.
func (c *Client) CreatePR(ctx context.Context, userEmail, owner, name, title, head, base, body string, draft bool) (CreatedPR, error) {
	if c == nil {
		return CreatedPR{}, errors.New("mcpgithub: client unavailable")
	}
	userEmail = strings.ToLower(strings.TrimSpace(userEmail))
	if userEmail == "" {
		return CreatedPR{}, errors.New("mcpgithub: user email is empty")
	}
	head = strings.TrimSpace(head)
	base = strings.TrimSpace(base)
	if head == "" || base == "" {
		return CreatedPR{}, errors.New("mcpgithub: create pr requires head and base")
	}
	token, err := c.tokenFor(ctx, userEmail)
	if err != nil {
		return CreatedPR{}, fmt.Errorf("mint on-behalf-of token: %w", err)
	}
	args := map[string]any{
		"owner": owner, "name": name, "title": title,
		"head": head, "base": base, "draft": draft,
	}
	if strings.TrimSpace(body) != "" {
		args["body"] = body
	}
	res, err := c.callTool(ctx, token, "create_pull_request", args)
	if err != nil {
		if isNoCommitsBetween(err) {
			return CreatedPR{NoDiff: true}, nil
		}
		return CreatedPR{}, err
	}
	return createdPRFromResult(res), nil
}

func createdPRFromResult(result map[string]any) CreatedPR {
	sc, ok := result["structuredContent"].(map[string]any)
	if !ok {
		return CreatedPR{}
	}
	out := CreatedPR{}
	switch n := sc["number"].(type) {
	case float64:
		out.Number = int(n)
	case int:
		out.Number = n
	}
	out.URL, _ = sc["html_url"].(string)
	out.State, _ = sc["state"].(string)
	return out
}

func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists")
}

func isNoCommitsBetween(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no commits between")
}

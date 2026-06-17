package mcpgithub

import (
	"context"
	"testing"
)

func newOrchTestClient(t *testing.T, fake *fakeMCPServer) *Client {
	t.Helper()
	exURL, mcpURL, stop := fake.start(t)
	t.Cleanup(stop)
	return NewClient(Options{
		ExchangeURL:  exURL,
		MCPGitHubURL: mcpURL,
		ReadToken:    func(string) (string, error) { return "sa.token", nil },
	})
}

// TestMergePRForwardsExpectedHeadSHA proves the autonomous orchestrator's head-
// SHA guard is actually sent to mcp-github when set, and omitted when empty (the
// human-merge surface), so the tool's guard engages only when intended.
func TestMergePRForwardsExpectedHeadSHA(t *testing.T) {
	fake := newFakeMCPServer()
	fake.mcpResponse = `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"merged":true,"sha":"merge-sha"}}}`
	c := newOrchTestClient(t, fake)

	sha, err := c.MergePR(context.Background(), "owner@example.com", "o", "r", 7, "squash", "head-sha-123")
	if err != nil {
		t.Fatalf("MergePR: %v", err)
	}
	if sha != "merge-sha" {
		t.Fatalf("merge sha = %q, want merge-sha", sha)
	}
	if got := fake.lastToolArgs()["expected_head_sha"]; got != "head-sha-123" {
		t.Fatalf("expected_head_sha forwarded = %v, want head-sha-123", got)
	}

	// Empty SHA -> the guard key must be absent (human-merge / unguarded path).
	if _, err := c.MergePR(context.Background(), "owner@example.com", "o", "r", 7, "squash", ""); err != nil {
		t.Fatalf("MergePR (no guard): %v", err)
	}
	if _, present := fake.lastToolArgs()["expected_head_sha"]; present {
		t.Fatalf("expected_head_sha must be absent when empty; args=%v", fake.lastToolArgs())
	}
}

// TestCreateBranchIdempotentAlreadyExists proves an "already exists" ref is
// treated as success, so creating a run's integration branch is safe to retry
// and safe under the reconcile backstop.
func TestCreateBranchIdempotentAlreadyExists(t *testing.T) {
	fake := newFakeMCPServer()
	fake.mcpResponse = `{"jsonrpc":"2.0","id":1,"error":{"message":"create_branch failed: Reference already exists"}}`
	c := newOrchTestClient(t, fake)

	if err := c.CreateBranch(context.Background(), "owner@example.com", "o", "r", "integration", "main"); err != nil {
		t.Fatalf("CreateBranch on already-exists should be nil, got %v", err)
	}
}

// TestCreatePRNoCommitsBetween proves a branch-sync PR with nothing to merge
// (GitHub's "No commits between") is returned as a clean no-op (NoDiff), not an
// error — the engine treats an already-synced branch as nothing-to-do.
func TestCreatePRNoCommitsBetween(t *testing.T) {
	fake := newFakeMCPServer()
	fake.mcpResponse = `{"jsonrpc":"2.0","id":1,"error":{"message":"422 No commits between main and integration"}}`
	c := newOrchTestClient(t, fake)

	pr, err := c.CreatePR(context.Background(), "owner@example.com", "o", "r", "sync", "main", "integration", "body", false)
	if err != nil {
		t.Fatalf("CreatePR no-diff should not error, got %v", err)
	}
	if !pr.NoDiff {
		t.Fatalf("CreatePR NoDiff = false, want true")
	}
}

func TestCreatePRSuccess(t *testing.T) {
	fake := newFakeMCPServer()
	fake.mcpResponse = `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"number":42,"html_url":"https://github.com/o/r/pull/42","state":"open"}}}`
	c := newOrchTestClient(t, fake)

	pr, err := c.CreatePR(context.Background(), "owner@example.com", "o", "r", "sync", "main", "integration", "body", false)
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if pr.Number != 42 || pr.NoDiff {
		t.Fatalf("CreatePR = %+v, want number 42, NoDiff false", pr)
	}
}

func TestDefaultBranch(t *testing.T) {
	fake := newFakeMCPServer()
	fake.mcpResponse = `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"full_name":"o/r","default_branch":"trunk"}}}`
	c := newOrchTestClient(t, fake)

	branch, err := c.DefaultBranch(context.Background(), "owner@example.com", "o", "r")
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if branch != "trunk" {
		t.Fatalf("default branch = %q, want trunk", branch)
	}
}

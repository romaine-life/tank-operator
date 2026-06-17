package mcpgithub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type PullRequestDetail struct {
	Number         int    `json:"number"`
	HTMLURL        string `json:"html_url"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Draft          bool   `json:"draft"`
	Head           struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// PullRequestState is the backend-owned reducer input for CI watches.
//
// Readiness is GitHub's own aggregate (`mergeable_state`): `clean` means
// mergeable and every check green, so there is no per-check reconstruction here.
// The check rollup is read only to (a) name a red for the wake and (b) report
// whether everything has settled, so we wake once with the full failure set
// rather than on the first red. See docs/features/ci-watch/redesign-from-1295-review.md.
type PullRequestState struct {
	PR               PullRequestDetail
	CIStatus         string // "succeeded" | "failed" | "started"
	CIError          string
	CheckState       string // "success" | "failure" | "pending"
	FailingChecks    []string
	PendingChecks    []string
	AllChecksSettled bool
	Mergeable        *bool
	MergeableState   string
	HeadSHA          string
	HTMLURL          string
}

func (s PullRequestState) MergeabilityUnknown() bool {
	state := strings.ToLower(strings.TrimSpace(s.MergeableState))
	return s.Mergeable == nil || state == "" || state == "unknown"
}

type checkRun struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	DetailsURL  string `json:"details_url"`
	HTMLURL     string `json:"html_url"`
	App         struct {
		Slug string `json:"slug"`
	} `json:"app"`
}

type combinedStatus struct {
	State    string         `json:"state"`
	Statuses []commitStatus `json:"statuses"`
}

type commitStatus struct {
	State   string `json:"state"`
	Context string `json:"context"`
}

// ResolvePullRequestState reads GitHub's current PR + mergeability + head check
// rollup using one internally-minted installation token. This is the
// backend-owned reducer input for CI watches; webhook payloads should trigger
// this read, not directly decide whether the watch is terminal.
func (c *Client) ResolvePullRequestState(ctx context.Context, userEmail, owner, name string, number int) (PullRequestState, error) {
	if c == nil {
		return PullRequestState{}, errors.New("mcpgithub: client unavailable")
	}
	userEmail = strings.ToLower(strings.TrimSpace(userEmail))
	if userEmail == "" {
		return PullRequestState{}, errors.New("mcpgithub: user email is empty")
	}
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" || number <= 0 {
		return PullRequestState{}, errors.New("mcpgithub: missing PR coordinates")
	}
	serviceToken, err := c.tokenFor(ctx, userEmail)
	if err != nil {
		return PullRequestState{}, fmt.Errorf("mint on-behalf-of token: %w", err)
	}
	githubToken, err := c.mintGitHubToken(ctx, serviceToken, owner+"/"+name)
	if err != nil {
		return PullRequestState{}, err
	}
	return c.resolvePullRequestStateWithToken(ctx, githubToken, owner, name, number)
}

// ResolveOpenPullRequestState resolves the open PR for owner:branch, then reads
// the same live PR/CI state ResolvePullRequestState returns. Test-slot deploy
// gates use this so they do not run a separate CI/mergeability algorithm.
func (c *Client) ResolveOpenPullRequestState(ctx context.Context, userEmail, owner, name, headOwner, branch string) (PullRequestState, error) {
	if c == nil {
		return PullRequestState{}, errors.New("mcpgithub: client unavailable")
	}
	userEmail = strings.ToLower(strings.TrimSpace(userEmail))
	if userEmail == "" {
		return PullRequestState{}, errors.New("mcpgithub: user email is empty")
	}
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(headOwner) == "" || strings.TrimSpace(branch) == "" {
		return PullRequestState{}, errors.New("mcpgithub: missing PR branch coordinates")
	}
	serviceToken, err := c.tokenFor(ctx, userEmail)
	if err != nil {
		return PullRequestState{}, fmt.Errorf("mint on-behalf-of token: %w", err)
	}
	githubToken, err := c.mintGitHubToken(ctx, serviceToken, owner+"/"+name)
	if err != nil {
		return PullRequestState{}, err
	}
	pr, err := c.githubOpenPullRequestForBranch(ctx, githubToken, owner, name, headOwner, branch)
	if err != nil {
		return PullRequestState{}, err
	}
	return c.resolvePullRequestStateWithToken(ctx, githubToken, owner, name, pr.Number)
}

func (c *Client) resolvePullRequestStateWithToken(ctx context.Context, githubToken, owner, name string, number int) (PullRequestState, error) {
	pr, err := c.githubPullRequest(ctx, githubToken, owner, name, number)
	if err != nil {
		return PullRequestState{}, err
	}
	out := PullRequestState{
		PR:             pr,
		Mergeable:      pr.Mergeable,
		MergeableState: strings.TrimSpace(pr.MergeableState),
		HeadSHA:        strings.TrimSpace(pr.Head.SHA),
		HTMLURL:        strings.TrimSpace(pr.HTMLURL),
		CheckState:     "pending",
		CIStatus:       "started",
		CIError:        "checks have not appeared yet",
	}
	if out.HeadSHA == "" {
		out.CIError = "PR head SHA is unavailable"
		return out, nil
	}
	failing, pending, observed := c.resolveHeadChecks(ctx, githubToken, owner, name, out.HeadSHA)
	out.FailingChecks = failing
	out.PendingChecks = pending
	// "settled" = at least one check observed and none still pending/queued. A
	// head with no checks yet is deliberately NOT settled and NOT success: that
	// guards the post-push window where mergeable_state can be momentarily clean
	// before the checks register.
	out.AllChecksSettled = observed && len(pending) == 0
	switch {
	case len(failing) > 0:
		out.CheckState = "failure"
		out.CIStatus = "failed"
		out.CIError = strings.Join(failing, "; ")
	case len(pending) > 0:
		out.CheckState = "pending"
		out.CIStatus = "started"
		out.CIError = "pending checks: " + strings.Join(pending, ", ")
	case observed:
		out.CheckState = "success"
		out.CIStatus = "succeeded"
		out.CIError = ""
	default:
		out.CheckState = "pending"
		out.CIStatus = "started"
		out.CIError = "checks have not appeared yet"
	}
	return out, nil
}

func (c *Client) mintGitHubToken(ctx context.Context, serviceToken, repoSlug string) (string, error) {
	// Mirror the existing session-side watcher: check-runs and workflow metadata
	// need the GitHub App installation token, but the token stays inside the
	// operator process and is never handed to the agent.
	res, err := c.callTool(ctx, serviceToken, "mint_clone_token", map[string]any{
		"repos": []string{repoSlug},
		"write": true,
	})
	if err != nil {
		return "", err
	}
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		return "", errors.New("mcp-github mint_clone_token returned no structuredContent")
	}
	token, _ := sc["token"].(string)
	if strings.TrimSpace(token) == "" {
		return "", errors.New("mcp-github mint_clone_token returned no token")
	}
	return strings.TrimSpace(token), nil
}

func (c *Client) githubPullRequest(ctx context.Context, token, owner, name string, number int) (PullRequestDetail, error) {
	var pr PullRequestDetail
	path := githubRepoPath(owner, name, "/pulls/"+strconv.Itoa(number))
	status, err := c.githubRESTJSON(ctx, token, http.MethodGet, path, &pr)
	if err != nil {
		return PullRequestDetail{}, fmt.Errorf("read PR #%d: %w", number, err)
	}
	if status >= 400 {
		return PullRequestDetail{}, fmt.Errorf("read PR #%d: GitHub returned %d", number, status)
	}
	return pr, nil
}

func (c *Client) githubOpenPullRequestForBranch(ctx context.Context, token, owner, name, headOwner, branch string) (PullRequestDetail, error) {
	var prs []PullRequestDetail
	head := url.QueryEscape(strings.TrimSpace(headOwner) + ":" + strings.TrimSpace(branch))
	path := githubRepoPath(owner, name, "/pulls?head="+head+"&state=open&per_page=2")
	status, err := c.githubRESTJSON(ctx, token, http.MethodGet, path, &prs)
	if err != nil {
		return PullRequestDetail{}, fmt.Errorf("list PRs for %s:%s: %w", headOwner, branch, err)
	}
	if status >= 400 {
		return PullRequestDetail{}, fmt.Errorf("list PRs for %s:%s: GitHub returned %d", headOwner, branch, status)
	}
	if len(prs) == 0 {
		return PullRequestDetail{}, fmt.Errorf("no open PR exists for %s:%s", headOwner, branch)
	}
	if prs[0].Number <= 0 {
		return PullRequestDetail{}, fmt.Errorf("open PR for %s:%s did not include a PR number", headOwner, branch)
	}
	return prs[0], nil
}

// resolveHeadChecks reads the latest check-run per name on the head SHA plus the
// legacy combined status, splitting them into failing and still-pending. It is
// deliberately a thin observer: readiness is mergeable_state, so this only names
// a red (for the wake) and reports whether anything is still running.
func (c *Client) resolveHeadChecks(ctx context.Context, token, owner, name, sha string) (failing []string, pending []string, observed bool) {
	for _, run := range latestCheckRuns(c.githubCheckRunsForSHA(ctx, token, owner, name, sha)) {
		observed = true
		runName := checkRunName(run)
		if run.Status != "completed" {
			pending = append(pending, runName)
			continue
		}
		if !checkRunConclusionOK(run.Conclusion) {
			conclusion := run.Conclusion
			if conclusion == "" {
				conclusion = "failed"
			}
			failing = append(failing, runName+": "+conclusion)
		}
	}
	if status := c.githubCombinedStatusForSHA(ctx, token, owner, name, sha); status != nil {
		for _, st := range status.Statuses {
			observed = true
			context := st.Context
			if context == "" {
				context = "status"
			}
			switch st.State {
			case "failure", "error":
				failing = append(failing, context+": "+st.State)
			case "pending":
				pending = append(pending, context)
			}
		}
	}
	sort.Strings(failing)
	sort.Strings(pending)
	return failing, pending, observed
}

func (c *Client) githubCheckRunsForSHA(ctx context.Context, token, owner, name, sha string) []checkRun {
	var body struct {
		CheckRuns []checkRun `json:"check_runs"`
	}
	status, err := c.githubRESTJSON(ctx, token, http.MethodGet, githubRepoPath(owner, name, "/commits/"+url.PathEscape(sha)+"/check-runs?per_page=100"), &body)
	if err != nil || status >= 400 {
		return nil
	}
	return body.CheckRuns
}

func (c *Client) githubCombinedStatusForSHA(ctx context.Context, token, owner, name, sha string) *combinedStatus {
	var body combinedStatus
	status, err := c.githubRESTJSON(ctx, token, http.MethodGet, githubRepoPath(owner, name, "/commits/"+url.PathEscape(sha)+"/status"), &body)
	if err != nil || status >= 400 {
		return nil
	}
	return &body
}

func (c *Client) githubRESTJSON(ctx context.Context, token, method, path string, dest any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.githubAPI, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return resp.StatusCode, fmt.Errorf("GitHub returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if dest != nil {
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func githubRepoPath(owner, name, suffix string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + suffix
}

func latestCheckRuns(runs []checkRun) []checkRun {
	latest := map[string]checkRun{}
	for _, run := range runs {
		name := checkRunName(run)
		existing, ok := latest[name]
		if !ok || checkRunRecency(run) >= checkRunRecency(existing) {
			latest[name] = run
		}
	}
	out := make([]checkRun, 0, len(latest))
	for _, name := range sortedCheckRunNames(latest) {
		out = append(out, latest[name])
	}
	return out
}

func checkRunName(run checkRun) string {
	if run.Name != "" {
		return run.Name
	}
	if run.App.Slug != "" {
		return run.App.Slug
	}
	return "check"
}

func checkRunRecency(run checkRun) string {
	t := run.StartedAt
	if t == "" {
		t = run.CompletedAt
	}
	return t + "\x00" + fmt.Sprintf("%020d", run.ID)
}

func checkRunConclusionOK(conclusion string) bool {
	switch conclusion {
	case "success", "skipped", "neutral":
		return true
	default:
		return false
	}
}

func sortedCheckRunNames(m map[string]checkRun) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

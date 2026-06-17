package mcpgithub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
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
	Head           struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type PullRequestState struct {
	PR             PullRequestDetail
	CIStatus       string
	CIError        string
	CIPayload      map[string]any
	CheckState     string
	FailingChecks  []string
	Mergeable      *bool
	MergeableState string
	HeadSHA        string
	HTMLURL        string
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

type actionRunInfo struct {
	Path  string `json:"path"`
	Event string `json:"event"`
}

// ResolvePullRequestState reads GitHub's current PR, mergeability, and CI state
// using one internally-minted installation token. This is the backend-owned
// reducer input for CI watches; webhook payloads should trigger this read, not
// directly decide whether the watch is terminal.
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
		CIPayload:      map[string]any{},
	}
	if out.HeadSHA == "" {
		out.CIError = "PR head SHA is unavailable"
		return out, nil
	}
	ciStatus, ciError, payload := c.resolveCIState(ctx, githubToken, owner, name, out.HeadSHA, number, pr.Head.Ref)
	out.CIStatus = ciStatus
	out.CIError = ciError
	out.CIPayload = payload
	switch ciStatus {
	case "succeeded":
		out.CheckState = "success"
	case "failed":
		out.CheckState = "failure"
	default:
		out.CheckState = "pending"
	}
	if failed, ok := payload["failed"].([]string); ok {
		for _, item := range failed {
			name := strings.TrimSpace(strings.SplitN(item, ":", 2)[0])
			if name != "" {
				out.FailingChecks = append(out.FailingChecks, name)
			}
		}
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

func (c *Client) resolveCIState(ctx context.Context, token, owner, name, sha string, prNumber int, branch string) (string, string, map[string]any) {
	checkRuns := c.githubCheckRunsForSHA(ctx, token, owner, name, sha)
	combined := c.githubCombinedStatusForSHA(ctx, token, owner, name, sha)
	headLatest := map[string]checkRun{}
	for _, run := range latestCheckRuns(checkRuns) {
		headLatest[checkRunName(run)] = run
	}
	ciStatus, ciError, basePayload := checksState(checkRuns, combined)
	evidence := []map[string]any{}
	failed := []string{}
	pending := []string{}
	for _, name := range sortedCheckRunNames(headLatest) {
		run := headLatest[name]
		if problem := checkRunFailure(run); problem != "" {
			if strings.HasSuffix(problem, ": pending") {
				pending = append(pending, name)
				evidence = append(evidence, map[string]any{"check": name, "status": "pending", "head_sha": sha, "reason": "observed_on_head"})
			} else {
				failed = append(failed, problem)
				evidence = append(evidence, map[string]any{"check": name, "status": "failed", "head_sha": sha, "reason": problem})
			}
		} else {
			evidence = append(evidence, map[string]any{"check": name, "status": "satisfied", "head_sha": sha, "satisfied_by_sha": sha, "reason": "exact_head_success"})
		}
	}
	if len(failed) > 0 {
		return "failed", strings.Join(failed, "; "), mergeCIMaps(basePayload, map[string]any{"evidence": evidence, "policy_source": "observed_head_checks"})
	}
	if len(pending) > 0 {
		return "started", "pending checks: " + strings.Join(pending, ", "), mergeCIMaps(basePayload, map[string]any{"evidence": evidence, "policy_source": "observed_head_checks"})
	}

	priorLatest := map[string]checkRun{}
	priorRunsByName := map[string]struct {
		sha string
		run checkRun
	}{}
	if prNumber > 0 {
		for _, priorSHA := range c.githubPRCommitSHAs(ctx, token, owner, name, prNumber, sha) {
			for _, run := range c.githubCheckRunsForSHA(ctx, token, owner, name, priorSHA) {
				runName := checkRunName(run)
				existing, ok := priorLatest[runName]
				if !ok || checkRunRecency(run) >= checkRunRecency(existing) {
					priorLatest[runName] = run
					priorRunsByName[runName] = struct {
						sha string
						run checkRun
					}{sha: priorSHA, run: run}
				}
			}
		}
	}

	missingBlockers := []string{}
	runInfoCache := map[int64]actionRunInfo{}
	workflowCache := map[string]workflowPathFilters{}
	changedCache := map[string][]string{}
	for _, runName := range sortedPriorRunNames(priorRunsByName) {
		if _, ok := headLatest[runName]; ok {
			continue
		}
		prior := priorRunsByName[runName]
		if !checkRunIsGreen(prior.run) {
			problem := checkRunFailure(prior.run)
			if problem == "" {
				problem = runName + ": prior run was not green"
			}
			missingBlockers = append(missingBlockers, fmt.Sprintf("%s missing on HEAD after non-green prior run (%s)", runName, problem))
			evidence = append(evidence, map[string]any{"check": runName, "status": "missing_no_prior_success", "head_sha": sha, "satisfied_by_sha": prior.sha, "reason": problem})
			continue
		}
		runID := checkRunActionsRunID(prior.run)
		runInfo, ok := runInfoCache[runID]
		if !ok {
			runInfo = c.githubActionRunInfo(ctx, token, owner, name, runID)
			runInfoCache[runID] = runInfo
		}
		if runInfo.Event != "" && runInfo.Event != "pull_request" && runInfo.Event != "pull_request_target" {
			continue
		}
		workflowPath := runInfo.Path
		if workflowPath == "" {
			missingBlockers = append(missingBlockers, runName+" missing on HEAD; could not identify the prior run's workflow")
			evidence = append(evidence, map[string]any{"check": runName, "status": "missing_no_workflow", "head_sha": sha, "satisfied_by_sha": prior.sha, "reason": "workflow_path_unavailable"})
			continue
		}
		filters, ok := workflowCache[workflowPath]
		if !ok {
			workflowText := c.githubFileTextAtRef(ctx, token, owner, name, workflowPath, sha)
			if workflowText == "" {
				filters = workflowPathFilters{parseReason: "workflow file is absent on HEAD"}
			} else {
				filters = workflowPathFiltersFromYAML(workflowText)
			}
			workflowCache[workflowPath] = filters
		}
		if filters.parseReason != "" {
			missingBlockers = append(missingBlockers, fmt.Sprintf("%s missing on HEAD; %s", runName, filters.parseReason))
			evidence = append(evidence, map[string]any{"check": runName, "status": "missing_uninspectable_inputs", "head_sha": sha, "satisfied_by_sha": prior.sha, "workflow_path": workflowPath, "reason": filters.parseReason})
			continue
		}
		if filters.include == nil && filters.ignore == nil {
			missingBlockers = append(missingBlockers, fmt.Sprintf("%s missing on HEAD; workflow %s has no pull_request path filter", runName, workflowPath))
			evidence = append(evidence, map[string]any{"check": runName, "status": "missing_unfiltered_workflow", "head_sha": sha, "satisfied_by_sha": prior.sha, "workflow_path": workflowPath, "reason": "exact_head_run_required"})
			continue
		}
		changedPaths, ok := changedCache[prior.sha]
		if !ok {
			changedPaths = c.githubChangedPaths(ctx, token, owner, name, prior.sha, sha)
			changedCache[prior.sha] = changedPaths
		}
		if containsString(changedPaths, workflowPath) {
			missingBlockers = append(missingBlockers, fmt.Sprintf("%s missing on HEAD; workflow file %s changed since %s", runName, workflowPath, shortSHA(prior.sha)))
			evidence = append(evidence, map[string]any{"check": runName, "status": "missing_changed_inputs", "head_sha": sha, "satisfied_by_sha": prior.sha, "workflow_path": workflowPath, "changed_paths": changedPaths, "reason": "workflow_file_changed"})
			continue
		}
		if workflowWouldRunForPaths(changedPaths, filters.include, filters.ignore) {
			matching := matchingWorkflowPaths(changedPaths, filters.include)
			if len(matching) == 0 {
				matching = changedPaths
			}
			missingBlockers = append(missingBlockers, fmt.Sprintf("%s missing on HEAD; inputs changed since %s: %s", runName, shortSHA(prior.sha), strings.Join(firstStrings(matching, 8), ", ")))
			evidence = append(evidence, map[string]any{"check": runName, "status": "missing_changed_inputs", "head_sha": sha, "satisfied_by_sha": prior.sha, "workflow_path": workflowPath, "changed_paths": changedPaths, "reason": "trigger_paths_changed"})
			continue
		}
		evidence = append(evidence, map[string]any{
			"check":            runName,
			"status":           "satisfied",
			"head_sha":         sha,
			"satisfied_by_sha": prior.sha,
			"workflow_path":    workflowPath,
			"changed_paths":    changedPaths,
			"reason":           "paths_unchanged_since_success",
		})
	}

	payload := mergeCIMaps(basePayload, map[string]any{
		"evidence":      evidence,
		"policy_source": map[bool]string{true: "pr_check_history_with_workflow_paths", false: "observed_head_checks"}[prNumber > 0],
		"branch":        branch,
	})
	if len(missingBlockers) > 0 {
		return "started", strings.Join(missingBlockers, "; "), mergeCIMaps(payload, map[string]any{"missing": missingBlockers})
	}
	if len(evidence) > 0 {
		return "succeeded", "all required checks satisfied", payload
	}
	if ciStatus != "succeeded" {
		return ciStatus, ciError, payload
	}
	return "succeeded", "all observed checks passed", payload
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

func (c *Client) githubPRCommitSHAs(ctx context.Context, token, owner, name string, prNumber int, headSHA string) []string {
	var body []struct {
		SHA string `json:"sha"`
	}
	status, err := c.githubRESTJSON(ctx, token, http.MethodGet, githubRepoPath(owner, name, "/pulls/"+strconv.Itoa(prNumber)+"/commits?per_page=100"), &body)
	if err != nil || status >= 400 {
		return nil
	}
	out := []string{}
	for _, item := range body {
		if item.SHA != "" && item.SHA != headSHA {
			out = append(out, item.SHA)
		}
	}
	return out
}

func (c *Client) githubActionRunInfo(ctx context.Context, token, owner, name string, runID int64) actionRunInfo {
	if runID <= 0 {
		return actionRunInfo{}
	}
	var body actionRunInfo
	status, err := c.githubRESTJSON(ctx, token, http.MethodGet, githubRepoPath(owner, name, "/actions/runs/"+strconv.FormatInt(runID, 10)), &body)
	if err != nil || status >= 400 {
		return actionRunInfo{}
	}
	return body
}

func (c *Client) githubFileTextAtRef(ctx context.Context, token, owner, name, path, ref string) string {
	var body struct {
		Content string `json:"content"`
	}
	escapedPath := escapePathPreservingSlashes(path)
	endpoint := githubRepoPath(owner, name, "/contents/"+escapedPath+"?ref="+url.QueryEscape(ref))
	status, err := c.githubRESTJSON(ctx, token, http.MethodGet, endpoint, &body)
	if err != nil || status >= 400 || body.Content == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(body.Content, "\n", ""))
	if err != nil {
		return ""
	}
	return string(raw)
}

func (c *Client) githubChangedPaths(ctx context.Context, token, owner, name, baseSHA, headSHA string) []string {
	var body struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	endpoint := githubRepoPath(owner, name, "/compare/"+url.PathEscape(baseSHA)+"..."+url.PathEscape(headSHA))
	status, err := c.githubRESTJSON(ctx, token, http.MethodGet, endpoint, &body)
	if err != nil || status >= 400 {
		return nil
	}
	out := []string{}
	for _, file := range body.Files {
		if file.Filename != "" {
			out = append(out, file.Filename)
		}
	}
	return out
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

func escapePathPreservingSlashes(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
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
	for _, run := range latest {
		out = append(out, run)
	}
	return out
}

func checksState(checkRuns []checkRun, status *combinedStatus) (string, string, map[string]any) {
	failed := []string{}
	pending := []string{}
	completed := 0
	for _, run := range latestCheckRuns(checkRuns) {
		name := checkRunName(run)
		if run.Status != "completed" {
			pending = append(pending, name)
			continue
		}
		completed++
		if !checkRunConclusionOK(run.Conclusion) {
			conclusion := run.Conclusion
			if conclusion == "" {
				conclusion = "failed"
			}
			failed = append(failed, name+": "+conclusion)
		}
	}
	statusCount := 0
	if status != nil {
		statusCount = len(status.Statuses)
		switch status.State {
		case "failure", "error":
			for _, st := range status.Statuses {
				if st.State == "failure" || st.State == "error" {
					context := st.Context
					if context == "" {
						context = "status"
					}
					failed = append(failed, context+": "+st.State)
				}
			}
		case "pending":
			if len(status.Statuses) > 0 {
				pending = append(pending, "combined status")
			}
		}
	}
	payload := map[string]any{"pending": pending, "failed": failed, "completed": completed, "statuses": statusCount}
	if len(failed) > 0 {
		return "failed", strings.Join(failed, "; "), payload
	}
	if len(pending) > 0 || (len(checkRuns) == 0 && statusCount == 0) {
		return "started", "checks are pending or have not appeared yet", payload
	}
	return "succeeded", "all observed checks passed", payload
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

func checkRunIsGreen(run checkRun) bool {
	return run.Status == "completed" && checkRunConclusionOK(run.Conclusion)
}

func checkRunFailure(run checkRun) string {
	name := checkRunName(run)
	if run.Status != "completed" {
		return name + ": pending"
	}
	if !checkRunConclusionOK(run.Conclusion) {
		conclusion := run.Conclusion
		if conclusion == "" {
			conclusion = "failed"
		}
		return name + ": " + conclusion
	}
	return ""
}

var actionRunIDPattern = regexp.MustCompile(`/actions/runs/([0-9]+)`)

func checkRunActionsRunID(run checkRun) int64 {
	for _, raw := range []string{run.DetailsURL, run.HTMLURL} {
		match := actionRunIDPattern.FindStringSubmatch(raw)
		if len(match) == 2 {
			id, _ := strconv.ParseInt(match[1], 10, 64)
			if id > 0 {
				return id
			}
		}
	}
	return 0
}

type workflowPathFilters struct {
	include     []string
	ignore      []string
	parseReason string
}

func workflowPathFiltersFromYAML(text string) workflowPathFilters {
	lines := strings.Split(text, "\n")
	pullRequestIndent := -1
	pathsIndent := -1
	pathsIgnoreIndent := -1
	var include []string
	var ignore []string
	inOn := false
	onIndent := 0
	for _, raw := range lines {
		stripped := strings.TrimSpace(raw)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if regexp.MustCompile(`^on:\s*$`).MatchString(stripped) {
			inOn = true
			onIndent = indent
			continue
		}
		if regexp.MustCompile(`^on:\s*\[.*pull_request.*\]\s*$`).MatchString(stripped) {
			return workflowPathFilters{}
		}
		if regexp.MustCompile(`^on:\s*\{`).MatchString(stripped) {
			return workflowPathFilters{parseReason: "workflow uses inline on: syntax Tank cannot inspect"}
		}
		if inOn && indent <= onIndent && !strings.HasPrefix(stripped, "-") {
			break
		}
		if !inOn {
			continue
		}
		if pullRequestIndent < 0 {
			if regexp.MustCompile(`^pull_request:\s*$`).MatchString(stripped) {
				pullRequestIndent = indent
				continue
			}
			if stripped == "pull_request" {
				return workflowPathFilters{}
			}
			continue
		}
		if indent <= pullRequestIndent {
			break
		}
		if regexp.MustCompile(`^paths:\s*$`).MatchString(stripped) {
			include = []string{}
			pathsIndent = indent
			pathsIgnoreIndent = -1
			continue
		}
		if regexp.MustCompile(`^paths-ignore:\s*$`).MatchString(stripped) {
			ignore = []string{}
			pathsIgnoreIndent = indent
			pathsIndent = -1
			continue
		}
		if pathsIndent >= 0 {
			if indent <= pathsIndent {
				pathsIndent = -1
			} else if strings.HasPrefix(stripped, "- ") {
				include = append(include, strings.Trim(strings.TrimSpace(stripped[2:]), `'"`))
				continue
			}
		}
		if pathsIgnoreIndent >= 0 {
			if indent <= pathsIgnoreIndent {
				pathsIgnoreIndent = -1
			} else if strings.HasPrefix(stripped, "- ") {
				ignore = append(ignore, strings.Trim(strings.TrimSpace(stripped[2:]), `'"`))
				continue
			}
		}
	}
	return workflowPathFilters{include: include, ignore: ignore}
}

func workflowWouldRunForPaths(paths, include, ignore []string) bool {
	if len(paths) == 0 {
		return false
	}
	if include != nil {
		for _, path := range paths {
			if pathMatchesAny(path, include) {
				return true
			}
		}
		return false
	}
	if ignore != nil {
		for _, path := range paths {
			if !pathMatchesAny(path, ignore) {
				return true
			}
		}
		return false
	}
	return true
}

func matchingWorkflowPaths(paths, include []string) []string {
	if include == nil {
		return nil
	}
	out := []string{}
	for _, path := range paths {
		if pathMatchesAny(path, include) {
			out = append(out, path)
		}
	}
	return out
}

func pathMatchesAny(path string, patterns []string) bool {
	matched := false
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		negated := strings.HasPrefix(pattern, "!")
		if negated {
			pattern = strings.TrimPrefix(pattern, "!")
		}
		if !pathPatternSupported(pattern) {
			return true
		}
		hit := pathMatchesPattern(path, pattern) || pathMatchesPattern(path, strings.TrimRight(pattern, "/")+"/**")
		if hit && negated {
			matched = false
		} else if hit {
			matched = true
		}
	}
	return matched
}

func pathPatternSupported(pattern string) bool {
	return !regexp.MustCompile(`[\{\}\(\)\+@]`).MatchString(pattern)
}

func pathMatchesPattern(path, pattern string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString(`[^/]*`)
			}
		case '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	b.WriteString("$")
	ok, _ := regexp.MatchString(b.String(), path)
	return ok
}

func sortedCheckRunNames(m map[string]checkRun) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedPriorRunNames(m map[string]struct {
	sha string
	run checkRun
}) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func mergeCIMaps(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func firstStrings(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}

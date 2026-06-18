package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/glimmung"
	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// provisionVerdict is the bounded outcome of the deterministic test-slot
// provisioning gate. It mirrors the CI-watch reducer's verdicts plus the two
// gate-specific refusals (settle timeout, SHA pin moved). Used as the
// `outcome` metric label, so the value set must stay bounded.
type provisionVerdict string

const (
	provisionVerdictReady           provisionVerdict = "ready"
	provisionVerdictFailed          provisionVerdict = "failed"
	provisionVerdictConflict        provisionVerdict = "conflict"
	provisionVerdictMerged          provisionVerdict = "merged"
	provisionVerdictWatchingTimeout provisionVerdict = "watching_timeout"
	provisionVerdictHeadMoved       provisionVerdict = "head_moved"
	provisionVerdictError           provisionVerdict = "error"
)

// provisionProvisionOutcome is the bounded `outcome` label for the provision
// step (which only runs after a ready verdict).
const (
	provisionStepProvisioned   = "provisioned"
	provisionStepCheckoutError = "checkout_failed"
	provisionStepNoSlot        = "no_slot"
	provisionStepDeployError   = "deploy_failed"
)

// Settle-wait tuning. GitHub CI checks and trial-merge mergeability can take
// several minutes; the gate re-polls on this interval until the verdict
// settles or the hard cap is reached. Both are overridable on appServer so
// tests advance a fake clock instead of sleeping.
const (
	defaultProvisionSettleInterval = 25 * time.Second
	defaultProvisionSettleTimeout  = 18 * time.Minute
	// provisionDeployGrace is added on top of the settle cap when the caller
	// derives a background-context timeout for the whole gate+provision run.
	provisionDeployGrace = 5 * time.Minute
)

// provisionTestSlotRequest carries the coordinates the gate validates and the
// slot it provisions on success. Built as a struct (rather than a long
// positional signature) so Slice 2's interactive endpoint can reuse it.
type provisionTestSlotRequest struct {
	OwnerEmail string
	SessionID  string
	Project    string
	// Workflow labels the glimmung checkout lease; preserved per provisioning
	// path so the orchestration-review lease keeps its existing label.
	Workflow  string
	RepoOwner string
	RepoName  string
	// Branch is the head branch to validate (when PRNumber == 0) and the
	// git_ref deployed to the slot on success.
	Branch string
	// PRNumber, when > 0, resolves live PR state by number; otherwise the gate
	// resolves the open PR for RepoOwner:Branch.
	PRNumber int
	// ExpectedSHA pins the validated head. When non-empty and the live PR head
	// has moved off it, the gate refuses rather than greenlight a superseded
	// commit. Empty disables the pin.
	ExpectedSHA string
	// progress, when set, is invoked by the gate as it advances through its
	// phases — "validating" before the first live read, "waiting" on each
	// settle-wait. The interactive path uses it to surface intermediate
	// test_provision.updated progress records (the callback dedupes per phase);
	// the autonomous path leaves it nil. Not persisted; never copied into the
	// durable pending-provision row.
	progress func(phase string)
}

// provisionOutcome is the structured result the gate returns. Verdict is
// always set; Provisioned is true only on a ready verdict that then completed
// checkout+deploy.
type provisionOutcome struct {
	Verdict       provisionVerdict
	Provisioned   bool
	Detail        string
	FailingChecks []string
	HeadSHA       string
	Checkout      glimmung.CheckoutTestSlotResult
	Deploy        glimmung.DeployImageToTestSlotResult
}

// provisionTestSlotForSession is the shared server-side "validate → wait →
// provision" primitive for deterministic, gated test-slot provisioning. It
// reuses classifyCIWatchState (the single definition of "is this code good")
// against a one-shot live read — it never registers a durable CI watch, so it
// has no watch-row or wake side effects. Only a ready verdict provisions the
// slot; every other verdict returns a refusal outcome and leaves glimmung
// untouched.
//
// The returned error is non-nil only for infrastructure failures (missing
// clients, GitHub read errors) where no verdict could be reached; gate
// refusals (failed/conflict/merged/timeout/head-moved) are returned as a
// non-error outcome with Provisioned=false.
func (s *appServer) provisionTestSlotForSession(ctx context.Context, req provisionTestSlotRequest) (provisionOutcome, error) {
	if s.mcpGitHub == nil {
		return provisionOutcome{Verdict: provisionVerdictError}, errors.New("mcp-github client not configured")
	}
	if s.glimmung == nil {
		return provisionOutcome{Verdict: provisionVerdictError}, errors.New("glimmung client not configured")
	}
	repoOwner := strings.TrimSpace(req.RepoOwner)
	repoName := strings.TrimSpace(req.RepoName)
	branch := strings.TrimSpace(req.Branch)
	expectedSHA := strings.TrimSpace(req.ExpectedSHA)
	if repoOwner == "" || repoName == "" {
		return provisionOutcome{Verdict: provisionVerdictError}, errors.New("provision gate requires repo owner and name")
	}
	if req.PRNumber <= 0 && branch == "" {
		return provisionOutcome{Verdict: provisionVerdictError}, errors.New("provision gate requires a PR number or branch")
	}

	// Transient, in-memory watch: just enough identity for classifyCIWatchState
	// and the head-pin comparison. Deliberately NOT persisted.
	watch := pgstore.CIWatch{
		OwnerEmail: req.OwnerEmail,
		PROwner:    repoOwner,
		PRName:     repoName,
		PRNumber:   req.PRNumber,
		HeadSHA:    expectedSHA,
	}

	deadline := s.provisionNowTime().Add(s.provisionSettleTimeoutDuration())
	repoPR := repoOwner + "/" + repoName
	if req.PRNumber > 0 {
		repoPR += " #" + strconv.Itoa(req.PRNumber)
	} else {
		repoPR += "@" + branch
	}

	if req.progress != nil {
		req.progress("validating")
	}

	for {
		state, err := s.resolveProvisionState(ctx, req)
		if err != nil {
			recordTestSlotValidate(string(provisionVerdictError))
			return provisionOutcome{Verdict: provisionVerdictError}, err
		}

		// SHA pin: refuse a head that moved past the commit we were asked to
		// validate rather than greenlight a superseded commit. Same head
		// comparison the CI-watch reconciler uses.
		if expectedSHA != "" && headMovedOffPin(watch, state) {
			detail := repoPR + " head moved to " + shortSHAForMessage(state.HeadSHA) +
				" past requested " + shortSHAForMessage(expectedSHA) + "; redeploy latest."
			recordTestSlotValidate(string(provisionVerdictHeadMoved))
			return provisionOutcome{Verdict: provisionVerdictHeadMoved, Detail: detail, HeadSHA: state.HeadSHA}, nil
		}

		result := classifyCIWatchState(watch, state)
		switch result.Status {
		case pgstore.CIWatchReady:
			recordTestSlotValidate(string(provisionVerdictReady))
			return s.provisionSlotAfterReady(ctx, req, result.HeadSHA)
		case pgstore.CIWatchFailed:
			detail := "CI failed on " + repoPR
			if len(result.FailingChecks) > 0 {
				detail += ": " + strings.Join(firstStringsMain(result.FailingChecks, 8), ", ")
			}
			detail += "."
			recordTestSlotValidate(string(provisionVerdictFailed))
			return provisionOutcome{Verdict: provisionVerdictFailed, Detail: detail, FailingChecks: result.FailingChecks, HeadSHA: result.HeadSHA}, nil
		case pgstore.CIWatchConflict:
			detail := repoPR + " needs a rebase onto main (" + result.Detail + ")."
			recordTestSlotValidate(string(provisionVerdictConflict))
			return provisionOutcome{Verdict: provisionVerdictConflict, Detail: detail, HeadSHA: result.HeadSHA}, nil
		case pgstore.CIWatchMerged:
			detail := repoPR + " is already merged; nothing to provision."
			recordTestSlotValidate(string(provisionVerdictMerged))
			return provisionOutcome{Verdict: provisionVerdictMerged, Detail: detail, HeadSHA: result.HeadSHA}, nil
		case pgstore.CIWatchWatching:
			if req.progress != nil {
				req.progress("waiting")
			}
			if !s.provisionNowTime().Before(deadline) {
				minutes := int(s.provisionSettleTimeoutDuration().Round(time.Minute) / time.Minute)
				detail := "Checks did not settle for " + repoPR + " within " + strconv.Itoa(minutes) + " min."
				recordTestSlotValidate(string(provisionVerdictWatchingTimeout))
				return provisionOutcome{Verdict: provisionVerdictWatchingTimeout, Detail: detail, HeadSHA: result.HeadSHA}, nil
			}
			if err := s.provisionSleep(ctx, s.provisionSettleIntervalDuration()); err != nil {
				recordTestSlotValidate(string(provisionVerdictError))
				return provisionOutcome{Verdict: provisionVerdictError}, err
			}
		}
	}
}

// resolveProvisionState reads live PR/CI state through the same mcp-github
// reducer inputs the CI-watch path uses: by number when given, else by branch.
func (s *appServer) resolveProvisionState(ctx context.Context, req provisionTestSlotRequest) (mcpgithub.PullRequestState, error) {
	if req.PRNumber > 0 {
		return s.mcpGitHub.ResolvePullRequestState(ctx, req.OwnerEmail, req.RepoOwner, req.RepoName, req.PRNumber)
	}
	return s.mcpGitHub.ResolveOpenPullRequestState(ctx, req.OwnerEmail, req.RepoOwner, req.RepoName, req.RepoOwner, req.Branch)
}

// provisionSlotAfterReady runs the checkout → deploy → SetTestState provision
// sequence, mirroring checkoutAndDeployOrchestrationReview. Only reached on a
// ready verdict.
func (s *appServer) provisionSlotAfterReady(ctx context.Context, req provisionTestSlotRequest, headSHA string) (provisionOutcome, error) {
	out := provisionOutcome{Verdict: provisionVerdictReady, HeadSHA: headSHA}
	workflow := strings.TrimSpace(req.Workflow)
	checkoutReq := glimmung.CheckoutTestSlotRequest{
		Project:       req.Project,
		TankSessionID: ptrIfNonEmpty(req.SessionID),
	}
	if workflow != "" {
		checkoutReq.Workflow = &workflow
	}
	checkout, err := s.glimmung.CheckoutTestSlot(ctx, req.OwnerEmail, checkoutReq)
	if err != nil {
		recordTestSlotProvision(provisionStepCheckoutError)
		return out, err
	}
	out.Checkout = checkout
	if checkout.SlotIndex == nil && checkout.SlotName == nil {
		recordTestSlotProvision(provisionStepNoSlot)
		return out, errors.New("glimmung checkout returned no slot identity")
	}
	deploy, err := s.glimmung.DeployImageToTestSlot(ctx, req.OwnerEmail, glimmung.DeployImageToTestSlotRequest{
		Project:   req.Project,
		SlotIndex: checkout.SlotIndex,
		SlotName:  checkout.SlotName,
		GitRef:    req.Branch,
	})
	if err != nil {
		recordTestSlotProvision(provisionStepDeployError)
		return out, err
	}
	out.Deploy = deploy
	if s.mgr != nil && checkout.URL != nil {
		if _, err := s.mgr.SetTestState(ctx, req.OwnerEmail, req.SessionID, true, checkout.SlotIndex, checkout.URL, nil); err != nil {
			slog.Warn("provision gate set test state failed", "session_id", req.SessionID, "error", err)
		}
	}
	out.Provisioned = true
	if checkout.URL != nil {
		out.Detail = "Test environment: " + strings.TrimSpace(*checkout.URL) + "."
	}
	recordTestSlotProvision(provisionStepProvisioned)
	return out, nil
}

func (s *appServer) provisionSettleIntervalDuration() time.Duration {
	if s.provisionSettleInterval > 0 {
		return s.provisionSettleInterval
	}
	return defaultProvisionSettleInterval
}

func (s *appServer) provisionSettleTimeoutDuration() time.Duration {
	if s.provisionSettleTimeout > 0 {
		return s.provisionSettleTimeout
	}
	return defaultProvisionSettleTimeout
}

// provisionBackgroundTimeout is the context budget a caller that runs the gate
// off the request path should use: the settle cap plus deploy grace.
func (s *appServer) provisionBackgroundTimeout() time.Duration {
	return s.provisionSettleTimeoutDuration() + provisionDeployGrace
}

func (s *appServer) provisionNowTime() time.Time {
	if s.provisionNow != nil {
		return s.provisionNow()
	}
	return time.Now()
}

// provisionSleep waits d, honoring the injected sleep when set (tests) and ctx
// cancellation otherwise.
func (s *appServer) provisionSleep(ctx context.Context, d time.Duration) error {
	if s.provisionSleepFunc != nil {
		return s.provisionSleepFunc(ctx, d)
	}
	if d <= 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func ptrIfNonEmpty(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	out := v
	return &out
}

func provisionRefusalError(out provisionOutcome) error {
	detail := strings.TrimSpace(out.Detail)
	if detail == "" {
		detail = string(out.Verdict)
	}
	return fmt.Errorf("test slot gate refused (%s): %s", out.Verdict, detail)
}

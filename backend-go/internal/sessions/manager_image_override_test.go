package sessions

import (
	"context"
	"errors"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

const (
	pinnedClaude      = "romainecr.azurecr.io/claude-container:claude-PINNED"
	pinnedCodex       = "romainecr.azurecr.io/codex-container:codex-PINNED"
	pinnedAntigravity = "romainecr.azurecr.io/antigravity-container:antigravity-PINNED"
	branchClaude      = "romainecr.azurecr.io/claude-container:claude-BRANCH"
	branchCodex       = "romainecr.azurecr.io/codex-container:codex-BRANCH"
	branchAntigravity = "romainecr.azurecr.io/antigravity-container:antigravity-BRANCH"
)

type fakeImageOverrides struct {
	claude      string
	codex       string
	antigravity string
	ok          bool
	err         error
	gotScope    string
	calls       int
}

func (f *fakeImageOverrides) Get(ctx context.Context, scope string) (string, string, string, bool, error) {
	f.calls++
	f.gotScope = scope
	return f.claude, f.codex, f.antigravity, f.ok, f.err
}

func newOverrideManager(scope string, ov SessionImageOverrides, hook func(string, string, string)) *Manager {
	return &Manager{
		scope:                  scope,
		imageOverrides:         ov,
		onImageOverrideApplied: hook,
		manifestOpts: sessionmodel.ManifestOptions{
			SessionImage:            pinnedClaude,
			CodexSessionImage:       pinnedCodex,
			AntigravitySessionImage: pinnedAntigravity,
		},
	}
}

// A slot-scope codex session stamps the codex override; the claude family is
// left at its pinned value (PodManifest picks the codex image for codex modes).
func TestApplyImageOverride_SlotCodexMode(t *testing.T) {
	var applied []string
	fake := &fakeImageOverrides{claude: branchClaude, codex: branchCodex, ok: true}
	m := newOverrideManager("tank-operator-slot-1", fake, func(scope, mode, kind string) {
		applied = []string{scope, mode, kind}
	})
	opts := m.manifestOpts
	m.applyImageOverride(context.Background(), &opts, sessionmodel.CodexGUIMode)

	if opts.CodexSessionImage != branchCodex {
		t.Fatalf("CodexSessionImage = %q, want %q", opts.CodexSessionImage, branchCodex)
	}
	if opts.SessionImage != pinnedClaude {
		t.Fatalf("SessionImage = %q, want it unchanged %q", opts.SessionImage, pinnedClaude)
	}
	if fake.gotScope != "tank-operator-slot-1" {
		t.Fatalf("resolver scope = %q, want the orchestrator scope", fake.gotScope)
	}
	if len(applied) != 3 || applied[2] != "codex" {
		t.Fatalf("hook = %v, want [scope mode codex]", applied)
	}
}

// A slot-scope claude session stamps the claude override; codex family pinned.
func TestApplyImageOverride_SlotClaudeMode(t *testing.T) {
	fake := &fakeImageOverrides{claude: branchClaude, codex: branchCodex, ok: true}
	m := newOverrideManager("tank-operator-slot-1", fake, nil)
	opts := m.manifestOpts
	m.applyImageOverride(context.Background(), &opts, sessionmodel.ClaudeGUIMode)

	if opts.SessionImage != branchClaude {
		t.Fatalf("SessionImage = %q, want %q", opts.SessionImage, branchClaude)
	}
	if opts.CodexSessionImage != pinnedCodex {
		t.Fatalf("CodexSessionImage = %q, want it unchanged %q", opts.CodexSessionImage, pinnedCodex)
	}
}

// A slot-scope Antigravity session stamps the Antigravity override; the other
// image families stay pinned.
func TestApplyImageOverride_SlotAntigravityMode(t *testing.T) {
	var applied []string
	fake := &fakeImageOverrides{claude: branchClaude, codex: branchCodex, antigravity: branchAntigravity, ok: true}
	m := newOverrideManager("tank-operator-slot-1", fake, func(scope, mode, kind string) {
		applied = []string{scope, mode, kind}
	})
	opts := m.manifestOpts
	m.applyImageOverride(context.Background(), &opts, sessionmodel.AntigravityGUIMode)

	if opts.AntigravitySessionImage != branchAntigravity {
		t.Fatalf("AntigravitySessionImage = %q, want %q", opts.AntigravitySessionImage, branchAntigravity)
	}
	if opts.SessionImage != pinnedClaude || opts.CodexSessionImage != pinnedCodex {
		t.Fatalf("other images mutated: claude=%q codex=%q", opts.SessionImage, opts.CodexSessionImage)
	}
	if len(applied) != 3 || applied[2] != "antigravity" {
		t.Fatalf("hook = %v, want [scope mode antigravity]", applied)
	}
}

// PROD SAFETY: the production scope is never overridden, even if a row somehow
// exists. The resolver must not even be consulted.
func TestApplyImageOverride_ProdScopeNeverOverridden(t *testing.T) {
	fake := &fakeImageOverrides{claude: branchClaude, codex: branchCodex, antigravity: branchAntigravity, ok: true}
	m := newOverrideManager(defaultSessionScope, fake, func(string, string, string) {
		t.Fatal("hook must not fire for the production scope")
	})
	opts := m.manifestOpts
	m.applyImageOverride(context.Background(), &opts, sessionmodel.CodexGUIMode)

	if opts.CodexSessionImage != pinnedCodex || opts.SessionImage != pinnedClaude || opts.AntigravitySessionImage != pinnedAntigravity {
		t.Fatalf("prod images mutated: claude=%q codex=%q antigravity=%q", opts.SessionImage, opts.CodexSessionImage, opts.AntigravitySessionImage)
	}
	if fake.calls != 0 {
		t.Fatalf("resolver consulted %d times for prod scope, want 0", fake.calls)
	}
}

// GATE OFF: a nil resolver (production default, or feature disabled) leaves the
// pinned images untouched and must not panic.
func TestApplyImageOverride_NilResolverNoOp(t *testing.T) {
	m := newOverrideManager("tank-operator-slot-1", nil, nil)
	opts := m.manifestOpts
	m.applyImageOverride(context.Background(), &opts, sessionmodel.CodexGUIMode)
	if opts.CodexSessionImage != pinnedCodex || opts.SessionImage != pinnedClaude || opts.AntigravitySessionImage != pinnedAntigravity {
		t.Fatalf("images mutated with nil resolver: claude=%q codex=%q antigravity=%q", opts.SessionImage, opts.CodexSessionImage, opts.AntigravitySessionImage)
	}
}

// A resolver error falls back to the pinned image rather than failing.
func TestApplyImageOverride_ResolverErrorFallsBack(t *testing.T) {
	fake := &fakeImageOverrides{err: errors.New("db down")}
	m := newOverrideManager("tank-operator-slot-1", fake, func(string, string, string) {
		t.Fatal("hook must not fire on resolver error")
	})
	opts := m.manifestOpts
	m.applyImageOverride(context.Background(), &opts, sessionmodel.CodexGUIMode)
	if opts.CodexSessionImage != pinnedCodex {
		t.Fatalf("CodexSessionImage = %q, want pinned on error", opts.CodexSessionImage)
	}
}

// A row that overrides only the other runner family is a no-op for this mode.
func TestApplyImageOverride_OtherFamilyOnlyIsNoOp(t *testing.T) {
	// Only claude overridden, but the session is codex → no change, no hook.
	fake := &fakeImageOverrides{claude: branchClaude, codex: "", ok: true}
	m := newOverrideManager("tank-operator-slot-1", fake, func(string, string, string) {
		t.Fatal("hook must not fire when the mode's image family has no override")
	})
	opts := m.manifestOpts
	m.applyImageOverride(context.Background(), &opts, sessionmodel.CodexGUIMode)
	if opts.CodexSessionImage != pinnedCodex {
		t.Fatalf("CodexSessionImage = %q, want pinned", opts.CodexSessionImage)
	}
}

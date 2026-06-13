package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// TestIsImageContentType pins the image/non-image routing decision.
// Images go to `screenshots/<n>.<ext>`; everything else falls back to
// `.attachments/<ns>-<sanitized>`. The Content-Type header is the
// canonical signal — the SPA's `uploadAttachment` always sets it from
// `file.type`, which is browser-populated from clipboard / file-picker.
func TestIsImageContentType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"IMAGE/PNG", true},
		{"image/png; charset=binary", true},
		{"  image/webp  ", true},
		{"application/pdf", false},
		{"text/plain", false},
		{"", false},
		{"image", false},
		{"application/json", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := isImageContentType(tc.in)
			if got != tc.want {
				t.Fatalf("isImageContentType(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestScreenshotExtension pins the extension-picking logic for the
// `screenshots/<n><ext>` path. The MIME map is authoritative; filename
// is a fallback; hard fallback is `.png` because every browser's
// clipboard paste is PNG.
func TestScreenshotExtension(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		fileName    string
		want        string
	}{
		{"png mime", "image/png", "image.png", ".png"},
		{"jpeg mime normalized", "image/jpeg", "photo.jpeg", ".jpg"},
		{"webp mime", "image/webp", "x.webp", ".webp"},
		{"unknown mime falls back to filename", "image/x-icon", "favicon.ico", ".ico"},
		{"unknown mime + bad filename falls back to .png", "image/x-icon", "favicon", ".png"},
		{"empty everything falls back to .png", "", "", ".png"},
		{"mime parameters are stripped", "image/png; charset=binary", "image.png", ".png"},
		{"long extension rejected", "image/x-unknown", "evil.verylongextension", ".png"},
		{"non-alnum extension rejected", "image/x-unknown", "evil.p%g", ".png"},
		{"path-traversal in filename ignored", "image/x-unknown", "evil.png/../../etc", ".png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := screenshotExtension(tc.contentType, tc.fileName)
			if got != tc.want {
				t.Fatalf("screenshotExtension(%q, %q) = %q, want %q",
					tc.contentType, tc.fileName, got, tc.want)
			}
		})
	}
}

func TestRawFileContentType(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"screenshots/1.png", "image/png"},
		{"/workspace/screenshots/result.JPG", "image/jpeg"},
		{"camera.heic", "image/heic"},
		{"diagram.svg", "image/svg+xml"},
		{"preview.webp", "image/webp"},
		{"archive.tar.gz", "application/octet-stream"},
		{"README", "application/octet-stream"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := rawFileContentType(tc.path)
			if got != tc.want {
				t.Fatalf("rawFileContentType(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestUniqueAttachmentRelPathNoCollisionForSameName is the regression
// guard for the non-image fallback path: the SPA composer fires
// `uploadAttachment` per file in a paste event without awaiting, so two
// uploads of identically-named files (e.g. two `notes.txt` drops) hit
// the orchestrator concurrently. The Python orchestrator stamped each
// upload; the Go rewrite briefly wrote files straight to the
// caller-supplied name, so the second upload overwrote the first.
func TestUniqueAttachmentRelPathNoCollisionForSameName(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	first := uniqueAttachmentRelPath("notes.txt", base)
	second := uniqueAttachmentRelPath("notes.txt", base.Add(time.Nanosecond))

	if first == second {
		t.Fatalf("expected distinct attachment paths, got %q and %q", first, second)
	}
	for _, p := range []string{first, second} {
		if !strings.HasPrefix(p, ".attachments/") {
			t.Fatalf("path %q missing .attachments/ prefix", p)
		}
		if !strings.HasSuffix(p, "-notes.txt") {
			t.Fatalf("path %q missing sanitized-name suffix", p)
		}
	}
}

func TestUniqueAttachmentRelPathSanitizesUnsafeChars(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // suffix after the `<ts>-` prefix
	}{
		{"spaces", "Notes 2026-05-16.txt", "Notes_2026-05-16.txt"},
		{"slashes", "../../etc/passwd", ".._.._etc_passwd"},
		{"unicode", "café ☕.txt", "caf___.txt"},
		{"empty", "", "file"},
		{"only-unsafe", "///", "___"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uniqueAttachmentRelPath(tc.in, time.Unix(0, 1))
			wantSuffix := "-" + tc.want
			if !strings.HasSuffix(got, wantSuffix) {
				t.Fatalf("got %q, want suffix %q", got, wantSuffix)
			}
			if !strings.HasPrefix(got, ".attachments/") {
				t.Fatalf("got %q, want .attachments/ prefix", got)
			}
		})
	}
}

func TestUniqueAttachmentRelPathCapsLongNames(t *testing.T) {
	long := strings.Repeat("a", 250) + ".txt"
	got := uniqueAttachmentRelPath(long, time.Unix(0, 1))
	parts := strings.SplitN(got, "-", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected shape: %q", got)
	}
	if len(parts[1]) > 100 {
		t.Fatalf("sanitized name not capped: len=%d", len(parts[1]))
	}
}

func TestSafeWorkspacePathRejectsLiteralTraversal(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"workspace root", "/workspace", "/workspace", false},
		{"absolute inside workspace", "/workspace/app/main.go", "/workspace/app/main.go", false},
		{"relative inside workspace", "app/../README.md", "/workspace/README.md", false},
		{"absolute traversal", "/workspace/../home/node/secret.txt", "", true},
		{"relative traversal", "../etc/passwd", "", true},
		{"absolute outside workspace", "/home/node/secret.txt", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safeWorkspacePath(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("safeWorkspacePath(%q) succeeded, want error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("safeWorkspacePath(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("safeWorkspacePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSafeReadablePath(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"blank defaults to workspace root", "", "/workspace", false},
		{"workspace root", "/workspace", "/workspace", false},
		{"inside workspace", "/workspace/app/main.go", "/workspace/app/main.go", false},
		{"home dir (the ~/.claude case)", "/home/node/.claude/plans/x.md", "/home/node/.claude/plans/x.md", false},
		{"tooling dir", "/opt/tank/session-config", "/opt/tank/session-config", false},
		{"tmp", "/tmp/out.txt", "/tmp/out.txt", false},
		{"arbitrary path is allowed (default-allow)", "/etc/hosts", "/etc/hosts", false},
		{"bypass-write target is allowed", "/var/tmp/agent.txt", "/var/tmp/agent.txt", false},
		{"relative resolves under workspace", "app/main.go", "/workspace/app/main.go", false},
		{"secret token mount is denied", "/var/run/secrets/auth.romaine.life/token", "", true},
		{"proc is denied", "/proc/1/environ", "", true},
		{"lexical traversal into secrets is denied", "/workspace/../var/run/secrets/x", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safeReadablePath(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("safeReadablePath(%q) succeeded, want error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("safeReadablePath(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("safeReadablePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRealpathResolveAndDenylist exercises the authoritative read-side boundary:
// the in-pod realpath script resolves symlinks, and sessionmodel.PathReadable
// decides allow/deny on the RESOLVED path. A symlink pointing into
// /var/run/secrets resolves into the denylist and is refused even though it sits
// lexically inside the workspace; a symlink to an ordinary directory resolves to
// a non-secret path and is allowed (reads are default-allow — only secrets are
// fenced). This is the symlink-escape protection plus the new default-allow
// semantics in one place.
func TestRealpathResolveAndDenylist(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	outside := filepath.Join(t.TempDir(), "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write inside: %v", err)
	}
	secretLink := filepath.Join(root, "leak")
	if err := os.Symlink("/var/run/secrets", secretLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	outsideLink := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, outsideLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	cases := []struct {
		name         string
		path         string
		wantReadable bool
	}{
		{"ordinary file inside", filepath.Join(root, "inside.txt"), true},
		{"new file inside", filepath.Join(root, "new.txt"), true},
		{"symlink into /var/run/secrets is denied", filepath.Join(secretLink, "auth.romaine.life", "token"), false},
		{"symlink to ordinary dir is allowed", filepath.Join(outsideLink, "notes.txt"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved := resolveRealpathLocally(t, tc.path)
			if got := sessionmodel.PathReadable(resolved); got != tc.wantReadable {
				t.Fatalf("PathReadable(resolved=%q) = %v, want %v", resolved, got, tc.wantReadable)
			}
		})
	}
}

func resolveRealpathLocally(t *testing.T, target string) string {
	t.Helper()
	cmd := exec.Command("python3", "-c", realpathResolveScript, target)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run realpath resolve: %v", err)
	}
	var body struct {
		ResolvedPath string `json:"resolved_path"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("parse realpath output %q: %v", string(out), err)
	}
	return body.ResolvedPath
}

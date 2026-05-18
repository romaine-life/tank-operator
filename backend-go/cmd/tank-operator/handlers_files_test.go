package main

import (
	"strings"
	"testing"
	"time"
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

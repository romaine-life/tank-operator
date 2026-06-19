package main

import "testing"

func TestSdkSessionIDPattern(t *testing.T) {
	valid := []string{
		"3f2a9c1e-1b2c-4d5e-8f90-abcdef012345",
		"session_340",
		"a.b-c_1",
	}
	for _, v := range valid {
		if !sdkSessionIDPattern.MatchString(v) {
			t.Errorf("expected %q to be a valid sdk session id", v)
		}
	}
	invalid := []string{
		"",
		"../etc/passwd",
		"a/b",
		"has space",
		"slash\\back",
		"emoji😀",
	}
	for _, v := range invalid {
		if sdkSessionIDPattern.MatchString(v) {
			t.Errorf("expected %q to be rejected", v)
		}
	}
}

func TestBlobSegment(t *testing.T) {
	cases := map[string]string{
		"User@Example.COM": "user-example.com",
		"8":                "8",
		"  spaced  ":       "spaced",
		"":                 "unknown",
		"...":              "unknown",
		"a/b\\c":           "a-b-c",
	}
	for in, want := range cases {
		if got := blobSegment(in); got != want {
			t.Errorf("blobSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTranscriptBlobKey(t *testing.T) {
	key := transcriptBlobKey("user@example.com", "8", "abc-123")
	want := "user-example.com/8/abc-123.jsonl"
	if key != want {
		t.Fatalf("transcriptBlobKey = %q, want %q", key, want)
	}
}

func TestSanitizeBlobMetadata(t *testing.T) {
	out := sanitizeBlobMetadata(map[string]string{
		"keep":     "value",
		"empty":    "   ",
		"nonascii": "café\x00bar",
	})
	if out["keep"] != "value" {
		t.Errorf("expected keep=value, got %q", out["keep"])
	}
	if _, ok := out["empty"]; ok {
		t.Errorf("expected empty value to be dropped")
	}
	// Non-ASCII and control bytes are stripped, leaving ASCII remnants.
	if got := out["nonascii"]; got != "cafbar" {
		t.Errorf("expected ascii-stripped value cafbar, got %q", got)
	}
}

func TestDecodeHeader(t *testing.T) {
	if got := decodeHeader("%2Eclaude%2Fprojects"); got != ".claude/projects" {
		t.Errorf("decodeHeader percent-decode failed: %q", got)
	}
	if got := decodeHeader("  plain  "); got != "plain" {
		t.Errorf("decodeHeader trim failed: %q", got)
	}
	if got := decodeHeader("%zz-not-encoded"); got != "%zz-not-encoded" {
		t.Errorf("decodeHeader should pass through invalid escape: %q", got)
	}
}

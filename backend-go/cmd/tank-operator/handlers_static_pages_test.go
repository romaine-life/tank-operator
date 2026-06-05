package main

import (
	"net/http"
	"testing"
)

func TestIsHTMLPath(t *testing.T) {
	cases := map[string]bool{
		"page.html":         true,
		"page.htm":          true,
		"PAGE.HTML":         true,
		"  diagram.html  ":  true,
		"out/report.html":   true,
		"a.b.html":          true,
		"notes.txt":         false,
		"page.html.txt":     false,
		"htmlfile":          false,
		"":                  false,
		"archive.html.zip":  false,
		"sub/dir/index.htm": true,
	}
	for path, want := range cases {
		if got := isHTMLPath(path); got != want {
			t.Errorf("isHTMLPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestStaticPageResolveResult(t *testing.T) {
	cases := map[int]string{
		http.StatusNotFound:            "not_found",
		http.StatusForbidden:           "denied",
		http.StatusServiceUnavailable:  "pod_unavailable",
		http.StatusInternalServerError: "store_error",
		http.StatusOK:                  "store_error", // unmapped -> conservative bucket
	}
	for status, want := range cases {
		if got := staticPageResolveResult(status); got != want {
			t.Errorf("staticPageResolveResult(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestStaticPageOperationLabel(t *testing.T) {
	for _, op := range []string{"capture", "read"} {
		if got := staticPageOperationLabel(op); got != op {
			t.Errorf("staticPageOperationLabel(%q) = %q, want %q", op, got, op)
		}
	}
	if got := staticPageOperationLabel("delete"); got != "unknown" {
		t.Errorf("staticPageOperationLabel(unmapped) = %q, want unknown", got)
	}
}

func TestStaticPageResultLabel(t *testing.T) {
	known := []string{
		"ok", "bad_request", "denied", "not_found",
		"pod_unavailable", "exec_error", "store_unavailable", "store_error",
	}
	for _, r := range known {
		if got := staticPageResultLabel(r); got != r {
			t.Errorf("staticPageResultLabel(%q) = %q, want passthrough", r, got)
		}
	}
	if got := staticPageResultLabel("kaboom"); got != "other" {
		t.Errorf("staticPageResultLabel(unmapped) = %q, want other", got)
	}
}

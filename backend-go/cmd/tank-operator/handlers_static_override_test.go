package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

// gzTar builds a gzipped tar of name→content for the static-override receiver
// tests. Names are emitted in sorted order so the archive is deterministic.
func gzTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		body := []byte(files[n])
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("write header %q: %v", n, err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("write body %q: %v", n, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// staticOverrideTestApp returns an appServer whose verifier accepts tokens from
// the returned mint helper, mirroring the service-principal harness in
// handlers_internal_test.go.
func staticOverrideTestApp(t *testing.T) (*appServer, func(role, actor string) string) {
	t.Helper()
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	app := &appServer{verifier: auth.NewVerifier(jwtKey)}
	mint := func(role, actor string) string {
		claims := jwt.MapClaims{
			"sub":   "svc:tank:session-x",
			"email": "pod-session-x@service.tank.romaine.life",
			"iss":   "https://auth.romaine.life",
			"name":  "Service: tank pod-session-x",
			"role":  role,
		}
		if actor != "" {
			claims["actor_email"] = actor
		}
		tok, err := jwtKey.MintJWT(context.Background(), claims)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	return app, mint
}

func putStaticOverride(t *testing.T, app *appServer, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/internal/static-override", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	app.handleInternalPutStaticOverride(rec, req)
	return rec
}

func TestStaticOverridePut_HappyPathServesOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TANK_OPERATOR_STATIC_OVERRIDE_ROOT", root)
	app, mint := staticOverrideTestApp(t)

	body := gzTar(t, map[string]string{
		"index.html":    "<!doctype html><title>live</title>",
		"assets/app.js": "console.log('streamed')",
	})
	rec := putStaticOverride(t, app, mint("service", "nelson@romaine.life"), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}

	// The served override dir is <root>/current (what the chart points
	// TANK_OPERATOR_STATIC_OVERRIDE_DIR at). It must resolve through the flipped
	// symlink and serve the streamed bundle.
	roots := tankStaticRootSet{override: filepath.Join(root, staticOverrideCurrentName)}
	found, ok := tankStaticFile(roots, "index.html")
	if !ok {
		t.Fatalf("index.html not served from override after push")
	}
	got, err := os.ReadFile(found)
	if err != nil {
		t.Fatalf("read served index.html: %v", err)
	}
	if string(got) != "<!doctype html><title>live</title>" {
		t.Fatalf("served index.html = %q, want streamed bundle", string(got))
	}
	if _, ok := tankStaticFile(roots, "assets", "app.js"); !ok {
		t.Fatalf("assets/app.js not served from override after push")
	}
}

func TestStaticOverridePut_SecondPushFlipsAtomically(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TANK_OPERATOR_STATIC_OVERRIDE_ROOT", root)
	app, mint := staticOverrideTestApp(t)
	tok := mint("service", "nelson@romaine.life")

	if rec := putStaticOverride(t, app, tok, gzTar(t, map[string]string{"index.html": "v1"})); rec.Code != http.StatusOK {
		t.Fatalf("first push status=%d", rec.Code)
	}
	if rec := putStaticOverride(t, app, tok, gzTar(t, map[string]string{"index.html": "v2"})); rec.Code != http.StatusOK {
		t.Fatalf("second push status=%d", rec.Code)
	}
	roots := tankStaticRootSet{override: filepath.Join(root, staticOverrideCurrentName)}
	found, ok := tankStaticFile(roots, "index.html")
	if !ok {
		t.Fatalf("index.html not served after second push")
	}
	got, _ := os.ReadFile(found)
	if string(got) != "v2" {
		t.Fatalf("served index.html = %q, want v2 after second flip", string(got))
	}
}

func TestStaticOverridePut_RequiresServicePrincipal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TANK_OPERATOR_STATIC_OVERRIDE_ROOT", root)
	app, mint := staticOverrideTestApp(t)
	body := gzTar(t, map[string]string{"index.html": "x"})

	if rec := putStaticOverride(t, app, "", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d, want 401", rec.Code)
	}
	if rec := putStaticOverride(t, app, mint("user", ""), body); rec.Code != http.StatusForbidden {
		t.Fatalf("role=user: status=%d, want 403", rec.Code)
	}
	if rec := putStaticOverride(t, app, mint("service", ""), body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("service without actor_email: status=%d, want 401", rec.Code)
	}
}

func TestStaticOverridePut_DisabledWhenRootUnset(t *testing.T) {
	t.Setenv("TANK_OPERATOR_STATIC_OVERRIDE_ROOT", "")
	app, mint := staticOverrideTestApp(t)
	rec := putStaticOverride(t, app, mint("service", "nelson@romaine.life"), gzTar(t, map[string]string{"index.html": "x"}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 when receiver disabled", rec.Code)
	}
}

func TestStaticOverridePut_RejectsArchiveMissingIndex(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TANK_OPERATOR_STATIC_OVERRIDE_ROOT", root)
	app, mint := staticOverrideTestApp(t)
	rec := putStaticOverride(t, app, mint("service", "nelson@romaine.life"), gzTar(t, map[string]string{"assets/app.js": "x"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for archive with no index.html", rec.Code)
	}
	// A rejected push must not leave a live override: current stays absent.
	if _, err := os.Lstat(filepath.Join(root, staticOverrideCurrentName)); !os.IsNotExist(err) {
		t.Fatalf("current symlink should not exist after a rejected push (err=%v)", err)
	}
}

func TestStaticOverrideExtract_RejectsTraversalAndLinks(t *testing.T) {
	t.Run("parent traversal", func(t *testing.T) {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		_ = tw.WriteHeader(&tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("pwn"))
		_ = tw.Close()
		_ = gw.Close()
		if _, _, err := extractStaticOverrideTar(&buf, t.TempDir()); err == nil {
			t.Fatalf("expected error for parent-traversal member")
		}
	})
	t.Run("symlink member", func(t *testing.T) {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		_ = tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
		_ = tw.Close()
		_ = gw.Close()
		dest := t.TempDir()
		if _, _, err := extractStaticOverrideTar(&buf, dest); err == nil {
			t.Fatalf("expected error for symlink member")
		}
		if _, err := os.Lstat(filepath.Join(dest, "link")); !os.IsNotExist(err) {
			t.Fatalf("symlink member should not have been written (err=%v)", err)
		}
	})
}

func TestStaticOverrideDelete_RevertsToBaked(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TANK_OPERATOR_STATIC_OVERRIDE_ROOT", root)
	app, mint := staticOverrideTestApp(t)
	tok := mint("service", "nelson@romaine.life")

	if rec := putStaticOverride(t, app, tok, gzTar(t, map[string]string{"index.html": "scratch"})); rec.Code != http.StatusOK {
		t.Fatalf("push status=%d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/internal/static-override", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	app.handleInternalDeleteStaticOverride(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if _, err := os.Lstat(filepath.Join(root, staticOverrideCurrentName)); !os.IsNotExist(err) {
		t.Fatalf("current symlink should be gone after revert (err=%v)", err)
	}
	roots := tankStaticRootSet{override: filepath.Join(root, staticOverrideCurrentName)}
	if _, ok := tankStaticFile(roots, "index.html"); ok {
		t.Fatalf("override should serve nothing after revert")
	}
}

#!/usr/bin/env python3
"""Functional smoke test for the generic live-preview sender.

Exercises live-preview-push.sh and live-preview-watch.sh end to end against
in-process fakes of the three real dependencies, so the build -> token-exchange
-> preview-resolve -> edge-push -> Glimmung-receipt -> observed-live contract is
verified without any cluster:

  * fake auth.romaine.life  : POST /api/auth/exchange/k8s  (projected token ->
    a service-principal JWT carrying sub + role=service)
  * fake live-preview-edge  : PUT/DELETE/GET /__live-preview/push|status — checks
    the Bearer JWT (role=service AND sub==authorized_subject) and the required
    X-Live-Preview-Build header; stores the uploaded gzip tar.
  * fake Glimmung           : GET /v1/previews (list, for resolution),
    POST /v1/previews/{p}/{n}/push-receipt, GET /v1/previews/{p}/{n}.

Run: python3 k8s/session-config/tests/live-preview-sender-test.py
Exits 0 on success, prints FAIL and exits 1 otherwise.
"""
import base64, gzip, io, json, os, subprocess, sys, tarfile, tempfile, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPTS = os.path.dirname(HERE)
PUSH = os.path.join(SCRIPTS, "live-preview-push.sh")
WATCH = os.path.join(SCRIPTS, "live-preview-watch.sh")

PROJECTED_TOKEN = "projected-sa-token-abc123"
# AUTHORIZED_SUBJECT is the preview owner the edge enforces (fixed). TOKEN_SUBJECT
# is the sub the fake auth mints into the JWT — normally equal, but the auth-
# failure test sets only TOKEN_SUBJECT to simulate a pod pushing another's preview.
AUTHORIZED_SUBJECT = "svc:tank:9999"
TOKEN_SUBJECT = AUTHORIZED_SUBJECT
SESSION_ID = "9999"
PROJECT = "demo-app"
NAME = "p-9999"

failures = []
def check(cond, msg):
    print(("  ok  " if cond else " FAIL ") + msg)
    if not cond:
        failures.append(msg)

def b64url(d: bytes) -> str:
    return base64.urlsafe_b64encode(d).rstrip(b"=").decode()

def make_jwt(sub: str, role: str) -> str:
    h = b64url(json.dumps({"alg": "RS256", "typ": "JWT", "kid": "fake"}).encode())
    p = b64url(json.dumps({"sub": sub, "role": role, "actor_email": "dev@romaine.life",
                           "iss": "https://auth.romaine.life", "exp": int(time.time()) + 3600}).encode())
    return f"{h}.{p}.fakesig"

def jwt_claims(token: str) -> dict:
    try:
        seg = token.split(".")[1]
        seg += "=" * (-len(seg) % 4)
        return json.loads(base64.urlsafe_b64decode(seg))
    except Exception:
        return {}

class State:
    def __init__(self):
        self.exchange_calls = 0
        self.pushed_build = None
        self.pushed_tar_names = None
        self.override_active = False
        self.receipts = []
        self.deleted = False
        self.edge_url = None
        self.auto_live = True  # flip the row to observed-live after a receipt
S = State()

def handler_for(name, routes):
    class H(BaseHTTPRequestHandler):
        def log_message(self, *a): pass
        def _send(self, code, obj):
            body = json.dumps(obj).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        def _bearer(self):
            h = self.headers.get("Authorization", "")
            return h[7:] if h.lower().startswith("bearer ") else ""
        def _read(self):
            n = int(self.headers.get("Content-Length", "0") or 0)
            return self.rfile.read(n) if n else b""
        def do_GET(self): routes(self, "GET")
        def do_PUT(self): routes(self, "PUT")
        def do_POST(self): routes(self, "POST")
        def do_DELETE(self): routes(self, "DELETE")
    return H

def auth_routes(h, method):
    if method == "POST" and h.path == "/api/auth/exchange/k8s":
        S.exchange_calls += 1
        tok = h._bearer()
        if tok != PROJECTED_TOKEN:
            return h._send(401, {"error": "bad projected token"})
        return h._send(200, {"token": make_jwt(TOKEN_SUBJECT, "service")})
    h._send(404, {"error": "nf"})

def edge_routes(h, method):
    if h.path.startswith("/__live-preview/"):
        suffix = h.path[len("/__live-preview/"):]
        claims = jwt_claims(h._bearer())
        if suffix == "push" and method == "PUT":
            if not h._bearer():
                return h._send(401, {"detail": "missing bearer"})
            if claims.get("role") != "service":
                return h._send(403, {"detail": "push requires role=service"})
            if claims.get("sub") != AUTHORIZED_SUBJECT:
                return h._send(403, {"detail": "push subject not authorized"})
            build = h.headers.get("X-Live-Preview-Build", "")
            if not build:
                return h._send(400, {"detail": "missing X-Live-Preview-Build"})
            raw = h._read()
            try:
                tf = tarfile.open(fileobj=io.BytesIO(gzip.decompress(raw)))
                names = sorted(tf.getnames())
            except Exception as e:
                return h._send(400, {"detail": f"bad archive: {e}"})
            S.pushed_build = build
            S.pushed_tar_names = names
            S.override_active = True
            return h._send(200, {"status": "ok", "build": build, "release": "rel-1",
                                 "files": len(names), "bytes": len(raw),
                                 "pushed_at": "2026-06-21T00:00:00Z", "by": claims.get("actor_email")})
        if suffix == "push" and method == "DELETE":
            if claims.get("sub") != AUTHORIZED_SUBJECT:
                return h._send(403, {"detail": "not authorized"})
            was = S.override_active
            S.override_active = False
            S.deleted = True
            return h._send(200, {"status": "reverted", "was_active": was})
        if suffix == "status" and method == "GET":
            if not h._bearer():
                return h._send(401, {"detail": "missing bearer"})
            return h._send(200, {"override_active": S.override_active, "build": S.pushed_build,
                                 "release": "rel-1", "pushed_at": "2026-06-21T00:00:00Z"})
    h._send(404, {"detail": "nf"})

def preview_row():
    observed = S.pushed_build if (S.auto_live and S.receipts) else None
    state = "live" if observed and observed == (S.receipts[-1] if S.receipts else None) else \
            ("pushed" if S.receipts else "ready")
    return {"project": PROJECT, "name": NAME, "session_id": SESSION_ID,
            "authorized_subject": AUTHORIZED_SUBJECT, "enabled": True, "state": state,
            "url": S.edge_url, "upstream_url": "http://127.0.0.1:1",
            "backend_prefixes": ["/api"], "live_build_id": (S.receipts[-1] if S.receipts else ""),
            "observed_build_id": observed or "", "detail": ""}

def glimmung_routes(h, method):
    if method == "GET" and h.path == "/v1/previews":
        if not h._bearer():
            return h._send(401, {"title": "unauthorized"})
        return h._send(200, [preview_row()])
    if method == "POST" and h.path == f"/v1/previews/{PROJECT}/{NAME}/push-receipt":
        body = json.loads(h._read() or b"{}")
        build = body.get("build", "")
        if not build:
            return h._send(422, {"title": "build required"})
        S.receipts.append(build)
        return h._send(200, preview_row())
    if method == "GET" and h.path == f"/v1/previews/{PROJECT}/{NAME}":
        return h._send(200, preview_row())
    h._send(404, {"title": "nf"})

def serve(routes, name):
    srv = ThreadingHTTPServer(("127.0.0.1", 0), handler_for(name, routes))
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_address[1]}"

def make_repo(tmp):
    """A minimal repo: frontend/ with a build that emits dist/index.html."""
    repo = os.path.join(tmp, "demo-repo")
    fe = os.path.join(repo, "frontend")
    os.makedirs(fe)
    with open(os.path.join(fe, "package.json"), "w") as f:
        json.dump({"name": "demo", "scripts": {"build": "true"}}, f)
    return repo, fe

def write_dist(fe, html):
    dist = os.path.join(fe, "dist")
    os.makedirs(dist, exist_ok=True)
    with open(os.path.join(dist, "index.html"), "w") as f:
        f.write(html)
    with open(os.path.join(dist, "app.js"), "w") as f:
        f.write("console.log('" + html + "')")
    return dist

def run_push(env, *args, timeout=60):
    return subprocess.run(["bash", PUSH, *args], env=env, capture_output=True, text=True, timeout=timeout)

def base_env(glimmung, token_path, extra=None):
    e = dict(os.environ)
    e.update({
        "AUTH_ROMAINE_TOKEN_PATH": token_path,
        "AUTH_ROMAINE_EXCHANGE_URL": AUTH_URL + "/api/auth/exchange/k8s",
        "GLIMMUNG_INTERNAL_URL": glimmung,
        "SESSION_ID": SESSION_ID,
    })
    if extra:
        e.update(extra)
    return e

def main():
    global AUTH_URL
    _, AUTH_URL = serve(auth_routes, "auth")
    _, S.edge_url = serve(edge_routes, "edge")
    _, GLIMMUNG = serve(glimmung_routes, "glimmung")

    tmp = tempfile.mkdtemp()
    token_path = os.path.join(tmp, "token")
    with open(token_path, "w") as f:
        f.write(PROJECTED_TOKEN)
    repo, fe = make_repo(tmp)

    print("\n[1] one-shot push: build via convention, resolve by SESSION_ID, push, receipt, wait-live")
    write_dist(fe, "v1")
    env = base_env(GLIMMUNG, token_path)
    r = run_push(env, "--repo", repo, "--build", "--build-cmd", "true", "--wait-live", "20")
    sys.stderr.write(r.stderr)
    check(r.returncode == 0, f"exit 0 (got {r.returncode})")
    check(S.exchange_calls >= 1, "exchanged projected token for service JWT")
    check(S.pushed_build and S.pushed_build.startswith("c-"), f"edge got content-hash build id ({S.pushed_build})")
    check(S.pushed_tar_names and "./index.html" in S.pushed_tar_names, "edge received tar with index.html")
    check(S.receipts and S.receipts[-1] == S.pushed_build, "receipt build matches pushed build")
    check("OBSERVED LIVE" in r.stderr, "--wait-live observed the build live")

    print("\n[2] resolve by authorized SUBJECT (no SESSION_ID)")
    S.receipts.clear(); S.pushed_build = None
    env2 = base_env(GLIMMUNG, token_path); env2.pop("SESSION_ID", None)
    r = run_push(env2, "--repo", repo, "--no-build")
    sys.stderr.write(r.stderr)
    check(r.returncode == 0, f"exit 0 resolving by subject (got {r.returncode})")
    check("resolved preview demo-app/p-9999" in r.stderr, "resolved the owner's preview by JWT subject")

    print("\n[3] explicit --project/--name/--preview-url (no Glimmung list)")
    S.receipts.clear(); S.pushed_build = None
    r = run_push(base_env(GLIMMUNG, token_path), "--repo", repo, "--no-build",
                 "--project", PROJECT, "--name", NAME, "--preview-url", S.edge_url)
    check(r.returncode == 0, f"exit 0 explicit target (got {r.returncode})")
    check(S.pushed_build is not None, "edge received the push with explicit target")

    print("\n[4] build id changes when dist content changes")
    first = S.pushed_build
    write_dist(fe, "v2-different-content")
    r = run_push(base_env(GLIMMUNG, token_path), "--repo", repo, "--no-build")
    check(S.pushed_build != first, f"new content -> new build id ({first} -> {S.pushed_build})")

    print("\n[5] auth failure surfaces actionable error (wrong subject -> edge 403)")
    global TOKEN_SUBJECT
    saved = TOKEN_SUBJECT
    TOKEN_SUBJECT = "svc:tank:OTHER"  # token now mints a sub the edge won't authorize
    r = run_push(base_env(GLIMMUNG, token_path), "--repo", repo, "--no-build",
                 "--project", PROJECT, "--name", NAME, "--preview-url", S.edge_url)
    check(r.returncode == 6, f"exit 6 on auth rejection (got {r.returncode})")
    check("authorized_subject" in r.stderr or "not authorized" in r.stderr, "error names the subject mismatch")
    TOKEN_SUBJECT = saved

    print("\n[6] --revert DELETEs the edge override")
    S.deleted = False
    r = run_push(base_env(GLIMMUNG, token_path), "--repo", repo, "--revert",
                 "--project", PROJECT, "--name", NAME, "--preview-url", S.edge_url)
    check(r.returncode == 0 and S.deleted, "edge override reverted")

    print("\n[7] watch daemon: detects a dist change and pushes once (--once)")
    S.receipts.clear(); S.pushed_build = None
    write_dist(fe, "watched-content-xyz")
    env = base_env(GLIMMUNG, token_path)
    r = subprocess.run(["bash", WATCH, "--repo", repo, "--once", "--debounce", "0",
                        "--interval", "0", "--project", PROJECT, "--name", NAME,
                        "--preview-url", S.edge_url], env=env, capture_output=True, text=True, timeout=60)
    sys.stderr.write(r.stderr)
    check(r.returncode == 0, f"watch --once exit 0 (got {r.returncode})")
    check(S.pushed_build is not None and S.receipts, "watch built id, pushed, and posted receipt")

    print("\n" + ("ALL PASSED" if not failures else f"{len(failures)} FAILURE(S)"))
    sys.exit(1 if failures else 0)

if __name__ == "__main__":
    main()

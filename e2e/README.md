# E2E smoke tests

Black-box smoke of the real proxy container, run on every PR and push to `main`
via GitHub Actions (see `.github/workflows/e2e.yaml`).

## Topology

```
host:8080 ──► mcp-auth-proxy (:80, Bearer-gated) ── stdio ──► testserver (tools)
```

- `docker-compose.yml` — builds the proxy image from the repo `Dockerfile`,
  runs it in stdio mode with `pkg/backend/testserver` as the backend, and
  exposes the proxied `/mcp` endpoint on the host.
- `smoke.sh` — the assertions. Run locally: `./e2e/smoke.sh`.
- `tokengen/` — mints a proxy-valid Bearer token from the key the proxy
  auto-generates at `/data/private_key.pem`, so `/mcp` can be called without an
  interactive OAuth flow.

## Assertions

| Check | Issue | Status |
| ----- | ----- | ------ |
| `/.well-known/oauth-protected-resource` advertises the root resource | RFC 9728 | hard |
| `/.well-known/oauth-protected-resource/mcp` advertises `<base>/mcp` | #15 | hard |
| `/mcp` and unknown well-known paths return 401 without a token | — | hard |
| container runs as non-root (UID 65532) | #19 | warn until #19 merges |
| `tools/list` relays `with_defs` schema verbatim (`definitions` kept) | #18 | warn until #18 (upstream PR #179 port) lands |

## CI

`.github/workflows/e2e.yaml` runs the smoke on `pull_request` (to `main`) and
`push` (to `main`), single `ubuntu-latest` job — free on a public repo.

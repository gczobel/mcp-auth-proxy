# E2E tests

Black-box tests of the real proxy container, run on every PR and push to
`main` via GitHub Actions (see `.github/workflows/e2e.yaml`). The suite is
TypeScript + Playwright. (Playwright was chosen over bash because the OAuth
regression it guards — issue #16's double-navigation — is a browser-behavior
bug best exercised by a real browser; the approach mirrors what
`smart-mcp-proxy/mcpproxy-go` does for its OAuth e2e, though the projects are
not related.)

## Topology

```
host:8080 ──► mcp-auth-proxy (:80, Bearer-gated) ── stdio ──► testserver (tools)
```

- `docker-compose.yml` — builds the proxy image from the repo `Dockerfile`,
  runs it in stdio mode with `pkg/backend/testserver` as the backend, and
  exposes the proxied `/mcp` endpoint on the host.
- `playwright/oauth-replay.spec.ts` — real-browser OAuth flow: DCR
  registration, password login, consent screen (client name + redirect host),
  Authorize, and the issue #16 replay window (a repeat navigation re-serves the
  same code); plus the Deny path (`error=access_denied`, request consumed).
- `playwright/smoke.spec.ts` — container smoke: healthz, the RFC 9728
  protected-resource documents, and the Bearer gate.
- `playwright/global-setup.ts` — waits for the stack at `E2E_BASE_URL`
  (default `http://localhost:8080`).

## Running locally

```bash
go build -o e2e/bin/testserver ./pkg/backend/testserver
docker compose -f e2e/docker-compose.yml build proxy
docker compose -f e2e/docker-compose.yml up -d

cd e2e/playwright
npm ci
npx playwright install chromium   # --with-deps on distros that need system libs
npx playwright test
```

Environment overrides: `E2E_BASE_URL` (default `http://localhost:8080`) and
`E2E_PASSWORD` (default `changeme`, matching the compose stack).

## CI

`.github/workflows/e2e.yaml` builds the image + fixture, starts the compose
stack, installs Chromium, and runs `npx playwright test` on `pull_request`
(to `main`) and `push` (to `main`), single `ubuntu-latest` job.

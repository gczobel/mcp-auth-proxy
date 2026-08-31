/**
 * Shared e2e configuration, resolved once from the environment.
 *
 * The stack under test is the compose stack from e2e/docker-compose.yml
 * (see global-setup), which runs with PASSWORD=changeme. The OAuth callback
 * is the stack's own /callback path, observed via waitForURL — so it must
 * derive from BASE, or overriding E2E_BASE_URL would strand the OAuth flow.
 */
export const BASE = process.env.E2E_BASE_URL || 'http://localhost:8080';
export const CALLBACK = `${BASE}/callback`;
export const PASSWORD = process.env.E2E_PASSWORD || 'changeme';

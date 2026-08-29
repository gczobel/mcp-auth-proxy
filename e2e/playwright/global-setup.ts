import { FullConfig } from '@playwright/test';

/**
 * Waits for the proxy stack under test to become healthy.
 *
 * The stack itself is brought up by the workflow / the operator (compose stack
 * from e2e/docker-compose.yml); this setup only gates the suite on it being
 * reachable, so a missing stack fails fast with a clear message instead of a
 * wall of connection errors from every spec.
 */
export default async function globalSetup(config: FullConfig) {
  const baseURL = process.env.E2E_BASE_URL || 'http://localhost:8080';
  const deadline = Date.now() + 120_000;

  for (;;) {
    try {
      const res = await fetch(`${baseURL}/healthz`);
      if (res.ok) {
        const body = await res.json();
        if (body && (body as { status?: string }).status === 'ok') {
          console.log(`e2e: stack healthy at ${baseURL}`);
          return;
        }
      }
    } catch {
      // not up yet
    }
    if (Date.now() > deadline) {
      throw new Error(
        `e2e: proxy stack never became healthy at ${baseURL}. ` +
          'Start it with: docker compose -f e2e/docker-compose.yml up -d ' +
          '(or set E2E_BASE_URL)',
      );
    }
    await new Promise((r) => setTimeout(r, 2000));
  }
}

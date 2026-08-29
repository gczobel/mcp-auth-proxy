import { defineConfig } from '@playwright/test';

/**
 * Playwright e2e config for mcp-auth-proxy.
 *
 * Requires a running stack under test (see global-setup): the compose stack
 * from e2e/docker-compose.yml exposed at E2E_BASE_URL (default
 * http://localhost:8080) with PASSWORD=changeme.
 *
 * OAuth tests share one proxy instance and one browser session per test, so
 * parallel execution would cause session-state conflicts — upstream
 * (smart-mcp-proxy/mcpproxy-go) runs its OAuth e2e the same way.
 */
export default defineConfig({
  testDir: '.',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { browserName: 'chromium' },
    },
  ],
  globalSetup: './global-setup',
});

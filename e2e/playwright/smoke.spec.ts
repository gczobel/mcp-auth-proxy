import { test, expect } from '@playwright/test';
import { BASE } from './env';

/**
 * Container smoke assertions migrated from e2e/smoke.sh to Playwright's
 * request fixture: healthz, both protected-resource documents (RFC 9728, #15),
 * and the Bearer gate. The tool-schema relay and non-root-user checks stay
 * deferred in smoke.sh's successor issues (#18, #19).
 */

test('healthz is up', async ({ request }) => {
  const res = await request.get(`${BASE}/healthz`);
  expect(res.status()).toBe(200);
  expect(await res.json()).toEqual({ status: 'ok' });
});

test('protected-resource metadata documents are served (RFC 9728, #15)', async ({
  request,
}) => {
  const root = await request.get(`${BASE}/.well-known/oauth-protected-resource`);
  expect(root.status()).toBe(200);
  expect((await root.json()).resource).toBe(`${BASE}/`);

  const mcp = await request.get(`${BASE}/.well-known/oauth-protected-resource/mcp`);
  expect(mcp.status()).toBe(200);
  expect((await mcp.json()).resource).toBe(`${BASE}/mcp`);
});

test('Bearer gate rejects unauthenticated access', async ({ request }) => {
  const mcp = await request.get(`${BASE}/mcp`);
  expect(mcp.status()).toBe(401);

  const unknown = await request.get(
    `${BASE}/.well-known/oauth-protected-resource/other`,
  );
  expect(unknown.status()).toBe(401);
});

import { test, expect, Page, APIRequestContext } from '@playwright/test';

/**
 * Real-browser OAuth consent flow + replay window (#16).
 *
 * Drives an actual Chromium through: DCR registration -> password login ->
 * consent screen (asserts client name + redirect host render) -> Authorize ->
 * a repeat GET/POST to the approval URL (the Claude.ai connector behavior that
 * regressed in #16) must re-serve the SAME redirect with the SAME code ->
 * token exchange with the replayed code.
 *
 * The consent screen's Deny button is covered too: it must redirect back to
 * the client with error=access_denied and consume the authorize request.
 *
 * The OAuth callback is observed via waitForURL (the redirect URI is what
 * Claude.ai would land back on), so the flow needs no real callback server.
 */

const BASE = process.env.E2E_BASE_URL || 'http://localhost:8080';
const CALLBACK = 'http://localhost:8080/callback';
const PASSWORD = process.env.E2E_PASSWORD || 'changeme';

interface Registration {
  client_id: string;
  client_secret: string;
}

async function registerClient(
  request: APIRequestContext,
  name: string,
): Promise<Registration> {
  const res = await request.post(`${BASE}/.idp/register`, {
    data: {
      client_name: name,
      grant_types: ['authorization_code', 'refresh_token'],
      response_types: ['code'],
      token_endpoint_auth_method: 'client_secret_basic',
      scope: 'test',
      redirect_uris: [CALLBACK],
    },
  });
  expect(res.ok(), `DCR registration failed: ${res.status()}`).toBeTruthy();
  const reg = (await res.json()) as Registration;
  expect(reg.client_id).toBeTruthy();
  return reg;
}

/** Extracts the authorization code from the callback URL after navigation. */
async function waitForCallbackCode(page: Page, waitMs = 10_000): Promise<string> {
  await page.waitForURL((url) => url.hostname === 'localhost' && url.pathname === '/callback', {
    timeout: waitMs,
  });
  const code = new URL(page.url()).searchParams.get('code') ?? '';
  if (!code) throw new Error('callback URL did not carry an authorization code');
  return code;
}

test('OAuth consent flow completes and repeat submissions replay the same code', async ({
  page,
  request,
}) => {
  const reg = await registerClient(request, 'e2e-oauth-client');

  const authURL =
    `${BASE}/.idp/auth?response_type=code&client_id=${reg.client_id}` +
    `&redirect_uri=${encodeURIComponent(CALLBACK)}&state=e2e-state`;

  // Step 1: land on the authorization endpoint -> login (RequireAuth redirects).
  await page.goto(authURL);
  await expect(page.locator('form.password-form')).toBeVisible({ timeout: 10_000 });

  // Step 2: real password login.
  await page.locator('#password').fill(PASSWORD);
  await page.locator('button[type="submit"]').click();

  // Step 3: consent screen renders client name + redirect host (MCP spec MUST).
  await expect(page.locator('h1')).toContainText('Authorize access', { timeout: 10_000 });
  await expect(page.locator('body')).toContainText('e2e-oauth-client is requesting access');
  await expect(page.locator('body')).toContainText('localhost');
  const approvalPath = new URL(page.url()).pathname;
  expect(approvalPath).toMatch(/^\/\.idp\/auth\//);

  // Step 4: Authorize -> browser is redirected to the callback with a code.
  await page.locator('button[name="decision"][value="authorize"]').click();
  const firstCode = await waitForCallbackCode(page);

  // Step 5: the browser re-navigates to the approval URL (Claude.ai re-navigation
  // hypothesis). The session is now past the consent step; the replay window must
  // re-serve the SAME redirect with the SAME code instead of a 403.
  await page.goto(`${BASE}${approvalPath}`);
  const replayCode = await waitForCallbackCode(page);
  expect(replayCode).toBe(firstCode);

  // Step 6: the replayed code must still be exchangeable at the token endpoint.
  const tokenRes = await request.post(`${BASE}/.idp/token`, {
    form: {
      grant_type: 'authorization_code',
      code: firstCode,
      redirect_uri: CALLBACK,
      client_id: reg.client_id,
      client_secret: reg.client_secret,
    },
  });
  expect(tokenRes.ok()).toBeTruthy();
  const tokens = (await tokenRes.json()) as { access_token?: string };
  expect(tokens.access_token).toBeTruthy();
});

test('consent screen Deny redirects to the client with access_denied and consumes the request', async ({
  page,
  request,
}) => {
  const reg = await registerClient(request, 'e2e-deny-client');

  const authURL =
    `${BASE}/.idp/auth?response_type=code&client_id=${reg.client_id}` +
    `&redirect_uri=${encodeURIComponent(CALLBACK)}&state=deny-state`;

  await page.goto(authURL);
  await expect(page.locator('form.password-form')).toBeVisible({ timeout: 10_000 });
  await page.locator('#password').fill(PASSWORD);
  await page.locator('button[type="submit"]').click();

  await expect(page.locator('h1')).toContainText('Authorize access', { timeout: 10_000 });
  const approvalPath = new URL(page.url()).pathname;
  await page.locator('button[name="decision"][value="deny"]').click();

  // Deny redirects the user-agent back to the client with error=access_denied.
  await page.waitForURL((url) => url.hostname === 'localhost' && url.pathname === '/callback', {
    timeout: 10_000,
  });
  expect(new URL(page.url()).searchParams.get('error')).toBe('access_denied');
  expect(new URL(page.url()).searchParams.get('state')).toBe('deny-state');

  // The authorize request is consumed: a repeat submission from the same
  // session must not issue a code — the HTML error page (403) shows instead.
  // page.request shares the browser context's cookies.
  const repeat = await page.request.post(`${BASE}${approvalPath}`, {
    form: { decision: 'authorize' },
  });
  expect(repeat.status()).toBe(403);
  expect(await repeat.text()).toContain('<html');
});

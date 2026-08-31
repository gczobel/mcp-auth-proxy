# mcp-auth-proxy

A self-hosted OAuth authorization server and reverse proxy that lets a home user publish an MCP server to the internet and access it securely from an agent client (e.g. Claude.ai), with login handled by a provider of their choice (GitHub, Google, OIDC, or password).

## Language

**Authorization session**:
The browser session that initiated an OAuth flow, together with the authorize-request ids bound to it. The consent step only completes for the session that started the flow; anything else is an "invalid authorization session".
_Avoid_: OAuth session, login session

**Consent screen**:
The explicit-authorization step: an HTML page shown after login that names the client and its redirect URI and requires the user to click Authorize before an authorization code is issued. Serves as the visible "who is getting access" confirmation for the owner.
_Avoid_: Approval form, explicit authorization step, authorize form

**Replay window**:
A short (~10s) same-session tolerance on the consent step: repeat GET/POSTs for an already-answered authorize request re-serve the same redirect with the same code instead of failing, so browser/client quirks (double POST, re-navigation) cannot break a completed flow.

**Home deployment**:
A single-operator deployment: one person publishes one MCP server to the internet and accesses it themselves. Drives defaults: secure-by-default, zero configuration, consent screen on.
_Avoid_: Production deployment, multi-tenant deployment

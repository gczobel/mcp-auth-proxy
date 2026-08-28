---
status: accepted
---

# Accept the concurrent double-POST race on the authorization return endpoint

`POST /.idp/auth/:ar_id` is the consent step: it consumes the authorize request and 303s back to the client's redirect URI with the authorization code (see issue #16). Two requests for the same authorize request can arrive concurrently — browser double-click on the Authorize button, retry layers, or re-submission heuristics. Both pass the `hasAuthorizeRequestID` session check before either consumes the request, so both generate a different code and both return a 303. The browser follows whichever response it processes first; the other code is orphaned in the store. We deliberately do **not** serialize consumption.

## Considered options

- **Per-AR mutex or atomic "consumed" flag** — rejected. The check reads the session cookie and consumption rewrites it; that read-modify-write is itself racy across goroutines in a way a handler mutex does not fix (two handlers read the same cookie, both mutate their copy, last save wins and silently drops a mutation). Correct serialization would mean locking the entire handler, and a process-local lock stops working the moment the server runs multi-instance. The complexity and its tests would protect a scenario that has not been observed in the wild and whose outcome is already benign (both POSTs succeed with a code; the client uses one).

## Consequences

- Two codes can be issued for one authorize request; the unexchanged one lingers in the KVS (no TTL sweep) — a few bytes per occurrence, acceptable at this project's single-operator home scale.
- The replay window (same-session, ~10s, session-carried) makes the endpoint converge: a repeat POST finds the cached redirect and re-serves the same response, so observable behavior stays consistent even under the race.
- If the race is ever observed to bite, or the server scales to multiple instances, revisit serialization. This ADR exists to stop a well-meaning "fix" that adds locking without adding correctness.

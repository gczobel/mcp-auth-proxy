---
status: accepted
---

# Fork release naming and default deployment pin

Defines the release convention for this fork so every future session and
conversation uses one scheme without re-litigating it.

## Release naming

Every release tag on this fork follows:

```
v<upstream-version>-fork.<n>
```

- `<upstream-version>` is the upstream `sigbit/mcp-auth-proxy` version this
  fork is based on at release time (e.g. `2.10.2`).
- `<n>` is a monotonically increasing fork build counter, starting at `1`.
- Examples: `v2.10.2-fork.1`, `v2.10.2-fork.2`, and `v2.10.3-fork.1` once the
  fork rebases onto a newer upstream.

A release is cut by tagging `main` HEAD and creating a GitHub Release (see
`.github/workflows/docker-build.yaml`: a `release` event publishes semver tags
and `latest` to `ghcr.io/gczobel/mcp-auth-proxy`).

## Default deployment pin: `latest`, rollback via explicit tags

The home deployment (Portainer stack `mcp-servers` on the NAS) uses

```
ghcr.io/gczobel/mcp-auth-proxy:latest
```

by default — the user releases fixes frequently and does not want to edit the
stack per release. `latest` only moves on a published release, so the
deployment tracks releases, not every push to `main`.

This intentionally supersedes the earlier "never `:latest`" guidance in the
deployment plan: that concern was rollback speed under pressure, and it is
still served — a rollback is a **one-line edit** of the compose image tag to an
explicit version (e.g. `v2.10.2-fork.1`), followed by a stack redeploy. The
explicit tags remain the rollback path; `latest` is the convenience default.

## Consequences / gotchas

- A release also pushes `{{major}}.{{minor}}` and `{{major}}` semver tags
  (e.g. `2.10`, `2`) via the workflow's metadata action; these are shared
  across the whole `2.x` line and must not be used for pins.
- `latest` does not restart running containers: picking up a new release
  requires a stack redeploy (Portainer re-pulls and recreates changed
  services).
- Deleting the `DATA_PATH` volume rotates the JWT/HMAC keys and invalidates all
  issued tokens — releases never touch it.

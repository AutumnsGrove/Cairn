# Security

Sepia is a private, single-user review journal. It runs on one machine on a Tailscale tailnet, and it holds
the operator's own reviews and the keys that let it talk to model providers. This file says how to report a
problem and what the security model actually is, so a report lands in the right place.

## Reporting

**Do not open a public issue for a vulnerability.** This repository is public. Report privately through
GitHub's [private vulnerability reporting](../../security/advisories/new), which goes only to the
maintainer.

Please include what you did, what happened, what you expected, and the smallest input that reproduces it. If
the report involves a key or a real review, do not paste it: describe the shape of it instead, and say that
you have one.

There is no bounty. This is one person's journal, and a clear report is thanks enough.

## The model, in one paragraph

There are no accounts and no passwords. The boundary is the tailnet: the service listens on `127.0.0.1:8893`
and `tailscale serve` is the only way in, so "can reach the port" is the whole authorisation story. Because
that boundary is a network rather than a login, the code refuses requests whose `Host` header is not the
tailnet name or loopback, and refuses state-changing requests and WebSocket upgrades whose `Origin` does not
match. Both checks exist together because a DNS-rebinding attack makes the two headers agree with each other
and disagree with their real origin.

The worst asset is the OpenRouter key in `.env` (mode 0600, never in the repo, never in a log or an error
body). The next worst is the review text itself, which is the operator's own words and must never appear in a
log, a fixture, a screenshot or a test.

## What is a vulnerability here

- Reading another person's reviews, or the key, through any request.
- Getting review text to a service that the plan did not name ([`docs/BUILD.md`](docs/BUILD.md) invariant 5).
- Making a request that is accepted with a hostile `Origin` or `Host`.
- Storing a non-draft entry without the confirm tap, or updating or deleting history the database is meant to
  reject.
- Reaching a live provider from a test or from CI.

## What is not

- "No authentication": that is the design, and the tailnet is the boundary. It is documented, not overlooked.
- Anything that requires the attacker to already be on the tailnet and able to run code on the operator's
  devices.
- Missing light mode, or the absence of a feature in [`PLAN.md`](PLAN.md) section 9 (phases).

## Supported versions

Sepia is deployed from `main` and from digest-pinned images. Only the current `main` is supported; there are
no maintenance branches.

# ccbunshin Go command

This directory contains the Go executable. It manages profiles, launches Claude Code, and runs the model-routed proxy.

Its documentation lives in two places. The project README, at [`../../README.md`](../../README.md), covers install, profiles, commands, shell integration and completion, systemd, and how to build and verify. The proxy reference is [`../../docs/PROXY.md`](../../docs/PROXY.md): `proxy.json` keys, dialects, provider credentials, and inbound authentication.

Maintainer-facing notes that are not part of the user documentation:

- [`../../docs/COMPATIBILITY_REVIEW.md`](../../docs/COMPATIBILITY_REVIEW.md) - what to re-check before a release.
- [`../../docs/DECISIONS.md`](../../docs/DECISIONS.md) - the decision log: what each settled design decision is, why, and what the rejected alternative cost.

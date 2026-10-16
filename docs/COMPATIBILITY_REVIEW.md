# Compatibility review

Maintainer-facing. This is what to re-check before a release; it is not needed to install or use
ccbunshin.

The Anthropic API is versioned such that new *optional* request fields and new streaming
event types can appear without a version bump. Both are additive, so the translator is
written to ignore unknown request fields and unknown stream events rather than fail on
them. Before a release, re-check that this still holds:

- API versioning policy - https://platform.claude.com/docs/en/api/versioning
- Anthropic API release notes - https://platform.claude.com/docs/en/release-notes/api
- Claude Code release notes - https://platform.claude.com/docs/en/release-notes/claude-code
- Claude Code changelog (raw, diffable) - https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md
- Messages and headers reference - https://platform.claude.com/docs/en/api/messages and https://platform.claude.com/docs/en/api/beta-headers
- Streaming event reference - https://platform.claude.com/docs/en/build-with-claude/streaming

The Claude Code changelog is worth watching specifically, because it documents environment
variables and endpoint behavior that affect a gateway rather than the model API.

# Security Policy

## Secret handling

Never commit real credentials or sensitive deployment data, including:

- `.env` files or production configuration dumps
- JWT/session secrets and passwords
- database, Redis, Kafka, NATS, Grafana, or Elasticsearch credentials
- TOS, DashScope, OpenAI, or other cloud/API keys
- SSH/TLS private keys, kubeconfigs, access tokens, or signed production URLs

Configuration examples must contain placeholders or explicitly local-only demo values. Production secrets belong in a secret manager and must be injected at runtime.

Every public release is scanned from a clean `git archive`, not directly from a developer worktree. This prevents ignored local files from entering the release snapshot.

## If a credential is exposed

1. Revoke or rotate it immediately.
2. Determine whether it exists only in the current tree or in Git history.
3. Remove it from the tree and rewrite affected history before publication when necessary.
4. Re-run secret scanning against both the release snapshot and repository history.
5. Review logs and provider audit events for unauthorized use.

Deleting a secret in a later commit is not sufficient because the earlier value remains recoverable from Git history.

## Reporting a vulnerability

Do not place credentials, exploit details, or personal data in a public issue. Use GitHub's private vulnerability reporting or a private Security Advisory when available. A normal public issue is appropriate only for non-sensitive hardening suggestions.

## Public delivery boundary

The public repository contains tracked application source, tests, migration/configuration templates, deployment manifests, and public documentation. It excludes real environments, build/test output, captured H5 datasets, internal agent notes, and private component repository histories.

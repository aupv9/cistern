# Security policy

Cistern handles credentials for production databases and protects their backups, so we take
security reports seriously.

## Supported versions

Cistern is pre-1.0. Security fixes are made on the `main` branch and released in the next version;
older 0.x releases are not patched.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report vulnerabilities privately through the hosting platform's private vulnerability reporting
(for example *Security → Report a vulnerability* on GitHub, or a confidential issue on GitLab), or
contact the maintainers directly. Include:

- the affected version or commit,
- a description of the issue and its impact,
- steps to reproduce, ideally with a minimal configuration,
- any suggested fix.

We aim to acknowledge reports within 3 working days and to agree on a disclosure timeline with you.

## Scope

Examples of issues we want to hear about:

- secrets (database passwords, S3 keys, private keys) reaching command lines, logs, manifests or
  webhooks;
- ways to read backup contents without the private key, or to alter an artifact without
  `cistern verify` noticing;
- ways for the credentials used by `cistern backup` to delete or overwrite existing snapshots in an
  S3 repository with Object Lock;
- path traversal or injection through job names, artifact names, database names or object keys.

The security design is described in [docs/encryption.md](docs/encryption.md) and
[docs/repositories.md](docs/repositories.md).

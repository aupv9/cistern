# Encryption and keys

Cistern encrypts every artifact on the backup host before it leaves the machine, using the
[age](https://age-encryption.org/v1) format with X25519 keys. Machines that make backups only need
the public key; reading a backup needs the private key, which should live somewhere else.

## Keys

```sh
cistern keygen --out identity.txt > recipient.txt     # or: age-keygen -o identity.txt
```

| File | Contains | Needed by |
| --- | --- | --- |
| `recipient.txt` | public key `age1…` | every host that runs `backup` |
| `identity.txt` | private key `AGE-SECRET-KEY-1…` (mode 0600) | `verify` (full mode), `restore`, incremental S3 backups |

Configure them with `encryption.recipients` / `recipients_file` and `encryption.identity_file`. An
artifact is encrypted to every configured recipient, so you can use two keys — for example one held
by operations and one kept offline — and restore with either.

## Key management

- **Keep the private key outside the backup system** (password manager, vault, offline copy). Losing
  it makes every backup unreadable; keep at least two copies in separate places.
- **Mount the private key only where it is needed**: verify and restore jobs, and S3-driver jobs
  that should run incrementally. Backup-only jobs work with the public key alone.
- **Rotation**: add the new recipient, keep the old private key until the last snapshot encrypted to
  it has expired, then remove it. Each manifest lists the recipients its snapshot was encrypted to.

## What is protected

| Threat | Protection | What remains |
| --- | --- | --- |
| Someone reads the repository | Artifacts are age-encrypted; one random file key per artifact | Manifests are **plain JSON**: job name, source host (without credentials), versions, artifact names (for SQL drivers: database names), sizes and times |
| The backup host is compromised | It holds only the public key; with Object Lock and least-privilege credentials it cannot delete existing snapshots | An attacker could write misleading new snapshots and read source data on that host |
| Ransomware deletes or overwrites backups | Object Lock; prune uses separate credentials | A bucket created without Object Lock offers no such protection |
| An artifact is modified | age authenticates every 64 KiB chunk; SHA-256 of the stored and the original bytes in the manifest | Manifests are not signed: someone with write access can replace a whole snapshot with data encrypted to the public key |
| Source passwords leak | Passed through environment variables or 0600 temporary files; credentials are stripped from manifests | Passwords remain visible to processes that can read the backup process's environment |
| Traffic is intercepted | HTTPS with TLS 1.2+ for S3 and webhooks | `http://` endpoints are allowed for test setups |

## The age format in Cistern

Cistern implements age v1 for X25519 recipients: a header with one stanza per recipient and an
HMAC-SHA256, followed by the payload in 64 KiB chunks encrypted with ChaCha20-Poly1305, with a flag
on the last chunk so truncated files are detected. Files are interoperable with the `age` CLI in both
directions (tested with age 1.2.1). ChaCha20-Poly1305 comes from an unmodified copy of
`golang.org/x/crypto`; X25519 and HKDF come from the Go standard library.

## Unencrypted repositories

Set `encryption.allow_plaintext: true` and configure no recipients to store artifacts unencrypted
(for example in a test lab). Manifests record `encryption: none`.

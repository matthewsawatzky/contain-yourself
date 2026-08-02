# VPN profile encryption key

Stored WireGuard profiles are sealed with AES-256-GCM under a single key. This
document covers where that key comes from and how to change it.

## Where the key comes from

In order of precedence:

1. `VPN_ENCRYPTION_KEY` — a hex-encoded 32-byte key supplied by the operator.
   When set, the controller never generates or writes a key file.
2. `VPN_ENCRYPTION_KEY_FILE` — a 32-byte key file, generated with mode `0600`
   on first use if absent. This is the default.

A malformed `VPN_ENCRYPTION_KEY` is fatal at startup. It is never quietly
ignored, because falling back to a generated key would leave every existing
profile unreadable while the controller reported itself healthy.

## Bringing your own key

Generate a key and keep it wherever you keep secrets:

```bash
docker compose exec controller vpnkeyctl generate
```

Put it in `.env` as `VPN_ENCRYPTION_KEY=<hex>` and restart. If you already have
profiles stored under a generated key file, rotate to the new key first
(below) — otherwise the controller will start but fail to read them.

In a multi-user deployment this key belongs to the host administrator. Every
user's private profiles are sealed under it. Per-user keys are deliberately not
supported: the controller has to decrypt a profile at provisioning time, which
can happen while that user is away, so it would have to hold the key anyway.
What the key protects is the data at rest — a stolen disk or backup — not
users from each other. Ownership and visibility rules do that.

## Verifying which key is in use

```bash
docker compose exec controller vpnkeyctl status
```

The fingerprint is the first four bytes of the key's SHA-256. It identifies a
key without revealing it, and it is also stored in each profile's header, so a
mismatch reports which key a profile actually needs instead of surfacing as an
indistinguishable authentication failure.

## Rotating

```bash
docker compose exec controller vpnkeyctl rotate --new-key <hex>
```

Rotation decrypts every profile before writing anything, so a wrong current key
or a single corrupt file aborts the whole operation with the directory
untouched. New files are staged alongside the originals and renamed in place
only after all of them are written.

Re-running a rotation that already finished is safe: profiles already under the
new key are recognised and skipped, so an interrupted run can simply be
repeated.

After rotating, update `VPN_ENCRYPTION_KEY` or the key file to the new value
and restart the controller. Until you do, it is still holding the old key and
will not be able to read the profiles it just re-encrypted.

Back up the profile directory before rotating. The key is the only thing
standing between a backup and the plaintext, and losing it means the profiles
are unrecoverable — they have to be re-uploaded.

## Storage format

```text
version 1: [0x01][nonce][ciphertext]
version 2: [0x02][key fingerprint (4 bytes)][nonce][ciphertext]
```

Version 1 files written by earlier releases are still read. Everything written
now is version 2; the fingerprint is what makes a wrong-key error legible.

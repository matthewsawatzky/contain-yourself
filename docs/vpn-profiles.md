# VPN profiles and multi-user access

The service does not include a VPN subscription or provider account. Obtain a
WireGuard configuration from your provider, open **VPN profiles**, give the
location a useful name, and paste the configuration. A VPN-required
workstation lets its owner choose any enabled profile they can access.

## Catalogue model

- **Global** profiles are created by an administrator and are visible to every
  current and future account. Marking one recommended sorts it to the front of
  the workstation selector. This is how new accounts can begin with a few
  organization-provided locations.
- **Private** profiles belong to one user. Every non-administrator upload is
  forced to private, so bring-your-own VPN material is not exposed to other
  users.
- The selector combines both sets. A user can choose an organization profile
  for one workstation and a personal profile for another.

Administrators can disable any profile. A normal user can enable or disable
only their own private profiles. Profile API responses expose location
metadata, never the stored WireGuard configuration or encryption reference.

## Storage and runtime

`DATA_DIRECTORY` is a host folder mounted at `/data` in the controller:

```text
data/
├── controller.db
├── vpn-profiles.key
├── vpn-profiles/
│   └── <opaque-id>.wg.enc
└── backups/
```

The controller strictly parses and canonicalizes the upload, rejecting
commands such as `PreUp` and `PostUp`, unknown directives, invalid keys,
split-tunnel-only routes, and hostname endpoints. It encrypts the result with
AES-GCM. At provisioning time it decrypts the selected profile and sends it
over the authenticated internal worker connection. The worker validates it
again and uploads it to the workstation's WSLAN gateway as the mode-`0600`
file `/run/wslan/wg0.conf` before start. The custom WSLAN image brings it up
with `wg-quick`; the private key is absent from Docker environment variables.

Profiles currently require an endpoint IP address rather than a hostname so
the tunnel can start without a pre-tunnel DNS dependency.

Keep `vpn-profiles.key` and the encrypted files together when backing up, and
protect the backup as credentials. Encryption prevents accidental plaintext
exposure; it is not a defense against an attacker who controls the host or
obtains both the key and ciphertext.

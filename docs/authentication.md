# Authentication

The first request redirects to `/setup` until one administrator exists.
Creation is transactional so two concurrent setup requests cannot both become
the initial administrator. Administrators can create additional standard or
administrator accounts from **Users**; there is no self-registration route.

Passwords require at least 12 characters. They are stored using
PBKDF2-HMAC-SHA256 with 600,000 iterations, a 128-bit random salt and a 256-bit
derived key. Comparison is constant-time.

Successful login creates a 256-bit random token. Only its SHA-256 hash is kept
in SQLite. The host-only `wm_session` cookie is `HttpOnly`, `SameSite=Lax`, has
a configured expiry, and becomes `Secure` when `SECURE_COOKIES=true`. Logout
revokes the server-side record. Login failures are rate-limited per source IP.
Unsafe authenticated requests receive same-origin checks.

Authorization is evaluated before workstation pages or app proxying. A normal
user sees owned workstations; administrators may inspect all records.
Global VPN profiles are selectable by every account, while private profiles
are selectable only by their owner. Administrators can see profile metadata
and disable profiles, but normal users cannot turn an upload into a global
profile.

Workstation owners can create share links with a named recipient, optional
expiration, maximum redemption count, and explicit permission set. Only the
token hash is stored. Redemption exchanges the URL secret for a path-scoped,
HttpOnly `wm_share` cookie; expiry and revocation are checked on every request.
The URL secret is shown to the owner once.

`open-apps` does not grant terminal access. Terminal WebSocket control requires
`terminal-control`; lifecycle actions require their corresponding restart or
stop permission. Share cookies cannot access the controller dashboard, logs,
metrics, other workstations, or owner management routes.

Future email OTP, TOTP, recovery codes, passkeys and key-file authentication
must layer onto server-side principals and must not turn app containers into
public endpoints.

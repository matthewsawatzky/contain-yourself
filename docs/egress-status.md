# Egress status

Every workstation reports how its traffic is *actually* leaving, not just how
it was configured to leave. The panel is on the workstation page, and the same
data is available from `GET /api/v1/workstations/{id}/egress`.

```json
{
  "workstation_id": "ws-abc123def4",
  "mode": "wireguard",
  "healthy": true,
  "fails_closed": true,
  "tunnel": {
    "up": true,
    "endpoint": "203.0.113.10:51820",
    "handshake_age_seconds": 14,
    "received_bytes": 5242880,
    "sent_bytes": 2097152
  }
}
```

## Where the numbers come from

The workstation's own WSLAN gateway reads them from local kernel state with
`wg show wg0 dump`. **Nothing is sent to an outside service.** A workstation
whose purpose is not to be observed should not have to phone an IP-echo service
to tell you it is working, so it does not.

The consequence is that this answers "is the tunnel up, to which peer, and is
traffic moving" — not "what does a website see as my address". Those are
usually the same thing, but the second cannot be established without asking
someone, which is a trade this deliberately does not make.

## The private key

`wg show <iface> dump` prints the interface on its first line, and the second
field of that line is the **private key**. The parser skips line 0 entirely and
reads only peer records. A test asserts the key never appears in a rendered
response; treat it as load-bearing if you touch that function.

## Reading the panel

| Field | Meaning |
| --- | --- |
| Mode | the configured egress mode the gateway is running |
| Tunnel | up once a handshake has completed within 180 seconds, matching WireGuard's own idea of a live session |
| Last handshake | `never` when no handshake has ever completed |
| Exit | the peer address traffic appears to come from |
| Transferred | cumulative bytes over the tunnel since it came up |

When the gateway cannot be reached, the panel falls back to the **configured**
mode and reports the state as unavailable. It never claims a connection is
working on the strength of configuration alone.

## Authorization

The status names the VPN exit, so the gateway requires the worker token, and
the worker endpoint requires it in turn. The controller's own endpoint requires
a session and workstation ownership like every other workstation route.

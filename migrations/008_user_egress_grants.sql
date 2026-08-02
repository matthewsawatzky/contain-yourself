-- Administrators decide which connection types each user may create a
-- workstation with.
--
-- An empty grant set denies everything, so existing users are backfilled
-- explicitly rather than being left to a default. Without the backfill this
-- migration would silently revoke every user's ability to create anything.
--
-- host-gateway is deliberately absent: it reaches services on the Docker host
-- and stays administrator-only, never a per-user grant.
ALTER TABLE users ADD COLUMN allowed_egress TEXT NOT NULL DEFAULT '';

UPDATE users SET allowed_egress = 'direct,wireguard,ipv6';

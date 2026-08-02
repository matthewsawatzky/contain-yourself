-- Named egress modes replace the boolean vpn_required as the way a workstation
-- says how its traffic reaches the network.
--
-- vpn_required is kept: it still drives profile selection in the UI, and rows
-- written before this migration carry an empty mode. An empty mode therefore
-- means "ask vpn_required", which is exactly what egress.Resolve does.
ALTER TABLE workstations ADD COLUMN egress_mode TEXT NOT NULL DEFAULT '';

-- Backfill the rows we can name unambiguously so the fallback path is only
-- ever exercised by genuinely unknown cases.
UPDATE workstations SET egress_mode = 'wireguard' WHERE vpn_required = 1;
UPDATE workstations SET egress_mode = 'direct' WHERE vpn_required = 0;

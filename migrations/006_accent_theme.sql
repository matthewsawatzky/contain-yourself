-- Accent colours personalise the controller UI and are published to app UIs
-- running inside a workstation so they can theme to match.
--
-- An empty string means "inherit": a user inherits the deployment default, and
-- a workstation inherits its viewer's colour. Storing the empty string rather
-- than NULL keeps the resolution logic in one place and avoids nullable scans.
ALTER TABLE users ADD COLUMN accent_color TEXT NOT NULL DEFAULT '';
ALTER TABLE workstations ADD COLUMN accent_color TEXT NOT NULL DEFAULT '';

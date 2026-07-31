# Recovery

## Backups

SQLite backups use `VACUUM INTO`, producing a consistent standalone database
under `/data/backups`.

```bash
export WORKSTATION_MANAGER_SESSION='...'
workstationctl backup
docker compose cp controller:/data/backups ./backups
```

Back up the complete host `DATA_DIRECTORY`, not only SQLite. It also contains
the encrypted VPN profile files and `vpn-profiles.key`; both are required to
restore profiles. Protect that backup as secret material. Docker volumes
contain workspace data and need a volume-aware backup tool.

## Restore

Restore is intentionally offline:

1. `docker compose stop controller`
2. Preserve the current `controller.db`, `controller.db-wal`, and
   `controller.db-shm`.
3. Validate the backup with `sqlite3 backup.db 'PRAGMA quick_check;'`.
4. Replace `controller.db` under the host `DATA_DIRECTORY` and remove stale
   WAL/SHM files for the old database.
5. Ensure ownership is UID/GID 10001.
6. `docker compose start controller`
7. Run `workstationctl reconcile`.

Never overwrite a live SQLite database file. The controller refuses to expose
a live restore API for this reason.

## Reconciliation

Startup reconciliation matches SQLite records to Docker labels. Missing
resource sets mark the workstation `error`. Orphans are logged and retained.
Inspect them before explicit deletion:

```bash
docker ps -a --filter label=managed-by=workstation-manager
docker volume ls --filter label=managed-by=workstation-manager
workstationctl reconcile
```

After interrupted deletion, retry the delete action. Worker deletion is
idempotent for missing containers and volumes.

If startup reports corruption, stop the controller, preserve all database
files, restore the newest verified backup, and only then reconcile. Do not
initialize a fresh database over the corrupt copy.

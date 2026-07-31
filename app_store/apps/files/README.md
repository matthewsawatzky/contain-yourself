# Files

File Browser configured with `/workspace` as its root and a private database
volume. Requests stay under `/apps/files/` so the controller can authenticate
and proxy them.

Test upload, download, rename, delete, and the health endpoint when updating
the image.

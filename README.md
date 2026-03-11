# claude_scripts

A collection of utility shell scripts.

## Scripts

### `scripts/main.sh` — Media Organizer

Organizes media files in a directory into a structured folder hierarchy by **file type** and **creation date**.

**Usage:**

```bash
./scripts/main.sh /path/to/directory
```

**What it does (3 passes):**

1. **Organize by type** — Moves files into folders based on extension (JPEG, RAW, HIF, MOV, MP4, BRAW, NEV, NDF)
2. **Organize by date** — Within each type folder, groups files into date-based subfolders (e.g. `March11th2026`)
3. **Reunite sidecars** — Finds orphaned sidecar files (xmp, dxo, dop, pp3, xml) and moves them next to their parent media file

**Supported formats:**

| Folder | Extensions |
|--------|-----------|
| JPEG | jpg, jpeg |
| HIF | hif, heif, heic |
| RAW | raf, arw, nef, cr2, cr3, dng, orf, rw2, pef |
| MOV | mov |
| MP4 | mp4 |
| BRAW | braw |
| NEV | nev |
| NDF | ndf |

**Requirements:** macOS (uses `stat -f %B` and `date -r` for file birth time)

### `scripts/sync_to_nas.sh` — NAS Sync

Syncs camera directories to a mounted NAS volume using rsync. Each subdirectory in the source (one per camera) is synced to a matching folder on the NAS.

**Usage:**

```bash
./scripts/sync_to_nas.sh /Volumes/CameraCards /Volumes/NAS/Media
```

**Behavior:**

- New files are copied over
- Existing files are only overwritten if the source is newer (`--update`)
- Destination directories are created automatically
- Preserves timestamps, permissions, and directory structure (`-a`)

**Requirements:** macOS, mounted NAS volume

# Organize Photos and Sync

A collection of utility shell scripts and a Go-based media organizer.

## Go Media Organizer (`main.go`)

Parallel rewrite of `scripts/main.sh` in Go. Uses a worker pool (`runtime.NumCPU()` goroutines) to move files concurrently across all 3 passes.

**Usage:**

```bash
go run main.go /path/to/directory

# Use modification time instead of birth time for date folders
go run main.go -mtime /path/to/directory
```

**What it does (3 passes):**

1. **Organize by type** — Moves files into folders based on extension (JPEG, RAW, HIF, MOV, MP4, BRAW, NEV, NDF)
2. **Organize by date** — Within each type folder, groups files into date-based subfolders using the file's birth time (e.g. `March11th2026`)
3. **Reunite sidecars** — Finds orphaned sidecar files (dxo, dop, pp3, xml, aac, lrf, mp3) and moves them next to their parent media file. Orphan XML/AAC sidecars default to MP4 (Sony/DJI workflow). Orphan MP3s (no matching WAV) default to AUDIO.

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
| AUDIO | wav |

**Key details:**

- Jobs are sorted by destination and filename before dispatch for deterministic ordering
- All destination directories are pre-created before workers start (no race conditions)
- Sidecars travel with their parent file in the same goroutine
- By default, birth time is used for date folders. If files on a drive all share the same birth time (common when bulk-copied to a new drive), use `-mtime` to organize by modification time instead
- **macOS only** — uses `syscall.Stat_t.Birthtimespec` (the same field Finder displays as "Date Created")
- **AppleDouble `._` files are skipped** — On non-native filesystems (exFAT, FAT32, NTFS — common on external SSDs like the Samsung T9), macOS creates hidden `._*` companion files to store extended attributes and resource forks. These files share the same extension as their parent (e.g. `._DSC00596.JPG`), so the organizer would otherwise treat them as real media files. However, when the real file is moved, macOS automatically cleans up the corresponding `._` file — causing a "no such file or directory" error when the organizer tries to move it separately. All `._` files are skipped in every pass.

## Tests

```bash
go test -v ./...
```

**How the tests work:**

1. **Fixture generation** — `TestOrganize` generates 1000+ empty files in a temp directory, simulating output from 10 different cameras (Sony, Canon, Fuji, Nikon, Blackmagic, DJI, GoPro, Phase One, Panasonic) with a mix of media extensions, sidecar files, uppercase extensions, orphan sidecars, files with no extension, and unknown extensions
2. **Immutable copy** — Fixtures are copied to a separate working directory before the organizer runs. The original fixture directory is verified unchanged both before and after the organize step
3. **Validation** — After organizing, the test checks:
   - Every media file is in the correct `TypeFolder/DateFolder/` path
   - Every sidecar with a parent is in the same directory as its parent
   - Orphan XMP sidecars still exist (left in place)
   - Orphan XML sidecars ended up in `MP4/<date>/`
   - No media files remain in the root directory
   - No files were lost (total count is preserved)
   - No files ended up in the wrong type folder

**Caveats:**

- **All test files share the same birth time** — macOS birth time is set at file creation and cannot be changed via `os.Chtimes` or any standard Go API. Since all fixtures are created during the same test run, they all get the same birth date. This means the date-grouping pass puts everything into a single date folder. The test reads the actual birth time to determine the expected folder name rather than assuming a specific date.
- **No multi-date coverage in integration test** — Because of the birth time limitation above, the test cannot verify that files from different days end up in different date folders. The `TestFormatDate` and `TestOrdinalSuffix` unit tests cover the date formatting logic independently.
- **macOS only** — The `fileTime` function uses `syscall.Stat_t.Birthtimespec` / `Mtimespec` which are macOS-specific. Tests will not compile on Linux.

## Shell Scripts

### `scripts/main.sh` — Media Organizer (Bash)

The original single-threaded bash version of the media organizer. Same 3-pass logic as the Go version.

```bash
./scripts/main.sh /path/to/directory
```

**Requirements:** macOS (uses `stat -f %B` and `date -r` for file birth time)

### `scripts/sync_to_nas.sh` — NAS Sync

Syncs camera directories to a mounted NAS volume using rsync. Each subdirectory in the source (one per camera) is synced to a matching folder on the NAS.

```bash
./scripts/sync_to_nas.sh /Volumes/CameraCards /Volumes/NAS/Media
```

**Behavior:**

- New files are copied over
- Existing files are only overwritten if the source is newer (`--update`)
- Destination directories are created automatically
- Preserves timestamps, permissions, and directory structure (`-a`)

**Requirements:** macOS, mounted NAS volume

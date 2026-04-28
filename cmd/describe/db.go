package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 2

// openDB opens (or creates) the SQLite library at path, runs schema +
// migrations, and returns a connection with FK enforcement on.
func openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(wal)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func initSchema(db *sql.DB) error {
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := dropLegacyPathColumns(db); err != nil {
		return fmt.Errorf("migrate legacy columns: %w", err)
	}
	if _, err := db.Exec(
		"INSERT OR IGNORE INTO schema_version(version, applied_at) VALUES (?, ?)",
		schemaVersion, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("schema_version row: %w", err)
	}
	return nil
}

// dropLegacyPathColumns removes the slice-1 path columns from `photos` if a
// previous version of the schema created them. No-op on fresh DBs and on DBs
// that have already been migrated.
func dropLegacyPathColumns(db *sql.DB) error {
	for _, col := range legacyPathCols {
		var found int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('photos') WHERE name = ?",
			col,
		).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE photos DROP COLUMN %s", col)); err != nil {
			return fmt.Errorf("drop %s: %w", col, err)
		}
	}
	return nil
}

// listExistingNames returns the set of photo names already in the DB. Used
// for the bulk skip-exists check at job-list construction time so we don't
// re-run vision inference for already-described photos.
func listExistingNames(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query("SELECT name FROM photos")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := make(map[string]bool)
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names[n] = true
	}
	return names, rows.Err()
}

// insertPhoto writes one describe result across photos / exif / descriptions /
// inference / thumbnails in a single transaction. UPSERT semantics so re-runs
// (e.g. with -force) overwrite cleanly.
func insertPhoto(
	db *sql.DB,
	name, srcPath string,
	exif exifData,
	desc string,
	fields descriptionFields,
	thumbnailBytes []byte,
	model string,
	previewMs, inferenceMs int64,
) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO photos (id, name, file_path, file_basename)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			file_path     = excluded.file_path,
			file_basename = excluded.file_basename,
			updated_at    = datetime('now')
	`, name, name, srcPath, exif.FileName); err != nil {
		return fmt.Errorf("upsert photos: %w", err)
	}

	iso, year, month := parseExifDate(exif.DateTimeOriginal)
	if _, err := tx.Exec(`
		INSERT INTO exif (
			photo_id, camera_make, camera_model, lens_model, lens_info,
			date_taken, date_taken_year, date_taken_month,
			focal_length_mm, focal_length_35mm, f_number, exposure_time_seconds,
			iso, exposure_compensation, exposure_mode, metering_mode, white_balance, flash,
			image_width, image_height, gps_latitude, gps_longitude, artist, software
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(photo_id) DO UPDATE SET
			camera_make           = excluded.camera_make,
			camera_model          = excluded.camera_model,
			lens_model            = excluded.lens_model,
			lens_info             = excluded.lens_info,
			date_taken            = excluded.date_taken,
			date_taken_year       = excluded.date_taken_year,
			date_taken_month      = excluded.date_taken_month,
			focal_length_mm       = excluded.focal_length_mm,
			focal_length_35mm     = excluded.focal_length_35mm,
			f_number              = excluded.f_number,
			exposure_time_seconds = excluded.exposure_time_seconds,
			iso                   = excluded.iso,
			exposure_compensation = excluded.exposure_compensation,
			exposure_mode         = excluded.exposure_mode,
			metering_mode         = excluded.metering_mode,
			white_balance         = excluded.white_balance,
			flash                 = excluded.flash,
			image_width           = excluded.image_width,
			image_height          = excluded.image_height,
			gps_latitude          = excluded.gps_latitude,
			gps_longitude         = excluded.gps_longitude,
			artist                = excluded.artist,
			software              = excluded.software
	`,
		name,
		nullIfEmpty(exif.Make),
		nullIfEmpty(exif.Model),
		nullIfEmpty(exif.LensModel),
		nullIfEmpty(exif.LensInfo),
		iso, year, month,
		parseDimensionMM(exif.FocalLength),
		parseDimensionMM(exif.FocalLengthIn35mm),
		parseFloatLoose(exif.FNumber),
		parseExposureTime(exif.ExposureTime),
		parseIntLoose(exif.ISO),
		parseFloatLoose(exif.ExposureCompensation),
		nullIfEmpty(exif.ExposureMode),
		nullIfEmpty(exif.MeteringMode),
		nullIfEmpty(exif.WhiteBalance),
		nullIfEmpty(exif.Flash),
		parseIntLoose(exif.ImageWidth),
		parseIntLoose(exif.ImageHeight),
		parseFloatLoose(exif.GPSLatitude),
		parseFloatLoose(exif.GPSLongitude),
		nullIfEmpty(exif.Artist),
		nullIfEmpty(exif.Software),
	); err != nil {
		return fmt.Errorf("upsert exif: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO descriptions (photo_id, subject, setting, light, colors, composition, full_description)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(photo_id) DO UPDATE SET
			subject          = excluded.subject,
			setting          = excluded.setting,
			light            = excluded.light,
			colors           = excluded.colors,
			composition      = excluded.composition,
			full_description = excluded.full_description
	`,
		name,
		nullIfEmpty(fields.Subject),
		nullIfEmpty(fields.Setting),
		nullIfEmpty(fields.Light),
		nullIfEmpty(fields.Colors),
		nullIfEmpty(fields.Composition),
		nullIfEmpty(desc),
	); err != nil {
		return fmt.Errorf("upsert descriptions: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO inference (photo_id, raw_response, model, preview_ms, inference_ms, described_at)
		VALUES (?, NULL, ?, ?, ?, datetime('now'))
		ON CONFLICT(photo_id) DO UPDATE SET
			model        = excluded.model,
			preview_ms   = excluded.preview_ms,
			inference_ms = excluded.inference_ms,
			described_at = datetime('now')
	`, name, nullIfEmpty(model), previewMs, inferenceMs); err != nil {
		return fmt.Errorf("upsert inference: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO thumbnails (photo_id, bytes, width, created_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(photo_id) DO UPDATE SET
			bytes      = excluded.bytes,
			width      = excluded.width,
			created_at = datetime('now')
	`, name, thumbnailBytes, 1024); err != nil {
		return fmt.Errorf("upsert thumbnails: %w", err)
	}

	return tx.Commit()
}

// findRepoRoot walks up from cwd looking for a .git directory; returns cwd
// unchanged if none is found. Lets the default DB path resolve to the repo's
// tools/.sql_index regardless of where the script invokes us from.
func findRepoRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// defaultDBPath returns the canonical library.db path resolved from the
// repo root (via .git lookup) so scripts that cd into cmd/describe still
// land on tools/.sql_index/library.db.
func defaultDBPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "library.db"
	}
	root := findRepoRoot(cwd)
	return filepath.Join(root, "tools", ".sql_index", "library.db")
}

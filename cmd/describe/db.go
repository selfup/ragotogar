package main

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const schemaVersion = 4 // v4: descriptions gains vantage + ground_truth (rich POV/count fields)

// openDB opens a connection to the library Postgres database, applies the
// schema (CREATE TABLE IF NOT EXISTS — idempotent), and returns it.
//
// The vector extension must already be loaded. Run ./scripts/bootstrap.sh
// on a fresh machine to install Postgres + pgvector and load the extension.
func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect %s: %w", dsn, err)
	}
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func initSchema(db *sql.DB) error {
	// pgvector is required for the chunks table — load it before any
	// schema DDL so a fresh DB (e.g. test temp database) self-bootstraps.
	// Trusted-extension flag in pgvector 0.7+ means no superuser needed.
	if _, err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return fmt.Errorf("load vector extension: %w (run ./scripts/bootstrap.sh on a fresh machine)", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if _, err := db.Exec(
		"INSERT INTO schema_version(version, applied_at) VALUES ($1, $2) ON CONFLICT (version) DO NOTHING",
		schemaVersion, time.Now().UTC(),
	); err != nil {
		return fmt.Errorf("schema_version row: %w", err)
	}
	return nil
}

// migrate applies forward-only schema deltas for libraries that pre-date the
// current schemaVersion. Each step is idempotent (ADD COLUMN IF NOT EXISTS
// etc.) so running on a fresh DB created by schemaSQL is a no-op. The
// schema_version row is bumped by the caller after migrate succeeds.
func migrate(db *sql.DB) error {
	var maxVersion int
	if err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&maxVersion); err != nil {
		// schema_version table doesn't exist yet — schemaSQL should have
		// just created it, so this is unreachable on a real DB; bail safe.
		return nil
	}
	if maxVersion < 4 {
		if err := migrateV4(db); err != nil {
			return fmt.Errorf("v4: %w", err)
		}
	}
	return nil
}

// migrateV4 adds the vantage + ground_truth prose columns and rebuilds the
// generated fts column to include them. Generated columns can't be ALTER'd
// in place, so the column gets dropped and recreated — this is non-lossy
// because the column is GENERATED ALWAYS … STORED (recomputed from inputs).
func migrateV4(db *sql.DB) error {
	if _, err := db.Exec(`ALTER TABLE descriptions ADD COLUMN IF NOT EXISTS vantage TEXT`); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE descriptions ADD COLUMN IF NOT EXISTS ground_truth TEXT`); err != nil {
		return err
	}
	// The fts column references columns that didn't exist when v3 created it;
	// drop and recreate. Indexes on a dropped column go with it, so the index
	// is recreated alongside.
	if _, err := db.Exec(`ALTER TABLE descriptions DROP COLUMN IF EXISTS fts`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		ALTER TABLE descriptions ADD COLUMN fts tsvector GENERATED ALWAYS AS (
			to_tsvector('english',
				coalesce(subject,'')          || ' ' ||
				coalesce(setting,'')          || ' ' ||
				coalesce(light,'')            || ' ' ||
				coalesce(colors,'')           || ' ' ||
				coalesce(composition,'')      || ' ' ||
				coalesce(vantage,'')          || ' ' ||
				coalesce(ground_truth,'')     || ' ' ||
				coalesce(full_description,''))
		) STORED
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_descriptions_fts ON descriptions USING gin(fts)`); err != nil {
		return err
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
//
// The chunks table is owned by the indexer (tools/index_and_vectorize); a new
// describe overwrites the photo row but does not touch chunks. Re-running the
// indexer regenerates chunks from the fresh description.
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
		VALUES ($1, $2, $3, $4)
		ON CONFLICT(id) DO UPDATE SET
			file_path     = EXCLUDED.file_path,
			file_basename = EXCLUDED.file_basename,
			updated_at    = now()
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
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
		ON CONFLICT(photo_id) DO UPDATE SET
			camera_make           = EXCLUDED.camera_make,
			camera_model          = EXCLUDED.camera_model,
			lens_model            = EXCLUDED.lens_model,
			lens_info             = EXCLUDED.lens_info,
			date_taken            = EXCLUDED.date_taken,
			date_taken_year       = EXCLUDED.date_taken_year,
			date_taken_month      = EXCLUDED.date_taken_month,
			focal_length_mm       = EXCLUDED.focal_length_mm,
			focal_length_35mm     = EXCLUDED.focal_length_35mm,
			f_number              = EXCLUDED.f_number,
			exposure_time_seconds = EXCLUDED.exposure_time_seconds,
			iso                   = EXCLUDED.iso,
			exposure_compensation = EXCLUDED.exposure_compensation,
			exposure_mode         = EXCLUDED.exposure_mode,
			metering_mode         = EXCLUDED.metering_mode,
			white_balance         = EXCLUDED.white_balance,
			flash                 = EXCLUDED.flash,
			image_width           = EXCLUDED.image_width,
			image_height          = EXCLUDED.image_height,
			gps_latitude          = EXCLUDED.gps_latitude,
			gps_longitude         = EXCLUDED.gps_longitude,
			artist                = EXCLUDED.artist,
			software              = EXCLUDED.software
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
		INSERT INTO descriptions (photo_id, subject, setting, light, colors, composition, vantage, ground_truth, full_description)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT(photo_id) DO UPDATE SET
			subject          = EXCLUDED.subject,
			setting          = EXCLUDED.setting,
			light            = EXCLUDED.light,
			colors           = EXCLUDED.colors,
			composition      = EXCLUDED.composition,
			vantage          = EXCLUDED.vantage,
			ground_truth     = EXCLUDED.ground_truth,
			full_description = EXCLUDED.full_description
	`,
		name,
		nullIfEmpty(fields.Subject),
		nullIfEmpty(fields.Setting),
		nullIfEmpty(fields.Light),
		nullIfEmpty(fields.Colors),
		nullIfEmpty(fields.Composition),
		nullIfEmpty(fields.Vantage),
		nullIfEmpty(fields.GroundTruth),
		nullIfEmpty(desc),
	); err != nil {
		return fmt.Errorf("upsert descriptions: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO inference (photo_id, raw_response, model, preview_ms, inference_ms, described_at)
		VALUES ($1, NULL, $2, $3, $4, now())
		ON CONFLICT(photo_id) DO UPDATE SET
			model        = EXCLUDED.model,
			preview_ms   = EXCLUDED.preview_ms,
			inference_ms = EXCLUDED.inference_ms,
			described_at = now()
	`, name, nullIfEmpty(model), previewMs, inferenceMs); err != nil {
		return fmt.Errorf("upsert inference: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO thumbnails (photo_id, bytes, width, created_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT(photo_id) DO UPDATE SET
			bytes      = EXCLUDED.bytes,
			width      = EXCLUDED.width,
			created_at = now()
	`, name, thumbnailBytes, 1024); err != nil {
		return fmt.Errorf("upsert thumbnails: %w", err)
	}

	return tx.Commit()
}

// defaultDSN reads the LIBRARY_DSN env var, falling back to a local Postgres
// connection over the default unix socket. Matches what scripts/bootstrap.sh
// sets up.
func defaultDSN() string {
	if v := os.Getenv("LIBRARY_DSN"); v != "" {
		return v
	}
	return "postgres:///ragotogar"
}

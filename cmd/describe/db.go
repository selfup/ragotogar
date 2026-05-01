package main

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const schemaVersion = 9 // v9: descriptions.condition prose column — wear/age/cleanliness/construction state

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
	// Migrate runs before schemaSQL: schemaSQL declares the *current* shape
	// (e.g. halfvec(2560) chunks + halfvec_cosine_ops HNSW), and on an existing
	// DB the chunks column may still be vector(768). Running schemaSQL first
	// would fail at CREATE INDEX IF NOT EXISTS because the new opclass can't
	// bind to the old column type. migrateV6 fixes the column shape first;
	// schemaSQL then becomes a no-op on existing DBs and a fresh-create on
	// empty ones (where migrate's version query errors and bails safely).
	if err := migrate(db); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
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
// current schemaVersion. Each step is idempotent on its own version range. On
// a fresh DB schema_version doesn't exist yet — the SELECT errors and we bail
// safely; schemaSQL then runs and creates the schema at the current version.
// The schema_version row is bumped by the caller after migrate succeeds.
func migrate(db *sql.DB) error {
	var maxVersion int
	if err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&maxVersion); err != nil {
		// Fresh DB: schemaSQL hasn't run yet, so schema_version doesn't
		// exist. No deltas to apply — schemaSQL will create everything at
		// the current shape.
		return nil
	}
	if maxVersion < 4 {
		if err := migrateV4(db); err != nil {
			return fmt.Errorf("v4: %w", err)
		}
	}
	if maxVersion < 6 {
		if err := migrateV6(db); err != nil {
			return fmt.Errorf("v6: %w", err)
		}
	}
	if maxVersion < 7 {
		if err := migrateV7(db); err != nil {
			return fmt.Errorf("v7: %w", err)
		}
	}
	if maxVersion < 8 {
		if err := migrateV8(db); err != nil {
			return fmt.Errorf("v8: %w", err)
		}
	}
	if maxVersion < 9 {
		if err := migrateV9(db); err != nil {
			return fmt.Errorf("v9: %w", err)
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

// migrateV6 swaps chunks.embedding from vector(768) to halfvec(2560) for the
// Qwen3-Embedding-4B cutover. The 768-dim data isn't dimensionally valid in
// the new column, so the rows are dropped — caller runs cmd/index -reindex
// to repopulate. Drop the HNSW index first (it depends on the column), drop
// and re-add the column to bypass cross-type cast rules, then recreate the
// index with halfvec_cosine_ops.
func migrateV6(db *sql.DB) error {
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_chunks_embedding`); err != nil {
		return err
	}
	if _, err := db.Exec(`TRUNCATE chunks`); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE chunks DROP COLUMN IF EXISTS embedding`); err != nil {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE chunks ADD COLUMN embedding halfvec(2560) NOT NULL`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_chunks_embedding ON chunks USING hnsw (embedding halfvec_cosine_ops)`); err != nil {
		return err
	}
	return nil
}

// migrateV9 adds the descriptions.condition prose column for state-of-the-
// frame descriptors (under construction, worn, pristine, etc.). The fts
// generated column is dropped and recreated to fold condition into its
// to_tsvector input — same pattern as migrateV4: generated columns can't be
// ALTER'd in place but the fts column has no source-of-truth data of its
// own (GENERATED ALWAYS … STORED), so the drop/recreate is non-lossy.
func migrateV9(db *sql.DB) error {
	if _, err := db.Exec(`ALTER TABLE descriptions ADD COLUMN IF NOT EXISTS condition TEXT`); err != nil {
		return err
	}
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
				coalesce(condition,'')        || ' ' ||
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

// migrateV8 adds a generated tsvector on the exif table so the FTS search
// arm can match against camera / lens / year / software / artist tokens —
// before this, descriptions.fts was the only FTS surface and a query like
// "2024" or "X100VI" got nothing because that text lived only in exif.
//
// Field selection is deliberate: camera_make / camera_model / lens_model /
// lens_info / date_taken_year::text / software / artist are high-signal
// (specific, distinguishing). exposure_mode / white_balance / flash are
// excluded because their values ("Auto", "Did not fire") would land in
// nearly every row and drown ranking signal.
func migrateV8(db *sql.DB) error {
	if _, err := db.Exec(`
		ALTER TABLE exif ADD COLUMN IF NOT EXISTS fts tsvector GENERATED ALWAYS AS (
			to_tsvector('english',
				coalesce(camera_make,'')              || ' ' ||
				coalesce(camera_model,'')             || ' ' ||
				coalesce(lens_model,'')               || ' ' ||
				coalesce(lens_info,'')                || ' ' ||
				coalesce(date_taken_year::text,'')    || ' ' ||
				coalesce(software,'')                 || ' ' ||
				coalesce(artist,''))
		) STORED
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_exif_fts ON exif USING gin(fts)`); err != nil {
		return err
	}
	return nil
}

// migrateV7 adds the verify_cache table — the persistent LLM yes/no verdict
// cache for the verify pass. PK includes verify_model so SEARCH_MODEL swaps
// don't cross-contaminate cached verdicts. Idempotent on existing libraries
// because schemaSQL also declares the table CREATE … IF NOT EXISTS — this
// migration is here for symmetry and to make the version bump explicit.
func migrateV7(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS verify_cache (
		    query         TEXT NOT NULL,
		    photo_id      TEXT NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
		    verify_model  TEXT NOT NULL,
		    verdict       BOOLEAN NOT NULL,
		    verified_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (query, photo_id, verify_model)
		)
	`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_verify_cache_query ON verify_cache(query, verify_model)`); err != nil {
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
		INSERT INTO descriptions (photo_id, subject, setting, light, colors, composition, vantage, ground_truth, condition, full_description)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT(photo_id) DO UPDATE SET
			subject          = EXCLUDED.subject,
			setting          = EXCLUDED.setting,
			light            = EXCLUDED.light,
			colors           = EXCLUDED.colors,
			composition      = EXCLUDED.composition,
			vantage          = EXCLUDED.vantage,
			ground_truth     = EXCLUDED.ground_truth,
			condition        = EXCLUDED.condition,
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
		nullIfEmpty(fields.Condition),
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

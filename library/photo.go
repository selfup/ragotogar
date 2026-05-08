package library

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/lib/pq"
)

// Photo is the typed view used by the v12 doc builders (indexing input)
// and the cmd/web template render. Pointers for nullable numeric fields
// so callers can distinguish "absent" from "zero".
type Photo struct {
	Name         string
	FileBasename string

	CameraMake           string
	CameraModel          string
	LensModel            string
	LensInfo             string
	DateTaken            string // ISO 8601 from the DB
	FocalLengthMM        *float64
	FocalLength35mm      *float64
	FNumber              *float64
	ShutterSeconds       *float64
	ISO                  *int64
	ExposureCompensation *float64
	ExposureMode         string
	WhiteBalance         string
	Flash                string
	Software             string
	Artist               string

	Subject         string
	Setting         string
	Light           string
	Colors          string
	Composition     string
	Vantage         string
	GroundTruth     string
	Condition       string
	Mood            string // v12: aesthetic descriptors from the describer's combined-call output. Empty until Step 3 lands the prompt change + descriptions.mood column.
	FullDescription string

	// Typed enum fields produced by cmd/classify. Empty/nil when the photo
	// hasn't been classified yet. Arrays are nil rather than [] when absent.
	POVContainer       string
	POVAltitude        string
	POVAngle           string
	SubjectAltitude    string
	SubjectCategory    []string
	SubjectDistance    string
	SubjectCount       string
	AnimalCount        string
	SceneTimeOfDay     string
	SceneIndoorOutdoor string
	SceneWeather       string
	Framing            []string
	Motion             string
	ColorPalette       string

	// v12: search phrasings emitted by the describer's combined vision
	// call, sourced from query_generations.queries (JSONB array). nil
	// until LoadPhoto learns the JOIN in Step 4. Each element becomes one
	// row in photo_queries.
	GeneratedQueries []string
}

// LoadPhoto fetches a photo by name and returns it fully populated. Returns
// sql.ErrNoRows when the photo doesn't exist.
//
// LEFT JOINs against descriptions / classified / query_generations mean
// photos that have only been organized (not described / classified /
// query-generated) still load — the corresponding fields just stay
// zero-valued. v13 added descriptions.mood; v12 added query_generations
// (queries JSONB → Photo.GeneratedQueries).
func LoadPhoto(db *sql.DB, name string) (*Photo, error) {
	var p Photo
	var (
		make_, model, lensModel, lensInfo                                                    sql.NullString
		dateTaken, exposureMode, whiteBalance, flash, software, artist                       sql.NullString
		subject, setting, light, colors, composition, vantage, gt, condition, mood, fullDesc sql.NullString
		focalMM, focal35, fnum, shutter, ec                                                  sql.NullFloat64
		iso                                                                                  sql.NullInt64
		fileBasename                                                                         sql.NullString
		// classified columns — all nullable (LEFT JOIN may not match)
		povContainer, povAltitude, povAngle                         sql.NullString
		subjectAltitude, subjectDistance, subjectCount, animalCount sql.NullString
		sceneTimeOfDay, sceneIndoorOutdoor, sceneWeather            sql.NullString
		motion, colorPalette                                        sql.NullString
		subjectCategory, framing                                    []string
		// v12 query_generations.queries JSONB → []string
		queriesJSON []byte
	)
	err := db.QueryRow(`
		SELECT p.name, p.file_basename,
		       e.camera_make, e.camera_model, e.lens_model, e.lens_info,
		       e.date_taken, e.focal_length_mm, e.focal_length_35mm,
		       e.f_number, e.exposure_time_seconds, e.iso, e.exposure_compensation,
		       e.exposure_mode, e.white_balance, e.flash, e.software, e.artist,
		       d.subject, d.setting, d.light, d.colors, d.composition,
		       d.vantage, d.ground_truth, d.condition, d.mood, d.full_description,
		       c.pov_container, c.pov_altitude, c.pov_angle,
		       c.subject_altitude, c.subject_category, c.subject_distance,
		       c.subject_count, c.animal_count,
		       c.scene_time_of_day, c.scene_indoor_outdoor, c.scene_weather,
		       c.framing, c.motion, c.color_palette,
		       qg.queries
		FROM photos p
		LEFT JOIN exif e              ON p.id = e.photo_id
		LEFT JOIN descriptions d      ON p.id = d.photo_id
		LEFT JOIN classified c        ON p.id = c.photo_id
		LEFT JOIN query_generations qg ON p.id = qg.photo_id
		WHERE p.name = $1
	`, name).Scan(
		&p.Name, &fileBasename,
		&make_, &model, &lensModel, &lensInfo,
		&dateTaken, &focalMM, &focal35,
		&fnum, &shutter, &iso, &ec,
		&exposureMode, &whiteBalance, &flash, &software, &artist,
		&subject, &setting, &light, &colors, &composition, &vantage, &gt, &condition, &mood, &fullDesc,
		&povContainer, &povAltitude, &povAngle,
		&subjectAltitude, pq.Array(&subjectCategory), &subjectDistance,
		&subjectCount, &animalCount,
		&sceneTimeOfDay, &sceneIndoorOutdoor, &sceneWeather,
		pq.Array(&framing), &motion, &colorPalette,
		&queriesJSON,
	)
	if err != nil {
		return nil, err
	}

	p.FileBasename = fileBasename.String
	p.CameraMake = make_.String
	p.CameraModel = model.String
	p.LensModel = lensModel.String
	p.LensInfo = lensInfo.String
	p.DateTaken = dateTaken.String
	p.ExposureMode = exposureMode.String
	p.WhiteBalance = whiteBalance.String
	p.Flash = flash.String
	p.Software = software.String
	p.Artist = artist.String
	if focalMM.Valid {
		v := focalMM.Float64
		p.FocalLengthMM = &v
	}
	if focal35.Valid {
		v := focal35.Float64
		p.FocalLength35mm = &v
	}
	if fnum.Valid {
		v := fnum.Float64
		p.FNumber = &v
	}
	if shutter.Valid {
		v := shutter.Float64
		p.ShutterSeconds = &v
	}
	if iso.Valid {
		v := iso.Int64
		p.ISO = &v
	}
	if ec.Valid {
		v := ec.Float64
		p.ExposureCompensation = &v
	}
	p.Subject = subject.String
	p.Setting = setting.String
	p.Light = light.String
	p.Colors = colors.String
	p.Composition = composition.String
	p.Vantage = vantage.String
	p.GroundTruth = gt.String
	p.Condition = condition.String
	p.Mood = mood.String
	p.FullDescription = fullDesc.String

	// queries JSONB → []string. The LEFT JOIN may not match (no query gen
	// row), in which case queriesJSON is nil — leave GeneratedQueries nil.
	// A non-nil empty array also leaves it nil (treated as "no queries").
	if len(queriesJSON) > 0 {
		var qs []string
		if err := json.Unmarshal(queriesJSON, &qs); err != nil {
			return nil, fmt.Errorf("decode query_generations.queries for %s: %w", name, err)
		}
		if len(qs) > 0 {
			p.GeneratedQueries = qs
		}
	}

	p.POVContainer = povContainer.String
	p.POVAltitude = povAltitude.String
	p.POVAngle = povAngle.String
	p.SubjectAltitude = subjectAltitude.String
	p.SubjectCategory = subjectCategory
	p.SubjectDistance = subjectDistance.String
	p.SubjectCount = subjectCount.String
	p.AnimalCount = animalCount.String
	p.SceneTimeOfDay = sceneTimeOfDay.String
	p.SceneIndoorOutdoor = sceneIndoorOutdoor.String
	p.SceneWeather = sceneWeather.String
	p.Framing = framing
	p.Motion = motion.String
	p.ColorPalette = colorPalette.String

	return &p, nil
}

// shutterFraction renders an exposure time in seconds as the more
// human-friendly "1/250" for sub-second exposures, matching the format
// the indexed text uses.
func shutterFraction(seconds float64) string {
	if seconds <= 0 {
		return ""
	}
	if seconds >= 1 {
		return strconv.FormatFloat(seconds, 'f', -1, 64) + "s"
	}
	return fmt.Sprintf("1/%ds", int(0.5+1.0/seconds))
}

// dateTakenToExifString converts the ISO 8601 stored in date_taken back to
// the legacy EXIF "YYYY:MM:DD HH:MM:SS" form, mirroring fetch_photo_dict
// in the original Python rag_common.py. Used by BuildDescriptionDocument's
// "Date: …" line.
func dateTakenToExifString(iso string) string {
	if iso == "" {
		return ""
	}
	if before, after, ok := strings.Cut(iso, "T"); ok {
		date := strings.ReplaceAll(before, "-", ":")
		return date + " " + after
	}
	return strings.ReplaceAll(iso, "-", ":")
}

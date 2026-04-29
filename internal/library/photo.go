package library

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Photo is the typed view used for both BuildDocument (indexing input) and
// the cmd/web template render. Pointers for nullable numeric fields so
// callers can distinguish "absent" from "zero".
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
	FullDescription string
}

// LoadPhoto fetches a photo by name and returns it fully populated. Returns
// sql.ErrNoRows when the photo doesn't exist.
func LoadPhoto(db *sql.DB, name string) (*Photo, error) {
	var p Photo
	var (
		make_, model, lensModel, lensInfo                              sql.NullString
		dateTaken, exposureMode, whiteBalance, flash, software, artist sql.NullString
		subject, setting, light, colors, composition, fullDesc         sql.NullString
		focalMM, focal35, fnum, shutter, ec                            sql.NullFloat64
		iso                                                            sql.NullInt64
		fileBasename                                                   sql.NullString
	)
	err := db.QueryRow(`
		SELECT p.name, p.file_basename,
		       e.camera_make, e.camera_model, e.lens_model, e.lens_info,
		       e.date_taken, e.focal_length_mm, e.focal_length_35mm,
		       e.f_number, e.exposure_time_seconds, e.iso, e.exposure_compensation,
		       e.exposure_mode, e.white_balance, e.flash, e.software, e.artist,
		       d.subject, d.setting, d.light, d.colors, d.composition, d.full_description
		FROM photos p
		LEFT JOIN exif e         ON p.id = e.photo_id
		LEFT JOIN descriptions d ON p.id = d.photo_id
		WHERE p.name = $1
	`, name).Scan(
		&p.Name, &fileBasename,
		&make_, &model, &lensModel, &lensInfo,
		&dateTaken, &focalMM, &focal35,
		&fnum, &shutter, &iso, &ec,
		&exposureMode, &whiteBalance, &flash, &software, &artist,
		&subject, &setting, &light, &colors, &composition, &fullDesc,
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
	p.FullDescription = fullDesc.String

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
// the legacy EXIF "YYYY:MM:DD HH:MM:SS" form so HumanizeExifDate can re-parse
// it the same way the Python build_document did. Mirrors fetch_photo_dict
// in tools/rag_common.py.
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

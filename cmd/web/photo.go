package main

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

// photoView is what the photo template binds against. Uses pointers for
// nullable numeric fields so `{{if .Exif.FNumber}}` works in the template
// without explicit IsValid checks.
type photoView struct {
	Photo struct {
		Name         string
		FileBasename string
		FilePath     string
	}
	Exif struct {
		CameraMake     string
		CameraModel    string
		LensModel      string
		LensInfo       string
		DateTaken      string
		FocalLengthMM  *float64
		FNumber        *float64
		ShutterSeconds *float64
		ISO            *int64
		ExposureMode   string
		WhiteBalance   string
		Flash          string
		Software       string
		Artist         string
	}
	Description struct {
		Subject         string
		Setting         string
		Light           string
		Colors          string
		Composition     string
		FullDescription string
	}
	Inference struct {
		Model       string
		PreviewMs   *int64
		InferenceMs *int64
	}
}

// loadPhotoView returns the typed view for a single photo, or sql.ErrNoRows.
func loadPhotoView(db *sql.DB, name string) (*photoView, error) {
	var v photoView
	var (
		cameraMake, cameraModel, lensModel, lensInfo                   sql.NullString
		dateTaken, exposureMode, whiteBalance, flash, software, artist sql.NullString
		subject, setting, light, colors, composition, fullDesc         sql.NullString
		model                                                          sql.NullString
		focalMM, fNum, shutter                                         sql.NullFloat64
		iso, previewMs, inferenceMs                                    sql.NullInt64
	)
	err := db.QueryRow(`
		SELECT p.name, COALESCE(p.file_basename, ''), COALESCE(p.file_path, ''),
		       e.camera_make, e.camera_model, e.lens_model, e.lens_info,
		       e.date_taken, e.focal_length_mm, e.f_number, e.exposure_time_seconds, e.iso,
		       e.exposure_mode, e.white_balance, e.flash, e.software, e.artist,
		       d.subject, d.setting, d.light, d.colors, d.composition, d.full_description,
		       i.model, i.preview_ms, i.inference_ms
		FROM photos p
		LEFT JOIN exif e         ON p.id = e.photo_id
		LEFT JOIN descriptions d ON p.id = d.photo_id
		LEFT JOIN inference i    ON p.id = i.photo_id
		WHERE p.name = $1
	`, name).Scan(
		&v.Photo.Name, &v.Photo.FileBasename, &v.Photo.FilePath,
		&cameraMake, &cameraModel, &lensModel, &lensInfo,
		&dateTaken, &focalMM, &fNum, &shutter, &iso,
		&exposureMode, &whiteBalance, &flash, &software, &artist,
		&subject, &setting, &light, &colors, &composition, &fullDesc,
		&model, &previewMs, &inferenceMs,
	)
	if err != nil {
		return nil, err
	}

	v.Exif.CameraMake = cameraMake.String
	v.Exif.CameraModel = cameraModel.String
	v.Exif.LensModel = lensModel.String
	v.Exif.LensInfo = lensInfo.String
	v.Exif.DateTaken = dateTaken.String
	v.Exif.ExposureMode = exposureMode.String
	v.Exif.WhiteBalance = whiteBalance.String
	v.Exif.Flash = flash.String
	v.Exif.Software = software.String
	v.Exif.Artist = artist.String
	if focalMM.Valid {
		f := focalMM.Float64
		v.Exif.FocalLengthMM = &f
	}
	if fNum.Valid {
		f := fNum.Float64
		v.Exif.FNumber = &f
	}
	if shutter.Valid {
		f := shutter.Float64
		v.Exif.ShutterSeconds = &f
	}
	if iso.Valid {
		n := iso.Int64
		v.Exif.ISO = &n
	}

	v.Description.Subject = subject.String
	v.Description.Setting = setting.String
	v.Description.Light = light.String
	v.Description.Colors = colors.String
	v.Description.Composition = composition.String
	v.Description.FullDescription = fullDesc.String

	v.Inference.Model = model.String
	if previewMs.Valid {
		n := previewMs.Int64
		v.Inference.PreviewMs = &n
	}
	if inferenceMs.Valid {
		n := inferenceMs.Int64
		v.Inference.InferenceMs = &n
	}

	return &v, nil
}

// photoExists returns true if the named photo is in the SQL library — used
// by search result validation in place of the old file-existence check.
func photoExists(db *sql.DB, name string) bool {
	var n int
	err := db.QueryRow("SELECT 1 FROM photos WHERE name = $1", name).Scan(&n)
	return err == nil
}

// thumbnailBytes pulls the BLOB row for a photo, returning ErrNoRows when
// the photo has no thumbnail (shouldn't happen post-cmd/describe).
func thumbnailBytes(db *sql.DB, name string) ([]byte, error) {
	var data []byte
	err := db.QueryRow(
		"SELECT bytes FROM thumbnails WHERE photo_id = $1", name,
	).Scan(&data)
	return data, err
}

// servePhotoHTML renders the photo template for /photos/<name>.
func servePhotoHTML(w http.ResponseWriter, r *http.Request, db *sql.DB, tmpl *template.Template, name string) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		http.NotFound(w, r)
		return
	}
	v, err := loadPhotoView(db, name)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, v); err != nil {
		// Headers may already be flushed; just log.
		fmt.Fprintf(w, "\n<!-- template error: %v -->", err)
	}
}

// servePhotoJPG streams the thumbnail BLOB for /photos/<name>.jpg.
func servePhotoJPG(w http.ResponseWriter, r *http.Request, db *sql.DB, name string) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		http.NotFound(w, r)
		return
	}
	data, err := thumbnailBytes(db, name)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "max-age=86400")
	w.Write(data)
}

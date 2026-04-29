package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lib/pq"
)

// Classification mirrors the classified table columns. Pointer types on
// scalar fields let us distinguish "model didn't return this field" (nil →
// NULL) from "model returned 'unclear'" (a real signal). Arrays default to
// nil if absent.
type Classification struct {
	POVContainer       *string  `json:"pov_container"`
	POVAltitude        *string  `json:"pov_altitude"`
	POVAngle           *string  `json:"pov_angle"`
	SubjectAltitude    *string  `json:"subject_altitude"`
	SubjectCategory    []string `json:"subject_category"`
	SubjectDistance   *string   `json:"subject_distance"`
	SubjectCount       *string  `json:"subject_count"`
	AnimalCount        *string  `json:"animal_count"`
	SceneTimeOfDay     *string  `json:"scene_time_of_day"`
	SceneIndoorOutdoor *string  `json:"scene_indoor_outdoor"`
	SceneWeather       *string  `json:"scene_weather"`
	Framing            []string `json:"framing"`
	Motion             *string  `json:"motion"`
	ColorPalette       *string  `json:"color_palette"`
}

// AllowedScalar lists permitted values for each scalar enum field. A value
// outside the set is dropped (column stored as NULL) so the model can't
// pollute the typed columns with hallucinated enums.
var AllowedScalar = map[string][]string{
	"pov_container":        {"handheld", "from_window", "from_balcony", "from_rooftop", "from_vehicle", "from_plane", "from_drone", "fixed_camera", "unclear"},
	"pov_altitude":         {"underground", "ground", "elevated", "aerial", "underwater", "unclear"},
	"pov_angle":            {"eye_level", "looking_up", "looking_down", "dutch", "unclear"},
	"subject_altitude":     {"on_ground", "elevated", "in_air", "suspended", "underwater", "unclear"},
	"subject_distance":     {"macro", "close", "medium", "wide", "landscape", "unclear"},
	"subject_count":        {"0", "1", "2", "few", "group", "crowd", "unclear"},
	"animal_count":         {"0", "1", "2", "few", "group", "crowd", "unclear"},
	"scene_time_of_day":    {"dawn", "day", "dusk", "night", "unclear"},
	"scene_indoor_outdoor": {"indoor", "outdoor", "mixed", "unclear"},
	"scene_weather":        {"clear", "overcast", "rain", "snow", "fog", "unclear"},
	"motion":               {"static", "subject_moving", "camera_moving", "both", "unclear"},
	"color_palette":        {"warm", "cool", "neutral", "desaturated", "monochrome", "mixed", "unclear"},
}

// AllowedArray lists permitted values for each multi-value array field.
var AllowedArray = map[string][]string{
	"subject_category": {"person", "people", "animal", "vehicle", "architecture", "landscape", "nature", "object", "sign_text", "abstract"},
	"framing":          {"through_window", "through_door", "through_foliage", "through_fence", "through_glass", "unobstructed", "unclear"},
}

// BuildClassifyPrompt formats the classifier instruction with the photo
// description substituted in. The schema lines mirror the column definitions
// in cmd/describe/schema.go — keep them in sync.
func BuildClassifyPrompt(description string) string {
	var b strings.Builder
	b.WriteString(`You map a photo description to typed enum fields. Read carefully. For each field, return the value that best matches the description. Use "unclear" if the description does not provide enough information — DO NOT guess. Some fields take an array of values (multiple may apply).

Allowed values:

pov_container:        handheld | from_window | from_balcony | from_rooftop | from_vehicle | from_plane | from_drone | fixed_camera | unclear
pov_altitude:         underground | ground | elevated | aerial | underwater | unclear
pov_angle:            eye_level | looking_up | looking_down | dutch | unclear
subject_altitude:     on_ground | elevated | in_air | suspended | underwater | unclear
subject_category:     [person | people | animal | vehicle | architecture | landscape | nature | object | sign_text | abstract]    (array — multiple may apply)
subject_distance:     macro | close | medium | wide | landscape | unclear
subject_count:        0 | 1 | 2 | few | group | crowd | unclear     (people only; animals separate)
animal_count:         0 | 1 | 2 | few | group | crowd | unclear
scene_time_of_day:    dawn | day | dusk | night | unclear
scene_indoor_outdoor: indoor | outdoor | mixed | unclear
scene_weather:        clear | overcast | rain | snow | fog | unclear
framing:              [through_window | through_door | through_foliage | through_fence | through_glass | unobstructed | unclear]    (array)
motion:               static | subject_moving | camera_moving | both | unclear
color_palette:        warm | cool | neutral | desaturated | monochrome | mixed | unclear

Photo description:
`)
	b.WriteString(description)
	b.WriteString(`

Respond with a single JSON object — keys are the field names above, values are strings (or arrays of strings for the array-typed fields). Do not explain. Do not add commentary. Do not wrap the JSON in code fences.`)
	return b.String()
}

// ParseClassifyResponse extracts the JSON object from a raw LLM response and
// decodes it leniently. Tolerates: leading/trailing prose, ```json``` code
// fences, JS-style // line comments inside the JSON, scalar fields returned
// as numbers (animal_count: 0), and scalar fields returned as single-element
// arrays (color_palette: ["cool"]). The 3B classifier model emits these
// shapes a few percent of the time on long schemas.
func ParseClassifyResponse(raw string) (Classification, error) {
	body := extractJSONObject(raw)
	if body == "" {
		return Classification{}, fmt.Errorf("no JSON object found in response: %s", truncate(raw, 200))
	}
	body = stripLineComments(body)
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return Classification{}, fmt.Errorf("decode classification JSON: %w (body: %s)", err, truncate(body, 200))
	}
	return Classification{
		POVContainer:       coerceString(m["pov_container"]),
		POVAltitude:        coerceString(m["pov_altitude"]),
		POVAngle:           coerceString(m["pov_angle"]),
		SubjectAltitude:    coerceString(m["subject_altitude"]),
		SubjectCategory:    coerceStringSlice(m["subject_category"]),
		SubjectDistance:    coerceString(m["subject_distance"]),
		SubjectCount:       coerceString(m["subject_count"]),
		AnimalCount:        coerceString(m["animal_count"]),
		SceneTimeOfDay:     coerceString(m["scene_time_of_day"]),
		SceneIndoorOutdoor: coerceString(m["scene_indoor_outdoor"]),
		SceneWeather:       coerceString(m["scene_weather"]),
		Framing:            coerceStringSlice(m["framing"]),
		Motion:             coerceString(m["motion"]),
		ColorPalette:       coerceString(m["color_palette"]),
	}, nil
}

// stripLineComments removes // ... \n comments from JSON without disturbing
// // sequences that appear inside string values (e.g. "http://example.com").
// Tracks string and escape state with a simple scanner. Block comments
// (/* … */) aren't handled — Ministral hasn't been observed emitting them.
func stripLineComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escape {
			b.WriteByte(c)
			escape = false
			continue
		}
		if inString {
			if c == '\\' {
				b.WriteByte(c)
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			b.WriteByte(c)
			continue
		}
		if c == '"' {
			inString = true
			b.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// extractJSONObject finds the substring from the first '{' to the matching
// last '}'. Doesn't validate balanced braces — json.Unmarshal will reject
// malformed input on the next pass.
func extractJSONObject(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end < 0 || end <= start {
		return ""
	}
	return raw[start : end+1]
}

// coerceString accepts a JSON value and returns *string. Non-coercible types
// yield nil. Used so a model-emitted "animal_count": 0 or "color_palette":
// ["cool"] don't sink the whole row.
func coerceString(v any) *string {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case string:
		s := t
		return &s
	case float64:
		s := strconv.FormatFloat(t, 'f', -1, 64)
		return &s
	case bool:
		s := strconv.FormatBool(t)
		return &s
	case []any:
		if len(t) == 0 {
			return nil
		}
		return coerceString(t[0])
	}
	return nil
}

// coerceStringSlice accepts a JSON value and returns []string. Tolerates a
// bare string (wrap in 1-element slice), an array of strings, or an array of
// mixed types (skip non-strings).
func coerceStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		var out []string
		for _, item := range t {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// ValidateClassification filters Classification through the AllowedScalar /
// AllowedArray sets. Scalar values not in the allowed set become nil (NULL
// in DB). Array values not in the allowed set are dropped from the slice.
// Empty arrays after filtering are returned as nil.
func ValidateClassification(c Classification) Classification {
	c.POVContainer = filterScalar(c.POVContainer, AllowedScalar["pov_container"])
	c.POVAltitude = filterScalar(c.POVAltitude, AllowedScalar["pov_altitude"])
	c.POVAngle = filterScalar(c.POVAngle, AllowedScalar["pov_angle"])
	c.SubjectAltitude = filterScalar(c.SubjectAltitude, AllowedScalar["subject_altitude"])
	c.SubjectDistance = filterScalar(c.SubjectDistance, AllowedScalar["subject_distance"])
	c.SubjectCount = filterScalar(c.SubjectCount, AllowedScalar["subject_count"])
	c.AnimalCount = filterScalar(c.AnimalCount, AllowedScalar["animal_count"])
	c.SceneTimeOfDay = filterScalar(c.SceneTimeOfDay, AllowedScalar["scene_time_of_day"])
	c.SceneIndoorOutdoor = filterScalar(c.SceneIndoorOutdoor, AllowedScalar["scene_indoor_outdoor"])
	c.SceneWeather = filterScalar(c.SceneWeather, AllowedScalar["scene_weather"])
	c.Motion = filterScalar(c.Motion, AllowedScalar["motion"])
	c.ColorPalette = filterScalar(c.ColorPalette, AllowedScalar["color_palette"])
	c.SubjectCategory = filterArray(c.SubjectCategory, AllowedArray["subject_category"])
	c.Framing = filterArray(c.Framing, AllowedArray["framing"])
	return c
}

func filterScalar(v *string, allowed []string) *string {
	if v == nil {
		return nil
	}
	if slices.Contains(allowed, *v) {
		return v
	}
	return nil
}

func filterArray(values, allowed []string) []string {
	if len(values) == 0 {
		return nil
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = struct{}{}
	}
	var kept []string
	for _, v := range values {
		if _, ok := allowedSet[v]; ok {
			kept = append(kept, v)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// LoadClassifyInput concatenates the structured prose fields the describer
// wrote, falling back to full_description if the structured fields are all
// empty. Used as the LLM input for classification.
func LoadClassifyInput(db *sql.DB, name string) (string, error) {
	var subject, setting, light, colors, composition, vantage, gt, full sql.NullString
	err := db.QueryRow(`
		SELECT subject, setting, light, colors, composition, vantage, ground_truth, full_description
		FROM descriptions WHERE photo_id = $1
	`, name).Scan(&subject, &setting, &light, &colors, &composition, &vantage, &gt, &full)
	if err != nil {
		return "", err
	}
	parts := []struct{ k, v string }{
		{"Subject", subject.String},
		{"Setting", setting.String},
		{"Light", light.String},
		{"Colors", colors.String},
		{"Composition", composition.String},
		{"Vantage", vantage.String},
		{"Ground truth", gt.String},
	}
	var lines []string
	for _, p := range parts {
		if p.v != "" {
			lines = append(lines, p.k+": "+p.v)
		}
	}
	if len(lines) > 0 {
		return strings.Join(lines, "\n"), nil
	}
	return full.String, nil
}

// UpsertClassified writes one classification row. Arrays use pq.Array so
// pgx accepts the TEXT[] shape correctly.
func UpsertClassified(db *sql.DB, name string, c Classification, model string) error {
	_, err := db.Exec(`
		INSERT INTO classified (
			photo_id,
			pov_container, pov_altitude, pov_angle,
			subject_altitude, subject_category, subject_distance,
			subject_count, animal_count,
			scene_time_of_day, scene_indoor_outdoor, scene_weather,
			framing, motion, color_palette,
			classified_at, classifier_model
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now(), $16
		)
		ON CONFLICT(photo_id) DO UPDATE SET
			pov_container        = EXCLUDED.pov_container,
			pov_altitude         = EXCLUDED.pov_altitude,
			pov_angle            = EXCLUDED.pov_angle,
			subject_altitude     = EXCLUDED.subject_altitude,
			subject_category     = EXCLUDED.subject_category,
			subject_distance     = EXCLUDED.subject_distance,
			subject_count        = EXCLUDED.subject_count,
			animal_count         = EXCLUDED.animal_count,
			scene_time_of_day    = EXCLUDED.scene_time_of_day,
			scene_indoor_outdoor = EXCLUDED.scene_indoor_outdoor,
			scene_weather        = EXCLUDED.scene_weather,
			framing              = EXCLUDED.framing,
			motion               = EXCLUDED.motion,
			color_palette        = EXCLUDED.color_palette,
			classified_at        = now(),
			classifier_model     = EXCLUDED.classifier_model
	`,
		name,
		c.POVContainer, c.POVAltitude, c.POVAngle,
		c.SubjectAltitude, pq.Array(c.SubjectCategory), c.SubjectDistance,
		c.SubjectCount, c.AnimalCount,
		c.SceneTimeOfDay, c.SceneIndoorOutdoor, c.SceneWeather,
		pq.Array(c.Framing), c.Motion, c.ColorPalette,
		model,
	)
	return err
}

// ClassifyOne is the all-in-one classifier entry point: load the photo's
// prose, call the LLM, parse + validate the response, UPSERT the row.
// Returns nil on success, an error otherwise — callers decide whether a
// classify failure should propagate (cmd/classify standalone) or be logged
// and swallowed (cmd/describe inline mode).
func ClassifyOne(ctx context.Context, db *sql.DB, name, model string) error {
	doc, err := LoadClassifyInput(db, name)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if doc == "" {
		return fmt.Errorf("empty description")
	}
	raw, err := LLMComplete(ctx, model, BuildClassifyPrompt(doc))
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	c, err := ParseClassifyResponse(raw)
	if err != nil {
		return fmt.Errorf("parse: %w (raw: %s)", err, truncate(raw, 400))
	}
	c = ValidateClassification(c)
	if err := UpsertClassified(db, name, c, model); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	return nil
}

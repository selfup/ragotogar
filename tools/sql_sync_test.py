"""Tests for sql_sync.py — schema, parsers, sync, FTS, cascade."""

import json
import sqlite3

import pytest

import sql_sync


# ── parsers ───────────────────────────────────────────────────────────────

def test_parse_float_handles_bare_and_prefixed_forms():
    assert sql_sync.parse_float("5.6") == 5.6
    assert sql_sync.parse_float("f/2") == 2.0
    assert sql_sync.parse_float("F/2.8") == 2.8
    assert sql_sync.parse_float(2.0) == 2.0
    assert sql_sync.parse_float("") is None
    assert sql_sync.parse_float(None) is None
    assert sql_sync.parse_float("garbage") is None


def test_parse_int():
    assert sql_sync.parse_int("500") == 500
    assert sql_sync.parse_int("12800") == 12800
    assert sql_sync.parse_int("") is None
    assert sql_sync.parse_int(None) is None
    assert sql_sync.parse_int("garbage") is None


def test_parse_dimension_mm():
    assert sql_sync.parse_dimension_mm("23.0 mm") == 23.0
    assert sql_sync.parse_dimension_mm("23.0") == 23.0
    assert sql_sync.parse_dimension_mm("23 MM") == 23.0
    assert sql_sync.parse_dimension_mm("") is None
    assert sql_sync.parse_dimension_mm("garbage mm") is None


def test_parse_exposure_time():
    assert sql_sync.parse_exposure_time("1/250") == pytest.approx(1 / 250)
    assert sql_sync.parse_exposure_time("1/4000") == pytest.approx(1 / 4000)
    assert sql_sync.parse_exposure_time("0.5") == 0.5
    assert sql_sync.parse_exposure_time("1/0") is None  # divide by zero
    assert sql_sync.parse_exposure_time("") is None
    assert sql_sync.parse_exposure_time("garbage") is None


def test_parse_exif_date_full():
    iso, year, month = sql_sync.parse_exif_date("2024:04:21 16:27:54")
    assert iso == "2024-04-21T16:27:54"
    assert year == 2024
    assert month == 4


def test_parse_exif_date_date_only():
    iso, year, month = sql_sync.parse_exif_date("2024:04:21")
    assert iso == "2024-04-21"
    assert year == 2024
    assert month == 4


def test_parse_exif_date_invalid():
    assert sql_sync.parse_exif_date("") == (None, None, None)
    assert sql_sync.parse_exif_date(None) == (None, None, None)
    assert sql_sync.parse_exif_date("garbage") == (None, None, None)
    assert sql_sync.parse_exif_date("2024:13:21 00:00:00") == (None, None, None)
    assert sql_sync.parse_exif_date("not:a:date") == (None, None, None)


# ── schema ────────────────────────────────────────────────────────────────

def _fresh_conn():
    conn = sqlite3.connect(":memory:")
    conn.execute("PRAGMA foreign_keys = ON")
    sql_sync.init_schema(conn)
    return conn


def test_schema_init_creates_all_tables_and_indexes():
    conn = _fresh_conn()
    tables = {r[0] for r in conn.execute(
        "SELECT name FROM sqlite_master WHERE type IN ('table')"
    )}
    assert {"schema_version", "photos", "exif", "descriptions", "descriptions_fts"} <= tables

    indexes = {r[0] for r in conn.execute(
        "SELECT name FROM sqlite_master WHERE type = 'index' AND name NOT LIKE 'sqlite_%'"
    )}
    expected = {
        "idx_photos_name", "idx_exif_camera", "idx_exif_make", "idx_exif_date",
        "idx_exif_year_month", "idx_exif_iso", "idx_exif_aperture",
        "idx_exif_focal", "idx_exif_artist",
    }
    assert expected <= indexes

    triggers = {r[0] for r in conn.execute(
        "SELECT name FROM sqlite_master WHERE type = 'trigger'"
    )}
    assert {"descriptions_ai", "descriptions_ad", "descriptions_au"} <= triggers


# ── sync flow ─────────────────────────────────────────────────────────────

def _sample_data(name="test_photo"):
    return {
        "name": name,
        "file": "TEST.JPG",
        "path": "/some/path/TEST.JPG",
        "metadata": {
            "make": "FUJIFILM",
            "model": "X100VI",
            "lens_model": "",
            "lens_info": "",
            "date_time_original": "2024:04:21 16:27:54",
            "focal_length": "23.0 mm",
            "f_number": "5.6",
            "exposure_time": "1/250",
            "iso": "500",
            "exposure_compensation": "-0.67",
            "exposure_mode": "Auto",
            "metering_mode": "Multi-segment",
            "white_balance": "Auto",
            "flash": "No Flash",
            "image_width": "7728",
            "image_height": "5152",
            "software": "Test Software",
        },
        "fields": {
            "subject": "A test subject with trees",
            "setting": "Test setting",
            "light": "Test light",
            "colors": "Test colors",
            "composition": "Test composition",
        },
        "description": "Full test description with trees and shadows.",
    }


@pytest.fixture
def sample_json(tmp_path):
    data = _sample_data()
    json_path = tmp_path / "test_photo.json"
    json_path.write_text(json.dumps(data))
    return str(json_path)


def test_sync_one_writes_typed_columns(sample_json):
    conn = _fresh_conn()
    assert sql_sync.sync_one(conn, sample_json) is True

    photos = conn.execute("SELECT name, file_basename, file_path FROM photos").fetchone()
    assert photos == ("test_photo", "TEST.JPG", "/some/path/TEST.JPG")

    exif = conn.execute("""
        SELECT camera_make, camera_model, lens_model, focal_length_mm, f_number,
               exposure_time_seconds, iso, exposure_compensation,
               date_taken, date_taken_year, date_taken_month
        FROM exif
    """).fetchone()
    assert exif[0:5] == ("FUJIFILM", "X100VI", None, 23.0, 5.6)
    assert exif[5] == pytest.approx(1 / 250)
    assert exif[6:11] == (500, -0.67, "2024-04-21T16:27:54", 2024, 4)


def test_upsert_idempotency_keeps_one_row_per_table(sample_json):
    conn = _fresh_conn()
    for _ in range(3):
        sql_sync.sync_one(conn, sample_json)

    for table in ("photos", "exif", "descriptions", "descriptions_fts"):
        count = conn.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
        assert count == 1, f"{table} should have 1 row, has {count}"


# ── FTS ───────────────────────────────────────────────────────────────────

def test_fts_search_with_porter_stemming(sample_json):
    conn = _fresh_conn()
    sql_sync.sync_one(conn, sample_json)

    # JSON has "trees" — porter should stem so "tree" matches.
    rows = conn.execute(
        "SELECT rowid FROM descriptions_fts WHERE descriptions_fts MATCH 'tree'"
    ).fetchall()
    assert len(rows) == 1

    # Boolean queries
    rows = conn.execute(
        "SELECT rowid FROM descriptions_fts WHERE descriptions_fts MATCH 'tree AND shadow'"
    ).fetchall()
    assert len(rows) == 1

    # No match
    rows = conn.execute(
        "SELECT rowid FROM descriptions_fts WHERE descriptions_fts MATCH 'wombat'"
    ).fetchall()
    assert rows == []


# ── cascade ───────────────────────────────────────────────────────────────

def test_cascade_delete_clears_exif_descriptions_and_fts(sample_json):
    conn = _fresh_conn()
    sql_sync.sync_one(conn, sample_json)

    conn.execute("DELETE FROM photos WHERE id = 'test_photo'")
    conn.commit()

    for table in ("photos", "exif", "descriptions", "descriptions_fts"):
        count = conn.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
        assert count == 0, f"{table} should be empty after cascade, has {count}"


# ── robustness ────────────────────────────────────────────────────────────

def test_missing_fields_dont_fail(tmp_path):
    """A JSON with bare-minimum metadata should sync without crashing.
    Missing fields land as NULL, not as crashes or empty strings."""
    minimal = {
        "name": "minimal",
        "file": "MIN.JPG",
        "path": "/x/MIN.JPG",
        "metadata": {"make": "TEST"},
    }
    p = tmp_path / "min.json"
    p.write_text(json.dumps(minimal))

    conn = _fresh_conn()
    assert sql_sync.sync_one(conn, str(p)) is True

    exif = conn.execute("""
        SELECT camera_make, camera_model, lens_model, iso, f_number,
               exposure_compensation, date_taken
        FROM exif
    """).fetchone()
    assert exif == ("TEST", None, None, None, None, None, None)

    desc = conn.execute("""
        SELECT subject, setting, light, colors, composition, full_description
        FROM descriptions
    """).fetchone()
    assert desc == (None, None, None, None, None, None)


def test_sync_one_skips_json_without_name(tmp_path, capsys):
    p = tmp_path / "broken.json"
    p.write_text(json.dumps({"metadata": {}}))

    conn = _fresh_conn()
    assert sql_sync.sync_one(conn, str(p)) is False

    err = capsys.readouterr().err
    assert "no 'name' field" in err


def test_empty_strings_become_null(sample_json):
    """Empty-string EXIF fields (X100VI's lens_model, focal_length_in_35mm)
    must land as NULL — schema relies on this for COUNT/GROUP-BY queries."""
    conn = _fresh_conn()
    sql_sync.sync_one(conn, sample_json)

    row = conn.execute(
        "SELECT lens_model, lens_info FROM exif WHERE photo_id = 'test_photo'"
    ).fetchone()
    assert row == (None, None)

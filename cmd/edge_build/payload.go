package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
)

// encodePayloadRecord serializes one payload record: caption + 5 tags,
// each as varint(len) + bytes. Empty strings produce varint(0) — read
// back as a zero-length string, which the runtime treats as "absent".
func encodePayloadRecord(caption string, tags [5]string) []byte {
	scratch := make([]byte, binary.MaxVarintLen64)
	var buf bytes.Buffer
	writeLP := func(s string) {
		n := binary.PutUvarint(scratch, uint64(len(s)))
		buf.Write(scratch[:n])
		buf.WriteString(s)
	}
	writeLP(caption)
	for _, t := range tags {
		writeLP(t)
	}
	return buf.Bytes()
}

// payloadTagFields fixes the on-disk tag order. The runtime reads tags
// by position, so this slice IS the contract — append only, never
// reorder. Mirror order in any future cmd/edge tag rendering.
var payloadTagFields = []string{
	"subject_altitude",
	"scene_indoor_outdoor",
	"scene_time_of_day",
	"scene_weather",
	"pov_container",
}

// buildPayload writes per-compact-id records to payload.bin:
//
//	[uint32 count]
//	[count × uint64 record_offset]   ← file-relative
//	[count × record]                  ← varint(len)+bytes per field
//
// Each record is: caption (varint+bytes) + 5 tags (varint+bytes each)
// in payloadTagFields order. Empty values write varint(0) — readable as
// "absent" without a separate null bit.
//
// LEFT JOIN classified so photos that haven't been classified still get
// a record (with empty tag strings); the runtime can render them with
// just the caption.
func buildPayload(db *sql.DB, ids *idSpace, path string) error {
	type record struct {
		caption string
		tags    [5]string
	}
	records := make([]record, len(ids.Names))

	rows, err := db.Query(`
		SELECT p.name,
		       COALESCE(d.subject, ''),
		       COALESCE(c.subject_altitude, ''),
		       COALESCE(c.scene_indoor_outdoor, ''),
		       COALESCE(c.scene_time_of_day, ''),
		       COALESCE(c.scene_weather, ''),
		       COALESCE(c.pov_container, '')
		FROM photos p
		LEFT JOIN descriptions d ON p.id = d.photo_id
		LEFT JOIN classified c   ON p.id = c.photo_id
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name, caption, t0, t1, t2, t3, t4 string
		if err := rows.Scan(&name, &caption, &t0, &t1, &t2, &t3, &t4); err != nil {
			return err
		}
		cid, ok := ids.CompactID(name)
		if !ok {
			continue
		}
		records[cid] = record{caption: caption, tags: [5]string{t0, t1, t2, t3, t4}}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Encode each record into a buffer first so we know its size before
	// assembling the offset table.
	encoded := make([][]byte, len(records))
	for i, r := range records {
		encoded[i] = encodePayloadRecord(r.caption, r.tags)
	}

	count := uint32(len(records))
	headerBytes := int64(4) + int64(count)*8 // count + offset table
	offsets := make([]uint64, count)
	pos := headerBytes
	for i, rec := range encoded {
		offsets[i] = uint64(pos)
		pos += int64(len(rec))
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)

	if err := binary.Write(bw, binary.LittleEndian, count); err != nil {
		return err
	}
	for _, off := range offsets {
		if err := binary.Write(bw, binary.LittleEndian, off); err != nil {
			return err
		}
	}
	for _, rec := range encoded {
		if _, err := bw.Write(rec); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if fi, err := f.Stat(); err == nil {
		// Sanity check: caller logs payload size; this assert catches a
		// header-vs-records arithmetic bug at build time rather than as
		// silent runtime corruption.
		if fi.Size() != pos {
			return fmt.Errorf("payload size mismatch: stat=%d expected=%d", fi.Size(), pos)
		}
	}
	return nil
}

package main

import (
	"database/sql"
)

// idSpace maps the compact uint32 photo id used inside every artifact
// blob back to the canonical photos.name. id_space.names[i] is the
// photo at compact id i.
//
// The ordering is ORDER BY name — byte-wise lex, NOT pg's
// default locale-dependent collation. The within-lexeme posting list
// is delta-encoded by compact_id, so the photo_id sort in the FST
// query must produce the same order this slice does. pg's default
// collation reorders punctuation by language rules (e.g. en_US treats
// `/` and `+` differently than ASCII), which would produce out-of-
// order compact_ids inside a posting and trip fstWriter's eager
// validation. Using "C" everywhere keeps the contract trivial: byte
// comparison, end of story.
type idSpace struct {
	Names []string
	index map[string]uint32
}

func loadIDSpace(db *sql.DB) (*idSpace, error) {
	rows, err := db.Query(`SELECT name FROM photos ORDER BY name COLLATE "C"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	names := make([]string, 0, 4096)
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	idx := make(map[string]uint32, len(names))
	for i, n := range names {
		idx[n] = uint32(i)
	}
	return &idSpace{Names: names, index: idx}, nil
}

// CompactID returns the compact id for a photos.name, or (0, false) if
// the name isn't in the id space.
func (s *idSpace) CompactID(name string) (uint32, bool) {
	id, ok := s.index[name]
	return id, ok
}

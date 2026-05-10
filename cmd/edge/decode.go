package main

import (
	"encoding/binary"
	"fmt"
)

// DecodePosting reads a posting record at offset and returns the
// reconstructed compact-id slice. Mirrors cmd/edge_build/fst.go's
// flushGroup encoding: varint(count) + count×varint(delta) where the
// first delta is from 0 (so absolute) and subsequent deltas accumulate.
//
// Returns an error rather than partial output on malformed input —
// silent corruption is the worst failure mode for a search runtime.
func (a *Artifacts) DecodePosting(offset uint64) ([]uint32, error) {
	if offset >= uint64(len(a.Postings)) {
		return nil, fmt.Errorf("posting offset %d beyond postings.bin length %d", offset, len(a.Postings))
	}
	p := a.Postings[offset:]

	count, n := binary.Uvarint(p)
	if n <= 0 {
		return nil, fmt.Errorf("malformed count varint at posting offset %d", offset)
	}
	p = p[n:]

	ids := make([]uint32, 0, count)
	var prev uint32
	for i := range count {
		delta, n := binary.Uvarint(p)
		if n <= 0 {
			return nil, fmt.Errorf("malformed delta varint at posting %d (offset %d)", i, offset)
		}
		prev += uint32(delta)
		ids = append(ids, prev)
		p = p[n:]
	}
	return ids, nil
}

// DecodePayload reads the payload record for a compact id, returning
// the caption and a slice of tag values aligned to manifest.Payload.Tags.
// Out-of-range cid is an error rather than a panic — callers may pass
// ids merged from external sources.
func (a *Artifacts) DecodePayload(cid uint32) (caption string, tags []string, err error) {
	if int(cid) >= len(a.PayloadOffsets) {
		return "", nil, fmt.Errorf("payload compact_id %d out of range (count=%d)", cid, len(a.PayloadOffsets))
	}
	offset := a.PayloadOffsets[cid]
	if offset >= uint64(len(a.PayloadBytes)) {
		return "", nil, fmt.Errorf("payload offset %d beyond file length %d", offset, len(a.PayloadBytes))
	}

	p := a.PayloadBytes[offset:]
	readLP := func() (string, error) {
		n, sz := binary.Uvarint(p)
		if sz <= 0 {
			return "", fmt.Errorf("malformed payload varint at cid=%d", cid)
		}
		p = p[sz:]
		if uint64(len(p)) < n {
			return "", fmt.Errorf("payload truncated at cid=%d: varint claims %d bytes, %d remain", cid, n, len(p))
		}
		s := string(p[:n])
		p = p[n:]
		return s, nil
	}

	if caption, err = readLP(); err != nil {
		return "", nil, err
	}
	tagCount := len(a.Manifest.Payload.Tags)
	tags = make([]string, tagCount)
	for i := range tags {
		if tags[i], err = readLP(); err != nil {
			return "", nil, err
		}
	}
	return caption, tags, nil
}

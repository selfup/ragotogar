package main

import "testing"

func newTestIDSpace(names ...string) *idSpace {
	idx := make(map[string]uint32, len(names))
	for i, n := range names {
		idx[n] = uint32(i)
	}
	return &idSpace{Names: names, index: idx}
}

func TestIDSpace_RoundTrip(t *testing.T) {
	s := newTestIDSpace("apple", "banana", "cherry")
	for i, name := range s.Names {
		cid, ok := s.CompactID(name)
		if !ok {
			t.Fatalf("CompactID(%q) returned ok=false", name)
		}
		if cid != uint32(i) {
			t.Fatalf("CompactID(%q) = %d, want %d", name, cid, i)
		}
		if s.Names[cid] != name {
			t.Fatalf("Names[%d] = %q, want %q", cid, s.Names[cid], name)
		}
	}
}

func TestIDSpace_MissingName(t *testing.T) {
	s := newTestIDSpace("apple", "banana")
	if cid, ok := s.CompactID("nope"); ok {
		t.Fatalf("CompactID(nope) returned ok=true cid=%d", cid)
	}
}

func TestIDSpace_Empty(t *testing.T) {
	s := newTestIDSpace()
	if _, ok := s.CompactID("anything"); ok {
		t.Fatal("empty id space returned ok=true")
	}
	if len(s.Names) != 0 {
		t.Fatalf("empty id space has Names len %d", len(s.Names))
	}
}

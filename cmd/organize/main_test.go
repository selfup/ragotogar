package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if err := loadConfig("../../scripts/.files.env"); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// camera simulates a real camera's file output
type camera struct {
	name        string
	mediaExts   []string
	sidecarExts []string
	fileCount   int
}

var testCameras = []camera{
	{name: "SonyA7IV", mediaExts: []string{"arw", "mp4"}, sidecarExts: []string{"xml"}, fileCount: 120},
	{name: "CanonR5", mediaExts: []string{"cr3", "mov", "jpg"}, sidecarExts: []string{}, fileCount: 80},
	{name: "FujiXT5", mediaExts: []string{"raf", "mov", "jpg"}, sidecarExts: []string{"dop"}, fileCount: 90},
	{name: "NikonZ9", mediaExts: []string{"nef", "mov"}, sidecarExts: []string{"dxo"}, fileCount: 70},
	{name: "BMPCC6K", mediaExts: []string{"braw"}, sidecarExts: []string{"dop"}, fileCount: 40},
	{name: "DJIMini", mediaExts: []string{"dng", "mp4", "jpg"}, sidecarExts: []string{}, fileCount: 60},
	{name: "DJIPocket3", mediaExts: []string{"mp4"}, sidecarExts: []string{"aac"}, fileCount: 40},
	{name: "GoPro12", mediaExts: []string{"mp4", "jpg"}, sidecarExts: []string{}, fileCount: 50},
	{name: "PhaseOne", mediaExts: []string{"dng", "hif"}, sidecarExts: []string{"pp3"}, fileCount: 30},
	{name: "SonyFX3", mediaExts: []string{"mp4"}, sidecarExts: []string{"xml"}, fileCount: 40},
	{name: "PanaS5", mediaExts: []string{"rw2", "mov", "jpg"}, sidecarExts: []string{}, fileCount: 50},
	{name: "AudioRec", mediaExts: []string{"wav"}, sidecarExts: []string{"mp3"}, fileCount: 30},
}

type fixtureManifest struct {
	// mediaFiles tracks all media files: filename -> expected type folder
	mediaFiles map[string]string
	// sidecarFiles tracks sidecars that have a parent: filename -> parent base name
	sidecarFiles map[string]string
	// orphanSidecars tracks sidecars with no parent media file
	orphanSidecars map[string]bool
	// orphanXMLs tracks XML/AAC sidecars with no parent (should go to MP4)
	orphanXMLs map[string]bool
	// orphanMP3s tracks MP3 sidecars with no parent WAV (should go to AUDIO)
	orphanMP3s map[string]bool
	// totalFiles is the total count of all created files
	totalFiles int
}

// generateFixtures creates 1000+ empty files in dir simulating multi-camera output.
// Birth time on macOS is set at file creation and cannot be changed via Chtimes,
// so all files will share a birth time of "now" (the test run time).
func generateFixtures(t *testing.T, dir string) *fixtureManifest {
	t.Helper()

	m := &fixtureManifest{
		mediaFiles:     make(map[string]string),
		sidecarFiles:   make(map[string]string),
		orphanSidecars: make(map[string]bool),
		orphanXMLs:     make(map[string]bool),
		orphanMP3s:     make(map[string]bool),
	}

	seq := 0
	for _, cam := range testCameras {
		for i := 0; i < cam.fileCount; i++ {
			for _, ext := range cam.mediaExts {
				seq++
				name := fmt.Sprintf("%s_%04d.%s", cam.name, seq, ext)
				touchFile(t, filepath.Join(dir, name))

				m.mediaFiles[name] = extToType[ext]

				// Every 3rd file gets sidecars
				if i%3 == 0 {
					for _, se := range cam.sidecarExts {
						sName := fmt.Sprintf("%s_%04d.%s", cam.name, seq, se)
						touchFile(t, filepath.Join(dir, sName))
						m.sidecarFiles[sName] = strings.TrimSuffix(name, filepath.Ext(name))
						m.totalFiles++
					}
				}

				m.totalFiles++
			}
		}
	}

	// Uppercase extension files
	for _, ext := range []string{"ARW", "CR3", "JPG", "MOV", "RAF", "DNG"} {
		seq++
		name := fmt.Sprintf("UPPER_%04d.%s", seq, ext)
		touchFile(t, filepath.Join(dir, name))
		m.mediaFiles[name] = extToType[strings.ToLower(ext)]
		m.totalFiles++
	}

	// Orphan DOP sidecars (no matching media file)
	for i := range 5 {
		name := fmt.Sprintf("orphan_%04d.dop", i)
		touchFile(t, filepath.Join(dir, name))
		m.orphanSidecars[name] = true
		m.totalFiles++
	}

	// Orphan XML sidecars (should default to MP4 folder)
	for i := range 5 {
		name := fmt.Sprintf("sony_orphan_%04d.xml", i)
		touchFile(t, filepath.Join(dir, name))
		m.orphanXMLs[name] = true
		m.totalFiles++
	}

	// Orphan AAC sidecars (should default to MP4 folder, like XML)
	for i := range 5 {
		name := fmt.Sprintf("dji_orphan_%04d.aac", i)
		touchFile(t, filepath.Join(dir, name))
		m.orphanXMLs[name] = true
		m.totalFiles++
	}

	// Orphan MP3 sidecars (no matching WAV, should go to AUDIO)
	for i := range 5 {
		name := fmt.Sprintf("solo_track_%04d.mp3", i)
		touchFile(t, filepath.Join(dir, name))
		m.orphanMP3s[name] = true
		m.totalFiles++
	}

	// Mixed-case sidecar patterns
	for range 10 {
		seq++
		name := fmt.Sprintf("MixCase_%04d.arw", seq)
		touchFile(t, filepath.Join(dir, name))
		m.mediaFiles[name] = "RAW"
		m.totalFiles++

		sName := fmt.Sprintf("MixCase_%04d.arw.DOP", seq)
		touchFile(t, filepath.Join(dir, sName))
		m.sidecarFiles[sName] = strings.TrimSuffix(name, filepath.Ext(name))
		m.totalFiles++
	}

	// Files with no extension (should be skipped)
	for i := range 5 {
		name := fmt.Sprintf("noext_%04d", i)
		touchFile(t, filepath.Join(dir, name))
		m.totalFiles++
	}

	// Files with unknown extensions (should be skipped)
	for i := range 5 {
		name := fmt.Sprintf("unknown_%04d.xyz", i)
		touchFile(t, filepath.Join(dir, name))
		m.totalFiles++
	}

	return m
}

func touchFile(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	f.Close()
}

// copyDir recursively copies src to dst, preserving mod times
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		info, _ := d.Info()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			t.Fatal(err)
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

// collectFiles walks a directory and returns a map of relative path -> true
func collectFiles(t *testing.T, dir string) map[string]bool {
	t.Helper()
	files := map[string]bool{}
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			files[rel] = true
		}
		return nil
	})
	return files
}

func TestOrganize(t *testing.T) {
	// Generate fixtures in a stable directory
	fixturesDir := t.TempDir()
	manifest := generateFixtures(t, fixturesDir)

	t.Logf("Generated %d total files", manifest.totalFiles)
	if manifest.totalFiles < 1000 {
		t.Fatalf("Expected 1000+ files, got %d", manifest.totalFiles)
	}

	// Copy fixtures immutably into a working directory
	workDir := t.TempDir()
	copyDir(t, fixturesDir, workDir)

	// Verify fixture dir is untouched (same file count)
	fixtureFiles := collectFiles(t, fixturesDir)
	if len(fixtureFiles) != manifest.totalFiles {
		t.Fatalf("Fixture dir corrupted: expected %d files, got %d", manifest.totalFiles, len(fixtureFiles))
	}

	// Get the expected date folder by reading the birth time of any file in the work dir
	// (all files created in the same test run share the same birth date)
	var expectedDateFolder string
	entries, _ := os.ReadDir(workDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		bt, err := fileTime(filepath.Join(workDir, e.Name()))
		if err != nil {
			t.Fatalf("could not get birth time for test file: %v", err)
		}
		expectedDateFolder = formatDate(bt)
		break
	}
	t.Logf("Expected date folder: %s", expectedDateFolder)

	// Run organizer
	workers := runtime.NumCPU()
	errs := organize(workDir, workers)
	if len(errs) > 0 {
		t.Fatalf("organize returned %d errors: %v", len(errs), errs)
	}

	// Verify fixtures are still untouched after organize
	fixtureFilesAfter := collectFiles(t, fixturesDir)
	if len(fixtureFilesAfter) != manifest.totalFiles {
		t.Fatalf("Fixture dir was modified! expected %d files, got %d", manifest.totalFiles, len(fixtureFilesAfter))
	}

	// Collect all files in the organized directory
	organized := collectFiles(t, workDir)

	// Verify: every media file ended up in TypeFolder/DateFolder/filename
	for name, typeFolder := range manifest.mediaFiles {
		expected := filepath.Join(typeFolder, expectedDateFolder, name)
		if !organized[expected] {
			t.Errorf("media file not found at expected path: %s", expected)
		}
	}

	// Verify: sidecars with parents ended up next to their parent
	for sName, parentBase := range manifest.sidecarFiles {
		found := false
		for path := range organized {
			if filepath.Base(path) == sName {
				dir := filepath.Dir(path)
				for oPath := range organized {
					oBase := strings.TrimSuffix(filepath.Base(oPath), filepath.Ext(filepath.Base(oPath)))
					if filepath.Dir(oPath) == dir && strings.EqualFold(oBase, parentBase) {
						found = true
						break
					}
				}
				break
			}
		}
		if !found {
			t.Errorf("sidecar %s not found next to parent %s", sName, parentBase)
		}
	}

	// Verify: orphan XMP sidecars are left in place (no parent found)
	for name := range manifest.orphanSidecars {
		found := false
		for path := range organized {
			if filepath.Base(path) == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("orphan sidecar %s disappeared", name)
		}
	}

	// Verify: orphan XML/AAC sidecars went to MP4/<date>/
	for name := range manifest.orphanXMLs {
		expected := filepath.Join("MP4", expectedDateFolder, name)
		if !organized[expected] {
			t.Errorf("orphan XML/AAC %s not found at expected path: %s", name, expected)
		}
	}

	// Verify: orphan MP3 sidecars went to AUDIO/<date>/
	for name := range manifest.orphanMP3s {
		expected := filepath.Join("AUDIO", expectedDateFolder, name)
		if !organized[expected] {
			t.Errorf("orphan MP3 %s not found at expected path: %s", name, expected)
		}
	}

	// Verify: no media files left in root
	rootEntries, _ := os.ReadDir(workDir)
	for _, e := range rootEntries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := extLower(name)
		if ext != "" && !isSidecar(ext) {
			if _, ok := extToType[ext]; ok {
				t.Errorf("media file still in root: %s", name)
			}
		}
	}

	// Verify: no files were lost
	if len(organized) != manifest.totalFiles {
		t.Errorf("file count mismatch: started with %d, ended with %d", manifest.totalFiles, len(organized))
	}

	// Verify: type folders contain only expected types
	for path := range organized {
		parts := strings.Split(path, string(filepath.Separator))
		if len(parts) < 2 {
			continue
		}
		topFolder := parts[0]
		fileName := parts[len(parts)-1]
		ext := extLower(fileName)

		if isSidecar(ext) || ext == "" {
			continue
		}

		expectedType, ok := extToType[ext]
		if ok && topFolder != expectedType {
			t.Errorf("file %s in wrong type folder: got %s, want %s", path, topFolder, expectedType)
		}
	}
}

func TestOrganizeEmpty(t *testing.T) {
	dir := t.TempDir()
	errs := organize(dir, runtime.NumCPU())
	if len(errs) > 0 {
		t.Fatalf("organize on empty dir returned errors: %v", errs)
	}
}

func TestFormatDate(t *testing.T) {
	tests := []struct {
		time     time.Time
		expected string
	}{
		{time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), "January1st2025"},
		{time.Date(2025, 2, 2, 0, 0, 0, 0, time.UTC), "February2nd2025"},
		{time.Date(2025, 3, 3, 0, 0, 0, 0, time.UTC), "March3rd2025"},
		{time.Date(2025, 4, 4, 0, 0, 0, 0, time.UTC), "April4th2025"},
		{time.Date(2025, 5, 11, 0, 0, 0, 0, time.UTC), "May11th2025"},
		{time.Date(2025, 6, 21, 0, 0, 0, 0, time.UTC), "June21st2025"},
		{time.Date(2025, 7, 22, 0, 0, 0, 0, time.UTC), "July22nd2025"},
		{time.Date(2025, 8, 23, 0, 0, 0, 0, time.UTC), "August23rd2025"},
		{time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), "December31st2025"},
	}

	for _, tt := range tests {
		got := formatDate(tt.time)
		if got != tt.expected {
			t.Errorf("formatDate(%v) = %q, want %q", tt.time, got, tt.expected)
		}
	}
}

func TestOrdinalSuffix(t *testing.T) {
	tests := map[int]string{
		1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 10: "10th",
		11: "11th", 12: "12th", 13: "13th",
		21: "21st", 22: "22nd", 23: "23rd", 24: "24th",
		31: "31st",
	}
	for day, want := range tests {
		got := ordinalSuffix(day)
		if got != want {
			t.Errorf("ordinalSuffix(%d) = %q, want %q", day, got, want)
		}
	}
}

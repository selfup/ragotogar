package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var sidecarExts = map[string]bool{
	"dxo": true, "dop": true, "pp3": true, "xml": true,
}

var extToType = map[string]string{
	"jpg": "JPEG", "jpeg": "JPEG",
	"hif": "HIF", "heif": "HIF", "heic": "HIF",
	"mov": "MOV",
	"mp4": "MP4",
	"braw": "BRAW",
	"nev": "NEV",
	"ndf": "NDF",
	"raf": "RAW", "arw": "RAW", "nef": "RAW", "cr2": "RAW",
	"cr3": "RAW", "dng": "RAW", "orf": "RAW", "rw2": "RAW", "pef": "RAW",
}

type moveJob struct {
	src     string
	destDir string
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <directory>\n", os.Args[0])
		os.Exit(1)
	}

	targetDir := os.Args[1]
	info, err := os.Stat(targetDir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Error: '%s' is not a directory\n", targetDir)
		os.Exit(1)
	}

	workers := runtime.NumCPU()
	fmt.Printf("Using %d workers\n\n", workers)

	if errs := organize(targetDir, workers); len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d error(s) occurred:\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
		os.Exit(1)
	}

	fmt.Println("Done!")
}

func organize(targetDir string, workers int) []error {
	var allErrs []error

	// Pass 1: Organize by type
	fmt.Println("=== Pass 1: Organizing files by type ===")
	var moved, skipped int64
	var pass1Jobs []moveJob

	entries, _ := os.ReadDir(targetDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := extLower(name)
		if ext == "" || isSidecar(ext) {
			continue
		}
		typeFolder, ok := extToType[ext]
		if !ok {
			atomic.AddInt64(&skipped, 1)
			continue
		}
		pass1Jobs = append(pass1Jobs, moveJob{
			src:     filepath.Join(targetDir, name),
			destDir: filepath.Join(targetDir, typeFolder),
		})
	}

	allErrs = append(allErrs, prepareAndRunJobs(pass1Jobs, workers, &moved)...)
	fmt.Printf("  Moved %d files (%d skipped)\n\n", moved, skipped)

	// Pass 2: Organize by date
	fmt.Println("=== Pass 2: Organizing files by creation date ===")
	var movedP2 int64
	var pass2Jobs []moveJob

	typeDirs, _ := os.ReadDir(targetDir)
	for _, td := range typeDirs {
		if !td.IsDir() {
			continue
		}
		typePath := filepath.Join(targetDir, td.Name())
		files, _ := os.ReadDir(typePath)
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			ext := extLower(name)
			if isSidecar(ext) {
				continue
			}
			fullPath := filepath.Join(typePath, name)
			btime, err := birthTime(fullPath)
			if err != nil {
				allErrs = append(allErrs, err)
				continue
			}
			dateFolder := formatDate(btime)

			if filepath.Base(filepath.Dir(fullPath)) == dateFolder {
				continue
			}

			pass2Jobs = append(pass2Jobs, moveJob{
				src:     fullPath,
				destDir: filepath.Join(typePath, dateFolder),
			})
		}
	}

	allErrs = append(allErrs, prepareAndRunJobs(pass2Jobs, workers, &movedP2)...)
	fmt.Println()

	// Pass 3: Reunite orphaned sidecars
	fmt.Println("=== Pass 3: Reuniting orphaned sidecar files ===")

	// Build index of all media files by base name
	mediaIndex := map[string]string{}
	filepath.Walk(targetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := extLower(info.Name())
		if ext != "" && !isSidecar(ext) {
			base := strings.TrimSuffix(info.Name(), filepath.Ext(info.Name()))
			mediaIndex[strings.ToLower(base)] = path
		}
		return nil
	})

	var movedP3 int64
	var pass3Jobs []moveJob

	entries, _ = os.ReadDir(targetDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := extLower(name)
		if !isSidecar(ext) {
			continue
		}

		base := strings.TrimSuffix(name, filepath.Ext(name))
		baseLower := strings.ToLower(base)

		parentPath := ""
		if p, ok := mediaIndex[baseLower]; ok {
			parentPath = p
		}

		if parentPath == "" {
			baseOfBase := strings.TrimSuffix(base, filepath.Ext(base))
			if baseOfBase != base {
				if p, ok := mediaIndex[strings.ToLower(baseOfBase)]; ok {
					parentPath = p
				}
			}
		}

		fullPath := filepath.Join(targetDir, name)
		if parentPath != "" {
			pass3Jobs = append(pass3Jobs, moveJob{
				src:     fullPath,
				destDir: filepath.Dir(parentPath),
			})
		} else if ext == "xml" {
			btime, err := birthTime(fullPath)
			if err != nil {
				allErrs = append(allErrs, err)
				continue
			}
			dateFolder := formatDate(btime)
			pass3Jobs = append(pass3Jobs, moveJob{
				src:     fullPath,
				destDir: filepath.Join(targetDir, "MP4", dateFolder),
			})
		} else {
			fmt.Printf("  %s — no parent found, leaving in place\n", name)
		}
	}

	allErrs = append(allErrs, prepareAndRunJobs(pass3Jobs, workers, &movedP3)...)
	fmt.Println()

	return allErrs
}

func prepareAndRunJobs(jobs []moveJob, workers int, count *int64) []error {
	if len(jobs) == 0 {
		return nil
	}

	// Sort jobs by destination then filename for deterministic order
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].destDir != jobs[j].destDir {
			return jobs[i].destDir < jobs[j].destDir
		}
		return jobs[i].src < jobs[j].src
	})

	// Pre-create all destination directories before dispatching workers
	created := map[string]bool{}
	for _, j := range jobs {
		if !created[j.destDir] {
			if err := os.MkdirAll(j.destDir, 0755); err != nil {
				return []error{fmt.Errorf("mkdir %s: %w", j.destDir, err)}
			}
			created[j.destDir] = true
		}
	}

	// Dispatch to workers
	ch := make(chan moveJob, len(jobs))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var printMu sync.Mutex
	var errs []error

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range ch {
				if err := moveWithSidecars(job, &printMu); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				} else {
					atomic.AddInt64(count, 1)
				}
			}
		}()
	}

	for _, j := range jobs {
		ch <- j
	}
	close(ch)
	wg.Wait()

	return errs
}

func moveWithSidecars(job moveJob, printMu *sync.Mutex) error {
	name := filepath.Base(job.src)
	sourceDir := filepath.Dir(job.src)
	base := strings.TrimSuffix(name, filepath.Ext(name))

	dest := filepath.Join(job.destDir, name)
	if err := os.Rename(job.src, dest); err != nil {
		return fmt.Errorf("move %s: %w", name, err)
	}

	printMu.Lock()
	fmt.Printf("  %s -> %s/\n", name, filepath.Base(job.destDir))
	printMu.Unlock()

	// Find sidecars by reading the directory and matching case-insensitively.
	// This avoids case-insensitive filesystem issues where os.Stat("file.xmp")
	// matches "file.XMP" but os.Rename would change the filename's case.
	dirEntries, _ := os.ReadDir(sourceDir)
	baseLower := strings.ToLower(base)
	nameLower := strings.ToLower(name)
	for _, entry := range dirEntries {
		if entry.IsDir() {
			continue
		}
		eName := entry.Name()
		eExt := extLower(eName)
		if !isSidecar(eExt) {
			continue
		}
		eBase := strings.ToLower(strings.TrimSuffix(eName, filepath.Ext(eName)))
		if eBase == baseLower || eBase == nameLower {
			sidecarPath := filepath.Join(sourceDir, eName)
			sidecarDest := filepath.Join(job.destDir, eName)
			if err := os.Rename(sidecarPath, sidecarDest); err == nil {
				printMu.Lock()
				fmt.Printf("  %s -> %s/ (sidecar)\n", eName, filepath.Base(job.destDir))
				printMu.Unlock()
			}
		}
	}

	return nil
}

func extLower(name string) string {
	ext := filepath.Ext(name)
	if ext == "" {
		return ""
	}
	return strings.ToLower(ext[1:])
}

func isSidecar(ext string) bool {
	return sidecarExts[ext]
}

func birthTime(path string) (time.Time, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return time.Time{}, fmt.Errorf("stat %s: %w", path, err)
	}
	bt := time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
	if bt.IsZero() {
		return time.Time{}, fmt.Errorf("no birth time for %s", path)
	}
	return bt, nil
}

func formatDate(t time.Time) string {
	month := t.Format("January")
	day := t.Day()
	year := t.Year()
	return fmt.Sprintf("%s%s%d", month, ordinalSuffix(day), year)
}

func ordinalSuffix(day int) string {
	switch day {
	case 1, 21, 31:
		return fmt.Sprintf("%dst", day)
	case 2, 22:
		return fmt.Sprintf("%dnd", day)
	case 3, 23:
		return fmt.Sprintf("%drd", day)
	default:
		return fmt.Sprintf("%dth", day)
	}
}

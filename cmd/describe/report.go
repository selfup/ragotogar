package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"ragotogar/library"
)

type describeReport struct {
	mu   sync.Mutex
	file *os.File
	err  error
}

func openDescribeReport(path string) (*describeReport, error) {
	if path == "" {
		return &describeReport{}, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	return &describeReport{file: f}, err
}

func (r *describeReport) save(db *sql.DB, name, status string, classifyErr error) {
	if r.file == nil {
		return
	}
	result, err := library.LoadDescribeResult(context.Background(), db, name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	if err != nil {
		r.err = fmt.Errorf("load report for %s: %w", name, err)
		return
	}
	result.Status = status
	result.IndexReady = status == "classified"
	if classifyErr != nil {
		result.Error = classifyErr.Error()
	}
	r.err = json.NewEncoder(r.file).Encode(result)
}

func (r *describeReport) close() error {
	if r.file != nil {
		if err := r.file.Close(); r.err == nil {
			r.err = err
		}
	}
	return r.err
}

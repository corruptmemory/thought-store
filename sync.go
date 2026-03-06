package main

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// ExportBundle creates a zstd-compressed tarball of the database.
// The tarball contains brain.db at the root (canonical name regardless of local path).
func ExportBundle(ctx context.Context, store *SQLiteStore, dbPath string, w io.Writer) error {
	if err := store.Checkpoint(ctx); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}

	dbFile, err := os.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer dbFile.Close()

	dbInfo, err := dbFile.Stat()
	if err != nil {
		return fmt.Errorf("stat db: %w", err)
	}

	zw, err := zstd.NewWriter(w)
	if err != nil {
		return fmt.Errorf("create zstd writer: %w", err)
	}
	defer zw.Close()

	tw := tar.NewWriter(zw)
	defer tw.Close()

	if err := tw.WriteHeader(&tar.Header{
		Name: "brain.db",
		Mode: 0644,
		Size: dbInfo.Size(),
	}); err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}

	if _, err := io.Copy(tw, dbFile); err != nil {
		return fmt.Errorf("write db to tar: %w", err)
	}

	if err := tw.WriteHeader(&tar.Header{
		Name:     "assets/",
		Typeflag: tar.TypeDir,
		Mode:     0755,
	}); err != nil {
		return fmt.Errorf("write assets dir header: %w", err)
	}

	return nil
}

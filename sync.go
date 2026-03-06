package main

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ImportResult summarizes what was imported from a sync bundle.
type ImportResult struct {
	NewInstances  int `json:"new_instances"`
	NewThoughts   int `json:"new_thoughts"`
	Skipped       int `json:"skipped"`
	ReembedQueued int `json:"reembed_queued"`
	Forgotten     int `json:"forgotten"`
}

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

// ImportBundle decompresses a sync bundle and merges it into the local database.
// myInstanceUID is this instance's unique_id — thoughts originating from this
// instance are skipped (they came from us originally).
func ImportBundle(ctx context.Context, store *SQLiteStore, myInstanceUID string, localModelID int, r io.Reader, vecIndex *VecIndex) (*ImportResult, error) {
	tmpDir, err := os.MkdirTemp("", "sync-import-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := unpackBundle(r, tmpDir); err != nil {
		return nil, fmt.Errorf("unpack: %w", err)
	}

	foreignDBPath := filepath.Join(tmpDir, "brain.db")
	foreignStore, err := NewSQLiteStore(foreignDBPath)
	if err != nil {
		return nil, fmt.Errorf("open foreign db: %w", err)
	}
	defer foreignStore.Close()

	result := &ImportResult{}

	// Step 1: Import new instances
	instanceMap, err := importInstances(ctx, store, foreignStore, result)
	if err != nil {
		return nil, fmt.Errorf("import instances: %w", err)
	}

	// Step 2: Get foreign active model info for embedding comparison
	foreignModelID, err := foreignStore.ActiveModelID(ctx)
	if err != nil {
		foreignModelID = 0
	}

	var foreignModelName, foreignModelVersion string
	if foreignModelID > 0 {
		foreignStore.db.QueryRowContext(ctx,
			`SELECT name, version FROM models WHERE id = ?`, foreignModelID,
		).Scan(&foreignModelName, &foreignModelVersion)
	}

	var localModelName, localModelVersion string
	store.db.QueryRowContext(ctx,
		`SELECT name, version FROM models WHERE id = ?`, localModelID,
	).Scan(&localModelName, &localModelVersion)

	modelsMatch := foreignModelName != "" &&
		foreignModelName == localModelName &&
		foreignModelVersion == localModelVersion

	// Step 3: Find candidate thoughts (not from my instance, not forgotten)
	rows, err := foreignStore.db.QueryContext(ctx,
		`SELECT t.unique_id, t.content, src.name, i.unique_id, t.created_at, t.state
		 FROM thoughts t
		 JOIN sources src ON src.id = t.source_id
		 JOIN instances i ON i.id = t.instance_id
		 WHERE i.unique_id != ? AND t.state != ?`, myInstanceUID, StateForgotten)
	if err != nil {
		return nil, fmt.Errorf("query foreign thoughts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var thoughtUID, content, sourceName, instanceUID, createdAt string
		var foreignState int
		if err := rows.Scan(&thoughtUID, &content, &sourceName, &instanceUID, &createdAt, &foreignState); err != nil {
			return nil, fmt.Errorf("scan foreign thought: %w", err)
		}

		// Check if we already have this thought
		var exists int
		existErr := store.db.QueryRowContext(ctx,
			`SELECT 1 FROM thoughts WHERE unique_id = ?`, thoughtUID).Scan(&exists)
		if existErr == nil {
			result.Skipped++
			continue
		}

		localInstanceID, ok := instanceMap[instanceUID]
		if !ok {
			continue
		}

		// Load embedding if models match
		var embedding []float32
		if modelsMatch && foreignModelID > 0 {
			var blob []byte
			err := foreignStore.db.QueryRowContext(ctx,
				`SELECT e.vector FROM embeddings e
				 JOIN thoughts t ON t.id = e.thought_id
				 WHERE t.unique_id = ? AND e.model_id = ?`,
				thoughtUID, foreignModelID).Scan(&blob)
			if err == nil && len(blob) > 0 {
				embedding = decodeVector(blob)
			}
		}

		err := store.ImportThought(ctx, thoughtUID, content, sourceName, localInstanceID, localModelID, embedding, ThoughtState(foreignState), createdAt)
		if err != nil {
			return nil, fmt.Errorf("import thought %s: %w", thoughtUID, err)
		}

		result.NewThoughts++
		if embedding != nil && vecIndex != nil {
			vecIndex.Add(thoughtUID, embedding)
		}
		if embedding == nil {
			result.ReembedQueued++
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("foreign thoughts rows: %w", err)
	}

	// Step 4: Propagate forgotten state
	forgottenRows, err := foreignStore.db.QueryContext(ctx,
		`SELECT t.unique_id FROM thoughts t
		 JOIN instances i ON i.id = t.instance_id
		 WHERE t.state = ? AND i.unique_id != ?`,
		StateForgotten, myInstanceUID)
	if err != nil {
		return nil, fmt.Errorf("query foreign forgotten: %w", err)
	}
	defer forgottenRows.Close()

	for forgottenRows.Next() {
		var thoughtUID string
		if err := forgottenRows.Scan(&thoughtUID); err != nil {
			return nil, fmt.Errorf("scan forgotten: %w", err)
		}

		// Check if we have this thought and it's not already forgotten
		var localState int
		err := store.db.QueryRowContext(ctx,
			`SELECT state FROM thoughts WHERE unique_id = ?`, thoughtUID).Scan(&localState)
		if err != nil {
			continue // we don't have it locally
		}
		if localState == int(StateForgotten) {
			continue // already forgotten
		}

		if err := store.ForgetThought(ctx, thoughtUID); err != nil {
			return nil, fmt.Errorf("propagate forget %s: %w", thoughtUID, err)
		}
		result.Forgotten++
		if vecIndex != nil {
			vecIndex.Remove(thoughtUID)
		}
	}
	if err := forgottenRows.Err(); err != nil {
		return nil, fmt.Errorf("forgotten rows: %w", err)
	}

	return result, nil
}

// importInstances imports all foreign instances into the local DB.
// Returns a map of foreign instance unique_id -> local instance integer ID.
func importInstances(ctx context.Context, localStore, foreignStore *SQLiteStore, result *ImportResult) (map[string]int, error) {
	rows, err := foreignStore.db.QueryContext(ctx,
		`SELECT unique_id, name, description FROM instances`)
	if err != nil {
		return nil, fmt.Errorf("query foreign instances: %w", err)
	}
	defer rows.Close()

	instanceMap := make(map[string]int)
	for rows.Next() {
		var uid, name, desc string
		if err := rows.Scan(&uid, &name, &desc); err != nil {
			return nil, fmt.Errorf("scan instance: %w", err)
		}

		var existingID int
		err := localStore.db.QueryRowContext(ctx,
			`SELECT id FROM instances WHERE unique_id = ?`, uid).Scan(&existingID)
		if err == nil {
			instanceMap[uid] = existingID
			continue
		}

		localID, err := localStore.ImportInstance(ctx, uid, name, desc)
		if err != nil {
			return nil, fmt.Errorf("import instance %s: %w", uid, err)
		}
		instanceMap[uid] = localID
		result.NewInstances++
	}
	return instanceMap, rows.Err()
}

// unpackBundle decompresses a zstd tarball into the given directory.
func unpackBundle(r io.Reader, destDir string) error {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		target := filepath.Join(destDir, header.Name)

		// Prevent path traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			return fmt.Errorf("invalid tar path: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			f, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("write %s: %w", target, err)
			}
			f.Close()
		}
	}
	return nil
}

package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMetadataTypeString(t *testing.T) {
	tests := []struct {
		mt   MetadataType
		want string
	}{
		{MetadataPerson, "person"},
		{MetadataDate, "date"},
		{MetadataTag, "tag"},
		{MetadataActionItem, "action_item"},
		{MetadataType(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.mt.String(); got != tt.want {
			t.Errorf("MetadataType(%d).String() = %q, want %q", tt.mt, got, tt.want)
		}
	}
}

func TestMetadataTypeMarshalText(t *testing.T) {
	b, err := MetadataTag.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "tag" {
		t.Errorf("MarshalText = %q, want %q", b, "tag")
	}
}

func TestMetadataTypeUnmarshalText(t *testing.T) {
	tests := []struct {
		text string
		want MetadataType
	}{
		{"person", MetadataPerson},
		{"date", MetadataDate},
		{"tag", MetadataTag},
		{"action_item", MetadataActionItem},
		{"bogus", MetadataPerson},
	}
	for _, tt := range tests {
		var got MetadataType
		if err := got.UnmarshalText([]byte(tt.text)); err != nil {
			t.Fatalf("UnmarshalText(%q): %v", tt.text, err)
		}
		if got != tt.want {
			t.Errorf("UnmarshalText(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}

func TestMigrateCreatesMetadataTables(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migrate-meta.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify metadata table exists
	var name string
	err = store.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='metadata'`).Scan(&name)
	if err != nil {
		t.Fatalf("metadata table not found: %v", err)
	}

	// Verify thought_metadata table exists
	err = store.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='thought_metadata'`).Scan(&name)
	if err != nil {
		t.Fatalf("thought_metadata table not found: %v", err)
	}

	// Verify unique constraint on metadata
	_, err = store.db.ExecContext(ctx,
		`INSERT INTO metadata (metadata_type, value) VALUES (0, 'Sarah')`)
	if err != nil {
		t.Fatalf("insert metadata: %v", err)
	}
	_, err = store.db.ExecContext(ctx,
		`INSERT INTO metadata (metadata_type, value) VALUES (0, 'Sarah')`)
	if err == nil {
		t.Fatal("expected unique constraint violation on duplicate metadata")
	}
}

# Thought Metadata Design

## Problem

Thoughts are unstructured text blobs. Nate's Open Brain extracts structured metadata at ingestion (type, people, topics, action items, dates) via LLM. We need the schema and storage to hold this metadata, surfaced in API responses, synced between peers — without coupling to any specific LLM.

## Design decisions

### Extraction is not the store's concern

The thought store is a storage/retrieval layer. Metadata extraction is the "brain" layer's job. The store provides schema, storage, API surfacing, and sync. Population happens separately — via a future CLI command (like `re-embed`) or the brain layer.

### Values are labels, associations are entities

- **metadata** table: content-addressed, deduplicated by `(metadata_type, value)`. No UUID, no instance tracking. Like `sources`.
- **thought_metadata** table: associations with `unique_id` (for sync) and `instance_id` (for provenance). Like thoughts.

This separation means "Sarah" exists once globally, but the association "thought X mentions Sarah" is a syncable entity.

### Type classification is just a tag

Nate has a `type` field (observation, task, idea, etc.). We represent this as a tag — the brain layer applies naming conventions (e.g., `type:observation`). The store doesn't need to know the taxonomy.

### Enum stability

All integer enums stored in the DB (`ThoughtState`, `MetadataType`, future enums) are append-only: order must be preserved forever, values can never be deleted or reordered.

## Schema

```sql
-- IMPORTANT: metadata_type integer values are append-only.
-- Order must be preserved forever. You can only add, never delete.
CREATE TABLE metadata (
  id            INTEGER PRIMARY KEY,
  metadata_type INTEGER NOT NULL,
  value         TEXT NOT NULL,
  UNIQUE(metadata_type, value)
);

CREATE TABLE thought_metadata (
  id          INTEGER PRIMARY KEY,
  unique_id   TEXT NOT NULL UNIQUE,
  thought_id  INTEGER NOT NULL REFERENCES thoughts(id),
  metadata_id INTEGER NOT NULL REFERENCES metadata(id),
  instance_id INTEGER NOT NULL REFERENCES instances(id)
);
```

## MetadataType enum

```go
type MetadataType int
const (
    MetadataPerson     MetadataType = iota // 0 — person mentioned
    MetadataDate                           // 1 — date referenced (YYYY-MM-DD)
    MetadataTag                            // 2 — topic tag
    MetadataActionItem                     // 3 — implied to-do
)
```

Same fake enum pattern as `ThoughtState` and `BrainMode` — `String()`, `MarshalText()`, `UnmarshalText()`.

## API changes

`Thought` struct gains a `Metadata` field:

```go
type ThoughtMetadataEntry struct {
    Type  MetadataType `json:"type"`
    Value string       `json:"value"`
}

type Thought struct {
    ID        string                  `json:"id"`
    Content   string                  `json:"content"`
    Source    string                  `json:"source"`
    State    ThoughtState            `json:"state"`
    Metadata []ThoughtMetadataEntry  `json:"metadata,omitempty"`
    CreatedAt time.Time              `json:"created_at"`
}
```

List, Search, and GetByUniqueIDs include metadata. No filtering yet.

## Sync

Import from foreign DB:

1. Read foreign `metadata` rows. `INSERT OR IGNORE` by `(metadata_type, value)`. Build foreign→local ID mapping.
2. Read foreign `thought_metadata` rows. Import by `unique_id` (skip if exists). Remap `thought_id` and `metadata_id` to local IDs using existing mappings.

Same shape as instance/source remapping during thought import.

## ForgetThought

`ForgetThought` adds: `DELETE FROM thought_metadata WHERE thought_id = ?` alongside existing content wipe and embedding deletion. Tombstone consistency.

## Future

- **List filters**: filter by metadata_type + value (e.g., "all thoughts mentioning Sarah", "all action items")
- **Stats breakdowns**: top people, top tags, action item counts
- **Brain layer**: LLM-powered extraction populates metadata via CLI or API
- **Batch extract**: `./thought-store extract` command (like `re-embed`) to retroactively tag all thoughts

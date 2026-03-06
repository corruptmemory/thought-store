package main

import "testing"

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

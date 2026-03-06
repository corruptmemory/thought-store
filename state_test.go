package main

import "testing"

func TestThoughtStateString(t *testing.T) {
	tests := []struct {
		state ThoughtState
		want  string
	}{
		{StateActive, "active"},
		{StateRefined, "refined"},
		{StateForgotten, "forgotten"},
		{ThoughtState(99), "unknown"},
	}

	for _, tt := range tests {
		got := tt.state.String()
		if got != tt.want {
			t.Errorf("ThoughtState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestThoughtStateMarshalText(t *testing.T) {
	tests := []struct {
		state ThoughtState
		want  string
	}{
		{StateActive, "active"},
		{StateRefined, "refined"},
		{StateForgotten, "forgotten"},
		{ThoughtState(42), "unknown"},
	}

	for _, tt := range tests {
		got, err := tt.state.MarshalText()
		if err != nil {
			t.Fatalf("ThoughtState(%d).MarshalText() returned error: %v", tt.state, err)
		}
		if string(got) != tt.want {
			t.Errorf("ThoughtState(%d).MarshalText() = %q, want %q", tt.state, string(got), tt.want)
		}
	}
}

func TestThoughtStateUnmarshalText(t *testing.T) {
	tests := []struct {
		text string
		want ThoughtState
	}{
		{"active", StateActive},
		{"refined", StateRefined},
		{"forgotten", StateForgotten},
		{"bogus", StateActive},
	}

	for _, tt := range tests {
		var got ThoughtState
		if err := got.UnmarshalText([]byte(tt.text)); err != nil {
			t.Fatalf("UnmarshalText(%q) returned error: %v", tt.text, err)
		}
		if got != tt.want {
			t.Errorf("UnmarshalText(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}

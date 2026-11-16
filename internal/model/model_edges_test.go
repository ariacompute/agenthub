package model

import (
	"strings"
	"testing"
)

// A payload the encoder refuses is a programming error surfaced at write time,
// so the message has to name the field encoding rather than a bare json error.
func TestNewJSONFieldReportsAnEncodingFailure(t *testing.T) {
	if field, err := NewJSONField(map[string]any{"callback": func() {}}); err == nil {
		t.Fatalf("encoding a func returned %q", field)
	}
}

// A typed nil marshals to the literal "null"; the column must still be SQL NULL,
// otherwise "absent" and "present but empty" become indistinguishable on read.
func TestNewJSONFieldTreatsATypedNilAsAbsent(t *testing.T) {
	var missing *struct{ Name string }
	field, err := NewJSONField(missing)
	if err != nil {
		t.Fatalf("NewJSONField: %v", err)
	}
	if len(field) != 0 {
		t.Fatalf("field = %q, want empty", field)
	}
	value, err := field.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if value != nil {
		t.Fatalf("Value = %v, want nil", value)
	}
}

// Decoding a column that is not the shape the caller asked for must fail with
// the model's own wrapper, not a bare json error a caller cannot classify.
func TestDecodeReportsAMalformedDocument(t *testing.T) {
	var dst map[string]string
	err := JSONField(`{"a":`).Decode(&dst)
	if err == nil {
		t.Fatal("decoding a truncated document must fail")
	}
	if !strings.Contains(err.Error(), "model: decode json field") {
		t.Fatalf("err = %v, want the model prefix", err)
	}

	// A document that is valid JSON but the wrong shape fails the same way.
	if err := JSONField(`[1,2]`).Decode(&dst); err == nil {
		t.Fatal("decoding an array into a map must fail")
	}
}

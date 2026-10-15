package model

import (
	"database/sql"
	"database/sql/driver"
	"testing"
)

func TestJSONFieldRoundTrip(t *testing.T) {
	type payload struct {
		Name string   `json:"name"`
		Tags []string `json:"tags,omitempty"`
	}

	in := payload{Name: "hr-mcp", Tags: []string{"hr"}}
	field, err := NewJSONField(in)
	if err != nil {
		t.Fatalf("NewJSONField: %v", err)
	}
	if len(field) == 0 {
		t.Fatal("encoded field is empty")
	}

	value, err := field.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	// Postgres needs the document as text so it can be coerced into jsonb.
	if _, ok := value.(string); !ok {
		t.Fatalf("Value = %T, want string", value)
	}

	var got payload
	scanned := JSONField(value.(string))
	if err := scanned.Decode(&got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Name != in.Name || len(got.Tags) != 1 {
		t.Fatalf("round trip = %+v, want %+v", got, in)
	}
}

func TestJSONFieldNullHandling(t *testing.T) {
	// nil maps to a SQL NULL column, not to the literal text "null".
	field, err := NewJSONField(nil)
	if err != nil {
		t.Fatalf("NewJSONField(nil): %v", err)
	}
	value, err := field.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if value != nil {
		t.Fatalf("Value = %v, want nil", value)
	}

	var scanned JSONField
	if err := scanned.Scan(nil); err != nil {
		t.Fatalf("Scan(nil): %v", err)
	}
	if len(scanned) != 0 {
		t.Fatalf("scanned = %q, want empty", scanned)
	}

	// A SQL NULL coming back from the driver must be tolerated.
	if err := (&scanned).Scan([]byte("null")); err != nil {
		t.Fatalf("Scan([]byte(\"null\")): %v", err)
	}
	var dst map[string]string
	if err := scanned.Decode(&dst); err != nil {
		t.Fatalf("Decode of a null document: %v", err)
	}
	if dst != nil {
		t.Fatalf("dst = %+v, want nil", dst)
	}
}

func TestJSONFieldScanSources(t *testing.T) {
	for _, src := range []any{[]byte(`{"a":1}`), `{"a":1}`} {
		var field JSONField
		if err := field.Scan(src); err != nil {
			t.Fatalf("Scan(%T): %v", src, err)
		}
		var dst map[string]int
		if err := field.Decode(&dst); err != nil {
			t.Fatalf("Decode(%T): %v", src, err)
		}
		if dst["a"] != 1 {
			t.Fatalf("dst = %+v", dst)
		}
	}

	var field JSONField
	if err := field.Scan(struct{}{}); err == nil {
		t.Fatal("Scan of an unsupported source must fail")
	}
}

func TestJSONFieldImplementsDriverInterfaces(t *testing.T) {
	var _ driver.Valuer = JSONField(nil)
	var _ sql.Scanner = &JSONField{}
	if got := (JSONField{}).GormDBDataType(nil, nil); got != "jsonb" {
		t.Fatalf("GormDBDataType = %q, want jsonb", got)
	}
}

func TestTableNameMapping(t *testing.T) {
	cases := map[string]string{
		(&Resource{}).TableName():        "resources",
		(&Task{}).TableName():            "tasks",
		(&TaskArtifact{}).TableName():    "task_artifacts",
		(&TaskSpan{}).TableName():        "task_spans",
		(&TaskPending{}).TableName():     "task_pending",
		(&TaskPush{}).TableName():        "task_push",
		(&PushRecord{}).TableName():      "push_records",
		(&Event{}).TableName():           "events",
		(&SchemaMigration{}).TableName(): "schema_migrations",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("TableName = %q, want %q", got, want)
		}
	}
}

package toolschema

import (
	"encoding/json"
	"testing"
)

func TestCompileRejectsExternalRefsAndNonObjects(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","properties":{"x":{"$ref":"http://127.0.0.1/secrets"}}}`,
		`{"type":"string"}`,
		`{}`,
	} {
		if _, err := Compile(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted unsafe schema %s", raw)
		}
	}
	schema, err := Compile(json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = schema.Validate(map[string]any{"query": "library"}); err != nil {
		t.Fatal(err)
	}
	if err = schema.Validate(map[string]any{"query": 1}); err == nil {
		t.Fatal("invalid argument type accepted")
	}
}

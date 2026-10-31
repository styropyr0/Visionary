package uptimecore

import (
	"encoding/json"
	"testing"
)

func TestJSONPath(t *testing.T) {
	var doc any
	_ = json.Unmarshal([]byte(`{"a":{"b":[1,{"c d":"x"}]},"n":2.5}`), &doc)
	for path, want := range map[string]string{"$.a.b[0]": "1", "$.a.b[1]['c d']": "x", "a.b[-1][\"c d\"]": "x", "$.n": "2.5", "$.a.b.length": "2", "$.a": `{"b":[1,{"c d":"x"}]}`} {
		steps, err := parseJSONPath(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		v, ok := jsonPathGet(doc, steps)
		if !ok || jsonText(v) != want {
			t.Errorf("%s = %v %v, want %s", path, jsonText(v), ok, want)
		}
	}
	steps, _ := parseJSONPath("$.a.z")
	if _, ok := jsonPathGet(doc, steps); ok {
		t.Error("missing key found")
	}
}

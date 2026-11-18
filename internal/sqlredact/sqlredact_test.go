package sqlredact

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct{ engine, in, want string }{
		{"postgres", "SELECT * FROM users WHERE email = 'a@b.c' AND id = 42", "SELECT * FROM users WHERE email = ? AND id = ?"},
		{"postgres", "SELECT * FROM t WHERE id IN (1, 2, 3)", "SELECT * FROM t WHERE id IN (?)"},
		{"postgres", "UPDATE t SET v = $1 WHERE id = $2", "UPDATE t SET v = $1 WHERE id = $2"},
		{"postgres", "DO $body$ BEGIN PERFORM 'secret'; END $body$", "DO ?"},
		{"postgres", "SELECT $$it's a secret$$, 1", "SELECT ?, ?"},
		{"postgres", `SELECT E'pass\'word' AS x`, "SELECT E? AS x"},
		{"postgres", `SELECT "Weird""Name" FROM "T" WHERE a = 'x'`, `SELECT "Weird""Name" FROM "T" WHERE a = ?`},
		{"postgres", "SELECT 1 -- it's 'secret'\nFROM t", "SELECT ? FROM t"},
		{"postgres", "SELECT a$1$ FROM t", "SELECT a$1$ FROM t"},
		{"mysql", `SELECT * FROM users WHERE name = "bob" AND pw = 'it\'s'`, "SELECT * FROM users WHERE name = ? AND pw = ?"},
		{"mysql", "SELECT `col` FROM t # token 'abc'\nWHERE x = 0x1F", "SELECT `col` FROM t WHERE x = ?"},
		{"mysql", "INSERT INTO t VALUES ('a\\\\', 'b')", "INSERT INTO t VALUES (?, ?)"},
		// Text cut off inside a literal (track_activity_query_size) leaks nothing.
		{"postgres", "SELECT * FROM t WHERE token = 'abcdef", "SELECT * FROM t WHERE token = ?"},
		{"mysql", `SELECT "unterminated secret`, "SELECT ?"},
	}
	for _, c := range cases {
		if got := Redact(c.engine, c.in); got != c.want {
			t.Errorf("Redact(%s, %q) = %q, want %q", c.engine, c.in, got, c.want)
		}
		// The server redacts again: already redacted text must not change.
		if again := Redact(c.engine, c.want); again != c.want {
			t.Errorf("not idempotent: %q -> %q", c.want, again)
		}
	}
}

func TestRedactClips(t *testing.T) {
	long := "SELECT " + strings.Repeat("é, ", 2000) + "x"
	got := Redact("postgres", long)
	if len(got) > MaxLen || !strings.HasPrefix(got, "SELECT é") {
		t.Fatalf("len %d prefix %q", len(got), got[:20])
	}
}

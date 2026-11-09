package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// user-redaction-vectors.json is the shared spec for the user-name step of
// report redaction (card #300): the same cases the app runs against
// StandaloneReport.kt. Keep this file byte-for-byte identical to the one on
// the card so both sides stay in step.
type userRedactionVectors struct {
	CollisionWords []string `json:"collisionWords"`
	Cases          []struct {
		Name     string   `json:"name"`
		Users    []string `json:"users"`
		Input    string   `json:"input"`
		Expected string   `json:"expected"`
	} `json:"cases"`
}

func loadUserRedactionVectors(t *testing.T) userRedactionVectors {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "user-redaction-vectors.json"))
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v userRedactionVectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(v.Cases) == 0 {
		t.Fatal("no cases in vectors")
	}
	return v
}

// TestUserRedactionVectors runs redactUserNames against every shared vector.
func TestUserRedactionVectors(t *testing.T) {
	v := loadUserRedactionVectors(t)
	for _, c := range v.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			got := redactUserNames(c.Input, c.Users)
			if got != c.Expected {
				t.Errorf("redactUserNames mismatch\nusers: %q\ninput: %q\n  got: %q\n want: %q",
					c.Users, c.Input, got, c.Expected)
			}
		})
	}
}

// TestUserRedactionCollisionWordsFrozen guards that the Go collision list and
// the shared vectors agree: a word added on one side but not the other would
// silently change what the user-name step wipes. The vectors hold the
// authoritative list; the app ports the same one.
func TestUserRedactionCollisionWordsFrozen(t *testing.T) {
	v := loadUserRedactionVectors(t)
	if len(v.CollisionWords) != len(collisionWords) {
		t.Fatalf("collision count: vectors %d, code %d", len(v.CollisionWords), len(collisionWords))
	}
	for _, w := range v.CollisionWords {
		if !collisionWords[w] {
			t.Errorf("collision word %q is in the vectors but not in collisionWords", w)
		}
	}
}

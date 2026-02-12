package main

import (
	"reflect"
	"testing"
)

func TestEnforceAlwaysEnabledRules(t *testing.T) {
	disabled := map[string]struct{}{
		"missing_statement_terminator":       {},
		"mongo_missing_statement_terminator": {},
		"select_star":                        {},
	}

	removed := enforceAlwaysEnabledRules(disabled)
	expectedRemoved := []string{"missing_statement_terminator", "mongo_missing_statement_terminator"}
	if !reflect.DeepEqual(removed, expectedRemoved) {
		t.Fatalf("removed rules mismatch, got=%v want=%v", removed, expectedRemoved)
	}

	if _, found := disabled["missing_statement_terminator"]; found {
		t.Fatalf("missing_statement_terminator should be enforced and removed from disabled set")
	}
	if _, found := disabled["mongo_missing_statement_terminator"]; found {
		t.Fatalf("mongo_missing_statement_terminator should be enforced and removed from disabled set")
	}
	if _, found := disabled["select_star"]; !found {
		t.Fatalf("non-mandatory rule should keep disabled state")
	}
}

func TestEnforceAlwaysEnabledRulesNil(t *testing.T) {
	if removed := enforceAlwaysEnabledRules(nil); len(removed) != 0 {
		t.Fatalf("nil map should return empty removed list, got=%v", removed)
	}
}

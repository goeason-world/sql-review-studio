package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryStoreSaveAndFetch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	store, err := NewHistoryStore(dbPath)
	if err != nil {
		t.Fatalf("NewHistoryStore err: %v", err)
	}
	defer store.Close()

	input := SaveHistoryInput{
		RequestID:     "req-test-1",
		Engine:        EngineMySQL,
		Source:        "paste",
		FileName:      "",
		SQLText:       "SELECT * FROM users;",
		DisabledRules: []string{"select_without_limit"},
		CheckResult: AnalyzeSQLWithOptions("SELECT * FROM users;", AnalyzeOptions{
			DisabledRules: map[string]struct{}{"select_without_limit": {}},
		}),
	}

	historyID, err := store.Save(input)
	if err != nil {
		t.Fatalf("Save err: %v", err)
	}
	if historyID <= 0 {
		t.Fatalf("invalid history id: %d", historyID)
	}

	items, total, err := store.List(20, 0)
	if err != nil {
		t.Fatalf("List err: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("unexpected list result, total=%d len=%d", total, len(items))
	}
	if items[0].Engine != EngineMySQL {
		t.Fatalf("engine mismatch in list: %+v", items[0])
	}

	detail, err := store.GetByID(historyID)
	if err != nil {
		t.Fatalf("GetByID err: %v", err)
	}
	if detail.RequestID != input.RequestID {
		t.Fatalf("request id mismatch, got %s", detail.RequestID)
	}
	if detail.Engine != EngineMySQL {
		t.Fatalf("engine mismatch in detail: %+v", detail)
	}
	if detail.SQLText != input.SQLText {
		t.Fatalf("sql text mismatch")
	}
	if len(detail.DisabledRules) != 1 || detail.DisabledRules[0] != "select_without_limit" {
		t.Fatalf("disabled rules mismatch: %+v", detail.DisabledRules)
	}
}

func TestHistoryStoreSaveLargeSQL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history-large.db")
	store, err := NewHistoryStore(dbPath)
	if err != nil {
		t.Fatalf("NewHistoryStore err: %v", err)
	}
	defer store.Close()

	largeSQL := strings.Repeat("SELECT 1;\n", 40000)
	input := SaveHistoryInput{
		RequestID:     "req-large-1",
		Engine:        EnginePostgreSQL,
		Source:        "upload",
		FileName:      "large.sql",
		SQLText:       largeSQL,
		DisabledRules: []string{},
		CheckResult: CheckResponse{
			RulesVersion: rulesVersion,
			CheckedAt:    time.Now().Format(time.RFC3339),
			Summary: Summary{
				StatementCount: 40000,
				ErrorCount:     0,
				WarningCount:   0,
				InfoCount:      0,
			},
			Issues: []Issue{},
			Advice: []string{"ok"},
		},
	}

	historyID, err := store.Save(input)
	if err != nil {
		t.Fatalf("Save large sql err: %v", err)
	}
	if historyID <= 0 {
		t.Fatalf("invalid history id: %d", historyID)
	}

	detail, err := store.GetByID(historyID)
	if err != nil {
		t.Fatalf("GetByID err: %v", err)
	}
	if detail.Engine != EnginePostgreSQL {
		t.Fatalf("engine mismatch: %+v", detail)
	}
	if len(detail.SQLText) != len(largeSQL) {
		t.Fatalf("large sql length mismatch, got=%d want=%d", len(detail.SQLText), len(largeSQL))
	}
}

func TestHistoryStoreDeleteByIDs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history-delete.db")
	store, err := NewHistoryStore(dbPath)
	if err != nil {
		t.Fatalf("NewHistoryStore err: %v", err)
	}
	defer store.Close()

	saveOne := func(requestID string) int64 {
		historyID, saveErr := store.Save(SaveHistoryInput{
			RequestID: requestID,
			Engine:    EngineMySQL,
			Source:    "paste",
			SQLText:   "SELECT 1;",
			CheckResult: CheckResponse{
				RulesVersion: rulesVersion,
				CheckedAt:    time.Now().Format(time.RFC3339),
				Summary: Summary{
					StatementCount: 1,
				},
			},
		})
		if saveErr != nil {
			t.Fatalf("save err: %v", saveErr)
		}
		return historyID
	}

	id1 := saveOne("req-delete-1")
	id2 := saveOne("req-delete-2")

	deleted, err := store.DeleteByIDs([]int64{id1, id2, id2, -1})
	if err != nil {
		t.Fatalf("DeleteByIDs err: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted mismatch, got=%d want=2", deleted)
	}

	items, total, err := store.List(20, 0)
	if err != nil {
		t.Fatalf("List err: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("expected empty history after delete, total=%d len=%d", total, len(items))
	}
}

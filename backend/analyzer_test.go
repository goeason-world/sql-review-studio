package main

import (
	"strings"
	"testing"
)

func TestAnalyzeSQLDetectsRiskRules(t *testing.T) {
	sql := `UPDATE users SET status='off';
DELETE FROM orders;
SELECT * FROM users;`

	res := AnalyzeSQL(sql)
	if res.Summary.ErrorCount < 2 {
		t.Fatalf("expected at least 2 errors, got %d", res.Summary.ErrorCount)
	}
	if res.Summary.WarningCount < 1 {
		t.Fatalf("expected at least 1 warning, got %d", res.Summary.WarningCount)
	}
}

func TestAnalyzeSQLDisabledRule(t *testing.T) {
	sql := `DELETE FROM orders;`
	res := AnalyzeSQLWithOptions(sql, AnalyzeOptions{
		DisabledRules: map[string]struct{}{"delete_without_where": {}},
	})

	for _, issue := range res.Issues {
		if issue.Rule == "delete_without_where" {
			t.Fatalf("delete_without_where should be disabled")
		}
	}
}

func TestSplitSQLStatementsIgnoresQuotedSemicolon(t *testing.T) {
	sql := `INSERT INTO t(v) VALUES('a;b');
SELECT id FROM t LIMIT 1;`

	items := splitSQLStatements(sql)
	if len(items) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(items))
	}
}

func TestSplitSQLStatementsSupportsRoutineDelimiter(t *testing.T) {
	sql := `DELIMITER $$
CREATE PROCEDURE p_demo()
BEGIN
  SELECT 1;
  SELECT 2;
END$$
DELIMITER ;
SELECT 3;`

	items := splitSQLStatements(sql)
	if len(items) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(items))
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(items[0])), "CREATE PROCEDURE") {
		t.Fatalf("first statement should be procedure, got: %s", items[0])
	}
}

func TestAnalyzeSQLWarnsMissingStatementTerminator(t *testing.T) {
	sql := `UPDATE users SET status='inactive'
DELETE FROM users WHERE id = 10`

	res := AnalyzeSQL(sql)
	if !hasRule(res.Issues, "missing_statement_terminator") {
		t.Fatalf("expected missing_statement_terminator rule")
	}
	if !hasRuleWithLevel(res.Issues, "missing_statement_terminator", LevelError) {
		t.Fatalf("missing_statement_terminator should be error")
	}
	if !hasRuleStatementContains(res.Issues, "missing_statement_terminator", "UPDATE users SET status='inactive'") {
		t.Fatalf("missing_statement_terminator should include problematic SQL statement")
	}
}

func TestAnalyzeSQLWarnsMissingTerminatorSingleSelect(t *testing.T) {
	sql := `SELECT * FROM users WHERE name LIKE '%tom%'`

	res := AnalyzeSQL(sql)
	if !hasRule(res.Issues, "missing_statement_terminator") {
		t.Fatalf("expected missing_statement_terminator for single statement")
	}
	if !hasRuleWithLevel(res.Issues, "missing_statement_terminator", LevelError) {
		t.Fatalf("single statement missing terminator should be error")
	}
	if !hasRuleStatementContains(res.Issues, "missing_statement_terminator", "SELECT * FROM users WHERE name LIKE '%tom%'") {
		t.Fatalf("missing_statement_terminator should include the problematic single statement")
	}
}

func TestAnalyzeSQLWarnsMissingTerminatorWhenPartiallySeparated(t *testing.T) {
	sql := `UPDATE users SET status='inactive'
DELETE FROM orders;
SELECT * FROM users WHERE name LIKE '%tom%';`

	res := AnalyzeSQL(sql)
	if !hasRule(res.Issues, "missing_statement_terminator") {
		t.Fatalf("expected missing_statement_terminator for partially separated statements")
	}
	if res.Summary.ErrorCount < 1 {
		t.Fatalf("expected error for missing terminator, got summary: %+v", res.Summary)
	}

	issue := getIssueByRule(res.Issues, "missing_statement_terminator")
	if issue == nil {
		t.Fatalf("missing_statement_terminator issue not found")
	}
	if !strings.Contains(issue.Statement, "UPDATE users SET status='inactive'") {
		t.Fatalf("expected suggested statement to include problematic UPDATE, got: %s", issue.Statement)
	}
	if strings.Contains(issue.Statement, "DELETE FROM orders;") {
		t.Fatalf("suggested statement should not include already-terminated DELETE, got: %s", issue.Statement)
	}
	if strings.Contains(issue.Message, "检测到问题语句：") {
		t.Fatalf("issue message should not repeat SQL snippet, got: %s", issue.Message)
	}
}

func TestAnalyzeSQLDetectsMissingTerminatorBeforeCommit(t *testing.T) {
	sql := `START TRANSACTION;
UPDATE users SET status='inactive' WHERE last_login < DATE_SUB(NOW(), INTERVAL 180 DAY);
DELETE FROM orders WHERE created_at < DATE_SUB(NOW(), INTERVAL 365 DAY);
SELECT * FROM users WHERE name LIKE '%tom%'
COMMIT;`

	res := AnalyzeSQL(sql)
	if !hasRule(res.Issues, "missing_statement_terminator") {
		t.Fatalf("expected missing_statement_terminator before COMMIT")
	}

	issue := getIssueByRule(res.Issues, "missing_statement_terminator")
	if issue == nil {
		t.Fatalf("missing_statement_terminator issue not found")
	}
	if issue.StatementIndex != 4 {
		t.Fatalf("expected missing statement index 4, got: %d", issue.StatementIndex)
	}
	if !strings.Contains(issue.Statement, "SELECT * FROM users WHERE name LIKE '%tom%'") {
		t.Fatalf("expected missing statement snippet to contain SELECT, got: %s", issue.Statement)
	}
	if strings.Contains(issue.Statement, "COMMIT;") {
		t.Fatalf("missing statement snippet should not include COMMIT, got: %s", issue.Statement)
	}
}

func TestAnalyzeSQLDetectsFullwidthTerminator(t *testing.T) {
	sql := `BEGIN;
UPDATE users SET status = 'inactive' WHERE last_login_at < now() - interval '180 days'；
DELETE FROM orders WHERE created_at < now() - interval '365 days';
SELECT * FROM users WHERE name ILIKE '%tom%';
COMMIT;`

	res := AnalyzeSQL(sql)
	if !hasRule(res.Issues, "fullwidth_statement_terminator") {
		t.Fatalf("expected fullwidth_statement_terminator")
	}
	issue := getIssueByRule(res.Issues, "fullwidth_statement_terminator")
	if issue == nil {
		t.Fatalf("fullwidth_statement_terminator issue not found")
	}
	if issue.StatementIndex != 2 {
		t.Fatalf("expected statement index 2, got: %d", issue.StatementIndex)
	}
	if !strings.Contains(issue.Message, "中文结束符") {
		t.Fatalf("expected fullwidth hint in message, got: %s", issue.Message)
	}
	if hasRule(res.Issues, "missing_statement_terminator") {
		t.Fatalf("should not report missing_statement_terminator for fullwidth terminator case")
	}
}

func TestAnalyzeSQLDoesNotWarnMissingTerminatorForCTE(t *testing.T) {
	sql := `WITH cte AS (
  SELECT id, name FROM users
)
SELECT * FROM cte;`

	res := AnalyzeSQL(sql)
	if hasRule(res.Issues, "missing_statement_terminator") {
		t.Fatalf("cte statement should not trigger missing_statement_terminator")
	}
}

func TestAnalyzeSQLRoutineDoesNotWarnMissingStatementTerminator(t *testing.T) {
	sql := `DELIMITER $$
CREATE PROCEDURE p_demo()
BEGIN
  UPDATE users SET status='inactive'
  DELETE FROM users WHERE id = 10;
END$$
DELIMITER ;`

	res := AnalyzeSQL(sql)
	if hasRule(res.Issues, "missing_statement_terminator") {
		t.Fatalf("routine script should not trigger missing_statement_terminator")
	}
	if !hasRule(res.Issues, "routine_definition_detected") {
		t.Fatalf("expected routine_definition_detected rule")
	}
}

func TestRulesCatalogNotEmpty(t *testing.T) {
	rules := BuiltInRules()
	if len(rules) < 5 {
		t.Fatalf("expected built-in rules, got %d", len(rules))
	}
}

func hasRule(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Rule == code {
			return true
		}
	}
	return false
}

func hasRuleWithLevel(issues []Issue, code string, level IssueLevel) bool {
	for _, issue := range issues {
		if issue.Rule == code && issue.Level == level {
			return true
		}
	}
	return false
}

func hasRuleStatementContains(issues []Issue, code, keyword string) bool {
	for _, issue := range issues {
		if issue.Rule == code && strings.Contains(issue.Statement, keyword) {
			return true
		}
	}
	return false
}
func getIssueByRule(issues []Issue, code string) *Issue {
	for i := range issues {
		if issues[i].Rule == code {
			return &issues[i]
		}
	}
	return nil
}

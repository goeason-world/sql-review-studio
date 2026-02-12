package main

import (
	"strings"
	"testing"
)

func TestNormalizeEngine(t *testing.T) {
	if NormalizeEngine("pg") != EnginePostgreSQL {
		t.Fatalf("pg alias should map to postgresql")
	}
	if NormalizeEngine("mongo") != EngineMongoDB {
		t.Fatalf("mongo alias should map to mongodb")
	}
	if NormalizeEngine("unknown") != EngineMySQL {
		t.Fatalf("unknown engine should fallback mysql")
	}
}

func TestRulesForEngine(t *testing.T) {
	version, rules := RulesForEngine(EnginePostgreSQL)
	if version == "" || len(rules) == 0 {
		t.Fatalf("postgres rules should not be empty")
	}

	version, rules = RulesForEngine(EngineMongoDB)
	if version == "" || len(rules) == 0 {
		t.Fatalf("mongo rules should not be empty")
	}
}

func TestAnalyzeByEngineMongoDetectsEmptyFilter(t *testing.T) {
	script := `db.users.updateMany({}, {$set: {status: "inactive"}})`
	result := AnalyzeByEngine(EngineMongoDB, script, AnalyzeOptions{})
	if result.Summary.ErrorCount < 1 {
		t.Fatalf("expected mongo error issue, got summary: %+v", result.Summary)
	}
}

func TestAnalyzeByEngineMongoDetectsMissingTerminator(t *testing.T) {
	script := `db.users.updateMany({ status: "pending" }, { $set: { status: "inactive" } });
db.orders.deleteMany({ createdAt: { $lt: new Date("2025-01-01") } })
db.users.find({ name: /tom/i });`

	result := AnalyzeByEngine(EngineMongoDB, script, AnalyzeOptions{})
	if !hasRule(result.Issues, "mongo_missing_statement_terminator") {
		t.Fatalf("expected mongo_missing_statement_terminator, got issues: %+v", result.Issues)
	}
	if result.Summary.ErrorCount < 1 {
		t.Fatalf("expected error for missing terminator, got summary: %+v", result.Summary)
	}

	if !hasRuleStatementContains(result.Issues, "mongo_missing_statement_terminator", "db.orders.deleteMany") {
		t.Fatalf("mongo_missing_statement_terminator should include problematic deleteMany statement")
	}
	if hasRuleStatementContains(result.Issues, "mongo_missing_statement_terminator", "db.users.updateMany") {
		t.Fatalf("mongo_missing_statement_terminator should not include already-terminated updateMany statement")
	}
	issue := getIssueByRule(result.Issues, "mongo_missing_statement_terminator")
	if issue == nil || strings.Contains(issue.Message, "检测到问题语句：") {
		t.Fatalf("mongo missing terminator message should not repeat SQL snippet, got: %+v", issue)
	}
}

func TestAnalyzeByEngineMongoDetectsFullwidthTerminator(t *testing.T) {
	script := `db.users.updateMany({ status: "pending" }, { $set: { status: "inactive" } })；
db.orders.deleteMany({ createdAt: { $lt: new Date("2025-01-01") } });
db.users.find({ name: /tom/i });`

	result := AnalyzeByEngine(EngineMongoDB, script, AnalyzeOptions{})
	if !hasRule(result.Issues, "fullwidth_statement_terminator") {
		t.Fatalf("expected fullwidth_statement_terminator, got issues: %+v", result.Issues)
	}
	issue := getIssueByRule(result.Issues, "fullwidth_statement_terminator")
	if issue == nil {
		t.Fatalf("fullwidth_statement_terminator issue not found")
	}
	if issue.StatementIndex != 1 {
		t.Fatalf("expected fullwidth statement index 1, got: %d", issue.StatementIndex)
	}
	if hasRule(result.Issues, "mongo_missing_statement_terminator") {
		t.Fatalf("should not report mongo_missing_statement_terminator for fullwidth terminator case")
	}
}

func TestAnalyzeByEngineMongoMultilineOperationNoFalseMissingTerminator(t *testing.T) {
	script := `db.users.updateMany(
  { status: "pending" },
  { $set: { status: "inactive" } }
);
db.users.find({ status: "inactive" }).limit(10);`

	result := AnalyzeByEngine(EngineMongoDB, script, AnalyzeOptions{})
	if hasRule(result.Issues, "mongo_missing_statement_terminator") {
		t.Fatalf("unexpected mongo_missing_statement_terminator, got issues: %+v", result.Issues)
	}
}

func TestAnalyzeByEnginePostgresDetectsUnsafeUpdate(t *testing.T) {
	script := `UPDATE users SET status='inactive';`
	result := AnalyzeByEngine(EnginePostgreSQL, script, AnalyzeOptions{})
	if result.Summary.ErrorCount < 1 {
		t.Fatalf("expected postgres error issue, got summary: %+v", result.Summary)
	}
}

func TestAnalyzeByEnginePostgresDetectsMissingTerminatorSingleSelect(t *testing.T) {
	script := `SELECT * FROM users WHERE name ILIKE '%tom%'`

	result := AnalyzeByEngine(EnginePostgreSQL, script, AnalyzeOptions{})
	if !hasRule(result.Issues, "missing_statement_terminator") {
		t.Fatalf("expected missing_statement_terminator for single postgres statement")
	}
	if result.Summary.ErrorCount < 1 {
		t.Fatalf("expected error for single postgres statement missing terminator, got summary: %+v", result.Summary)
	}
	if !hasRuleStatementContains(result.Issues, "missing_statement_terminator", "SELECT * FROM users WHERE name ILIKE '%tom%'") {
		t.Fatalf("missing_statement_terminator should include the problematic single postgres statement")
	}
}

func TestAnalyzeByEnginePostgresDetectsMissingTerminatorBeforeCommit(t *testing.T) {
	script := `BEGIN;
UPDATE users SET status = 'inactive' WHERE last_login_at < now() - interval '180 days';
DELETE FROM orders WHERE created_at < now() - interval '365 days';
SELECT * FROM users WHERE name ILIKE '%tom%'
COMMIT;`

	result := AnalyzeByEngine(EnginePostgreSQL, script, AnalyzeOptions{})
	if !hasRule(result.Issues, "missing_statement_terminator") {
		t.Fatalf("expected missing_statement_terminator before COMMIT, got issues: %+v", result.Issues)
	}

	issue := getIssueByRule(result.Issues, "missing_statement_terminator")
	if issue == nil {
		t.Fatalf("missing_statement_terminator issue not found")
	}
	if issue.StatementIndex != 4 {
		t.Fatalf("expected missing statement index 4, got: %d", issue.StatementIndex)
	}
	if !hasRuleStatementContains(result.Issues, "missing_statement_terminator", "SELECT * FROM users WHERE name ILIKE '%tom%'") {
		t.Fatalf("expected missing statement snippet to contain SELECT")
	}
	if strings.Contains(issue.Statement, "COMMIT;") {
		t.Fatalf("missing statement snippet should not include COMMIT, got: %s", issue.Statement)
	}
}

func TestAnalyzeByEnginePostgresDetectsFullwidthTerminator(t *testing.T) {
	script := `BEGIN;
UPDATE users SET status = 'inactive' WHERE last_login_at < now() - interval '180 days'；
DELETE FROM orders WHERE created_at < now() - interval '365 days';
SELECT * FROM users WHERE name ILIKE '%tom%';
COMMIT;`

	result := AnalyzeByEngine(EnginePostgreSQL, script, AnalyzeOptions{})
	if !hasRule(result.Issues, "fullwidth_statement_terminator") {
		t.Fatalf("expected fullwidth_statement_terminator, got issues: %+v", result.Issues)
	}
	issue := getIssueByRule(result.Issues, "fullwidth_statement_terminator")
	if issue == nil {
		t.Fatalf("fullwidth_statement_terminator issue not found")
	}
	if issue.StatementIndex != 2 {
		t.Fatalf("expected statement index 2, got: %d", issue.StatementIndex)
	}
	if hasRule(result.Issues, "missing_statement_terminator") {
		t.Fatalf("should not report missing_statement_terminator for fullwidth terminator case")
	}
}

func TestAnalyzeByEnginePostgresDetectsMissingTerminator(t *testing.T) {
	script := `BEGIN;
UPDATE users SET status = 'inactive' WHERE last_login_at < now() - interval
  '180 days'
DELETE FROM orders WHERE created_at < now() - interval '365 days';
SELECT * FROM users WHERE name ILIKE '%tom%';
COMMIT;`

	result := AnalyzeByEngine(EnginePostgreSQL, script, AnalyzeOptions{})
	if !hasRule(result.Issues, "missing_statement_terminator") {
		t.Fatalf("expected missing_statement_terminator, got issues: %+v", result.Issues)
	}
	if result.Summary.ErrorCount < 1 {
		t.Fatalf("expected error for missing terminator, got summary: %+v", result.Summary)
	}
	if !hasRuleStatementContains(result.Issues, "missing_statement_terminator", "UPDATE users SET status = 'inactive' WHERE last_login_at < now() - interval\n'180 days'") {
		t.Fatalf("missing_statement_terminator should include problematic UPDATE statement")
	}
	if hasRuleStatementContains(result.Issues, "missing_statement_terminator", "DELETE FROM orders WHERE created_at < now() - interval '365 days';") {
		t.Fatalf("missing_statement_terminator should not include already-terminated DELETE statement")
	}
	issue := getIssueByRule(result.Issues, "missing_statement_terminator")
	if issue == nil || strings.Contains(issue.Message, "检测到问题语句：") {
		t.Fatalf("postgres missing terminator message should not repeat SQL snippet, got: %+v", issue)
	}
}

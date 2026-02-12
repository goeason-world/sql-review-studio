package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type DBEngine string

const (
	EngineMySQL      DBEngine = "mysql"
	EnginePostgreSQL DBEngine = "postgresql"
	EngineMongoDB    DBEngine = "mongodb"
)

const (
	postgresRulesVersion = "pg-v0.1"
	mongoRulesVersion    = "mongo-v0.1"
)

var (
	rePostgresLikeLeadWild = regexp.MustCompile(`(?is)(LIKE|ILIKE)\s+['"]%[^'"]*['"]`)
)

func SupportedEngines() []DBEngine {
	return []DBEngine{EngineMySQL, EnginePostgreSQL, EngineMongoDB}
}

func NormalizeEngine(raw string) DBEngine {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	switch trimmed {
	case "pg", "postgres", "postgresql":
		return EnginePostgreSQL
	case "mongo", "mongodb":
		return EngineMongoDB
	case "mysql", "":
		return EngineMySQL
	default:
		return EngineMySQL
	}
}

func RulesForEngine(engine DBEngine) (string, []RuleDefinition) {
	switch NormalizeEngine(string(engine)) {
	case EnginePostgreSQL:
		return postgresRulesVersion, BuiltInPostgresRules()
	case EngineMongoDB:
		return mongoRulesVersion, BuiltInMongoRules()
	default:
		return rulesVersion, BuiltInRules()
	}
}

func AnalyzeByEngine(engine DBEngine, content string, options AnalyzeOptions) CheckResponse {
	switch NormalizeEngine(string(engine)) {
	case EnginePostgreSQL:
		return AnalyzePostgresWithOptions(content, options)
	case EngineMongoDB:
		return AnalyzeMongoWithOptions(content, options)
	default:
		return AnalyzeSQLWithOptions(content, options)
	}
}

func BuiltInPostgresRules() []RuleDefinition {
	return []RuleDefinition{
		{Code: "empty_input", Level: LevelError, Category: "输入校验", Description: "输入为空"},
		{Code: "too_many_statements", Level: LevelWarning, Category: "变更规模", Description: "语句数过多，建议拆批执行"},
		{Code: "missing_statement_terminator", Level: LevelError, Category: "脚本语法", Description: "多条 SQL 场景疑似缺少结束符"},
		{Code: "fullwidth_statement_terminator", Level: LevelError, Category: "脚本语法", Description: "检测到中文结束符（；）"},
		{Code: "pg_dangerous_drop", Level: LevelError, Category: "高危DDL", Description: "检测到 DROP 高危对象删除"},
		{Code: "pg_dangerous_truncate", Level: LevelError, Category: "高危DDL", Description: "检测到 TRUNCATE 全表清理"},
		{Code: "pg_update_without_where", Level: LevelError, Category: "DML安全", Description: "UPDATE 无 WHERE"},
		{Code: "pg_delete_without_where", Level: LevelError, Category: "DML安全", Description: "DELETE 无 WHERE"},
		{Code: "pg_select_star", Level: LevelWarning, Category: "查询规范", Description: "SELECT * 可维护性与性能风险"},
		{Code: "pg_select_without_limit", Level: LevelInfo, Category: "查询规范", Description: "SELECT 未设置 LIMIT"},
		{Code: "pg_like_leading_wildcard", Level: LevelWarning, Category: "查询性能", Description: "LIKE/ILIKE 前导 % 可能导致索引失效"},
		{Code: "pg_create_index_without_concurrently", Level: LevelWarning, Category: "DDL并发", Description: "CREATE INDEX 未使用 CONCURRENTLY"},
		{Code: "risky_writes_without_transaction", Level: LevelWarning, Category: "事务一致性", Description: "多条写语句未显式事务包裹"},
	}
}

func AnalyzePostgresWithOptions(content string, options AnalyzeOptions) CheckResponse {
	result := CheckResponse{
		RulesVersion: postgresRulesVersion,
		CheckedAt:    time.Now().Format(time.RFC3339),
		Advice:       make([]string, 0, 3),
	}

	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		result.Issues = append(result.Issues, Issue{
			StatementIndex: 0,
			Level:          LevelError,
			Rule:           "empty_input",
			Message:        "SQL 内容为空",
			Suggestion:     "请上传 SQL 文件或粘贴 SQL 语句后再检查",
		})
		result.Summary = summarizeIssues(0, result.Issues)
		result.Advice = append(result.Advice, "请输入待审核 SQL 后重试")
		return filterDisabledRules(result, options)
	}

	statements := splitSQLStatements(content)
	issues := make([]Issue, 0)
	containsRiskWrite := false
	hasBegin := false
	hasCommit := false

	fullwidthTerminatorStatements := detectFullwidthTerminatorStatements(content, false)
	if len(fullwidthTerminatorStatements) > 0 {
		issues = append(issues, Issue{
			StatementIndex: fullwidthTerminatorStatements[0].Index,
			Level:          LevelError,
			Rule:           "fullwidth_statement_terminator",
			Message:        buildFullwidthTerminatorIssueMessage(fullwidthTerminatorStatements),
			Suggestion:     "请将中文结束符（；）替换为英文半角分号（;），避免解析歧义",
			Statement:      buildMissingTerminatorStatementSnippet(fullwidthTerminatorStatements),
		})
	}

	missingTerminatorStatements := detectMissingTerminatorStatements(content, statements, false)
	missingTerminatorStatements = excludeMissingTerminatorStatements(missingTerminatorStatements, fullwidthTerminatorStatements)
	if len(missingTerminatorStatements) > 0 {
		issues = append(issues, Issue{
			StatementIndex: missingTerminatorStatements[0].Index,
			Level:          LevelError,
			Rule:           "missing_statement_terminator",
			Message:        buildMissingTerminatorIssueMessage(missingTerminatorStatements),
			Suggestion:     "建议为每条语句补齐结束符，避免自动审查/执行阶段误拆分",
			Statement:      buildMissingTerminatorStatementSnippet(missingTerminatorStatements),
		})
	}

	if len(statements) > 80 {
		issues = append(issues, Issue{
			StatementIndex: 0,
			Level:          LevelWarning,
			Rule:           "too_many_statements",
			Message:        fmt.Sprintf("SQL 语句数量较多（%d 条）", len(statements)),
			Suggestion:     "建议分批审核与执行，降低发布风险",
		})
	}

	for i, st := range statements {
		stmt := strings.TrimSpace(st)
		if stmt == "" {
			continue
		}
		upper := strings.ToUpper(stmt)
		upperTrim := strings.TrimSpace(upper)

		if reRiskWrite.MatchString(upperTrim) {
			containsRiskWrite = true
		}
		if reBeginTx.MatchString(upperTrim) {
			hasBegin = true
		}
		if reCommitTx.MatchString(upperTrim) {
			hasCommit = true
		}

		if reDropObj.MatchString(upperTrim) {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelError, Rule: "pg_dangerous_drop", Message: "检测到 DROP 高风险语句", Suggestion: "生产建议禁用 DROP；确需执行请先备份并审批", Statement: stmt})
		}
		if reTruncate.MatchString(upperTrim) {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelError, Rule: "pg_dangerous_truncate", Message: "检测到 TRUNCATE 语句", Suggestion: "TRUNCATE 风险高，请确认恢复方案", Statement: stmt})
		}
		if reUpdateNoWhere.MatchString(upperTrim) && !strings.Contains(upperTrim, " WHERE ") {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelError, Rule: "pg_update_without_where", Message: "UPDATE 缺少 WHERE 条件", Suggestion: "请添加精确 WHERE 条件，避免全表更新", Statement: stmt})
		}
		if reDeleteNoWhere.MatchString(upperTrim) && !strings.Contains(upperTrim, " WHERE ") {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelError, Rule: "pg_delete_without_where", Message: "DELETE 缺少 WHERE 条件", Suggestion: "请添加 WHERE 条件，或改为分批删除", Statement: stmt})
		}
		if reSelectStar.MatchString(upperTrim) {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelWarning, Rule: "pg_select_star", Message: "SELECT * 可能带来性能和兼容风险", Suggestion: "建议显式列出字段", Statement: stmt})
		}
		if reSelect.MatchString(upperTrim) && !reLimit.MatchString(upperTrim) {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelInfo, Rule: "pg_select_without_limit", Message: "SELECT 未检测到 LIMIT", Suggestion: "在线查询建议补充 LIMIT", Statement: stmt})
		}
		if rePostgresLikeLeadWild.MatchString(stmt) {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelWarning, Rule: "pg_like_leading_wildcard", Message: "LIKE/ILIKE 前导通配符可能导致索引失效", Suggestion: "可考虑全文检索或改写匹配策略", Statement: stmt})
		}
		if strings.HasPrefix(upperTrim, "CREATE INDEX") && !strings.Contains(upperTrim, " CONCURRENTLY ") {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelWarning, Rule: "pg_create_index_without_concurrently", Message: "CREATE INDEX 未使用 CONCURRENTLY", Suggestion: "在线变更建议使用 CONCURRENTLY 以降低锁影响", Statement: stmt})
		}
	}

	if containsRiskWrite && len(statements) > 1 && (!hasBegin || !hasCommit) {
		issues = append(issues, Issue{StatementIndex: 0, Level: LevelWarning, Rule: "risky_writes_without_transaction", Message: "检测到多条写语句但未发现完整事务边界", Suggestion: "建议使用 BEGIN/COMMIT 包裹，保证一致性"})
	}

	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].StatementIndex == issues[j].StatementIndex {
			return severityWeight(issues[i].Level) > severityWeight(issues[j].Level)
		}
		return issues[i].StatementIndex < issues[j].StatementIndex
	})

	result.Issues = issues
	result = filterDisabledRules(result, options)
	result.Summary = summarizeIssues(len(statements), result.Issues)
	result.Advice = buildAdvice(result.Summary)
	return result
}

func BuiltInMongoRules() []RuleDefinition {
	return []RuleDefinition{
		{Code: "empty_input", Level: LevelError, Category: "输入校验", Description: "输入为空"},
		{Code: "mongo_update_many_without_filter", Level: LevelError, Category: "写入安全", Description: "updateMany 使用空过滤条件"},
		{Code: "mongo_delete_many_without_filter", Level: LevelError, Category: "写入安全", Description: "deleteMany 使用空过滤条件"},
		{Code: "mongo_missing_statement_terminator", Level: LevelError, Category: "脚本语法", Description: "多条 Mongo 语句疑似缺少结束符 ;"},
		{Code: "fullwidth_statement_terminator", Level: LevelError, Category: "脚本语法", Description: "检测到中文结束符（；）"},
		{Code: "mongo_find_without_limit", Level: LevelInfo, Category: "查询规范", Description: "find 查询未设置 limit"},
		{Code: "mongo_where_operator", Level: LevelWarning, Category: "查询安全", Description: "使用 $where 可能导致执行风险"},
		{Code: "mongo_aggregate_out_merge", Level: LevelWarning, Category: "数据流向", Description: "聚合中使用 $out/$merge 需审慎"},
	}
}

func AnalyzeMongoWithOptions(content string, options AnalyzeOptions) CheckResponse {
	result := CheckResponse{
		RulesVersion: mongoRulesVersion,
		CheckedAt:    time.Now().Format(time.RFC3339),
		Advice:       make([]string, 0, 3),
	}

	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		result.Issues = append(result.Issues, Issue{
			StatementIndex: 0,
			Level:          LevelError,
			Rule:           "empty_input",
			Message:        "脚本内容为空",
			Suggestion:     "请上传脚本或粘贴 Mongo 操作语句后重试",
		})
		result.Summary = summarizeIssues(0, result.Issues)
		result.Advice = append(result.Advice, "请输入待审核脚本后重试")
		return filterDisabledRules(result, options)
	}

	mongoOps := parseMongoOperations(content)
	issues := make([]Issue, 0)

	fullwidthItems := make([]missingTerminatorStatement, 0)
	for i, op := range mongoOps {
		if !op.FullwidthTerminator {
			continue
		}
		statement := normalizeStatementWithTerminator(op.Text)
		if statement == "" {
			continue
		}
		fullwidthItems = append(fullwidthItems, missingTerminatorStatement{Index: i + 1, Statement: statement})
	}
	if len(fullwidthItems) > 0 {
		issues = append(issues, Issue{
			StatementIndex: fullwidthItems[0].Index,
			Level:          LevelError,
			Rule:           "fullwidth_statement_terminator",
			Message:        buildFullwidthTerminatorIssueMessageWithSubject(fullwidthItems, "Mongo"),
			Suggestion:     "请将中文结束符（；）替换为英文半角分号（;），避免解析歧义",
			Statement:      buildMissingTerminatorStatementSnippet(fullwidthItems),
		})
	}

	if len(mongoOps) > 1 {
		missingItems := make([]missingTerminatorStatement, 0)
		for i, op := range mongoOps {
			if op.Terminated {
				continue
			}
			statement := normalizeStatementWithTerminator(op.Text)
			if statement == "" {
				continue
			}
			missingItems = append(missingItems, missingTerminatorStatement{Index: i + 1, Statement: statement})
		}
		if len(missingItems) > 0 {
			issues = append(issues, Issue{
				StatementIndex: missingItems[0].Index,
				Level:          LevelError,
				Rule:           "mongo_missing_statement_terminator",
				Message:        buildMissingTerminatorIssueMessageWithSubject(missingItems, "Mongo"),
				Suggestion:     "建议为每条 Mongo 语句补齐结束符 ;，避免脚本解析或执行阶段误拆分",
				Statement:      buildMissingTerminatorStatementSnippet(missingItems),
			})
		}
	}

	for i, op := range mongoOps {
		compact := compactScriptText(strings.ToLower(op.Text))
		if compact == "" {
			continue
		}

		if strings.Contains(compact, ".updatemany({},") {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelError, Rule: "mongo_update_many_without_filter", Message: "updateMany 使用空过滤条件，可能全量更新", Suggestion: "请补充明确过滤条件", Statement: strings.TrimSpace(op.Text)})
		}
		if strings.Contains(compact, ".deletemany({})") {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelError, Rule: "mongo_delete_many_without_filter", Message: "deleteMany 使用空过滤条件，可能全量删除", Suggestion: "请补充明确过滤条件", Statement: strings.TrimSpace(op.Text)})
		}
		if strings.Contains(compact, ".find(") && !strings.Contains(compact, ".limit(") {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelInfo, Rule: "mongo_find_without_limit", Message: "find 查询未设置 limit", Suggestion: "在线查询建议加 limit，避免返回超大结果集", Statement: strings.TrimSpace(op.Text)})
		}
		if strings.Contains(compact, "$where") {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelWarning, Rule: "mongo_where_operator", Message: "检测到 $where，可能引入执行与安全风险", Suggestion: "优先使用结构化查询条件，避免 JS 表达式", Statement: strings.TrimSpace(op.Text)})
		}
		if strings.Contains(compact, ".aggregate(") && (strings.Contains(compact, "$out") || strings.Contains(compact, "$merge")) {
			issues = append(issues, Issue{StatementIndex: i + 1, Level: LevelWarning, Rule: "mongo_aggregate_out_merge", Message: "聚合中使用 $out/$merge，存在数据覆盖风险", Suggestion: "请确认目标集合、幂等策略与回滚预案", Statement: strings.TrimSpace(op.Text)})
		}
	}

	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].StatementIndex == issues[j].StatementIndex {
			return severityWeight(issues[i].Level) > severityWeight(issues[j].Level)
		}
		return issues[i].StatementIndex < issues[j].StatementIndex
	})

	result.Issues = issues
	result = filterDisabledRules(result, options)
	result.Summary = summarizeIssues(len(mongoOps), result.Issues)
	result.Advice = buildAdvice(result.Summary)
	return result
}

func filterDisabledRules(result CheckResponse, options AnalyzeOptions) CheckResponse {
	if options.DisabledRules == nil || len(options.DisabledRules) == 0 || len(result.Issues) == 0 {
		return result
	}

	filtered := make([]Issue, 0, len(result.Issues))
	for _, issue := range result.Issues {
		if _, found := options.DisabledRules[issue.Rule]; found {
			continue
		}
		filtered = append(filtered, issue)
	}
	result.Issues = filtered
	return result
}

func summarizeIssues(statementCount int, issues []Issue) Summary {
	summary := Summary{StatementCount: statementCount}
	for _, issue := range issues {
		switch issue.Level {
		case LevelError:
			summary.ErrorCount++
		case LevelWarning:
			summary.WarningCount++
		case LevelInfo:
			summary.InfoCount++
		}
	}

	return summary
}

func buildAdvice(summary Summary) []string {
	advice := make([]string, 0, 3)
	if summary.ErrorCount > 0 {
		advice = append(advice, "存在高风险语句，建议阻断自动执行并人工复核")
	}
	if summary.WarningCount > 0 {
		advice = append(advice, "存在中风险项，建议补充执行计划与回滚预案")
	}
	if summary.ErrorCount == 0 && summary.WarningCount == 0 {
		advice = append(advice, "未发现明显高风险模式，仍建议做一次业务语义抽样复查")
	}
	return advice
}

type mongoOperation struct {
	Text                string
	Terminated          bool
	FullwidthTerminator bool
}

func parseMongoOperations(content string) []mongoOperation {
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	runes := []rune(normalized)
	items := make([]mongoOperation, 0)
	var builder strings.Builder

	inSingleQuote := false
	inDoubleQuote := false
	inBacktick := false
	inLineComment := false
	inBlockComment := false
	parenDepth := 0
	braceDepth := 0
	bracketDepth := 0

	flush := func(terminated bool, fullwidthTerminator bool) {
		statement := strings.TrimSpace(builder.String())
		builder.Reset()
		if statement == "" {
			return
		}
		items = append(items, mongoOperation{Text: statement, Terminated: terminated, FullwidthTerminator: fullwidthTerminator})
	}

	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				if !inSingleQuote && !inDoubleQuote && !inBacktick && parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 {
					flush(false, false)
				}
			}
			continue
		}

		if inBlockComment {
			if ch == '*' && next == '/' {
				inBlockComment = false
				i++
			}
			continue
		}

		if !inSingleQuote && !inDoubleQuote && !inBacktick {
			if ch == '/' && next == '/' {
				inLineComment = true
				i++
				continue
			}
			if ch == '/' && next == '*' {
				inBlockComment = true
				i++
				continue
			}
		}

		if ch == '\'' && !inDoubleQuote && !inBacktick {
			if !isEscapedByBackslash(runes, i) {
				inSingleQuote = !inSingleQuote
			}
		}
		if ch == '"' && !inSingleQuote && !inBacktick {
			if !isEscapedByBackslash(runes, i) {
				inDoubleQuote = !inDoubleQuote
			}
		}
		if ch == '`' && !inSingleQuote && !inDoubleQuote {
			inBacktick = !inBacktick
		}

		if !inSingleQuote && !inDoubleQuote && !inBacktick {
			if isFullwidthSemicolon(ch) && parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 {
				flush(true, true)
				continue
			}

			switch ch {
			case '(':
				parenDepth++
			case ')':
				if parenDepth > 0 {
					parenDepth--
				}
			case '{':
				braceDepth++
			case '}':
				if braceDepth > 0 {
					braceDepth--
				}
			case '[':
				bracketDepth++
			case ']':
				if bracketDepth > 0 {
					bracketDepth--
				}
			}

			if ch == ';' && parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 {
				flush(true, false)
				continue
			}
			if ch == '\n' && parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 {
				flush(false, false)
				continue
			}
		}

		builder.WriteRune(ch)
	}

	flush(false, false)
	return items
}

func splitMongoOperations(content string) []string {
	ops := parseMongoOperations(content)
	items := make([]string, 0, len(ops))
	for _, op := range ops {
		if strings.TrimSpace(op.Text) == "" {
			continue
		}
		items = append(items, op.Text)
	}
	if len(items) == 0 {
		return []string{strings.TrimSpace(content)}
	}
	return items
}

func compactScriptText(input string) string {
	replacer := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "")
	return replacer.Replace(strings.TrimSpace(input))
}

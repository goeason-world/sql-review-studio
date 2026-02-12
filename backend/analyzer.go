package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type IssueLevel string

const (
	LevelError   IssueLevel = "error"
	LevelWarning IssueLevel = "warning"
	LevelInfo    IssueLevel = "info"
)

const rulesVersion = "v1.3"

type AnalyzeOptions struct {
	DisabledRules map[string]struct{}
}

type RuleDefinition struct {
	Code        string     `json:"code"`
	Level       IssueLevel `json:"level"`
	Description string     `json:"description"`
	Category    string     `json:"category"`
}

type Issue struct {
	StatementIndex int        `json:"statementIndex"`
	Level          IssueLevel `json:"level"`
	Rule           string     `json:"rule"`
	Message        string     `json:"message"`
	Suggestion     string     `json:"suggestion"`
	Statement      string     `json:"statement"`
}

type Summary struct {
	StatementCount int `json:"statementCount"`
	ErrorCount     int `json:"errorCount"`
	WarningCount   int `json:"warningCount"`
	InfoCount      int `json:"infoCount"`
}

type CheckResponse struct {
	RulesVersion string   `json:"rulesVersion"`
	CheckedAt    string   `json:"checkedAt"`
	Summary      Summary  `json:"summary"`
	Issues       []Issue  `json:"issues"`
	Advice       []string `json:"advice"`
}

var (
	reUpdateNoWhere      = regexp.MustCompile(`(?is)^\s*UPDATE\s+.+?\s+SET\s+.+$`)
	reDeleteNoWhere      = regexp.MustCompile(`(?is)^\s*DELETE\s+FROM\s+.+$`)
	reSelectStar         = regexp.MustCompile(`(?is)^\s*SELECT\s+\*\s+FROM\s+`)
	reSelect             = regexp.MustCompile(`(?is)^\s*SELECT\s+`)
	reLimit              = regexp.MustCompile(`(?is)\s+LIMIT\s+\d+`)
	reDropObj            = regexp.MustCompile(`(?is)^\s*DROP\s+(TABLE|DATABASE|VIEW|INDEX)\b`)
	reTruncate           = regexp.MustCompile(`(?is)^\s*TRUNCATE\s+TABLE\b`)
	reAlterDropCol       = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+.+\s+DROP\s+COLUMN\b`)
	reSelectIntoOut      = regexp.MustCompile(`(?is)\bINTO\s+OUTFILE\b`)
	reLikeLeadWild       = regexp.MustCompile(`(?is)LIKE\s+['"]%[^'"]*['"]`)
	reOrderByRand        = regexp.MustCompile(`(?is)ORDER\s+BY\s+RAND\s*\(`)
	reWhereOneEqOne      = regexp.MustCompile(`(?is)\bWHERE\s+1\s*=\s*1\b`)
	reInsertNoCols       = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+[\w.]+\s+VALUES\s*\(`)
	reCreateTable        = regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+`)
	reCreateIfNE         = regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+`)
	reBeginTx            = regexp.MustCompile(`(?is)^\s*(BEGIN|START\s+TRANSACTION)\b`)
	reCommitTx           = regexp.MustCompile(`(?is)^\s*COMMIT\b`)
	reRiskWrite          = regexp.MustCompile(`(?is)^\s*(UPDATE|DELETE|INSERT|ALTER|DROP|TRUNCATE)\b`)
	reRoutineDefinition  = regexp.MustCompile(`(?is)\bCREATE\s+(?:DEFINER\s*=\s*[^\s]+\s+)?(?:PROCEDURE|FUNCTION|TRIGGER|EVENT)\b`)
	reStatementStart     = regexp.MustCompile(`(?im)^\s*(SELECT|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|TRUNCATE|WITH|CALL|REPLACE|MERGE|BEGIN|START\s+TRANSACTION|COMMIT|ROLLBACK)\b`)
	reHardStatementStart = regexp.MustCompile(`(?im)^\s*(INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|TRUNCATE|CALL|REPLACE|MERGE|BEGIN|START\s+TRANSACTION|COMMIT|ROLLBACK)\b`)
)

func BuiltInRules() []RuleDefinition {
	return []RuleDefinition{
		{Code: "empty_input", Level: LevelError, Category: "输入校验", Description: "输入为空"},
		{Code: "too_many_statements", Level: LevelWarning, Category: "变更规模", Description: "语句数过多，建议拆批执行"},
		{Code: "missing_statement_terminator", Level: LevelError, Category: "脚本语法", Description: "多条 SQL 场景疑似缺少结束符"},
		{Code: "fullwidth_statement_terminator", Level: LevelError, Category: "脚本语法", Description: "检测到中文结束符（；）"},
		{Code: "routine_definition_detected", Level: LevelInfo, Category: "脚本语法", Description: "检测到存储过程/函数/触发器定义，已按 DELIMITER 语法解析"},
		{Code: "dangerous_drop", Level: LevelError, Category: "高危DDL", Description: "检测到 DROP 高危对象删除"},
		{Code: "dangerous_truncate", Level: LevelError, Category: "高危DDL", Description: "检测到 TRUNCATE 全表清理"},
		{Code: "alter_drop_column", Level: LevelWarning, Category: "DDL兼容", Description: "检测到 DROP COLUMN 结构破坏性变更"},
		{Code: "update_without_where", Level: LevelError, Category: "DML安全", Description: "UPDATE 无 WHERE"},
		{Code: "delete_without_where", Level: LevelError, Category: "DML安全", Description: "DELETE 无 WHERE"},
		{Code: "where_1_eq_1", Level: LevelWarning, Category: "条件有效性", Description: "WHERE 1=1 可能掩盖条件缺失"},
		{Code: "select_star", Level: LevelWarning, Category: "查询规范", Description: "SELECT * 可维护性与性能风险"},
		{Code: "select_without_limit", Level: LevelInfo, Category: "查询规范", Description: "SELECT 未设置 LIMIT"},
		{Code: "like_leading_wildcard", Level: LevelWarning, Category: "查询性能", Description: "LIKE 前导 % 可能导致索引失效"},
		{Code: "order_by_rand", Level: LevelWarning, Category: "查询性能", Description: "ORDER BY RAND 大表开销高"},
		{Code: "into_outfile", Level: LevelError, Category: "数据安全", Description: "INTO OUTFILE 存在数据外流风险"},
		{Code: "insert_without_column_list", Level: LevelInfo, Category: "可维护性", Description: "INSERT 未显式列清单"},
		{Code: "create_table_without_if_not_exists", Level: LevelInfo, Category: "幂等性", Description: "CREATE TABLE 未使用 IF NOT EXISTS"},
		{Code: "risky_writes_without_transaction", Level: LevelWarning, Category: "事务一致性", Description: "多条写语句未显式事务包裹"},
	}
}

func AnalyzeSQL(content string) CheckResponse {
	return AnalyzeSQLWithOptions(content, AnalyzeOptions{})
}

func AnalyzeSQLWithOptions(content string, options AnalyzeOptions) CheckResponse {
	result := CheckResponse{
		RulesVersion: rulesVersion,
		CheckedAt:    time.Now().Format(time.RFC3339),
		Advice:       make([]string, 0),
	}

	ruleEnabled := func(rule string) bool {
		if options.DisabledRules == nil {
			return true
		}
		_, found := options.DisabledRules[rule]
		return !found
	}

	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		result.Summary = Summary{StatementCount: 0, ErrorCount: 1, WarningCount: 0, InfoCount: 0}
		if ruleEnabled("empty_input") {
			result.Issues = []Issue{{
				StatementIndex: 0,
				Level:          LevelError,
				Rule:           "empty_input",
				Message:        "SQL 内容为空",
				Suggestion:     "请上传 SQL 文件或粘贴 SQL 语句后再检查",
				Statement:      "",
			}}
		} else {
			result.Issues = []Issue{}
		}
		result.Advice = append(result.Advice, "请输入待审核 SQL 后重试")
		return result
	}

	statements := splitSQLStatements(content)
	issues := make([]Issue, 0)
	addIssue := func(issue Issue) {
		if ruleEnabled(issue.Rule) {
			issues = append(issues, issue)
		}
	}

	containsRoutine := reRoutineDefinition.MatchString(content)
	if containsRoutine {
		addIssue(Issue{
			StatementIndex: 0,
			Level:          LevelInfo,
			Rule:           "routine_definition_detected",
			Message:        "检测到存储过程/函数/触发器定义",
			Suggestion:     "已按 DELIMITER 语法解析，请重点关注过程体中的写操作与权限控制",
			Statement:      "",
		})
	}

	fullwidthTerminatorStatements := detectFullwidthTerminatorStatements(content, containsRoutine)
	if len(fullwidthTerminatorStatements) > 0 {
		addIssue(Issue{
			StatementIndex: fullwidthTerminatorStatements[0].Index,
			Level:          LevelError,
			Rule:           "fullwidth_statement_terminator",
			Message:        buildFullwidthTerminatorIssueMessage(fullwidthTerminatorStatements),
			Suggestion:     "请将中文结束符（；）替换为英文半角分号（;），避免解析歧义",
			Statement:      buildMissingTerminatorStatementSnippet(fullwidthTerminatorStatements),
		})
	}

	missingTerminatorStatements := detectMissingTerminatorStatements(content, statements, containsRoutine)
	missingTerminatorStatements = excludeMissingTerminatorStatements(missingTerminatorStatements, fullwidthTerminatorStatements)
	if len(missingTerminatorStatements) > 0 {
		addIssue(Issue{
			StatementIndex: missingTerminatorStatements[0].Index,
			Level:          LevelError,
			Rule:           "missing_statement_terminator",
			Message:        buildMissingTerminatorIssueMessage(missingTerminatorStatements),
			Suggestion:     "建议为每条语句补齐结束符，避免自动审查/执行阶段误拆分",
			Statement:      buildMissingTerminatorStatementSnippet(missingTerminatorStatements),
		})
	}

	containsRiskWrite := false
	hasBegin := false
	hasCommit := false

	if len(statements) > 60 {
		addIssue(Issue{
			StatementIndex: 0,
			Level:          LevelWarning,
			Rule:           "too_many_statements",
			Message:        fmt.Sprintf("SQL 语句数量较多（%d 条）", len(statements)),
			Suggestion:     "建议按业务模块拆分后分批审核，降低误判并方便回滚",
			Statement:      "",
		})
	}

	for i, st := range statements {
		stmt := strings.TrimSpace(st)
		if stmt == "" {
			continue
		}
		upper := strings.ToUpper(stmt)

		if reRiskWrite.MatchString(upper) {
			containsRiskWrite = true
		}
		if reBeginTx.MatchString(upper) {
			hasBegin = true
		}
		if reCommitTx.MatchString(upper) {
			hasCommit = true
		}

		if reDropObj.MatchString(upper) {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelError, Rule: "dangerous_drop", Message: "检测到 DROP 高风险语句", Suggestion: "生产建议禁用 DROP；确需执行请先做完整备份并审批", Statement: stmt})
		}
		if reTruncate.MatchString(upper) {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelError, Rule: "dangerous_truncate", Message: "检测到 TRUNCATE 语句", Suggestion: "TRUNCATE 回滚代价高，请确认窗口期与恢复方案", Statement: stmt})
		}
		if reAlterDropCol.MatchString(upper) {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelWarning, Rule: "alter_drop_column", Message: "检测到 ALTER TABLE DROP COLUMN", Suggestion: "请确认上下游代码兼容，并提前完成历史数据归档", Statement: stmt})
		}
		if reUpdateNoWhere.MatchString(upper) && !strings.Contains(upper, " WHERE ") {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelError, Rule: "update_without_where", Message: "UPDATE 缺少 WHERE 条件", Suggestion: "请添加精确 WHERE 条件，避免全表更新", Statement: stmt})
		}
		if reDeleteNoWhere.MatchString(upper) && !strings.Contains(upper, " WHERE ") {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelError, Rule: "delete_without_where", Message: "DELETE 缺少 WHERE 条件", Suggestion: "请添加 WHERE 条件，或改为分批删除并保留回滚点", Statement: stmt})
		}
		if reWhereOneEqOne.MatchString(upper) {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelWarning, Rule: "where_1_eq_1", Message: "检测到 WHERE 1=1，可能导致条件失效", Suggestion: "请核查动态 SQL 拼接逻辑，避免误更新/误删除", Statement: stmt})
		}
		if reSelectStar.MatchString(upper) {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelWarning, Rule: "select_star", Message: "SELECT * 可能带来性能和兼容风险", Suggestion: "建议显式列出字段，减少 I/O 并降低结构变更影响", Statement: stmt})
		}
		if reSelect.MatchString(upper) && !reLimit.MatchString(upper) {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelInfo, Rule: "select_without_limit", Message: "SELECT 未检测到 LIMIT", Suggestion: "在线查询建议补充 LIMIT，避免大结果集拖慢库实例", Statement: stmt})
		}
		if reLikeLeadWild.MatchString(stmt) {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelWarning, Rule: "like_leading_wildcard", Message: "LIKE 前导通配符可能导致索引失效", Suggestion: "可考虑全文检索、倒排索引或改写匹配策略", Statement: stmt})
		}
		if reOrderByRand.MatchString(upper) {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelWarning, Rule: "order_by_rand", Message: "ORDER BY RAND() 在大表上性能差", Suggestion: "建议改用随机主键范围抽样或预生成随机池", Statement: stmt})
		}
		if reSelectIntoOut.MatchString(upper) {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelError, Rule: "into_outfile", Message: "检测到 INTO OUTFILE，存在数据外流风险", Suggestion: "请确认导出合规性、审计记录及数据库账号最小权限", Statement: stmt})
		}
		if reInsertNoCols.MatchString(upper) {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelInfo, Rule: "insert_without_column_list", Message: "INSERT 未显式字段列表", Suggestion: "建议 INSERT INTO t(col1,col2...) VALUES(...)，提高可维护性", Statement: stmt})
		}
		if reCreateTable.MatchString(upper) && !reCreateIfNE.MatchString(upper) {
			addIssue(Issue{StatementIndex: i + 1, Level: LevelInfo, Rule: "create_table_without_if_not_exists", Message: "CREATE TABLE 未使用 IF NOT EXISTS", Suggestion: "建议补充 IF NOT EXISTS，提升脚本重放幂等性", Statement: stmt})
		}
	}

	if containsRiskWrite && len(statements) > 1 && (!hasBegin || !hasCommit) {
		addIssue(Issue{StatementIndex: 0, Level: LevelWarning, Rule: "risky_writes_without_transaction", Message: "检测到多条写语句但未发现完整事务边界", Suggestion: "建议用 BEGIN/COMMIT 包裹，保证批量变更一致性", Statement: ""})
	}

	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].StatementIndex == issues[j].StatementIndex {
			return severityWeight(issues[i].Level) > severityWeight(issues[j].Level)
		}
		return issues[i].StatementIndex < issues[j].StatementIndex
	})

	summary := Summary{StatementCount: len(statements)}
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

	advice := buildAdvice(summary)
	if containsRoutine {
		advice = append(advice, "检测到存储过程/函数定义，建议补充过程权限控制、异常处理与审计日志检查")
	}

	result.Summary = summary
	result.Issues = issues
	result.Advice = advice
	return result
}

func severityWeight(level IssueLevel) int {
	switch level {
	case LevelError:
		return 3
	case LevelWarning:
		return 2
	default:
		return 1
	}
}

type missingTerminatorStatement struct {
	Index     int
	Statement string
}

type sqlHeuristicStatement struct {
	Text                string
	Terminated          bool
	FullwidthTerminator bool
}

func detectMissingTerminatorStatements(content string, statements []string, containsRoutine bool) []missingTerminatorStatement {
	if containsRoutine {
		return nil
	}

	normalized := strings.TrimSpace(stripCommentsAndStrings(content))
	if normalized == "" {
		return nil
	}

	if strings.Contains(strings.ToUpper(normalized), "DELIMITER ") {
		return nil
	}

	if hasLikelyMergedStatements(statements) {
		detailed := splitSQLByLineStartHeuristicDetailed(content)
		if len(detailed) <= 1 {
			return nil
		}

		missing := make([]missingTerminatorStatement, 0)
		for idx, statement := range detailed {
			if statement.Terminated {
				continue
			}
			normalizedStatement := normalizeStatementWithTerminator(statement.Text)
			if normalizedStatement == "" {
				continue
			}
			missing = append(missing, missingTerminatorStatement{Index: idx + 1, Statement: normalizedStatement})
		}
		return missing
	}

	if len(statements) >= 1 && !hasStatementTerminatorSuffix(normalized) {
		last := normalizeStatementWithTerminator(statements[len(statements)-1])
		if last == "" {
			return nil
		}
		return []missingTerminatorStatement{{Index: len(statements), Statement: last}}
	}

	return nil
}

func buildMissingTerminatorIssueMessage(items []missingTerminatorStatement) string {
	return buildMissingTerminatorIssueMessageWithSubject(items, "SQL")
}

func buildMissingTerminatorIssueMessageWithSubject(items []missingTerminatorStatement, subject string) string {
	if len(items) == 0 {
		return fmt.Sprintf("多条 %s 语句疑似缺少结束符（;）", subject)
	}

	if len(items) == 1 {
		return fmt.Sprintf("第 %d 条 %s 语句疑似缺少结束符（;）", items[0].Index, subject)
	}

	indices := make([]string, 0, len(items))
	for _, item := range items {
		indices = append(indices, strconv.Itoa(item.Index))
	}
	return fmt.Sprintf("第 %s 条 %s 语句疑似缺少结束符（;）", strings.Join(indices, "、"), subject)
}

func buildFullwidthTerminatorIssueMessage(items []missingTerminatorStatement) string {
	return buildFullwidthTerminatorIssueMessageWithSubject(items, "SQL")
}

func buildFullwidthTerminatorIssueMessageWithSubject(items []missingTerminatorStatement, subject string) string {
	if len(items) == 0 {
		return fmt.Sprintf("检测到中文结束符（；），请使用英文分号（;）")
	}

	if len(items) == 1 {
		return fmt.Sprintf("第 %d 条 %s 语句使用了中文结束符（；）", items[0].Index, subject)
	}

	indices := make([]string, 0, len(items))
	for _, item := range items {
		indices = append(indices, strconv.Itoa(item.Index))
	}
	return fmt.Sprintf("第 %s 条 %s 语句使用了中文结束符（；）", strings.Join(indices, "、"), subject)
}

func detectFullwidthTerminatorStatements(content string, containsRoutine bool) []missingTerminatorStatement {
	if containsRoutine {
		return nil
	}

	detailed := splitSQLByLineStartHeuristicDetailed(content)
	if len(detailed) == 0 {
		return nil
	}

	items := make([]missingTerminatorStatement, 0)
	for idx, statement := range detailed {
		if !statement.FullwidthTerminator {
			continue
		}
		normalizedStatement := normalizeStatementWithTerminator(statement.Text)
		if normalizedStatement == "" {
			continue
		}
		items = append(items, missingTerminatorStatement{Index: idx + 1, Statement: normalizedStatement})
	}
	return items
}

func excludeMissingTerminatorStatements(missing []missingTerminatorStatement, excludes []missingTerminatorStatement) []missingTerminatorStatement {
	if len(missing) == 0 || len(excludes) == 0 {
		return missing
	}

	excluded := make(map[int]struct{}, len(excludes))
	for _, item := range excludes {
		excluded[item.Index] = struct{}{}
	}

	filtered := make([]missingTerminatorStatement, 0, len(missing))
	for _, item := range missing {
		if _, found := excluded[item.Index]; found {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func buildMissingTerminatorStatementSnippet(items []missingTerminatorStatement) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		if item.Statement == "" {
			continue
		}
		lines = append(lines, item.Statement)
	}
	return strings.Join(lines, "\n")
}

func normalizeStatementWithTerminator(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	return trimmed
}

func splitSQLByLineStartHeuristicDetailed(content string) []sqlHeuristicStatement {
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	statements := make([]sqlHeuristicStatement, 0, len(lines))
	var builder strings.Builder

	flush := func(terminated bool, fullwidthTerminator bool) {
		statement := strings.TrimSpace(builder.String())
		builder.Reset()
		if statement == "" {
			return
		}
		statements = append(statements, sqlHeuristicStatement{Text: statement, Terminated: terminated, FullwidthTerminator: fullwidthTerminator})
	}

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "--") || strings.HasPrefix(line, "#") {
			continue
		}

		if builder.Len() > 0 && reStatementStart.MatchString(line) {
			flush(false, false)
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(line)

		terminated, fullwidthTerminator := detectLineTerminator(line)
		if terminated {
			flush(true, fullwidthTerminator)
		}
	}

	flush(false, false)
	return statements
}

func splitSQLByLineStartHeuristic(content string) []string {
	detailed := splitSQLByLineStartHeuristicDetailed(content)
	statements := make([]string, 0, len(detailed))
	for _, item := range detailed {
		trimmed := strings.TrimSpace(item.Text)
		if trimmed == "" {
			continue
		}
		statements = append(statements, trimmed)
	}
	return statements
}

func hasLikelyMergedStatements(statements []string) bool {
	for _, statement := range statements {
		normalized := strings.TrimSpace(stripCommentsAndStrings(statement))
		if normalized == "" {
			continue
		}

		allStarts := len(reStatementStart.FindAllString(normalized, -1))
		hardStarts := len(reHardStatementStart.FindAllString(normalized, -1))

		if hardStarts >= 2 {
			return true
		}
		if hardStarts >= 1 && allStarts >= 2 {
			return true
		}
	}
	return false
}

func splitSQLStatements(content string) []string {
	items := make([]string, 0)
	var builder strings.Builder

	delimiter := ";"
	inSingleQuote := false
	inDoubleQuote := false
	inBacktick := false
	inBlockComment := false

	lines := strings.SplitAfter(content, "\n")
	if len(lines) == 0 {
		lines = []string{content}
	}

	for _, line := range lines {
		if !inSingleQuote && !inDoubleQuote && !inBacktick && !inBlockComment {
			if delim, ok := parseDelimiterDirective(line); ok {
				delimiter = delim
				continue
			}
		}

		runes := []rune(line)
		delimRunes := []rune(delimiter)
		inLineComment := false

		for i := 0; i < len(runes); i++ {
			ch := runes[i]
			next := rune(0)
			if i+1 < len(runes) {
				next = runes[i+1]
			}

			if inLineComment {
				if ch == '\n' {
					inLineComment = false
					builder.WriteRune(ch)
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
				if ch == '-' && next == '-' {
					inLineComment = true
					i++
					continue
				}
				if ch == '#' {
					inLineComment = true
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

			if !inSingleQuote && !inDoubleQuote && !inBacktick && len(delimRunes) > 0 {
				if matchRunesAt(runes, i, delimRunes) {
					piece := strings.TrimSpace(builder.String())
					if piece != "" {
						items = append(items, piece)
					}
					builder.Reset()
					i += len(delimRunes) - 1
					continue
				}
				if delimiter == ";" && isFullwidthSemicolon(ch) {
					piece := strings.TrimSpace(builder.String())
					if piece != "" {
						items = append(items, piece)
					}
					builder.Reset()
					continue
				}
			}

			builder.WriteRune(ch)

		}
	}

	if tail := strings.TrimSpace(builder.String()); tail != "" {
		items = append(items, tail)
	}

	return items
}

func parseDelimiterDirective(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "", false
	}

	parts := strings.Fields(trimmed)
	if len(parts) < 2 {
		return "", false
	}
	if !strings.EqualFold(parts[0], "DELIMITER") {
		return "", false
	}

	delimiter := strings.TrimSpace(parts[1])
	if delimiter == "" {
		return ";", true
	}
	return delimiter, true
}

func detectLineTerminator(line string) (bool, bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasSuffix(trimmed, ";") {
		return true, false
	}
	if strings.HasSuffix(trimmed, "；") {
		return true, true
	}
	return false, false
}

func hasStatementTerminatorSuffix(normalized string) bool {
	trimmed := strings.TrimSpace(normalized)
	return strings.HasSuffix(trimmed, ";") || strings.HasSuffix(trimmed, "；")
}

func isFullwidthSemicolon(ch rune) bool {
	return ch == '；'

}

func matchRunesAt(source []rune, index int, target []rune) bool {
	if len(target) == 0 || index+len(target) > len(source) {
		return false
	}
	for i := range target {
		if source[index+i] != target[i] {
			return false
		}
	}
	return true
}

func isEscapedByBackslash(runes []rune, index int) bool {
	if index <= 0 {
		return false
	}
	escapeCount := 0
	for i := index - 1; i >= 0; i-- {
		if runes[i] == '\\' {
			escapeCount++
			continue
		}
		break
	}
	return escapeCount%2 == 1
}

func stripCommentsAndStrings(content string) string {
	var builder strings.Builder
	runes := []rune(content)

	inSingleQuote := false
	inDoubleQuote := false
	inBacktick := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				builder.WriteRune('\n')
			}
			continue
		}

		if inBlockComment {
			if ch == '*' && next == '/' {
				inBlockComment = false
				i++
			}
			if ch == '\n' {
				builder.WriteRune('\n')
			}
			continue
		}

		if !inSingleQuote && !inDoubleQuote && !inBacktick {
			if ch == '-' && next == '-' {
				inLineComment = true
				i++
				continue
			}
			if ch == '#' {
				inLineComment = true
				continue
			}
			if ch == '/' && next == '*' {
				inBlockComment = true
				i++
				continue
			}
		}

		if inSingleQuote {
			if ch == '\'' && !isEscapedByBackslash(runes, i) {
				inSingleQuote = false
			}
			if ch == '\n' {
				builder.WriteRune('\n')
			} else {
				builder.WriteRune(' ')
			}
			continue
		}

		if inDoubleQuote {
			if ch == '"' && !isEscapedByBackslash(runes, i) {
				inDoubleQuote = false
			}
			if ch == '\n' {
				builder.WriteRune('\n')
			} else {
				builder.WriteRune(' ')
			}
			continue
		}

		if inBacktick {
			if ch == '`' {
				inBacktick = false
			}
			if ch == '\n' {
				builder.WriteRune('\n')
			} else {
				builder.WriteRune(' ')
			}
			continue
		}

		if ch == '\'' {
			inSingleQuote = true
			builder.WriteRune(' ')
			continue
		}
		if ch == '"' {
			inDoubleQuote = true
			builder.WriteRune(' ')
			continue
		}
		if ch == '`' {
			inBacktick = true
			builder.WriteRune(' ')
			continue
		}

		builder.WriteRune(ch)
	}

	return builder.String()
}

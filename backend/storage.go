package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrHistoryNotFound = errors.New("history not found")

type HistoryStore struct {
	dbPath string
}

type SaveHistoryInput struct {
	RequestID     string
	Engine        DBEngine
	Source        string
	FileName      string
	SQLText       string
	DisabledRules []string
	CheckResult   CheckResponse
}

type HistoryItem struct {
	ID         int64    `json:"id"`
	RequestID  string   `json:"requestId"`
	Engine     DBEngine `json:"engine"`
	Source     string   `json:"source"`
	FileName   string   `json:"fileName"`
	CreatedAt  string   `json:"createdAt"`
	Summary    Summary  `json:"summary"`
	SQLPreview string   `json:"sqlPreview"`
}

type HistoryDetail struct {
	ID            int64         `json:"id"`
	RequestID     string        `json:"requestId"`
	Engine        DBEngine      `json:"engine"`
	Source        string        `json:"source"`
	FileName      string        `json:"fileName"`
	CreatedAt     string        `json:"createdAt"`
	SQLText       string        `json:"sqlText"`
	DisabledRules []string      `json:"disabledRules"`
	CheckResult   CheckResponse `json:"checkResult"`
}

func NewHistoryStore(dbPath string) (*HistoryStore, error) {
	resolvedPath := strings.TrimSpace(dbPath)
	if resolvedPath == "" {
		resolvedPath = "./data/sql_review.db"
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, errors.New("sqlite3 command not found, please install sqlite3")
	}

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		return nil, err
	}

	store := &HistoryStore{dbPath: resolvedPath}
	if err := store.initSchema(); err != nil {
		return nil, err
	}

	return store, nil
}

func (store *HistoryStore) Close() error {
	return nil
}

func (store *HistoryStore) initSchema() error {
	query := `
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
CREATE TABLE IF NOT EXISTS review_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL,
  engine TEXT NOT NULL DEFAULT 'mysql',
  source TEXT NOT NULL,
  file_name TEXT NOT NULL DEFAULT '',
  sql_text TEXT NOT NULL,
  disabled_rules_json TEXT NOT NULL,
  result_json TEXT NOT NULL,
  statement_count INTEGER NOT NULL,
  error_count INTEGER NOT NULL,
  warning_count INTEGER NOT NULL,
  info_count INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_review_history_created_at ON review_history(created_at DESC);
`
	if err := store.execQuery(query); err != nil {
		return err
	}

	if err := store.ensureColumn("engine", "TEXT NOT NULL DEFAULT 'mysql'"); err != nil {
		return err
	}
	if err := store.migrateLegacyHistorySchema(); err != nil {
		return err
	}

	return nil
}

func (store *HistoryStore) migrateLegacyHistorySchema() error {
	hasProfile, err := store.hasColumn("profile")
	if err != nil {
		return err
	}

	hasScore, err := store.hasColumn("score")
	if err != nil {
		return err
	}

	if !hasProfile && !hasScore {
		return nil
	}

	migration := `
BEGIN IMMEDIATE;
CREATE TABLE review_history_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL,
  engine TEXT NOT NULL DEFAULT 'mysql',
  source TEXT NOT NULL,
  file_name TEXT NOT NULL DEFAULT '',
  sql_text TEXT NOT NULL,
  disabled_rules_json TEXT NOT NULL,
  result_json TEXT NOT NULL,
  statement_count INTEGER NOT NULL,
  error_count INTEGER NOT NULL,
  warning_count INTEGER NOT NULL,
  info_count INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
INSERT INTO review_history_new (
  id, request_id, engine, source, file_name, sql_text,
  disabled_rules_json, result_json,
  statement_count, error_count, warning_count, info_count, created_at
)
SELECT
  id,
  request_id,
  COALESCE(NULLIF(engine, ''), 'mysql') AS engine,
  source,
  file_name,
  sql_text,
  disabled_rules_json,
  result_json,
  statement_count,
  error_count,
  warning_count,
  info_count,
  created_at
FROM review_history;
DROP TABLE review_history;
ALTER TABLE review_history_new RENAME TO review_history;
CREATE INDEX IF NOT EXISTS idx_review_history_created_at ON review_history(created_at DESC);
COMMIT;
`

	if err := store.execQuery(migration); err != nil {
		_ = store.execQuery("ROLLBACK;")
		return err
	}
	return nil
}

func (store *HistoryStore) ensureColumn(columnName, columnDef string) error {
	has, err := store.hasColumn(columnName)
	if err != nil {
		return err
	}
	if has {
		return nil
	}

	alterQuery := fmt.Sprintf("ALTER TABLE review_history ADD COLUMN %s %s;", columnName, columnDef)
	return store.execQuery(alterQuery)
}

func (store *HistoryStore) hasColumn(columnName string) (bool, error) {
	type tableInfoRow struct {
		Name string `json:"name"`
	}

	var rows []tableInfoRow
	if err := store.queryJSON(`PRAGMA table_info(review_history);`, &rows); err != nil {
		return false, err
	}

	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(row.Name), columnName) {
			return true, nil
		}
	}
	return false, nil
}

func (store *HistoryStore) Save(input SaveHistoryInput) (int64, error) {
	disabledRulesJSON, err := json.Marshal(input.DisabledRules)
	if err != nil {
		return 0, err
	}

	resultJSON, err := json.Marshal(input.CheckResult)
	if err != nil {
		return 0, err
	}

	engine := NormalizeEngine(string(input.Engine))

	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	insertQuery := fmt.Sprintf(`
INSERT INTO review_history (
  request_id, engine, source, file_name, sql_text,
  disabled_rules_json, result_json,
  statement_count, error_count, warning_count, info_count, created_at
) VALUES (
  %s, %s, %s, %s, %s,
  %s, %s,
  %d, %d, %d, %d, %s
);
`,
		sqlQuote(input.RequestID),
		sqlQuote(string(engine)),
		sqlQuote(input.Source),
		sqlQuote(input.FileName),
		sqlQuote(input.SQLText),
		sqlQuote(string(disabledRulesJSON)),
		sqlQuote(string(resultJSON)),
		input.CheckResult.Summary.StatementCount,
		input.CheckResult.Summary.ErrorCount,
		input.CheckResult.Summary.WarningCount,
		input.CheckResult.Summary.InfoCount,
		sqlQuote(createdAt),
	)

	if err := store.execQuery(insertQuery); err != nil {
		return 0, err
	}

	type idRow struct {
		ID int64 `json:"id"`
	}
	var rows []idRow
	if err := store.queryJSON(fmt.Sprintf(
		`SELECT id FROM review_history WHERE request_id = %s ORDER BY id DESC LIMIT 1;`,
		sqlQuote(input.RequestID),
	), &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, errors.New("failed to fetch last insert id")
	}
	return rows[0].ID, nil
}

func (store *HistoryStore) List(limit, offset int) ([]HistoryItem, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	type listRow struct {
		ID             int64  `json:"id"`
		RequestID      string `json:"requestId"`
		Engine         string `json:"engine"`
		Source         string `json:"source"`
		FileName       string `json:"fileName"`
		CreatedAt      string `json:"createdAt"`
		StatementCount int    `json:"statementCount"`
		ErrorCount     int    `json:"errorCount"`
		WarningCount   int    `json:"warningCount"`
		InfoCount      int    `json:"infoCount"`
		SQLPreview     string `json:"sqlPreview"`
	}

	query := fmt.Sprintf(`
SELECT
  id,
  request_id AS requestId,
  engine,
  source,
  file_name AS fileName,
  created_at AS createdAt,
  statement_count AS statementCount,
  error_count AS errorCount,
  warning_count AS warningCount,
  info_count AS infoCount,
  CASE
    WHEN length(replace(replace(sql_text, char(10), ' '), char(13), ' ')) > 200
      THEN substr(replace(replace(sql_text, char(10), ' '), char(13), ' '), 1, 200) || '...'
    ELSE replace(replace(sql_text, char(10), ' '), char(13), ' ')
  END AS sqlPreview
FROM review_history
ORDER BY id DESC
LIMIT %d OFFSET %d;
`, limit, offset)

	var rows []listRow
	if err := store.queryJSON(query, &rows); err != nil {
		return nil, 0, err
	}

	items := make([]HistoryItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, HistoryItem{
			ID:        row.ID,
			RequestID: row.RequestID,
			Engine:    NormalizeEngine(row.Engine),
			Source:    row.Source,
			FileName:  row.FileName,
			CreatedAt: row.CreatedAt,
			Summary: Summary{
				StatementCount: row.StatementCount,
				ErrorCount:     row.ErrorCount,
				WarningCount:   row.WarningCount,
				InfoCount:      row.InfoCount,
			},
			SQLPreview: row.SQLPreview,
		})
	}

	type countRow struct {
		Total int `json:"total"`
	}
	var countRows []countRow
	if err := store.queryJSON(`SELECT COUNT(1) AS total FROM review_history;`, &countRows); err != nil {
		return nil, 0, err
	}
	if len(countRows) == 0 {
		return items, 0, nil
	}

	return items, countRows[0].Total, nil
}

func (store *HistoryStore) GetByID(id int64) (HistoryDetail, error) {
	type detailRow struct {
		ID                int64  `json:"id"`
		RequestID         string `json:"requestId"`
		Engine            string `json:"engine"`
		Source            string `json:"source"`
		FileName          string `json:"fileName"`
		CreatedAt         string `json:"createdAt"`
		SQLText           string `json:"sqlText"`
		DisabledRulesJSON string `json:"disabledRulesJson"`
		ResultJSON        string `json:"resultJson"`
	}

	query := fmt.Sprintf(`
SELECT
  id,
  request_id AS requestId,
  engine,
  source,
  file_name AS fileName,
  created_at AS createdAt,
  sql_text AS sqlText,
  disabled_rules_json AS disabledRulesJson,
  result_json AS resultJson
FROM review_history
WHERE id = %d
LIMIT 1;
`, id)

	var rows []detailRow
	if err := store.queryJSON(query, &rows); err != nil {
		return HistoryDetail{}, err
	}
	if len(rows) == 0 {
		return HistoryDetail{}, ErrHistoryNotFound
	}

	row := rows[0]
	detail := HistoryDetail{
		ID:        row.ID,
		RequestID: row.RequestID,
		Engine:    NormalizeEngine(row.Engine),
		Source:    row.Source,
		FileName:  row.FileName,
		CreatedAt: row.CreatedAt,
		SQLText:   row.SQLText,
	}

	detail.DisabledRules = make([]string, 0)
	if strings.TrimSpace(row.DisabledRulesJSON) != "" {
		if err := json.Unmarshal([]byte(row.DisabledRulesJSON), &detail.DisabledRules); err != nil {
			return HistoryDetail{}, err
		}
	}

	if err := json.Unmarshal([]byte(row.ResultJSON), &detail.CheckResult); err != nil {
		return HistoryDetail{}, err
	}

	return detail, nil
}

func (store *HistoryStore) DeleteByIDs(ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	seen := make(map[int64]struct{}, len(ids))
	normalizedIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalizedIDs = append(normalizedIDs, id)
	}
	if len(normalizedIDs) == 0 {
		return 0, nil
	}

	idTexts := make([]string, 0, len(normalizedIDs))
	for _, id := range normalizedIDs {
		idTexts = append(idTexts, strconv.FormatInt(id, 10))
	}
	whereIn := strings.Join(idTexts, ",")

	type countRow struct {
		Total int `json:"total"`
	}
	var countRows []countRow
	countQuery := fmt.Sprintf(`SELECT COUNT(1) AS total FROM review_history WHERE id IN (%s);`, whereIn)
	if err := store.queryJSON(countQuery, &countRows); err != nil {
		return 0, err
	}
	if len(countRows) == 0 || countRows[0].Total <= 0 {
		return 0, nil
	}

	deleteQuery := fmt.Sprintf(`DELETE FROM review_history WHERE id IN (%s);`, whereIn)
	if err := store.execQuery(deleteQuery); err != nil {
		return 0, err
	}

	return countRows[0].Total, nil
}

func (store *HistoryStore) execQuery(query string) error {
	output, err := store.runSQLite(query, false)
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return fmt.Errorf("sqlite3 exec error: %s", trimmed)
		}
		return err
	}
	return nil
}

func (store *HistoryStore) queryJSON(query string, target any) error {
	output, err := store.runSQLite(query, true)
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return fmt.Errorf("sqlite3 query error: %s", trimmed)
		}
		return err
	}

	content := strings.TrimSpace(string(output))
	if content == "" {
		content = "[]"
	}

	if err := json.Unmarshal([]byte(content), target); err != nil {
		return fmt.Errorf("decode sqlite json output failed: %w (raw=%s)", err, truncate(content, 200))
	}
	return nil
}

func (store *HistoryStore) runSQLite(query string, asJSON bool) ([]byte, error) {
	args := make([]string, 0, 2)
	if asJSON {
		args = append(args, "-json")
	}
	args = append(args, store.dbPath)

	cmd := exec.Command("sqlite3", args...)
	cmd.Stdin = strings.NewReader(query + "\n")
	return cmd.CombinedOutput()
}

func sqlQuote(input string) string {
	escaped := strings.ReplaceAll(input, "'", "''")
	return "'" + escaped + "'"
}

func truncate(text string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(text) <= max {
		return text
	}
	return text[:max] + "..."
}

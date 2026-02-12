package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxPayloadBytes = 4 << 20

var historyStore *HistoryStore

var alwaysEnabledRules = map[string]struct{}{
	"empty_input":                        {},
	"missing_statement_terminator":       {},
	"mongo_missing_statement_terminator": {},
	"fullwidth_statement_terminator":     {},
}

type checkRequest struct {
	SQL           string   `json:"sql"`
	Engine        string   `json:"engine"`
	DisabledRules []string `json:"disabledRules"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type healthResponse struct {
	Service string `json:"service"`
	Status  string `json:"status"`
	Time    string `json:"time"`
}

type checkAPIResponse struct {
	RequestID      string   `json:"requestId"`
	HistoryID      int64    `json:"historyId"`
	HistoryWarning string   `json:"historyWarning,omitempty"`
	Engine         DBEngine `json:"engine"`
	Source         string   `json:"source"`
	FileName       string   `json:"fileName"`
	DisabledRules  []string `json:"disabledRules"`
	CheckResponse
}

type historyListResponse struct {
	Items  []HistoryItem `json:"items"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

type historyDeleteRequest struct {
	IDs []int64 `json:"ids"`
}

type uploadReadResult struct {
	SQLContent    string
	Source        string
	FileName      string
	Engine        DBEngine
	DisabledRules map[string]struct{}
}

func main() {
	dbPath := strings.TrimSpace(os.Getenv("SQL_REVIEW_DB_PATH"))
	if dbPath == "" {
		dbPath = "./data/sql_review.db"
	}

	store, err := NewHistoryStore(dbPath)
	if err != nil {
		log.Fatalf("init sqlite store failed: %v", err)
	}
	historyStore = store
	defer func() {
		if err := historyStore.Close(); err != nil {
			log.Printf("close sqlite store error: %v", err)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", handleHealth)
	mux.HandleFunc("/api/v1/rules", handleRules)
	mux.HandleFunc("/api/v1/check", handleCheck)
	mux.HandleFunc("/api/v1/history", handleHistoryList)
	mux.HandleFunc("/api/v1/history/", handleHistoryDetail)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	addr := ":" + port
	log.Printf("SQL Review API running on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, loggingMiddleware(corsMiddleware(mux))); err != nil {
		log.Fatal(err)
	}
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	allowedOrigin := strings.TrimSpace(os.Getenv("ALLOWED_ORIGIN"))
	if allowedOrigin == "" {
		allowedOrigin = "*"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "only GET is allowed"})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{
		Service: "sql-review-api",
		Status:  "ok",
		Time:    time.Now().Format(time.RFC3339),
	})
}

func handleRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "only GET is allowed"})
		return
	}

	engine := NormalizeEngine(r.URL.Query().Get("engine"))
	rulesVersionValue, rules := RulesForEngine(engine)

	writeJSON(w, http.StatusOK, map[string]any{
		"engine":       engine,
		"engines":      SupportedEngines(),
		"rulesVersion": rulesVersionValue,
		"rules":        rules,
	})
}

func handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "only POST is allowed"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadBytes)
	contentType := r.Header.Get("Content-Type")

	var sqlContent string
	source := "paste"
	fileName := ""
	engine := NormalizeEngine(r.URL.Query().Get("engine"))
	disabledRules := make(map[string]struct{})

	switch {
	case strings.Contains(contentType, "application/json"):
		var req checkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid json payload"})
			return
		}
		sqlContent = req.SQL
		engine = NormalizeEngine(req.Engine)
		for _, code := range req.DisabledRules {
			if trimmed := strings.TrimSpace(code); trimmed != "" {
				disabledRules[trimmed] = struct{}{}
			}
		}
	case strings.Contains(contentType, "multipart/form-data"):
		parsed, err := readSQLFromUpload(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		sqlContent = parsed.SQLContent
		source = parsed.Source
		fileName = parsed.FileName
		engine = parsed.Engine
		disabledRules = parsed.DisabledRules
	case strings.Contains(contentType, "text/plain"):
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "cannot read request body"})
			return
		}
		sqlContent = string(body)
	default:
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "unsupported content type"})
		return
	}

	forcedRules := enforceAlwaysEnabledRules(disabledRules)
	if len(forcedRules) > 0 {
		log.Printf("enforce always-enabled rules: %s", strings.Join(forcedRules, ", "))
	}

	result := AnalyzeByEngine(engine, sqlContent, AnalyzeOptions{
		DisabledRules: disabledRules,
	})
	requestID := fmt.Sprintf("req-%d", time.Now().UnixNano())
	disabledRulesSlice := disabledRulesToSlice(disabledRules)

	historyWarning := ""
	if len(forcedRules) > 0 {
		historyWarning = fmt.Sprintf("以下基础规则不可关闭，已自动启用：%s", strings.Join(forcedRules, ", "))
	}
	historyID, err := historyStore.Save(SaveHistoryInput{
		RequestID:     requestID,
		Engine:        engine,
		Source:        source,
		FileName:      fileName,
		SQLText:       sqlContent,
		DisabledRules: disabledRulesSlice,
		CheckResult:   result,
	})
	if err != nil {
		if historyWarning == "" {
			historyWarning = "历史保存失败，请检查数据库权限或磁盘状态"
		} else {
			historyWarning = historyWarning + "；历史保存失败，请检查数据库权限或磁盘状态"
		}
		log.Printf("save history failed: %v", err)
	}

	writeJSON(w, http.StatusOK, checkAPIResponse{
		RequestID:      requestID,
		HistoryID:      historyID,
		HistoryWarning: historyWarning,
		Engine:         engine,
		Source:         source,
		FileName:       fileName,
		DisabledRules:  disabledRulesSlice,
		CheckResponse: CheckResponse{
			RulesVersion: result.RulesVersion,
			CheckedAt:    result.CheckedAt,
			Summary:      result.Summary,
			Issues:       result.Issues,
			Advice:       result.Advice,
		},
	})
}

func handleHistoryList(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit := parseIntWithDefault(r.URL.Query().Get("limit"), 20)
		offset := parseIntWithDefault(r.URL.Query().Get("offset"), 0)
		if limit <= 0 {
			limit = 20
		}
		if limit > 100 {
			limit = 100
		}
		if offset < 0 {
			offset = 0
		}

		items, total, err := historyStore.List(limit, offset)
		if err != nil {
			log.Printf("list history failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list history"})
			return
		}

		writeJSON(w, http.StatusOK, historyListResponse{
			Items:  items,
			Total:  total,
			Limit:  limit,
			Offset: offset,
		})
	case http.MethodDelete:
		var req historyDeleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid delete payload"})
			return
		}

		ids := normalizeHistoryIDs(req.IDs)
		if len(ids) == 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing valid history ids"})
			return
		}

		deleted, err := historyStore.DeleteByIDs(ids)
		if err != nil {
			log.Printf("delete history failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete history"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "only GET and DELETE are allowed"})
	}
}

func handleHistoryDetail(w http.ResponseWriter, r *http.Request) {
	id, err := parseHistoryIDFromPath(r.URL.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	switch r.Method {
	case http.MethodGet:
		detail, getErr := historyStore.GetByID(id)
		if getErr != nil {
			if errors.Is(getErr, ErrHistoryNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: "history not found"})
				return
			}
			log.Printf("get history detail failed: %v", getErr)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to get history detail"})
			return
		}

		writeJSON(w, http.StatusOK, detail)
	case http.MethodDelete:
		deleted, delErr := historyStore.DeleteByIDs([]int64{id})
		if delErr != nil {
			log.Printf("delete history detail failed: %v", delErr)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete history"})
			return
		}
		if deleted == 0 {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "history not found"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "only GET and DELETE are allowed"})
	}
}

func parseHistoryIDFromPath(path string) (int64, error) {
	idText := strings.TrimPrefix(path, "/api/v1/history/")
	idText = strings.TrimSpace(idText)
	if idText == "" {
		return 0, errors.New("missing history id")
	}

	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid history id")
	}

	return id, nil
}

func normalizeHistoryIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}

	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return normalized
}

func readSQLFromUpload(r *http.Request) (uploadReadResult, error) {
	if err := r.ParseMultipartForm(6 << 20); err != nil {
		return uploadReadResult{}, errors.New("failed to parse upload form")
	}

	disabledRules, err := parseDisabledRulesString(r.FormValue("disabledRules"))
	if err != nil {
		return uploadReadResult{}, err
	}

	engine := NormalizeEngine(r.FormValue("engine"))

	if sql := strings.TrimSpace(r.FormValue("sql")); sql != "" {
		return uploadReadResult{
			SQLContent:    sql,
			Source:        "paste",
			FileName:      "",
			Engine:        engine,
			DisabledRules: disabledRules,
		}, nil
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		return uploadReadResult{}, errors.New("missing file field: file")
	}
	defer file.Close()

	if !isLikelySQLFile(header) {
		return uploadReadResult{}, fmt.Errorf("unsupported file type: %s", header.Filename)
	}

	body, err := io.ReadAll(io.LimitReader(file, maxPayloadBytes))
	if err != nil {
		return uploadReadResult{}, errors.New("failed to read uploaded file")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return uploadReadResult{}, errors.New("uploaded file is empty")
	}

	return uploadReadResult{
		SQLContent:    string(body),
		Source:        "upload",
		FileName:      header.Filename,
		Engine:        engine,
		DisabledRules: disabledRules,
	}, nil
}

func parseDisabledRulesString(raw string) (map[string]struct{}, error) {
	rules := make(map[string]struct{})
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return rules, nil
	}

	if strings.HasPrefix(trimmed, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, errors.New("invalid disabledRules payload")
		}
		for _, item := range arr {
			if code := strings.TrimSpace(item); code != "" {
				rules[code] = struct{}{}
			}
		}
		return rules, nil
	}

	parts := strings.Split(trimmed, ",")
	for _, item := range parts {
		if code := strings.TrimSpace(item); code != "" {
			rules[code] = struct{}{}
		}
	}
	return rules, nil
}

func enforceAlwaysEnabledRules(disabled map[string]struct{}) []string {
	if disabled == nil || len(disabled) == 0 {
		return nil
	}

	removed := make([]string, 0)
	for code := range alwaysEnabledRules {
		if _, found := disabled[code]; !found {
			continue
		}
		delete(disabled, code)
		removed = append(removed, code)
	}
	sort.Strings(removed)
	return removed
}

func disabledRulesToSlice(disabled map[string]struct{}) []string {
	items := make([]string, 0, len(disabled))
	for code := range disabled {
		items = append(items, code)
	}
	sort.Strings(items)
	return items
}

func isLikelySQLFile(header *multipart.FileHeader) bool {
	name := strings.ToLower(header.Filename)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".sql" || ext == ".txt" || ext == ".js" || ext == ".mongo" {
		return true
	}

	contentType := strings.ToLower(header.Header.Get("Content-Type"))
	return strings.Contains(contentType, "sql") || strings.Contains(contentType, "text/plain")
}

func parseIntWithDefault(raw string, defaultValue int) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil {
		return defaultValue
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response error: %v", err)
	}
}

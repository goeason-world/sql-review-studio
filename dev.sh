#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
RUN_DIR="$ROOT_DIR/.run"
LOG_DIR="${LOG_DIR:-$ROOT_DIR/logs}"
DATA_DIR="${DATA_DIR:-$ROOT_DIR/data}"
DEFAULT_DB_PATH="$DATA_DIR/sql_review.db"
BACKEND_DIR="$ROOT_DIR/backend"
FRONTEND_DIR="$ROOT_DIR/frontend"

BACKEND_PID_FILE="$RUN_DIR/backend.pid"
FRONTEND_PID_FILE="$RUN_DIR/frontend.pid"
BACKEND_LOG_FILE="$LOG_DIR/backend.log"
FRONTEND_LOG_FILE="$LOG_DIR/frontend.log"
BACKEND_PORT_FILE="$RUN_DIR/backend.port"
FRONTEND_PORT_FILE="$RUN_DIR/frontend.port"
NPM_CACHE_DIR="$RUN_DIR/npm-cache"

BACKEND_PORT="${BACKEND_PORT:-8080}"
FRONTEND_PORT="${FRONTEND_PORT:-5173}"
FORCE_FREE_PORTS="${FORCE_FREE_PORTS:-1}"

ensure_run_dir() {
  if [ -e "$RUN_DIR" ] && [ ! -w "$RUN_DIR" ]; then
    echo "无权限写入 $RUN_DIR" >&2
    echo "请执行: sudo chown -R $(id -u):$(id -g) \"$ROOT_DIR\"" >&2
    exit 1
  fi

  if [ -e "$LOG_DIR" ] && [ ! -w "$LOG_DIR" ]; then
    echo "无权限写入 $LOG_DIR" >&2
    echo "请执行: sudo chown -R $(id -u):$(id -g) \"$ROOT_DIR\"" >&2
    exit 1
  fi

  if [ -e "$DATA_DIR" ] && [ ! -w "$DATA_DIR" ]; then
    echo "无权限写入 $DATA_DIR" >&2
    echo "请执行: sudo chown -R $(id -u):$(id -g) \"$ROOT_DIR\"" >&2
    exit 1
  fi

  mkdir -p "$RUN_DIR"
  mkdir -p "$NPM_CACHE_DIR"
  mkdir -p "$LOG_DIR"
  mkdir -p "$DATA_DIR"
}

migrate_legacy_db_if_needed() {
  if [ -n "${SQL_REVIEW_DB_PATH:-}" ]; then
    return 0
  fi

  legacy_db_path="$RUN_DIR/sql_review.db"
  if [ ! -f "$legacy_db_path" ] || [ -f "$DEFAULT_DB_PATH" ]; then
    return 0
  fi

  echo "检测到旧数据库路径，迁移到: $DEFAULT_DB_PATH"
  if mv "$legacy_db_path" "$DEFAULT_DB_PATH" 2>/dev/null; then
    [ -f "$legacy_db_path-wal" ] && mv "$legacy_db_path-wal" "$DEFAULT_DB_PATH-wal" 2>/dev/null || true
    [ -f "$legacy_db_path-shm" ] && mv "$legacy_db_path-shm" "$DEFAULT_DB_PATH-shm" 2>/dev/null || true
    echo "数据库迁移完成"
  else
    echo "数据库迁移失败，可手动迁移: $legacy_db_path -> $DEFAULT_DB_PATH" >&2
  fi
}

read_pid() {
  pid_file="$1"
  if [ -f "$pid_file" ]; then
    cat "$pid_file"
  fi
}

read_port_or_default() {
  port_file="$1"
  default_port="$2"
  if [ -f "$port_file" ]; then
    cat "$port_file"
  else
    echo "$default_port"
  fi
}

is_pid_running() {
  pid="$1"
  if [ -z "$pid" ]; then
    return 1
  fi
  kill -0 "$pid" 2>/dev/null
}

port_in_use() {
  port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
    return $?
  fi
  if command -v nc >/dev/null 2>&1; then
    nc -z 127.0.0.1 "$port" >/dev/null 2>&1
    return $?
  fi
  return 1
}

get_listener_pids() {
  port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -t -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null | sort -u
  fi
}

port_owner_hint() {
  port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null | sed -n '2p'
  fi
}

wait_for_http() {
  url="$1"
  name="$2"

  if ! command -v curl >/dev/null 2>&1; then
    return 0
  fi

  i=0
  while [ "$i" -lt 40 ]; do
    if curl -fsS --max-time 2 "$url" >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    sleep 0.25
  done

  echo "$name 健康检查失败: $url" >&2
  return 1
}

release_port_if_needed() {
  name="$1"
  port="$2"

  if ! port_in_use "$port"; then
    return 0
  fi

  echo "$name 端口 $port 已被占用"
  owner=$(port_owner_hint "$port" || true)
  if [ -n "$owner" ]; then
    echo "占用进程: $owner"
  fi

  if [ "$FORCE_FREE_PORTS" != "1" ]; then
    echo "已禁用自动释放端口（FORCE_FREE_PORTS=0）" >&2
    return 1
  fi

  pids=$(get_listener_pids "$port" || true)
  if [ -z "$pids" ]; then
    echo "无法定位占用 PID，释放端口失败" >&2
    return 1
  fi

  echo "尝试释放端口 $port..."
  for pid in $pids; do
    cmd=$(ps -p "$pid" -o comm= 2>/dev/null | awk '{$1=$1;print}')
    if [ -n "$cmd" ]; then
      echo "停止 PID $pid ($cmd)"
    else
      echo "停止 PID $pid"
    fi
    if ! kill "$pid" 2>/dev/null; then
      echo "停止 PID $pid 失败，可能权限不足" >&2
      return 1
    fi
  done

  i=0
  while port_in_use "$port"; do
    i=$((i + 1))
    if [ "$i" -ge 20 ]; then
      break
    fi
    sleep 0.2
  done

  if port_in_use "$port"; then
    pids=$(get_listener_pids "$port" || true)
    if [ -n "$pids" ]; then
      echo "端口仍被占用，尝试强制结束..."
      for pid in $pids; do
        kill -9 "$pid" 2>/dev/null || true
      done
      sleep 0.2
    fi
  fi

  if port_in_use "$port"; then
    echo "端口 $port 释放失败" >&2
    return 1
  fi

  echo "端口 $port 已释放"
}

stop_by_pid_file() {
  name="$1"
  pid_file="$2"

  pid=$(read_pid "$pid_file" || true)
  if ! is_pid_running "$pid"; then
    rm -f "$pid_file"
    echo "$name 未运行"
    return 0
  fi

  echo "停止 $name (PID: $pid)..."
  kill "$pid" 2>/dev/null || true

  i=0
  while is_pid_running "$pid"; do
    i=$((i + 1))
    if [ "$i" -ge 20 ]; then
      echo "$name 未及时退出，强制终止"
      kill -9 "$pid" 2>/dev/null || true
      break
    fi
    sleep 0.2
  done

  rm -f "$pid_file"
  echo "$name 已停止"
}

check_cmd() {
  cmd="$1"
  if command -v "$cmd" >/dev/null 2>&1; then
    echo "[OK] 命令可用: $cmd"
    return 0
  fi
  echo "[ERR] 缺少命令: $cmd"
  return 1
}

doctor() {
  ensure_run_dir
  errs=0

  echo "== doctor: 环境检查 =="
  echo "项目目录: $ROOT_DIR"
  echo "当前用户: $(id -un) ($(id -u):$(id -g))"

  if [ "$(id -u)" -eq 0 ]; then
    echo "[WARN] 当前是 root 用户，建议不要用 sudo 启动"
  else
    echo "[OK] 当前非 root 用户"
  fi

  for c in go npm node curl lsof sqlite3; do
    if ! check_cmd "$c"; then
      errs=$((errs + 1))
    fi
  done

  if [ -w "$ROOT_DIR" ] && [ -w "$FRONTEND_DIR" ] && [ -w "$BACKEND_DIR" ]; then
    echo "[OK] 项目目录可写"
  else
    echo "[ERR] 项目目录存在不可写路径"
    echo "      修复建议: sudo chown -R $(id -u):$(id -g) \"$ROOT_DIR\""
    errs=$((errs + 1))
  fi

  root_owned=$(find "$ROOT_DIR" -maxdepth 3 -user 0 2>/dev/null | head -n 3 || true)
  if [ -n "$root_owned" ] && [ "$(id -u)" -ne 0 ]; then
    echo "[WARN] 检测到 root 拥有的文件（示例）:"
    echo "$root_owned"
    echo "      建议执行: sudo chown -R $(id -u):$(id -g) \"$ROOT_DIR\""
  else
    echo "[OK] 未发现明显 root 所有权问题"
  fi

  for p in "$BACKEND_PORT" "$FRONTEND_PORT"; do
    if port_in_use "$p"; then
      echo "[WARN] 端口 $p 被占用"
      hint=$(port_owner_hint "$p" || true)
      [ -n "$hint" ] && echo "       $hint"
    else
      echo "[OK] 端口 $p 空闲"
    fi
  done

  if [ -d "$FRONTEND_DIR/node_modules" ]; then
    echo "[OK] frontend/node_modules 已存在"
  else
    echo "[WARN] frontend/node_modules 不存在，首次启动会自动 npm install"
  fi

  if [ "$errs" -eq 0 ]; then
    echo "doctor 结果: 通过"
    return 0
  fi

  echo "doctor 结果: 发现 $errs 个错误，请先修复"
  return 1
}

start_backend() {
  ensure_run_dir
  migrate_legacy_db_if_needed

  pid=$(read_pid "$BACKEND_PID_FILE" || true)
  if is_pid_running "$pid"; then
    running_port=$(read_port_or_default "$BACKEND_PORT_FILE" "$BACKEND_PORT")
    echo "后端已在运行 (PID: $pid, 端口: $running_port)"
    return 0
  fi

  selected_port="$BACKEND_PORT"
  release_port_if_needed "BACKEND" "$selected_port"

  echo "启动后端..."
  (
    cd "$BACKEND_DIR"
    export PORT="$selected_port"
    export SQL_REVIEW_DB_PATH="${SQL_REVIEW_DB_PATH:-$DEFAULT_DB_PATH}"
    export GOCACHE="$BACKEND_DIR/.cache/go-build"
    nohup go run . >"$BACKEND_LOG_FILE" 2>&1 &
    echo $! >"$BACKEND_PID_FILE"
  )
  echo "$selected_port" >"$BACKEND_PORT_FILE"

  sleep 1
  pid=$(read_pid "$BACKEND_PID_FILE" || true)
  if ! is_pid_running "$pid"; then
    rm -f "$BACKEND_PORT_FILE"
    echo "后端启动失败，请检查日志: $BACKEND_LOG_FILE"
    return 1
  fi

  if ! wait_for_http "http://127.0.0.1:$selected_port/api/v1/health" "后端"; then
    stop_by_pid_file "后端" "$BACKEND_PID_FILE"
    rm -f "$BACKEND_PORT_FILE"
    echo "后端日志（最近 40 行）："
    tail -n 40 "$BACKEND_LOG_FILE" 2>/dev/null || true
    return 1
  fi

  echo "后端已启动: http://localhost:$selected_port (PID: $pid)"
}

start_frontend() {
  ensure_run_dir

  pid=$(read_pid "$FRONTEND_PID_FILE" || true)
  if is_pid_running "$pid"; then
    running_port=$(read_port_or_default "$FRONTEND_PORT_FILE" "$FRONTEND_PORT")
    echo "前端已在运行 (PID: $pid, 端口: $running_port)"
    return 0
  fi

  if [ ! -w "$FRONTEND_DIR" ]; then
    echo "前端目录无写权限: $FRONTEND_DIR" >&2
    echo "请执行: sudo chown -R $(id -u):$(id -g) \"$ROOT_DIR\"" >&2
    return 1
  fi

  selected_frontend_port="$FRONTEND_PORT"
  release_port_if_needed "FRONTEND" "$selected_frontend_port"

  selected_backend_port=$(read_port_or_default "$BACKEND_PORT_FILE" "$BACKEND_PORT")

  if [ ! -d "$FRONTEND_DIR/node_modules" ]; then
    echo "检测到 frontend/node_modules 不存在，先执行 npm install..."
    (
      cd "$FRONTEND_DIR"
      export NPM_CONFIG_CACHE="$NPM_CACHE_DIR"
      npm install
    )
  fi

  echo "启动前端..."
  (
    cd "$FRONTEND_DIR"
    export VITE_API_BASE_URL="${VITE_API_BASE_URL:-http://localhost:$selected_backend_port}"
    export NPM_CONFIG_CACHE="$NPM_CACHE_DIR"
    nohup npm run dev -- --host 0.0.0.0 --port "$selected_frontend_port" >"$FRONTEND_LOG_FILE" 2>&1 &
    echo $! >"$FRONTEND_PID_FILE"
  )
  echo "$selected_frontend_port" >"$FRONTEND_PORT_FILE"

  sleep 1
  pid=$(read_pid "$FRONTEND_PID_FILE" || true)
  if ! is_pid_running "$pid"; then
    rm -f "$FRONTEND_PORT_FILE"
    echo "前端启动失败，请检查日志: $FRONTEND_LOG_FILE"
    return 1
  fi

  if ! wait_for_http "http://127.0.0.1:$selected_frontend_port" "前端"; then
    stop_by_pid_file "前端" "$FRONTEND_PID_FILE"
    rm -f "$FRONTEND_PORT_FILE"
    echo "前端日志（最近 40 行）："
    tail -n 40 "$FRONTEND_LOG_FILE" 2>/dev/null || true
    return 1
  fi

  echo "前端已启动: http://localhost:$selected_frontend_port (PID: $pid)"
}

show_status() {
  backend_pid=$(read_pid "$BACKEND_PID_FILE" || true)
  frontend_pid=$(read_pid "$FRONTEND_PID_FILE" || true)
  backend_port=$(read_port_or_default "$BACKEND_PORT_FILE" "$BACKEND_PORT")
  frontend_port=$(read_port_or_default "$FRONTEND_PORT_FILE" "$FRONTEND_PORT")

  if is_pid_running "$backend_pid"; then
    echo "后端: 运行中 (PID: $backend_pid, URL: http://localhost:$backend_port)"
  else
    echo "后端: 未运行"
  fi

  if is_pid_running "$frontend_pid"; then
    echo "前端: 运行中 (PID: $frontend_pid, URL: http://localhost:$frontend_port)"
  else
    echo "前端: 未运行"
  fi
}

show_logs() {
  ensure_run_dir
  lines="${2:-80}"

  echo "===== backend.log (tail -n $lines) ====="
  if [ -f "$BACKEND_LOG_FILE" ]; then
    tail -n "$lines" "$BACKEND_LOG_FILE"
  else
    echo "暂无后端日志"
  fi

  echo ""
  echo "===== frontend.log (tail -n $lines) ====="
  if [ -f "$FRONTEND_LOG_FILE" ]; then
    tail -n "$lines" "$FRONTEND_LOG_FILE"
  else
    echo "暂无前端日志"
  fi
}

clean_all() {
  echo "开始清理（会先停止前后端）..."
  stop_by_pid_file "前端" "$FRONTEND_PID_FILE"
  stop_by_pid_file "后端" "$BACKEND_PID_FILE"

  rm -f "$BACKEND_PORT_FILE" "$FRONTEND_PORT_FILE"
  rm -rf "$RUN_DIR"
  rm -rf "$LOG_DIR"
  rm -rf "$FRONTEND_DIR/node_modules/.vite"
  rm -rf "$FRONTEND_DIR/.vite"

  echo "清理完成：已清除 .run、logs、旧 PID/日志、前端运行缓存（保留 data 持久化数据）"
}

usage() {
  cat <<USAGE
用法: ./dev.sh <命令>

命令:
  start      启动后端和前端
  stop       停止后端和前端
  restart    重启后端和前端
  status     查看运行状态
  logs [n]   查看日志 (默认最后 80 行)
  clean      停止并清理运行文件与缓存
  doctor     检查环境与端口占用

可选环境变量:
  BACKEND_PORT      后端目标端口 (默认 8080)
  FRONTEND_PORT     前端目标端口 (默认 5173)
  FORCE_FREE_PORTS  启动前先释放占用端口 (1/0，默认 1)
  LOG_DIR           运行日志目录 (默认 $ROOT_DIR/logs)
  DATA_DIR          SQLite 持久化目录 (默认 $ROOT_DIR/data)
  SQL_REVIEW_DB_PATH SQLite 文件完整路径 (优先级高于 DATA_DIR)
  VITE_API_BASE_URL 前端请求后端地址 (默认 http://localhost:后端实际端口)
USAGE
}

cmd="${1:-help}"

case "$cmd" in
  start)
    start_backend
    start_frontend
    ;;
  stop)
    stop_by_pid_file "前端" "$FRONTEND_PID_FILE"
    stop_by_pid_file "后端" "$BACKEND_PID_FILE"
    rm -f "$BACKEND_PORT_FILE" "$FRONTEND_PORT_FILE"
    ;;
  restart)
    stop_by_pid_file "前端" "$FRONTEND_PID_FILE"
    stop_by_pid_file "后端" "$BACKEND_PID_FILE"
    rm -f "$BACKEND_PORT_FILE" "$FRONTEND_PORT_FILE"
    start_backend
    start_frontend
    ;;
  status)
    show_status
    ;;
  logs)
    show_logs "$@"
    ;;
  clean)
    clean_all
    ;;
  doctor)
    doctor
    ;;
  help|-h|--help)
    usage
    ;;
  *)
    echo "未知命令: $cmd"
    usage
    exit 1
    ;;
esac

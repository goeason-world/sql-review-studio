<script setup>
import { computed, onMounted, ref } from 'vue';

const storageKey = 'sql-review-api-base';
const selectedEngineKey = 'sql-review-selected-engine';
const ruleConfigsKeyPrefix = 'sql-review-rule-configs-v1';
const activeRuleConfigKeyPrefix = 'sql-review-active-rule-config-v1';
const defaultApi = import.meta.env.VITE_API_BASE_URL || 'http://localhost:8080';
const alwaysEnabledRuleCodes = new Set([
  'empty_input',
  'missing_statement_terminator',
  'mongo_missing_statement_terminator',
  'fullwidth_statement_terminator',
]);

const apiBase = ref(localStorage.getItem(storageKey) || defaultApi);
const activeMenu = ref('review');
const selectedEngine = ref(localStorage.getItem(selectedEngineKey) || 'mysql');
const availableEngines = ref(['mysql', 'postgresql', 'mongodb']);

const mode = ref('paste');
const sqlText = ref('');
const file = ref(null);
const fileInputRef = ref(null);
const loading = ref(false);
const errorMsg = ref('');
const result = ref(null);
const lastRequestId = ref('');
const lastHistoryID = ref(0);
const lastSource = ref('');
const lastEngine = ref('mysql');
const levelFilter = ref('all');

const rulesVersion = ref('');
const rules = ref([]);
const ruleEnabledMap = ref({});
const ruleSearch = ref('');

const ruleConfigs = ref({});
const activeRuleConfigName = ref(loadActiveRuleConfigName(selectedEngine.value));
const configNameInput = ref(activeRuleConfigName.value || 'default');
const ruleConfigMsg = ref('');

const historyItems = ref([]);
const historyLoading = ref(false);
const historyError = ref('');
const historyTotal = ref(0);
const historyLimit = ref(20);
const historyOffset = ref(0);
const historyDetailModalVisible = ref(false);
const historyDetailLoading = ref(false);
const historyDetailError = ref('');
const historyDetail = ref(null);
const selectedHistoryIDs = ref([]);
const historySelectionMsg = ref('');

const normalizedApiBase = computed(() => apiBase.value.replace(/\/$/, ''));

const disabledRules = computed(() =>
  rules.value
    .filter((rule) => !isRuleAlwaysEnabled(rule.code) && ruleEnabledMap.value[rule.code] === false)
    .map((rule) => rule.code),
);

const enabledRuleCount = computed(() => rules.value.length - disabledRules.value.length);

const filteredRules = computed(() => {
  const keyword = ruleSearch.value.trim().toLowerCase();
  if (!keyword) {
    return rules.value;
  }
  return rules.value.filter((rule) => {
    return (
      rule.code.toLowerCase().includes(keyword)
      || rule.category.toLowerCase().includes(keyword)
      || rule.description.toLowerCase().includes(keyword)
    );
  });
});

const filteredIssues = computed(() => {
  if (!result.value || !Array.isArray(result.value.issues)) {
    return [];
  }
  if (levelFilter.value === 'all') {
    return result.value.issues;
  }
  return result.value.issues.filter((item) => item.level === levelFilter.value);
});

const canPrevHistory = computed(() => historyOffset.value > 0);
const canNextHistory = computed(() => historyOffset.value + historyLimit.value < historyTotal.value);
const hasHistorySelection = computed(() => selectedHistoryIDs.value.length > 0);
const historySelectionHint = computed(() => (
  hasHistorySelection.value ? '' : '请先勾选记录'
));
const clearSelectedLabel = computed(() => (
  hasHistorySelection.value ? `清空选择(${selectedHistoryIDs.value.length})` : '清空选择'
));
const deleteSelectedLabel = computed(() => (
  hasHistorySelection.value ? `删除选中(${selectedHistoryIDs.value.length})` : '删除选中'
));
const allHistorySelected = computed(() =>
  historyItems.value.length > 0
  && historyItems.value.every((item) => selectedHistoryIDs.value.includes(Number(item.id))),
);
const historyPageStart = computed(() => {
  if (!historyTotal.value || !historyItems.value.length) {
    return 0;
  }
  return historyOffset.value + 1;
});
const historyPageEnd = computed(() => {
  if (!historyTotal.value || !historyItems.value.length) {
    return 0;
  }
  return historyOffset.value + historyItems.value.length;
});
const configNames = computed(() => Object.keys(ruleConfigs.value).sort((a, b) => a.localeCompare(b, 'zh-CN')));

function ruleConfigsStorageKey() {
  return `${ruleConfigsKeyPrefix}-${selectedEngine.value}`;
}

function activeRuleConfigStorageKey() {
  return `${activeRuleConfigKeyPrefix}-${selectedEngine.value}`;
}

function loadActiveRuleConfigName(engine) {
  const storageKeyName = `${activeRuleConfigKeyPrefix}-${engine}`;
  return localStorage.getItem(storageKeyName) || 'default';
}

function syncActiveRuleConfigFromStorage() {
  activeRuleConfigName.value = loadActiveRuleConfigName(selectedEngine.value);
  configNameInput.value = activeRuleConfigName.value;
}

async function onEngineChange() {
  localStorage.setItem(selectedEngineKey, selectedEngine.value);
  ruleConfigMsg.value = '';
  syncActiveRuleConfigFromStorage();
  loadRuleConfigsFromStorage();
  historyOffset.value = 0;
  await loadRules();
  await loadHistoryList();
}

function switchMenu(menu) {
  activeMenu.value = menu;
  if (menu === 'history' && !historyLoading.value && !historyItems.value.length) {
    loadHistoryList();
  }
}

function levelText(level) {
  if (level === 'error') return '错误';
  if (level === 'warning') return '警告';
  return '提示';
}

function sourceText(source) {
  if (source === 'upload') return '文件上传';
  return '粘贴输入';
}

function sourceIcon(source) {
  if (source === 'upload') return '📁';
  return '⌨️';
}

function engineText(engine) {
  if (engine === 'postgresql') return 'PostgreSQL';
  if (engine === 'mongodb') return 'MongoDB';
  return 'MySQL';
}

function engineBadgeClass(engine) {
  if (engine === 'postgresql') return 'postgresql';
  if (engine === 'mongodb') return 'mongodb';
  return 'mysql';
}

function formatDateTime(raw) {
  if (!raw) return '-';
  const dt = new Date(raw);
  if (Number.isNaN(dt.getTime())) return raw;
  return dt.toLocaleString('zh-CN', { hour12: false });
}

function fillExample() {
  if (selectedEngine.value === 'postgresql') {
    sqlText.value = [
      'BEGIN;',
      "UPDATE users SET status = 'inactive' WHERE last_login_at < now() - interval '180 days';",
      "DELETE FROM orders WHERE created_at < now() - interval '365 days';",
      "SELECT * FROM users WHERE name ILIKE '%tom%';",
      'COMMIT;',
    ].join('\n');
    return;
  }

  if (selectedEngine.value === 'mongodb') {
    sqlText.value = [
      "db.users.updateMany({ status: 'pending' }, { $set: { status: 'inactive' } });",
      "db.orders.deleteMany({ createdAt: { $lt: new Date('2025-01-01') } });",
      "db.users.find({ name: /tom/i });",
    ].join('\n');
    return;
  }

  sqlText.value = [
    'START TRANSACTION;',
    "UPDATE users SET status='inactive' WHERE last_login < DATE_SUB(NOW(), INTERVAL 180 DAY);",
    "DELETE FROM orders WHERE created_at < DATE_SUB(NOW(), INTERVAL 365 DAY);",
    "SELECT * FROM users WHERE name LIKE '%tom%';",
    'COMMIT;',
  ].join('\n');
}

function clearInput() {
  sqlText.value = '';
  errorMsg.value = '';
}

function onFileChange(event) {
  const target = event.target;
  file.value = target.files && target.files[0] ? target.files[0] : null;
  errorMsg.value = '';
}

function triggerFileSelect() {
  if (!fileInputRef.value) {
    return;
  }
  fileInputRef.value.click();
}

function clearFile() {
  file.value = null;
  errorMsg.value = '';
  if (fileInputRef.value) {
    fileInputRef.value.value = '';
  }
}

function ensureDefaultRuleConfig() {
  if (!ruleConfigs.value || typeof ruleConfigs.value !== 'object' || Array.isArray(ruleConfigs.value)) {
    ruleConfigs.value = {};
  }
  if (!ruleConfigs.value.default || typeof ruleConfigs.value.default !== 'object') {
    ruleConfigs.value = {
      ...ruleConfigs.value,
      default: {},
    };
  }
}

function isRuleAlwaysEnabled(code) {
  return alwaysEnabledRuleCodes.has(code);
}

function normalizeRuleMap(rawConfig) {
  const source = rawConfig && typeof rawConfig === 'object' ? rawConfig : {};
  const nextMap = {};
  rules.value.forEach((rule) => {
    if (isRuleAlwaysEnabled(rule.code)) {
      nextMap[rule.code] = true;
      return;
    }
    nextMap[rule.code] = source[rule.code] !== false;
  });
  return nextMap;
}

function persistRuleConfigs() {
  localStorage.setItem(ruleConfigsStorageKey(), JSON.stringify(ruleConfigs.value));
}

function loadRuleConfigsFromStorage() {
  try {
    const raw = localStorage.getItem(ruleConfigsStorageKey());
    if (!raw) {
      ruleConfigs.value = { default: {} };
      return;
    }
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      ruleConfigs.value = parsed;
    }
  } catch {
    ruleConfigs.value = { default: {} };
  }
  ensureDefaultRuleConfig();
}

function snapshotCurrentRuleMap() {
  const snapshot = {};
  rules.value.forEach((rule) => {
    snapshot[rule.code] = isRuleEnabled(rule.code);
  });
  return snapshot;
}

function saveCurrentRuleConfigSnapshot() {
  if (!rules.value.length) {
    return;
  }
  ensureDefaultRuleConfig();
  const name = activeRuleConfigName.value || 'default';
  ruleConfigs.value = {
    ...ruleConfigs.value,
    [name]: snapshotCurrentRuleMap(),
  };
  persistRuleConfigs();
}

function applyRuleConfig(name) {
  ensureDefaultRuleConfig();
  const targetName = (name || 'default').trim() || 'default';

  if (!ruleConfigs.value[targetName]) {
    ruleConfigs.value = {
      ...ruleConfigs.value,
      [targetName]: {},
    };
    persistRuleConfigs();
  }

  activeRuleConfigName.value = targetName;
  configNameInput.value = targetName;
  localStorage.setItem(activeRuleConfigStorageKey(), targetName);

  if (rules.value.length) {
    ruleEnabledMap.value = normalizeRuleMap(ruleConfigs.value[targetName]);
  }
}

function saveCurrentRuleConfig() {
  const name = (configNameInput.value || activeRuleConfigName.value || 'default').trim() || 'default';
  activeRuleConfigName.value = name;
  localStorage.setItem(activeRuleConfigStorageKey(), name);

  ruleConfigs.value = {
    ...ruleConfigs.value,
    [name]: snapshotCurrentRuleMap(),
  };
  persistRuleConfigs();

  configNameInput.value = name;
  ruleConfigMsg.value = `已保存规则配置：${name}`;
}

function loadSelectedRuleConfig() {
  const name = (configNameInput.value || '').trim();
  if (!name) {
    ruleConfigMsg.value = '请先输入或选择配置名';
    return;
  }
  if (!ruleConfigs.value[name]) {
    ruleConfigMsg.value = `配置不存在：${name}`;
    return;
  }

  applyRuleConfig(name);
  ruleConfigMsg.value = `已加载规则配置：${name}`;
}

function deleteSelectedRuleConfig() {
  const name = (configNameInput.value || activeRuleConfigName.value || '').trim();
  if (!name) {
    ruleConfigMsg.value = '请先输入或选择配置名';
    return;
  }
  if (name === 'default') {
    ruleConfigMsg.value = '默认配置不可删除';
    return;
  }
  if (!ruleConfigs.value[name]) {
    ruleConfigMsg.value = `配置不存在：${name}`;
    return;
  }

  const nextConfigs = { ...ruleConfigs.value };
  delete nextConfigs[name];
  ruleConfigs.value = nextConfigs;
  persistRuleConfigs();
  applyRuleConfig('default');
  ruleConfigMsg.value = `已删除配置：${name}`;
}

function isRuleEnabled(code) {
  if (isRuleAlwaysEnabled(code)) {
    return true;
  }
  return ruleEnabledMap.value[code] !== false;
}

function toggleRule(code, enabled) {
  if (isRuleAlwaysEnabled(code) && !enabled) {
    return;
  }

  ruleEnabledMap.value = {
    ...ruleEnabledMap.value,
    [code]: isRuleAlwaysEnabled(code) ? true : !!enabled,
  };
  saveCurrentRuleConfigSnapshot();
}

function enableAllRules() {
  const map = {};
  rules.value.forEach((rule) => {
    map[rule.code] = true;
  });
  ruleEnabledMap.value = map;
  saveCurrentRuleConfigSnapshot();
}

function disableAllRules() {
  const map = {};
  rules.value.forEach((rule) => {
    map[rule.code] = isRuleAlwaysEnabled(rule.code);
  });
  ruleEnabledMap.value = map;
  saveCurrentRuleConfigSnapshot();
}

function consumeResult(payload) {
  result.value = {
    rulesVersion: payload.rulesVersion,
    checkedAt: payload.checkedAt,
    summary: payload.summary,
    issues: payload.issues || [],
    advice: payload.advice || [],
  };
  lastRequestId.value = payload.requestId || '';
  lastHistoryID.value = Number(payload.historyId || 0);
  lastSource.value = payload.source || '';
  lastEngine.value = payload.engine || selectedEngine.value;
  levelFilter.value = 'all';
}

async function restoreFromHistory(detailPayload) {
  if (!detailPayload || !detailPayload.checkResult) {
    return;
  }

  const historyEngine = detailPayload.engine || selectedEngine.value;
  if (historyEngine !== selectedEngine.value) {
    selectedEngine.value = historyEngine;
    localStorage.setItem(selectedEngineKey, selectedEngine.value);
    syncActiveRuleConfigFromStorage();
    loadRuleConfigsFromStorage();
    await loadRules();
  }

  sqlText.value = detailPayload.sqlText || '';
  mode.value = detailPayload.source === 'upload' ? 'upload' : 'paste';
  file.value = null;

  const disabledSet = new Set(Array.isArray(detailPayload.disabledRules) ? detailPayload.disabledRules : []);
  const nextMap = {};
  rules.value.forEach((rule) => {
    if (isRuleAlwaysEnabled(rule.code)) {
      nextMap[rule.code] = true;
      return;
    }
    nextMap[rule.code] = !disabledSet.has(rule.code);
  });
  ruleEnabledMap.value = nextMap;

  result.value = {
    rulesVersion: detailPayload.checkResult.rulesVersion,
    checkedAt: detailPayload.checkResult.checkedAt,
    summary: detailPayload.checkResult.summary,
    issues: detailPayload.checkResult.issues || [],
    advice: detailPayload.checkResult.advice || [],
  };
  lastRequestId.value = detailPayload.requestId || '';
  lastHistoryID.value = Number(detailPayload.id || 0);
  lastSource.value = detailPayload.source || '';
  lastEngine.value = historyEngine;
  levelFilter.value = 'all';

  activeMenu.value = 'review';
}

async function loadHistoryList() {
  historyLoading.value = true;
  historyError.value = '';
  try {
    const response = await fetch(
      `${normalizedApiBase.value}/api/v1/history?limit=${historyLimit.value}&offset=${historyOffset.value}`,
    );
    const data = await response.json();
    if (!response.ok) {
      throw new Error(data.error || '加载历史失败');
    }

    historyItems.value = Array.isArray(data.items) ? data.items : [];
    historyTotal.value = Number(data.total || 0);

    const pageIDSet = new Set(historyItems.value.map((item) => Number(item.id)));
    selectedHistoryIDs.value = selectedHistoryIDs.value.filter((id) => pageIDSet.has(Number(id)));
  } catch (error) {
    historyError.value = error.message || '加载历史失败';
    historyItems.value = [];
    selectedHistoryIDs.value = [];
  } finally {
    historyLoading.value = false;
  }
}

function isHistorySelected(id) {
  return selectedHistoryIDs.value.includes(Number(id));
}

function toggleHistorySelection(id, checked) {
  const targetID = Number(id);
  if (!targetID) {
    return;
  }

  if (checked) {
    if (!selectedHistoryIDs.value.includes(targetID)) {
      selectedHistoryIDs.value = [...selectedHistoryIDs.value, targetID];
    }
    return;
  }

  selectedHistoryIDs.value = selectedHistoryIDs.value.filter((itemID) => itemID !== targetID);
}

function toggleAllHistory(checked) {
  if (!checked) {
    selectedHistoryIDs.value = [];
    return;
  }
  selectedHistoryIDs.value = historyItems.value.map((item) => Number(item.id)).filter((id) => id > 0);
}

function clearHistorySelection() {
  selectedHistoryIDs.value = [];
  historySelectionMsg.value = '';
}

async function deleteHistoryByIDs(ids) {
  const targetIDs = Array.isArray(ids)
    ? [...new Set(ids.map((id) => Number(id)).filter((id) => Number.isInteger(id) && id > 0))]
    : [];

  if (!targetIDs.length) {
    throw new Error('请选择要删除的历史记录');
  }

  const response = await fetch(`${normalizedApiBase.value}/api/v1/history`, {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ids: targetIDs }),
  });
  const data = await response.json();
  if (!response.ok) {
    throw new Error(data.error || '删除历史失败');
  }

  const deleted = Number(data.deleted || 0);
  if (deleted > 0) {
    selectedHistoryIDs.value = selectedHistoryIDs.value.filter((id) => !targetIDs.includes(id));

    if (historyDetail.value && targetIDs.includes(Number(historyDetail.value.id))) {
      closeHistoryDetail();
    }

    const nextTotal = Math.max(0, historyTotal.value - deleted);
    historyTotal.value = nextTotal;
    if (historyOffset.value >= nextTotal && historyOffset.value > 0) {
      historyOffset.value = Math.max(0, historyOffset.value - historyLimit.value);
    }

    await loadHistoryList();
  }

  return deleted;
}

async function deleteHistoryItem(id) {
  const targetID = Number(id);
  if (!targetID) {
    return;
  }

  if (!window.confirm(`确认删除历史记录 #${targetID} 吗？`)) {
    return;
  }

  const typed = window.prompt(`为避免误删，请输入 DELETE 确认删除历史记录 #${targetID}`);
  if (typed === null) {
    historySelectionMsg.value = '已取消删除';
    return;
  }
  if (typed.trim().toUpperCase() !== 'DELETE') {
    historySelectionMsg.value = '口令不匹配，未执行删除';
    return;
  }

  try {
    const deleted = await deleteHistoryByIDs([targetID]);
    historySelectionMsg.value = deleted > 0 ? `已删除 1 条历史记录` : '未删除任何记录';
  } catch (error) {
    historyError.value = error.message || '删除历史失败';
  }
}

async function deleteSelectedHistory() {
  if (!hasHistorySelection.value) {
    return;
  }

  const count = selectedHistoryIDs.value.length;
  if (!window.confirm(`确认删除选中的 ${count} 条历史记录吗？`)) {
    return;
  }

  const typed = window.prompt(`为避免误删，请输入 DELETE 确认删除 ${count} 条历史记录`);
  if (typed === null) {
    historySelectionMsg.value = '已取消删除';
    return;
  }
  if (typed.trim().toUpperCase() != 'DELETE') {
    historySelectionMsg.value = '口令不匹配，未执行删除';
    return;
  }

  try {
    const deleted = await deleteHistoryByIDs(selectedHistoryIDs.value);
    historySelectionMsg.value = deleted > 0 ? `已删除 ${deleted} 条历史记录` : '未删除任何记录';
  } catch (error) {
    historyError.value = error.message || '删除历史失败';
  }
}

async function nextHistoryPage() {
  if (!canNextHistory.value) {
    return;
  }
  historyOffset.value += historyLimit.value;
  await loadHistoryList();
}

async function prevHistoryPage() {
  if (!canPrevHistory.value) {
    return;
  }
  historyOffset.value = Math.max(0, historyOffset.value - historyLimit.value);
  await loadHistoryList();
}

async function viewHistoryDetail(id) {
  if (!id) {
    return;
  }
  historyDetailModalVisible.value = true;
  historyDetailLoading.value = true;
  historyDetailError.value = '';
  try {
    const response = await fetch(`${normalizedApiBase.value}/api/v1/history/${id}`);
    const data = await response.json();
    if (!response.ok) {
      throw new Error(data.error || '加载详情失败');
    }
    historyDetail.value = data;
  } catch (error) {
    historyDetailError.value = error.message || '加载详情失败';
    historyDetail.value = null;
  } finally {
    historyDetailLoading.value = false;
  }
}

function closeHistoryDetail() {
  historyDetailModalVisible.value = false;
  historyDetailLoading.value = false;
  historyDetailError.value = '';
  historyDetail.value = null;
}

async function useHistoryResult() {
  if (!historyDetail.value) {
    return;
  }
  await restoreFromHistory(historyDetail.value);
  closeHistoryDetail();
}

async function submitByText() {
  if (!sqlText.value.trim()) {
    errorMsg.value = '请先粘贴 SQL';
    return;
  }

  loading.value = true;
  errorMsg.value = '';

  try {
    const response = await fetch(`${normalizedApiBase.value}/api/v1/check`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        sql: sqlText.value,
        engine: selectedEngine.value,
        disabledRules: disabledRules.value,
      }),
    });
    const data = await response.json();
    if (!response.ok) {
      throw new Error(data.error || '检查失败');
    }
    consumeResult(data);
    if (data.historyWarning) {
      errorMsg.value = data.historyWarning;
    }
    await loadHistoryList();
  } catch (error) {
    errorMsg.value = error.message || '请求失败';
  } finally {
    loading.value = false;
  }
}

async function submitByFile() {
  if (!file.value) {
    errorMsg.value = '请先选择 SQL 文件';
    return;
  }

  loading.value = true;
  errorMsg.value = '';

  try {
    const formData = new FormData();
    formData.append('file', file.value);
    formData.append('engine', selectedEngine.value);
    formData.append('disabledRules', JSON.stringify(disabledRules.value));

    const response = await fetch(`${normalizedApiBase.value}/api/v1/check`, {
      method: 'POST',
      body: formData,
    });
    const data = await response.json();
    if (!response.ok) {
      throw new Error(data.error || '检查失败');
    }
    consumeResult(data);
    if (data.historyWarning) {
      errorMsg.value = data.historyWarning;
    }
    await loadHistoryList();
  } catch (error) {
    errorMsg.value = error.message || '请求失败';
  } finally {
    loading.value = false;
  }
}

async function loadRules() {
  try {
    const response = await fetch(
      `${normalizedApiBase.value}/api/v1/rules?engine=${encodeURIComponent(selectedEngine.value)}`,
    );
    const data = await response.json();
    if (!response.ok) {
      return;
    }

    selectedEngine.value = data.engine || selectedEngine.value;
    localStorage.setItem(selectedEngineKey, selectedEngine.value);

    availableEngines.value = Array.isArray(data.engines) && data.engines.length
      ? data.engines
      : ['mysql', 'postgresql', 'mongodb'];

    rules.value = Array.isArray(data.rules) ? data.rules : [];
    rulesVersion.value = data.rulesVersion || '';

    ensureDefaultRuleConfig();
    const targetConfigName = ruleConfigs.value[activeRuleConfigName.value] ? activeRuleConfigName.value : 'default';
    applyRuleConfig(targetConfigName);
  } catch {
    rules.value = [];
  }
}

onMounted(async () => {
  apiBase.value = normalizedApiBase.value;
  syncActiveRuleConfigFromStorage();
  loadRuleConfigsFromStorage();
  await loadRules();
  await loadHistoryList();
});
</script>

<template>
  <div class="layout">
    <header class="topbar card">
      <div class="brand">
        <div class="top-title">SQL Review Console</div>
        <div class="top-sub">为研发与变更管理提供发布前 SQL 质量门禁，统一审查标准，前置识别高风险变更，降低生产事故与返工成本，提升交付可控性与可追溯性。</div>
      </div>
      <div class="top-tags">
        <span class="top-tag">自动预审</span>
        <span class="top-tag">多引擎</span>
        <span class="top-tag">历史追溯</span>
      </div>
    </header>

    <section class="card menu-card">
      <div class="menu-tabs">
        <button :class="['menu-btn', activeMenu === 'review' ? 'active' : '']" @click="switchMenu('review')">
          审查台
        </button>
        <button :class="['menu-btn', activeMenu === 'history' ? 'active' : '']" @click="switchMenu('history')">
          历史记录
        </button>
        <button :class="['menu-btn', activeMenu === 'rules' ? 'active' : '']" @click="switchMenu('rules')">
          规则配置
        </button>
      </div>
      <div class="menu-meta">
        当前引擎：{{ engineText(selectedEngine) }} · 规则配置：{{ activeRuleConfigName }} · 启用 {{ enabledRuleCount }}/{{ rules.length }}
      </div>
    </section>

    <template v-if="activeMenu === 'review'">
      <section class="stats-grid">
        <div class="card stat stat-neutral">
          <div class="stat-head">
            <div class="stat-label">语句数</div>
            <span class="risk-badge neutral">总量</span>
          </div>
          <div class="stat-value">{{ result ? result.summary.statementCount : '-' }}</div>
        </div>
        <div class="card stat stat-error">
          <div class="stat-head">
            <div class="stat-label">错误</div>
            <span class="risk-badge error">高风险</span>
          </div>
          <div class="stat-value">{{ result ? result.summary.errorCount : '-' }}</div>
        </div>
        <div class="card stat stat-warning">
          <div class="stat-head">
            <div class="stat-label">警告</div>
            <span class="risk-badge warning">中风险</span>
          </div>
          <div class="stat-value">{{ result ? result.summary.warningCount : '-' }}</div>
        </div>
        <div class="card stat stat-info">
          <div class="stat-head">
            <div class="stat-label">提示</div>
            <span class="risk-badge info">提示项</span>
          </div>
          <div class="stat-value">{{ result ? result.summary.infoCount : '-' }}</div>
        </div>
      </section>

      <main class="main-grid">
        <section class="card input-panel">
          <div class="panel-head input-head">
            <h3>提交 SQL</h3>
            <div class="input-head-tools">
              <div class="engine-inline">
                <label>SQL 引擎</label>
                <select v-model="selectedEngine" @change="onEngineChange">
                  <option v-for="engine in availableEngines" :key="engine" :value="engine">
                    {{ engineText(engine) }}
                  </option>
                </select>
              </div>
              <div class="tabs">
                <button :class="['tab', mode === 'paste' ? 'active' : '']" @click="mode = 'paste'">粘贴模式</button>
                <button :class="['tab', mode === 'upload' ? 'active' : '']" @click="mode = 'upload'">上传模式</button>
              </div>
            </div>
          </div>

          <div v-if="mode === 'paste'" class="input-body">
            <textarea v-model="sqlText" placeholder="在这里粘贴 SQL，可包含多条语句"></textarea>
            <div class="row">
              <button class="btn primary" :disabled="loading" @click="submitByText">
                {{ loading ? '检查中...' : '开始检查' }}
              </button>
              <button class="btn" :disabled="loading" @click="fillExample">示例 SQL</button>
              <button class="btn" :disabled="loading" @click="clearInput">清空</button>
              <button class="btn" :disabled="loading" @click="switchMenu('rules')">规则配置</button>
            </div>
          </div>

          <div v-else class="input-body">
            <div class="upload-box">
              <div class="upload-text">点击选择脚本文件（.sql / .txt / .js）</div>
              <div class="upload-actions">
                <button class="btn" :disabled="loading" type="button" @click="triggerFileSelect">选择文件</button>
                <div v-if="file" class="upload-file">已选择：{{ file.name }}</div>
                <div v-else class="upload-empty">尚未选择文件</div>
              </div>
              <input
                ref="fileInputRef"
                class="file-input"
                type="file"
                accept=".sql,.txt,.js,.mongo,text/plain"
                @change="onFileChange"
              />
            </div>
            <div class="row">
              <button class="btn primary" :disabled="loading" @click="submitByFile">
                {{ loading ? '检查中...' : '上传并检查' }}
              </button>
              <button class="btn" :disabled="loading" @click="clearFile">清空文件</button>
              <button class="btn" :disabled="loading" @click="switchMenu('rules')">规则配置</button>
            </div>
          </div>

          <p v-if="errorMsg" class="error-msg">{{ errorMsg }}</p>
          <p v-if="lastRequestId" class="meta">
            请求ID：{{ lastRequestId }} · 历史ID：{{ lastHistoryID || '-' }} · 引擎：{{ engineText(lastEngine) }} · 来源：{{ sourceText(lastSource) }} · 关闭规则：{{ disabledRules.length }}
          </p>
        </section>

        <section class="card result-panel">
          <div class="panel-head">
            <h3>检查结果</h3>
            <div v-if="result" class="actions">
              <select v-model="levelFilter">
                <option value="all">全部级别</option>
                <option value="error">仅错误</option>
                <option value="warning">仅警告</option>
                <option value="info">仅提示</option>
              </select>
            </div>
          </div>

          <div v-if="!result" class="empty">还没有结果，提交 SQL 后会在这里展示详细风险。</div>

          <template v-else>
            <div v-if="result.advice && result.advice.length" class="advice">
              <div class="advice-title">自动建议</div>
              <ul>
                <li v-for="(item, idx) in result.advice" :key="idx">{{ item }}</li>
              </ul>
            </div>

            <div v-if="filteredIssues.length" class="issue-list">
              <article v-for="(item, idx) in filteredIssues" :key="idx" :class="['issue', item.level]">
                <div class="issue-head">
                  <div class="issue-meta">
                    <span class="risk-badge neutral">#{{ item.statementIndex || '-' }}</span>
                    <span class="rule-badge">{{ item.rule }}</span>
                  </div>
                  <span :class="['risk-badge', item.level]">{{ levelText(item.level) }}</span>
                </div>
                <div class="issue-msg">{{ item.message }}</div>
                <div class="issue-suggestion"><span class="issue-label">建议</span>{{ item.suggestion }}</div>
                <pre v-if="item.statement">{{ item.statement }}</pre>
              </article>
            </div>
            <div v-else class="empty">当前筛选条件下无结果。</div>
          </template>
        </section>
      </main>
    </template>

    <section v-else-if="activeMenu === 'history'" class="card history-panel">
      <div class="panel-head">
        <h3>历史记录（{{ historyTotal }}）</h3>
        <div class="actions">
          <button class="btn" :disabled="historyLoading" @click="loadHistoryList">刷新</button>
          <button class="btn" :disabled="historyLoading || !canPrevHistory" @click="prevHistoryPage">上一页</button>
          <button class="btn" :disabled="historyLoading || !canNextHistory" @click="nextHistoryPage">下一页</button>
          <button
            class="btn"
            :disabled="historyLoading || !hasHistorySelection"
            :title="historySelectionHint"
            @click="clearHistorySelection"
          >
            {{ clearSelectedLabel }}
          </button>
          <button
            class="btn btn-danger"
            :disabled="historyLoading || !hasHistorySelection"
            :title="historySelectionHint"
            @click="deleteSelectedHistory"
          >
            {{ deleteSelectedLabel }}
          </button>
        </div>
      </div>
      <div v-if="historyError" class="error-msg">{{ historyError }}</div>
      <p v-if="historyTotal" class="history-summary-line">
        当前显示 {{ historyPageStart }} - {{ historyPageEnd }} / {{ historyTotal }} 条
        <span v-if="selectedHistoryIDs.length"> · 已选 {{ selectedHistoryIDs.length }} 条</span>
      </p>
      <p v-if="historySelectionMsg" class="meta">{{ historySelectionMsg }}</p>
      <div v-if="historyLoading" class="empty">历史记录加载中...</div>
      <div v-else-if="!historyItems.length" class="empty">暂无历史记录，提交一次 SQL 后会在这里显示。</div>
      <div v-else class="history-table-wrap">
        <table class="history-table">
          <thead>
            <tr>
              <th class="history-col-check">
                <input type="checkbox" :checked="allHistorySelected" @change="toggleAllHistory($event.target.checked)" />
              </th>
              <th>时间</th>
              <th>来源</th>
              <th>引擎</th>
              <th>风险</th>
              <th class="history-preview-col">SQL 预览</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in historyItems" :key="item.id">
              <td class="history-check-cell">
                <input
                  type="checkbox"
                  :checked="isHistorySelected(item.id)"
                  @change="toggleHistorySelection(item.id, $event.target.checked)"
                />
              </td>
              <td class="history-time-cell">{{ formatDateTime(item.createdAt) }}</td>
              <td class="history-source-cell">
                <span :class="['history-source-badge', item.source === 'upload' ? 'upload' : 'paste']">
                  <span class="history-source-icon">{{ sourceIcon(item.source) }}</span>
                  {{ sourceText(item.source) }}
                </span>
              </td>
              <td>
                <span :class="['engine-badge', engineBadgeClass(item.engine)]">{{ engineText(item.engine) }}</span>
              </td>
              <td>
                <div class="history-risk">
                  <span class="risk-badge error">错误 {{ item.summary.errorCount }}</span>
                  <span class="risk-badge warning">警告 {{ item.summary.warningCount }}</span>
                  <span class="risk-badge info">提示 {{ item.summary.infoCount }}</span>
                  <span class="risk-badge neutral">语句 {{ item.summary.statementCount }}</span>
                </div>
              </td>
              <td class="history-preview-cell" :title="item.sqlPreview || '-'">{{ item.sqlPreview || '-' }}</td>
              <td>
                <div class="history-actions">
                  <button class="btn btn-compact" @click="viewHistoryDetail(item.id)">详情</button>
                  <button class="btn btn-compact btn-danger" @click="deleteHistoryItem(item.id)">删除</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section v-else class="rules-grid">
      <section class="card config-panel">
        <div class="panel-head">
          <h3>规则配置</h3>
          <span class="meta">当前：{{ activeRuleConfigName }}</span>
        </div>

        <div class="config-form">
          <label>配置名称</label>
          <input v-model.trim="configNameInput" class="config-input" placeholder="例如：prod-safe / qa-loose" />
          <div class="row">
            <button class="btn primary" @click="saveCurrentRuleConfig">保存当前开关</button>
            <button class="btn" @click="loadSelectedRuleConfig">加载配置</button>
            <button class="btn" @click="deleteSelectedRuleConfig">删除配置</button>
          </div>
        </div>

        <div class="config-list">
          <button
            v-for="name in configNames"
            :key="name"
            :class="['config-chip', name === activeRuleConfigName ? 'active' : '']"
            @click="applyRuleConfig(name)"
          >
            {{ name }}
          </button>
        </div>

        <p v-if="ruleConfigMsg" class="meta">{{ ruleConfigMsg }}</p>

      </section>

      <section class="card rules-panel">
        <div class="panel-head">
          <h3>内置规则（{{ engineText(selectedEngine) }} · {{ filteredRules.length }}/{{ rules.length }}）</h3>
          <span v-if="rulesVersion" class="meta">版本：{{ rulesVersion }}</span>
        </div>
        <div class="rule-toolbar">
          <div class="row-inline">
            <span>启用 {{ enabledRuleCount }} / {{ rules.length }}</span>
            <button class="text-btn" @click="enableAllRules">全选</button>
            <button class="text-btn" @click="disableAllRules">全关</button>
          </div>
          <input v-model.trim="ruleSearch" class="rule-search" placeholder="搜索规则/分类/描述" />
        </div>
        <div class="rule-table-wrap">
          <table class="rule-table">
            <thead>
              <tr>
                <th class="col-toggle">启用</th>
                <th>规则</th>
                <th>级别</th>
                <th>分类</th>
                <th>说明</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="rule in filteredRules" :key="rule.code">
                <td class="col-toggle">
                  <input
                    type="checkbox"
                    :checked="isRuleEnabled(rule.code)"
                    :disabled="isRuleAlwaysEnabled(rule.code)"
                    :title="isRuleAlwaysEnabled(rule.code) ? '基础规则，始终启用' : ''"
                    @change="toggleRule(rule.code, $event.target.checked)"
                  />
                </td>
                <td>{{ rule.code }}</td>
                <td>{{ levelText(rule.level) }}</td>
                <td>{{ rule.category }}</td>
                <td>{{ rule.description }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </section>

    <div v-if="historyDetailModalVisible" class="modal-mask" @click.self="closeHistoryDetail">
      <div class="modal-card">
        <div class="panel-head">
          <h3>历史详情</h3>
          <button class="btn" @click="closeHistoryDetail">关闭</button>
        </div>
        <div v-if="historyDetailLoading" class="empty">详情加载中...</div>
        <div v-else-if="historyDetailError" class="error-msg">{{ historyDetailError }}</div>
        <div v-else-if="historyDetail" class="modal-body">
          <div class="meta-line">
            <span>ID: {{ historyDetail.id }}</span>
            <span>请求ID: {{ historyDetail.requestId }}</span>
            <span class="meta-engine">
              引擎:
              <span :class="['engine-badge', engineBadgeClass(historyDetail.engine)]">{{ engineText(historyDetail.engine) }}</span>
            </span>
            <span class="meta-source">
              来源:
              <span :class="['history-source-badge', historyDetail.source === 'upload' ? 'upload' : 'paste']">
                <span class="history-source-icon">{{ sourceIcon(historyDetail.source) }}</span>
                {{ sourceText(historyDetail.source) }}
              </span>
            </span>
            <span>文件: {{ historyDetail.fileName || '-' }}</span>
            <span>时间: {{ formatDateTime(historyDetail.createdAt) }}</span>
          </div>
          <div class="summary-mini">
            <span class="risk-badge neutral">语句 {{ historyDetail.checkResult.summary.statementCount }}</span>
            <span class="risk-badge error">错误 {{ historyDetail.checkResult.summary.errorCount }}</span>
            <span class="risk-badge warning">警告 {{ historyDetail.checkResult.summary.warningCount }}</span>
            <span class="risk-badge info">提示 {{ historyDetail.checkResult.summary.infoCount }}</span>
          </div>
          <h4>SQL 内容</h4>
          <pre class="history-sql">{{ historyDetail.sqlText }}</pre>
          <h4>风险明细</h4>
          <div v-if="historyDetail.checkResult.issues && historyDetail.checkResult.issues.length" class="issue-list">
            <article
              v-for="(item, idx) in historyDetail.checkResult.issues"
              :key="idx"
              :class="['issue', item.level]"
            >
              <div class="issue-head">
                <div class="issue-meta">
                  <span class="risk-badge neutral">#{{ item.statementIndex || '-' }}</span>
                  <span class="rule-badge">{{ item.rule }}</span>
                </div>
                <span :class="['risk-badge', item.level]">{{ levelText(item.level) }}</span>
              </div>
              <div class="issue-msg">{{ item.message }}</div>
              <div class="issue-suggestion"><span class="issue-label">建议</span>{{ item.suggestion }}</div>
              <pre v-if="item.statement">{{ item.statement }}</pre>
            </article>
          </div>
          <div v-else class="empty">无风险明细</div>
          <div class="row">
            <button class="btn primary" @click="useHistoryResult">恢复到当前工作区</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

/* Miao Panel web UI. Plain Vue 3 (global build), no build step. */
const { createApp, ref, reactive, computed, watch, onMounted, onUnmounted, nextTick, inject, provide } = Vue;

async function api(method, path, body) {
  const opts = { method, headers: { 'X-Miao': '1' }, credentials: 'same-origin' };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `请求失败（${res.status}）`);
  return data;
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// Minimal Markdown: escape everything first, then add a few known tags.
function md(text) {
  const inline = s => s
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  const out = [];
  let list = null, code = null;
  const closeList = () => { if (list) { out.push(`</${list}>`); list = null; } };
  for (const raw of esc(text || '').split('\n')) {
    if (raw.trim().startsWith('```')) {
      if (code === null) { closeList(); code = []; } else { out.push(`<pre>${code.join('\n')}</pre>`); code = null; }
      continue;
    }
    if (code !== null) { code.push(raw); continue; }
    let m;
    if ((m = raw.match(/^\s*#{1,4}\s+(.*)$/))) { closeList(); out.push(`<h4>${inline(m[1])}</h4>`); continue; }
    if ((m = raw.match(/^\s*[-*]\s+(.*)$/))) {
      if (list !== 'ul') { closeList(); out.push('<ul>'); list = 'ul'; }
      out.push(`<li>${inline(m[1])}</li>`); continue;
    }
    if ((m = raw.match(/^\s*\d+[.)、]\s*(.*)$/))) {
      if (list !== 'ol') { closeList(); out.push('<ol>'); list = 'ol'; }
      out.push(`<li>${inline(m[1])}</li>`); continue;
    }
    closeList();
    if (raw.trim() === '') continue;
    out.push(`<p>${inline(raw)}</p>`);
  }
  closeList();
  if (code !== null) out.push(`<pre>${code.join('\n')}</pre>`);
  return out.join('');
}

// Line icons (24x24, stroked with currentColor).
const ICONS = {
  layers: 'M12 3l9 5-9 5-9-5zM3 13l9 5 9-5',
  sparkles: 'M12 3l1.8 4.9L19 9.5l-5.2 1.6L12 16l-1.8-4.9L5 9.5l5.2-1.6zM19 15l.7 1.8 1.8.7-1.8.7-.7 1.8-.7-1.8-1.8-.7 1.8-.7z',
  bulb: 'M9 18h6M10 21h4M12 3a6 6 0 0 0-3.6 10.8c.6.5 1.1 1.2 1.1 2V16h5v-.2c0-.8.5-1.5 1.1-2A6 6 0 0 0 12 3z',
  clock: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2',
  server: 'M5 4h14a2 2 0 0 1 2 2v3a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2zM5 13h14a2 2 0 0 1 2 2v3a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-3a2 2 0 0 1 2-2zM7 7.5h.01M7 16.5h.01',
  sliders: 'M4 7h9M17 7h3M15 5v4M4 17h3M11 17h9M9 15v4',
  plus: 'M12 5v14M5 12h14',
  refresh: 'M20 11a8 8 0 1 0-2.3 5.7M20 4v7h-7',
  plug: 'M9 7H7a5 5 0 0 0 0 10h2M15 7h2a5 5 0 0 1 0 10h-2M8 12h8',
  bubble: 'M21 12a8 8 0 0 1-11.6 7.1L4 20l1.1-4.2A8 8 0 1 1 21 12z',
  trash: 'M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3',
  check: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM8 12.5l2.7 2.7L16 9.5',
  warn: 'M10.3 4.2L2.6 18a2 2 0 0 0 1.7 3h15.4a2 2 0 0 0 1.7-3L13.7 4.2a2 2 0 0 0-3.4 0zM12 10v4M12 17.5h.01',
  alert: 'M8.5 3h7L21 8.5v7L15.5 21h-7L3 15.5v-7zM12 8v5M12 16.5h.01',
  info: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v6M12 7.5h.01',
  chevron: 'M9 6l6 6-6 6',
  'arrow-up': 'M12 19V5M6 11l6-6 6 6',
  cpu: 'M7 7h10v10H7zM10 10h4v4h-4zM9 3v4M15 3v4M9 17v4M15 17v4M3 9h4M3 15h4M17 9h4M17 15h4',
  memory: 'M3 8h18v8H3zM7 16v3M12 16v3M17 16v3M7 11v2M12 11v2M17 11v2',
  disk: 'M3 13h18v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2zM3 13l3-8h12l3 8M7 16.5h.01',
  terminal: 'M4 5h16a1 1 0 0 1 1 1v12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1zM7 10l3 2.5L7 15M12.5 15H17',
  undo: 'M9 14L4 9l5-5M4 9h10.5a5.5 5.5 0 0 1 0 11H11',
  eye: 'M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
  chart: 'M4 20V10M10 20V4M16 20v-7M22 20H2',
  lock: 'M6 11h12v10H6zM8 11V7a4 4 0 0 1 8 0v4',
  stop: 'M8 8h8v8H8z',
  cloud: 'M7 18a4.5 4.5 0 0 1-.6-8.96A6 6 0 0 1 18 8.6 4.5 4.5 0 0 1 17.5 18z',
};

// In Miao Panel's own window, links that would open a new window go to
// the system browser instead.
document.addEventListener('click', e => {
  const a = e.target.closest && e.target.closest('a[target="_blank"]');
  if (!a || typeof window.miaoOpenExternal !== 'function') return;
  e.preventDefault();
  Promise.resolve(window.miaoOpenExternal(a.href)).catch(err => notify(String(err), 'error'));
});

// Toast shared by the app and its components.
const toast = reactive({ text: '', kind: 'ok' });
let toastTimer = null;
function notify(text, kind = 'ok') {
  toast.text = text; toast.kind = kind;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toast.text = ''; }, kind === 'error' ? 8000 : 3500);
}

const STEP_STATUS = {
  queued: { icon: 'clock', cls: 'info', text: '等待执行' },
  running: { icon: '', cls: 'info', text: '执行中' },
  done: { icon: 'check', cls: 'ok', text: '完成' },
  refused: { icon: 'info', cls: 'info', text: '没有执行：条件不满足，没有做任何修改' },
  rolled_back: { icon: 'warn', cls: 'warn', text: '失败了，已自动恢复原状' },
  failed: { icon: 'alert', cls: 'crit', text: '失败了，需要检查' },
  skipped: { icon: 'info', cls: 'info', text: '已跳过' },
  undone: { icon: 'undo', cls: 'info', text: '已撤销' },
  interrupted: { icon: 'warn', cls: 'warn', text: '中断：Miao Panel 在执行时被关闭，结果未知' },
};
const RISK_NAME = { R0: '只读', R1: '可撤销', R2: '影响线上', R3: '高风险' };

const mbText = v => v >= 1024 ? (v / 1024).toFixed(1) + ' GB' : (v || 0) + ' MB';

// A checklist the AI proposed: pick steps, run them, follow progress, undo.
// diffLines turns a unified diff into lines to show, without its headers.
function diffLines(diff) {
  return (diff || '').replace(/\n$/, '').split('\n').filter(l => !l.startsWith('--- ') && !l.startsWith('+++ ')).map(l => ({
    t: l || ' ', cls: l.startsWith('+') ? 'add' : l.startsWith('-') ? 'del' : l.startsWith('@@') ? 'hunk' : '',
  }));
}
function fileVerb(free, path) {
  const d = (free.diffs || []).find(x => x.path === path);
  if (!d) return '修改';
  if (d.diff.startsWith('软链接')) return '链接';
  if (/@@ -0,0 /.test(d.diff)) return '新建';
  if (/ \+0,0 @@/.test(d.diff)) return '删除';
  return '修改';
}
const scriptLines = text => (text || '').split('\n');
function serviceList(free) {
  return (free.services || []).map(svc => {
    const name = svc.replace(/^docker:/, '');
    const restart = (free.serviceOps || []).some(op => op.includes('restart') && op.includes(name));
    if (svc.startsWith('docker:')) return `重启容器 ${name}（中断几秒）`;
    return restart ? `重启 ${name}（会中断几秒）` : `重新加载 ${name}（不中断访问）`;
  });
}

const PlanCard = {
  props: { plan: { type: Object, required: true }, serverName: { type: String, default: '' } },
  setup(props) {
    const openLog = inject('openLog', () => {});
    const p = ref(props.plan);
    const picked = ref(new Set());
    const confirming = ref(false);
    const busy = ref(false);
    let timer = null;

    const steps = computed(() => p.value.stepList || []);
    const running = computed(() => p.value.status === 'running');
    const canPick = s => s.executable && !['done', 'running', 'queued'].includes(s.status);
    const chosen = computed(() => steps.value.map((s, i) => i).filter(i => picked.value.has(i)));
    const chosenSteps = computed(() => chosen.value.map(i => steps.value[i]));

    function resetPicks() {
      picked.value = new Set(steps.value.map((s, i) => i).filter(i => canPick(steps.value[i]) && !steps.value[i].status));
    }
    function toggle(i) {
      const next = new Set(picked.value);
      next.has(i) ? next.delete(i) : next.add(i);
      picked.value = next;
    }
    async function refresh() {
      try {
        p.value = await api('GET', `/api/plans/${p.value.id}`);
        if (p.value.status !== 'running') {
          stop();
          const failed = steps.value.some(s => ['failed', 'rolled_back', 'refused'].includes(s.status));
          notify(failed ? '执行结束，有步骤没有成功，请查看详情' : '全部执行完成', failed ? 'error' : 'ok');
        }
      } catch (e) { stop(); notify(e.message, 'error'); }
    }
    function poll() { stop(); timer = setInterval(refresh, 1500); }
    function stop() { if (timer) { clearInterval(timer); timer = null; } }
    async function run() {
      busy.value = true;
      try {
        p.value = await api('POST', `/api/plans/${p.value.id}/execute`, { steps: chosen.value });
        confirming.value = false;
        poll();
      } catch (e) { notify(e.message, 'error'); } finally { busy.value = false; }
    }
    async function undo(i) {
      if (!confirm(`确定要撤销「${steps.value[i].title || steps.value[i].summary}」吗？`)) return;
      busy.value = true;
      try {
        p.value = await api('POST', `/api/plans/${p.value.id}/steps/${i}/undo`);
        notify('已撤销');
      } catch (e) { notify(e.message, 'error'); await refresh(); } finally { busy.value = false; }
    }
    const undoable = computed(() => steps.value.filter(s => s.status === 'done' && s.reversible).length);
    async function undoAll() {
      if (!confirm(`确定要把这份清单里已执行的 ${undoable.value} 项全部撤销吗？会从最后一项开始，按相反的顺序逐项恢复。`)) return;
      busy.value = true;
      try {
        p.value = await api('POST', `/api/plans/${p.value.id}/undo`);
        notify('已全部撤销');
      } catch (e) { notify(e.message, 'error'); await refresh(); } finally { busy.value = false; }
    }

    resetPicks();
    if (running.value) poll();
    onUnmounted(stop);

    const status = s => STEP_STATUS[s.status] || null;
    const hasFree = computed(() => chosenSteps.value.some(s => s.capability === 'free_command'));
    const deltas = computed(() => {
      const b = p.value.before, a = p.value.after;
      if (!b || !a) return [];
      const rows = [
        ['可用内存', mbText(b.memAvailableMB), mbText(a.memAvailableMB)],
        ['swap', b.swapMB ? mbText(b.swapMB) : '没有', a.swapMB ? mbText(a.swapMB) : '没有'],
        ['系统盘使用率', b.rootDiskPct + '%', a.rootDiskPct + '%'],
        ['需要注意的问题', b.findings + ' 项', a.findings + ' 项'],
      ];
      return rows;
    });
    return { p, steps, running, canPick, picked, toggle, chosen, chosenSteps, confirming, busy, run, undo, undoable, undoAll, openLog, status, deltas,
      hasFree, diffLines, fileVerb, serviceList, scriptLines, riskName: r => RISK_NAME[r] || '' };
  },
  template: `
  <div class="plan">
    <div class="group-title">清单<span v-if="serverName"> · {{ serverName }}</span></div>
    <div class="group">
      <div class="row stack"><b>{{ p.title }}</b><div class="small secondary">{{ p.reason }}</div></div>
      <div class="row step" v-for="(s, i) in steps" :key="i" :class="{off: !s.executable}">
        <span class="step-mark">
          <span v-if="s.status === 'running'" class="spinner"></span>
          <ui-icon v-else-if="status(s)" :name="status(s).icon" :class="'st-' + status(s).cls"></ui-icon>
          <input v-else type="checkbox" :disabled="!canPick(s) || running" :checked="picked.has(i)" @change="toggle(i)" :aria-label="'选择第 ' + (i + 1) + ' 项'">
        </span>
        <div class="grow">
          <div>{{ s.summary }}</div>
          <div class="small tertiary" v-if="s.executable">
            <span class="risk">{{ s.risk }} {{ riskName(s.risk) }}</span>{{ s.via }} · {{ s.downtime }} · {{ s.reversible ? '可以撤销' : '无法撤销' }}
          </div>
          <div class="small secondary" v-else>暂时不能自动执行：{{ s.blocked }}</div>
          <div class="free" v-if="s.free && s.free.passed">
            <div class="free-title"><ui-icon name="sparkles"></ui-icon>AI 自定义操作（没有现成模板，由 AI 现场编写）</div>
            <div class="free-row"><span class="k">要做什么</span><span>{{ s.free.summary || s.free.goal }}</span></div>
            <div class="free-row"><span class="k">会改动</span><div>
              <div v-for="f in s.free.files" :key="f">{{ fileVerb(s.free, f) }} <span class="mono">{{ f }}</span></div>
              <div v-for="v in serviceList(s.free)" :key="v">{{ v }}</div>
            </div></div>
            <details class="free-more" v-for="d in s.free.diffs || []" :key="'d' + d.path">
              <summary><ui-icon name="chevron"></ui-icon>{{ d.path }} 的具体改动</summary>
              <div class="diff"><div v-for="(l, j) in diffLines(d.diff)" :key="j" :class="l.cls">{{ l.t }}</div></div>
            </details>
            <div class="free-row"><span class="k">不会</span><span>改动上面以外的文件、安装软件、访问网络、改动账号或防火墙</span></div>
            <div class="free-checks">
              <div v-if="s.free.dryRun === 'ok'"><ui-icon name="check" class="st-ok"></ui-icon>已在隔离环境里试运行，实际改动就是上面这些</div>
              <div v-else><ui-icon name="warn" class="st-warn"></ui-icon>这台服务器不能隔离试运行（{{ s.free.dryRunNote }}），所以要先做快照</div>
              <div><ui-icon name="check" class="st-ok"></ui-icon>独立审查通过：{{ s.free.review }}</div>
              <div><ui-icon name="check" class="st-ok"></ui-icon>执行前自动备份上面的文件，失败立即恢复</div>
              <div><ui-icon name="check" class="st-ok"></ui-icon>5 分钟保险：执行后如果 Miao Panel 连不上服务器，服务器会自己恢复原状</div>
            </div>
            <div class="free-row"><span class="k">最坏情况</span><span>改动让服务出问题 → 自动恢复，最长约 5 分钟</span></div>
            <details class="free-more">
              <summary><ui-icon name="chevron"></ui-icon>查看原始命令（高级）</summary>
              <div class="diff"><div v-for="(l, j) in scriptLines(s.free.script)" :key="j">{{ l || ' ' }}</div></div>
            </details>
          </div>
          <details class="free-more" v-else-if="s.free && s.free.script">
            <summary><ui-icon name="chevron"></ui-icon>查看 AI 写的命令</summary>
            <div class="diff"><div v-for="(l, j) in scriptLines(s.free.script)" :key="j">{{ l || ' ' }}</div></div>
          </details>
          <div class="small" v-if="status(s)" :class="'st-' + status(s).cls">{{ status(s).text }}<button v-if="s.logId" class="link small log-link" @click="openLog(s.logId)">查看执行日志</button></div>
          <div class="step-log" v-if="s.log && s.log.length"><div v-for="(l, j) in s.log" :key="j">{{ l }}</div></div>
        </div>
        <button v-if="s.status === 'done' && s.reversible" class="plain" @click="undo(i)" :disabled="busy || running"><ui-icon name="undo"></ui-icon>撤销</button>
      </div>
      <div class="row" v-if="deltas.length">
        <div class="grow">
          <div class="small secondary" style="margin-bottom: 4px">执行前后对比</div>
          <div class="delta" v-for="d in deltas" :key="d[0]"><span>{{ d[0] }}</span><span class="secondary">{{ d[1] }}</span><ui-icon name="chevron"></ui-icon><span>{{ d[2] }}</span></div>
        </div>
      </div>
      <div class="row plan-foot">
        <span class="grow small secondary" v-if="running"><span class="spinner inline"></span>正在执行，请不要关闭 Miao Panel……</span>
        <span class="grow small secondary" v-else-if="!steps.some(s => s.executable)">这份清单里没有能自动执行的项目</span>
 <span class="grow small secondary" v-else>执行前会先检查和备份；失败会自动恢复原状</span>
        <button v-if="undoable && !running" @click="undoAll" :disabled="busy"><ui-icon name="undo"></ui-icon>撤销全部</button>
        <button class="primary" :disabled="!chosen.length || running || busy" @click="confirming = true">执行选中的 {{ chosen.length }} 项</button>
      </div>
    </div>

    <div class="sheet-mask" v-if="confirming" @click.self="confirming = false">
      <div class="sheet" role="dialog" aria-label="确认执行">
        <h2>确认执行</h2>
        <p>将在 {{ serverName || '这台服务器' }} 上执行以下 {{ chosen.length }} 项修改：</p>
        <div class="group">
          <div class="row stack" v-for="s in chosenSteps" :key="s.summary">
            <div>{{ s.summary }}</div>
            <div class="small tertiary">{{ s.downtime }} · {{ s.reversible ? '可以撤销' : '无法撤销' }}</div>
          </div>
        </div>
        <div class="hint">每一步执行前会先检查条件并备份；某一步失败会自动恢复原状，并停止后面的步骤。</div>
        <div class="free-warn" v-if="hasFree"><ui-icon name="warn"></ui-icon><div>其中有 AI 现场编写的命令。它通过了检查、试运行和独立审查，也有备份和 5 分钟保险，能大幅降低风险，但做不到零风险。</div></div>
        <div class="sheet-actions">
          <button @click="confirming = false">取消</button>
          <button class="primary" @click="run" :disabled="busy">{{ hasFree ? '我了解，执行' : '确定执行' }}</button>
        </div>
      </div>
    </div>
  </div>`,
};

const ORIGIN_NAME = { ai: 'AI 检查', user: '你操作的', plan: '清单（你确认后执行）' };
const EXEC_STATUS = {
  running: { icon: '', cls: 'info', text: '执行中' },
  done: { icon: 'check', cls: 'ok', text: '完成' },
  undone: { icon: 'undo', cls: 'ok', text: '已回滚' },
  refused: { icon: 'info', cls: 'info', text: '没有执行（条件不满足）' },
  rolled_back: { icon: 'warn', cls: 'warn', text: '失败，已自动恢复' },
  failed: { icon: 'alert', cls: 'crit', text: '失败' },
  interrupted: { icon: 'warn', cls: 'warn', text: '中断' },
};

// Everything Miao Panel ran on servers, with details and rollback.
const ExecLog = {
  props: { focus: { type: Number, default: 0 } },
  setup(props) {
    const list = ref([]);
    const changesOnly = ref(false);
    const open = ref(0);
    const detail = reactive({});
    const busy = ref(false);
    const loading = ref(false);

    async function load() {
      loading.value = true;
      try { list.value = await api('GET', '/api/exec' + (changesOnly.value ? '?changes=1' : '')); }
      catch (e) { notify(e.message, 'error'); } finally { loading.value = false; }
    }
    async function toggle(id) {
      if (open.value === id) { open.value = 0; return; }
      open.value = id;
      try { detail[id] = await api('GET', `/api/exec/${id}`); } catch (e) { notify(e.message, 'error'); }
    }
    async function rollback(e) {
      const how = e.rollbackHow ? `\n\n回滚会：${e.rollbackHow}` : '';
      if (!confirm(`确定要回滚「${e.title}」吗？${how}`)) return;
      busy.value = true;
      try {
        const v = await api('POST', `/api/exec/${e.id}/rollback`);
        detail[e.id] = v;
        notify('已回滚');
      } catch (err) { notify(err.message, 'error'); }
      finally { busy.value = false; await load(); }
    }
    watch(changesOnly, load);
    watch(() => props.focus, id => { if (id) { open.value = 0; toggle(id); } });
    onMounted(async () => { await load(); if (props.focus) toggle(props.focus); });

    const status = e => EXEC_STATUS[e.status] || { icon: 'info', cls: 'info', text: e.status };
    const kindIcon = e => ({ read: 'eye', change: 'sliders', rollback: 'undo' }[e.kind] || 'terminal');
    const fmtTime = t => t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '';
    const undoLines = e => Object.entries(e.undo || {}).map(([k, v]) => `${k} = ${v}`).join('\n');
    return { list, changesOnly, open, detail, busy, loading, load, toggle, rollback, status, kindIcon, fmtTime, undoLines, originName: o => ORIGIN_NAME[o] || o };
  },
  template: `
  <div>
    <div class="log-bar">
      <span class="segmented">
        <button :class="{on: !changesOnly}" @click="changesOnly = false">全部</button>
        <button :class="{on: changesOnly}" @click="changesOnly = true">只看修改和回滚</button>
      </span>
      <span class="grow small tertiary">Miao Panel 在服务器上执行的每一条命令都记在这里，包括 AI 做的只读检查</span>
      <button class="plain" @click="load" :disabled="loading"><ui-icon name="refresh"></ui-icon>刷新</button>
    </div>
    <div class="group">
      <div v-if="!list.length" class="row secondary">{{ loading ? '正在读取……' : '还没有记录' }}</div>
      <template v-for="e in list" :key="e.id">
        <div class="row exec" :class="{active: open === e.id}" @click="toggle(e.id)">
          <ui-icon :name="kindIcon(e)" class="kind" :class="'k-' + e.kind"></ui-icon>
          <div class="grow">
            <div class="exec-title">{{ e.title }}</div>
            <div class="small tertiary">#{{ e.id }} · {{ e.serverName }} · {{ originName(e.origin) }} · {{ fmtTime(e.startedAt) }}<span v-if="e.via"> · {{ e.via }}</span></div>
          </div>
          <span class="exec-status small" :class="'st-' + status(e).cls">
            <span v-if="e.status === 'running'" class="spinner inline"></span><ui-icon v-else :name="status(e).icon"></ui-icon>{{ e.undoneBy ? '已回滚' : status(e).text }}
          </span>
          <button v-if="e.canRollback" @click.stop="rollback(e)" :disabled="busy"><ui-icon name="undo"></ui-icon>回滚</button>
          <ui-icon name="chevron" class="chev"></ui-icon>
        </div>
        <div class="exec-detail" v-if="open === e.id">
          <div v-if="!detail[e.id]" class="small secondary"><span class="spinner inline"></span>正在读取……</div>
          <template v-else>
            <div class="kv" v-if="detail[e.id].note"><span class="k">AI 的说明</span><span>{{ detail[e.id].note }}</span></div>
            <div class="kv"><span class="k">回滚</span>
              <span v-if="detail[e.id].canRollback">可以一键回滚：{{ detail[e.id].rollbackHow }}</span>
              <span v-else class="secondary">{{ detail[e.id].noRollback }}</span>
            </div>
            <div class="kv" v-if="detail[e.id].rollbackFile"><span class="k">服务器上的回滚文件</span>
              <span><code>{{ detail[e.id].rollbackFile }}</code><br><span class="small secondary">就算这台电脑上的 Miao Panel 不在了，也可以在服务器上用 root 执行 <code>sh {{ detail[e.id].rollbackFile }}</code> 恢复到修改前</span></span>
            </div>
            <div class="kv" v-if="detail[e.id].backupDir"><span class="k">备份位置</span><span><code>{{ detail[e.id].backupDir }}</code></span></div>
            <div class="kv" v-if="detail[e.id].planId"><span class="k">来自清单</span><span>#{{ detail[e.id].planId }} 第 {{ detail[e.id].stepIdx + 1 }} 项</span></div>
            <div class="kv" v-if="detail[e.id].undoOf"><span class="k">撤销的记录</span><span><button class="link" @click="toggle(detail[e.id].undoOf)">#{{ detail[e.id].undoOf }}</button></span></div>
            <div class="kv" v-if="detail[e.id].undoneBy"><span class="k">回滚记录</span><span><button class="link" @click="toggle(detail[e.id].undoneBy)">#{{ detail[e.id].undoneBy }}</button></span></div>
            <div class="sub-title">结果</div>
            <pre class="raw">{{ detail[e.id].output || '（没有输出）' }}</pre>
            <details><summary class="sub-title"><ui-icon name="chevron"></ui-icon>实际执行的命令（原样记录，给懂命令的人核对）</summary><pre class="raw">{{ detail[e.id].commands || '（没有记录）' }}</pre></details>
            <details v-if="undoLines(detail[e.id])"><summary class="sub-title"><ui-icon name="chevron"></ui-icon>回滚需要的数据（修改前的状态）</summary><pre class="raw">{{ undoLines(detail[e.id]) }}</pre></details>
            <details v-if="detail[e.id].script"><summary class="sub-title"><ui-icon name="chevron"></ui-icon>脚本全文：{{ detail[e.id].scriptName }}</summary><pre class="raw">{{ detail[e.id].script }}</pre></details>
          </template>
        </div>
      </template>
    </div>
  </div>`,
};

// Numbers the way people read them in Chinese: 1,284 / 12.9万 / 3.4亿.
function fmtCount(n) {
  n = Number(n) || 0;
  if (n < 10000) return Math.round(n).toLocaleString('zh-CN');
  if (n < 1e8) return (n / 1e4).toFixed(n < 1e5 ? 1 : 0) + '万';
  return (n / 1e8).toFixed(1) + '亿';
}
function fmtBytes(v) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0; v = Number(v) || 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return (i ? v.toFixed(1) : Math.round(v)) + ' ' + units[i];
}
function fmtBits(v) {
  const units = ['bps', 'Kbps', 'Mbps', 'Gbps'];
  let i = 0; v = Number(v) || 0;
  while (v >= 1000 && i < units.length - 1) { v /= 1000; i++; }
  return (i ? v.toFixed(1) : Math.round(v)) + ' ' + units[i];
}
// A round axis maximum and step: 0 / 2,500 / 5,000 / 7,500 / 10,000.
function niceScale(max, ticks = 4) {
  if (!(max > 0)) return { max: 1, step: 0.25 };
  const raw = max / ticks, mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map(m => m * mag).find(s => s >= raw);
  return { max: step * ticks, step };
}

// One series over time: 2px line with a light wash, a crosshair that snaps
// to the nearest point, and a tooltip listing every value at that time.
const LineChart = {
  props: {
    points: { type: Array, required: true },   // [{t, v}]
    extra: { type: Array, default: () => [] }, // same times, shown in the tooltip only
    label: { type: String, default: '' },
    extraLabel: { type: String, default: '' },
    format: { type: Function, default: fmtCount },
    extraFormat: { type: Function, default: fmtBytes },
    span: { type: Number, default: 24 },       // hours shown, picks the time format
  },
  setup(props) {
    const box = ref(null);
    const width = ref(640);
    const height = 240, m = { l: 52, r: 16, t: 12, b: 26 };
    const hover = ref(-1);
    let ro = null;
    onMounted(() => {
      ro = new ResizeObserver(es => { width.value = Math.max(280, Math.floor(es[0].contentRect.width)); });
      ro.observe(box.value);
    });
    onUnmounted(() => ro && ro.disconnect());

    const scale = computed(() => niceScale(Math.max(0, ...props.points.map(p => p.v))));
    const x = i => m.l + (props.points.length < 2 ? 0 : i * (width.value - m.l - m.r) / (props.points.length - 1));
    const y = v => m.t + (1 - v / scale.value.max) * (height - m.t - m.b);
    const line = computed(() => props.points.map((p, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(p.v).toFixed(1)}`).join(''));
    const area = computed(() => props.points.length ? `${line.value}L${x(props.points.length - 1).toFixed(1)},${y(0)}L${x(0).toFixed(1)},${y(0)}Z` : '');
    const yTicks = computed(() => {
      const out = [];
      for (let v = 0; v <= scale.value.max + 1e-9; v += scale.value.step) out.push({ v, y: y(v) });
      return out;
    });
    const timeText = t => {
      const d = new Date(t * 1000);
      const hm = d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false });
      return props.span <= 24 ? hm : `${d.getMonth() + 1}-${d.getDate()}${props.span <= 72 ? ' ' + hm : ''}`;
    };
    const xTicks = computed(() => {
      const n = props.points.length;
      if (!n) return [];
      const want = Math.max(2, Math.min(6, Math.floor(width.value / 110)));
      const out = [];
      for (let k = 0; k < want; k++) {
        const i = Math.round(k * (n - 1) / (want - 1));
        out.push({ x: x(i), text: timeText(props.points[i].t), anchor: k === 0 ? 'start' : k === want - 1 ? 'end' : 'middle' });
      }
      return out;
    });
    function onMove(e) {
      const r = box.value.getBoundingClientRect();
      const px = e.clientX - r.left, n = props.points.length;
      if (!n) return;
      const step = (width.value - m.l - m.r) / Math.max(1, n - 1);
      hover.value = Math.max(0, Math.min(n - 1, Math.round((px - m.l) / step)));
    }
    function onKey(e) {
      const n = props.points.length;
      if (!n) return;
      if (e.key === 'ArrowRight') { hover.value = Math.min(n - 1, hover.value < 0 ? 0 : hover.value + 1); e.preventDefault(); }
      if (e.key === 'ArrowLeft') { hover.value = Math.max(0, hover.value < 0 ? n - 1 : hover.value - 1); e.preventDefault(); }
    }
    const tip = computed(() => {
      const i = hover.value;
      if (i < 0 || i >= props.points.length) return null;
      const px = x(i);
      return {
        x: px, y: y(props.points[i].v), time: timeText(props.points[i].t), value: props.format(props.points[i].v),
        extra: props.extra[i] ? props.extraFormat(props.extra[i].v) : '', left: px > width.value * 0.6,
      };
    });
    return { box, width, height, m, line, area, yTicks, xTicks, onMove, onKey, hover, tip, format: props.format, fmtCount };
  },
  template: `
  <div class="lchart" ref="box" @pointermove="onMove" @pointerleave="hover = -1">
    <svg :width="width" :height="height" role="img" :aria-label="label + '走势图'" tabindex="0" @keydown="onKey" @blur="hover = -1">
      <g class="grid">
        <line v-for="t in yTicks" :key="t.v" :x1="m.l" :x2="width - m.r" :y1="t.y" :y2="t.y"></line>
      </g>
      <g class="ticks">
        <text v-for="t in yTicks" :key="'y' + t.v" :x="m.l - 8" :y="t.y + 4" text-anchor="end">{{ format(t.v) }}</text>
        <text v-for="(t, i) in xTicks" :key="'x' + i" :x="t.x" :y="height - 6" :text-anchor="t.anchor">{{ t.text }}</text>
      </g>
      <path class="area" :d="area"></path>
      <path class="line" :d="line"></path>
      <template v-if="tip">
        <line class="cross" :x1="tip.x" :x2="tip.x" :y1="m.t" :y2="height - m.b"></line>
        <circle class="dot" :cx="tip.x" :cy="tip.y" r="4"></circle>
      </template>
    </svg>
    <div class="ctip" v-if="tip" :style="{left: tip.x + 'px', transform: tip.left ? 'translateX(calc(-100% - 12px))' : 'translateX(12px)'}">
      <div class="small secondary">{{ tip.time }}</div>
      <div class="ctip-row"><span class="key"></span><b>{{ tip.value }}</b><span class="secondary">{{ label }}</span></div>
      <div class="ctip-row" v-if="tip.extra"><span class="key none"></span><b>{{ tip.extra }}</b><span class="secondary">{{ extraLabel }}</span></div>
    </div>
  </div>`,
};

const EO_RANGES = [{ h: 1, text: '1 小时' }, { h: 24, text: '24 小时' }, { h: 168, text: '7 天' }, { h: 720, text: '30 天' }];
// Reports already seen this session, by site and range, so going back to
// a view shows it at once while a fresh copy loads in the background.
const eoMemo = new Map();
const EO_FRESH_MS = 20000;
const clockText = t => new Date(t).toLocaleTimeString('zh-CN', { hour12: false });
// whenText: the time alone for today, otherwise the date too.
const whenText = t => {
  const d = new Date(t);
  if (isNaN(d)) return '';
  const hm = d.toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit' });
  return d.toDateString() === new Date().toDateString() ? hm : `${d.getMonth() + 1}月${d.getDate()}日 ${hm}`;
};
const TOP_NAMES = { url: '热门路径', country: '国家/地区', status: '状态码', ip: '访问最多的 IP' };

// 网站统计: EdgeOne analytics for one site or domain.
const LEVEL_ICON = { ok: 'check', warn: 'warn', crit: 'alert', info: 'info' };

const CertPage = {
  props: { configured: Boolean, active: Boolean },
  emits: ['ask', 'settings'],
  setup(props, { emit }) {
    const data = ref(null);
    const loading = ref(false);
    const error = ref('');
    const loadedAt = ref(0);
    let seq = 0;
    // The server answers at once with the last overview it has, even one
    // from before Miao Panel restarted; if that is old it is shown while a
    // new one is gathered (refreshing), then replaced.
    async function load(refresh) {
      const n = ++seq;
      loading.value = true; error.value = '';
      try {
        let d = await api('GET', '/api/certificates' + (refresh ? '?refresh=1' : ''));
        if (n !== seq) return;
        data.value = d;
        if (d.refreshing) {
          d = await api('GET', '/api/certificates?wait=1');
          if (n !== seq) return;
          data.value = d;
        }
        loadedAt.value = Date.now();
      } catch (e) { if (n === seq) error.value = e.message; }
      finally { if (n === seq) loading.value = false; }
    }
    onMounted(() => load(false));
    watch(() => props.active, on => { if (on && !loading.value && Date.now() - loadedAt.value > 5 * 60 * 1000) load(false); });
    const entries = computed(() => (data.value && data.value.entries) || []);
    const live = computed(() => (data.value && data.value.live) || []);
    const counts = computed(() => {
      const e = entries.value.filter(x => x.daysLeft != null);
      return {
        total: e.length,
        auto: e.filter(x => x.autoRenew).length,
        soon: e.filter(x => x.daysLeft >= 0 && x.daysLeft < 30 && !x.autoRenew).length,
        bad: entries.value.filter(x => x.level === 'crit').length + live.value.filter(x => x.level === 'crit').length,
      };
    });
    const date = t => t ? new Date(t).toLocaleDateString('zh-CN') : '—';
    const others = e => (e.names || []).filter(n => n !== e.domain).join('、');
    function action(e) {
      if (e.source === '1panel' && e.level !== 'ok' && e.canRenew) return ['让 AI 续签', `帮我立即续签 1Panel 里 ${e.domain} 的证书，并看看自动续签为什么没有成功`];
      if (e.source === '1panel' && !e.autoRenew && e.canRenew) return ['开启自动续签', `帮我开启 1Panel 里 ${e.domain} 证书的自动续签`];
      if (e.source === 'eo' && e.renew === '—') return ['开启 HTTPS', `帮 ${e.domain} 开启 HTTPS（EdgeOne 免费证书，自动续签）`];
      if (e.level === 'crit' || e.level === 'warn') return ['让 AI 处理', `${e.domain} 的证书${e.status}，帮我看看怎么处理`];
      return null;
    }
    const ask = text => emit('ask', text);
    return { data, loading, error, load, whenText, entries, live, counts, date, others, action, ask, icon: l => LEVEL_ICON[l] || 'info' };
  },
  template: `
  <div>
    <div class="page-head"><p>所有网站的 HTTPS 证书：EdgeOne、腾讯云 SSL 证书、各台服务器的 1Panel，以及实际访问时拿到的证书。</p></div>
    <div class="filter-row">
      <button @click="load(true)" :disabled="loading"><ui-icon name="refresh"></ui-icon>刷新</button>
      <button @click="ask('我想给网站申请 HTTPS 证书并开启自动续签，域名是：')"><ui-icon name="plus"></ui-icon>申请证书</button>
      <span class="small tertiary live-note" v-if="data"><template v-if="loading"><span class="spinner inline"></span>正在重新检查，下面是 {{ whenText(data.checkedAt) }} 的结果</template><template v-else>检查于 {{ whenText(data.checkedAt) }}</template></span>
      <span class="grow"></span>
      <button class="primary" @click="ask('检查一下我所有网站的 HTTPS 证书：有没有快到期、已经过期、没有自动续签或者申请失败的？有问题帮我处理。')"><ui-icon name="sparkles"></ui-icon>让 AI 检查</button>
    </div>
    <div class="notice" v-if="loading && !data"><span class="spinner"></span>正在读取证书，并逐个访问网站确认（可能要十几秒）……</div>
    <div class="notice" v-if="error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ error }}</div>
    <template v-if="data">
      <div class="tiles">
        <div class="tile"><div class="label"><ui-icon name="lock"></ui-icon>证书</div><div class="value">{{ counts.total }}</div><div class="sub">有到期时间的证书</div></div>
        <div class="tile"><div class="label"><ui-icon name="refresh"></ui-icon>自动续签</div><div class="value">{{ counts.auto }}</div><div class="sub">由 EdgeOne 或 1Panel 自动续签</div></div>
        <div class="tile"><div class="label"><ui-icon name="clock"></ui-icon>30 天内到期</div><div class="value">{{ counts.soon }}</div><div class="sub">而且不会自动续签</div></div>
        <div class="tile"><div class="label"><ui-icon name="alert"></ui-icon>有问题</div><div class="value">{{ counts.bad }}</div><div class="sub">已过期、申请失败或访问异常</div></div>
      </div>

      <div class="group-title">证书</div>
      <div class="group table-wrap">
        <table class="table cert-table" v-if="entries.length">
          <thead><tr><th>域名</th><th>在哪里</th><th>到期</th><th>续签</th><th>状态</th><th></th></tr></thead>
          <tbody>
            <tr v-for="(e, i) in entries" :key="i">
              <td><div class="mono-ish">{{ e.domain }}</div>
                <div class="small tertiary" v-if="others(e)">也包括 {{ others(e) }}</div>
                <div class="small tertiary" v-if="e.usedBy && e.usedBy.length">用在网站 {{ e.usedBy.join('、') }}</div></td>
              <td class="small">{{ e.where }}<div class="tertiary">{{ e.issuer }}</div></td>
              <td class="num">{{ date(e.notAfter) }}<div class="small tertiary" v-if="e.daysLeft != null">{{ e.daysLeft >= 0 ? '剩 ' + e.daysLeft + ' 天' : '已过期' }}</div></td>
              <td class="small">{{ e.renew }}</td>
              <td><span class="cert-st" :class="'st-' + e.level"><ui-icon :name="icon(e.level)"></ui-icon>{{ e.status }}</span>
                <div class="small tertiary" v-if="e.renewError">{{ e.renewError }}</div></td>
              <td><button class="link small" v-if="action(e)" @click="ask(action(e)[1])">{{ action(e)[0] }}</button></td>
            </tr>
          </tbody>
        </table>
        <div class="row small secondary" v-else>还没有找到证书。{{ configured ? '' : '在「设置 → 腾讯云」填好密钥后可以看到 EdgeOne 和腾讯云的证书；' }}配置了 1Panel 接口的服务器会显示 1Panel 里的证书。</div>
      </div>

      <template v-if="live.length">
        <div class="group-title">实际访问到的证书</div>
        <div class="group table-wrap">
          <table class="table cert-table">
            <thead><tr><th>网址</th><th>签发</th><th>到期</th><th>状态</th></tr></thead>
            <tbody>
              <tr v-for="l in live" :key="l.domain">
                <td>https://{{ l.domain }}</td>
                <td class="small">{{ l.issuer || '—' }}</td>
                <td class="num">{{ date(l.notAfter) }}<div class="small tertiary" v-if="l.daysLeft != null">剩 {{ l.daysLeft }} 天</div></td>
                <td><span class="cert-st" :class="'st-' + l.level"><ui-icon :name="icon(l.level)"></ui-icon>{{ l.status }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="small tertiary" style="margin: -16px 4px 22px">从这台电脑直接访问每个网站得到的证书，和访问者看到的一致。</p>
      </template>
      <div class="notice" v-for="n in data.notes || []" :key="n"><ui-icon name="info"></ui-icon>没能读取：{{ n }}</div>
    </template>
  </div>`,
};

const EoStats = {
  props: { configured: Boolean, active: Boolean },
  emits: ['ask', 'settings'],
  setup(props, { emit }) {
    const sites = ref([]);
    const domain = ref('');
    const hours = ref(24);
    const data = ref(null);
    const loading = ref(false);    // nothing to show yet
    const refreshing = ref(false); // updating what is shown, quietly
    const error = ref('');
    const updatedAt = ref(0);
    let seq = 0;
    async function loadSites() {
      if (!props.configured) return;
      try {
        sites.value = await api('GET', '/api/eo/sites');
        if (!domain.value && sites.value.length) domain.value = sites.value[0];
      } catch (e) { error.value = e.message; }
    }
    // load shows the remembered report for this view at once, then fetches
    // a fresh one unless it is only seconds old; force skips every cache.
    async function load(force) {
      if (!domain.value) return;
      const key = domain.value + '|' + hours.value;
      const memo = eoMemo.get(key);
      data.value = memo ? memo.data : null;
      updatedAt.value = memo ? memo.at : 0;
      if (!force && memo && Date.now() - memo.at < EO_FRESH_MS) return;
      const mine = ++seq;
      (data.value ? refreshing : loading).value = true;
      error.value = '';
      try {
        const r = await api('GET', `/api/eo/analytics?domain=${encodeURIComponent(domain.value)}&hours=${hours.value}${force ? '&refresh=1' : ''}`);
        eoMemo.set(key, { data: r, at: Date.now() });
        if (mine === seq) { data.value = r; updatedAt.value = Date.now(); }
      } catch (e) { if (mine === seq) error.value = e.message; }
      finally { if (mine === seq) { loading.value = false; refreshing.value = false; } }
    }
    // While the page is open it keeps itself up to date.
    const every = computed(() => hours.value <= 1 ? 30000 : 60000);
    let timer = null;
    function stop() { if (timer) { clearInterval(timer); timer = null; } }
    function start() { stop(); timer = setInterval(() => { if (document.visibilityState === 'visible') load(false); }, every.value); }
    watch([domain, hours], () => load(false));
    watch(every, () => { if (props.active) start(); });
    watch(() => props.active, on => { if (on) { load(false); start(); } else stop(); });
    watch(() => props.configured, v => { if (v) loadSites(); });
    onMounted(() => { loadSites(); if (props.active) start(); });
    onUnmounted(stop);
    const rangeText = computed(() => (EO_RANGES.find(r => r.h === hours.value) || {}).text || '');
    function ask() {
      emit('ask', `分析一下 ${domain.value} 最近${rangeText.value}的访问情况：访问量有没有异常变化，主要是谁在访问、访问了什么，有没有需要处理的问题？`);
    }
    const hit = computed(() => data.value && data.value.hitRatio >= 0 ? Math.round(data.value.hitRatio * 1000) / 10 : null);
    const timeText = t => new Date(t * 1000).toLocaleString('zh-CN', { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
    return { sites, domain, hours, data, loading, refreshing, error, updatedAt, every, load, ask, hit, EO_RANGES, TOP_NAMES, fmtCount, fmtBytes, fmtBits, timeText, clockText };
  },
  template: `
  <div>
    <div class="group" v-if="!configured">
      <div class="row"><ui-icon name="info" class="lg" style="color: var(--accent)"></ui-icon><div class="grow">网站统计来自腾讯云 EdgeOne，需要先填写腾讯云密钥。</div><button @click="$emit('settings')">去设置</button></div>
    </div>
    <template v-else>
      <div class="filter-row">
        <select v-model="domain" aria-label="站点或域名" :disabled="!sites.length">
          <option v-for="s in sites" :key="s" :value="s">{{ s }}</option>
        </select>
        <span class="segmented">
          <button v-for="r in EO_RANGES" :key="r.h" :class="{on: hours === r.h}" @click="hours = r.h">{{ r.text }}</button>
        </span>
        <button class="plain" @click="load(true)" :disabled="loading || refreshing || !domain"><ui-icon name="refresh"></ui-icon>刷新</button>
        <span class="small tertiary live-note" v-if="updatedAt">
          <span class="spinner inline" v-if="refreshing"></span>更新于 {{ clockText(updatedAt) }} · {{ every === 30000 ? '每 30 秒' : '每分钟' }}自动更新
        </span>
        <span class="grow"></span>
        <button class="primary" @click="ask" :disabled="!domain"><ui-icon name="sparkles"></ui-icon>让 AI 分析</button>
      </div>
      <div class="group" v-if="!sites.length && !error"><div class="row secondary">EdgeOne 里还没有站点。</div></div>
      <div class="group" v-if="error"><div class="row st-crit"><ui-icon name="alert"></ui-icon>{{ error }}</div></div>
      <p class="small tertiary" v-if="hours === 1" style="margin: -8px 4px 14px">最近 1 小时按分钟统计。EdgeOne 的统计一般有几分钟延迟，最右边的几个点可能还在补齐。</p>
      <div v-if="data">
        <div class="tiles five">
          <div class="tile"><div class="label">请求数</div><div class="value">{{ fmtCount(data.requests) }}</div><div class="sub">{{ data.domain }}</div></div>
          <div class="tile"><div class="label">流量</div><div class="value">{{ fmtBytes(data.bytes) }}</div><div class="sub">EdgeOne 响应</div></div>
          <div class="tile"><div class="label">峰值带宽</div><div class="value">{{ fmtBits(data.peakBps) }}</div><div class="sub">这段时间最高</div></div>
          <div class="tile"><div class="label">缓存命中率</div><div class="value">{{ hit === null ? '—' : hit + '%' }}</div>
            <div class="meter" v-if="hit !== null" role="meter" :aria-valuenow="hit" aria-valuemin="0" aria-valuemax="100" aria-label="缓存命中率"><div :style="{width: hit + '%'}"></div></div>
            <div class="sub">按流量计算</div></div>
          <div class="tile"><div class="label">平均响应</div><div class="value">{{ data.avgRespMs }} ms</div><div class="sub">EdgeOne 到访客</div></div>
        </div>

        <div class="group-title">请求数</div>
        <div class="group chart-card">
          <line-chart v-if="data.series && data.series.length" :points="data.series" :extra="data.flux || []" label="次请求" extra-label="流量" :span="hours"></line-chart>
          <div class="row secondary" v-else>这段时间没有访问数据</div>
          <details class="raw-box" v-if="data.series && data.series.length">
            <summary><ui-icon name="chevron"></ui-icon>数据表</summary>
            <table class="table">
              <thead><tr><th>时间</th><th class="num">请求数</th><th class="num">流量</th></tr></thead>
              <tbody><tr v-for="(p, i) in data.series" :key="p.t"><td class="num">{{ timeText(p.t) }}</td><td class="num">{{ fmtCount(p.v) }}</td><td class="num">{{ data.flux && data.flux[i] ? fmtBytes(data.flux[i].v) : '—' }}</td></tr></tbody>
            </table>
          </details>
        </div>

        <div class="top-grid">
          <div v-for="(name, key) in TOP_NAMES" :key="key">
            <div class="group-title">{{ name }}</div>
            <div class="group">
              <div class="row top-row" v-for="t in (data.tops && data.tops[key]) || []" :key="t.key">
                <div class="grow">
                  <div class="top-line"><span class="top-key" :title="t.key">{{ t.key }}</span><span class="num">{{ fmtCount(t.value) }}</span><span class="top-share">{{ (t.share * 100).toFixed(1) }}%</span></div>
                  <div class="share-bar"><div :style="{width: Math.max(1, t.share * 100) + '%'}"></div></div>
                </div>
              </div>
              <div class="row secondary" v-if="!data.tops || !(data.tops[key] || []).length">没有数据</div>
            </div>
          </div>
        </div>
      </div>
      <div class="group" v-else-if="loading"><div class="row secondary"><span class="spinner inline"></span>正在读取 EdgeOne 数据……</div></div>
    </template>
  </div>`,
};

const app = createApp({
  setup() {
    const tab = ref('servers');
    const servers = ref([]);
    const selectedId = ref(null);
    const current = ref(null);
    const busy = ref(false);
    const busyText = ref('');
    const info = ref({});
    const ai = ref({ hasKey: false });
    const presets = ref([]);
    const aiForm = reactive({});
    const spend = ref({});
    const plans = ref([]);
    const audit = ref([]);
    const logView = ref('exec');
    const logFocus = ref(0);
    const showAdd = ref(false);
    const addForm = reactive({});
    const messages = ref([]);
    const conversationId = ref('');
    const convs = ref([]);
    const showConvs = ref(false);
    const draft = ref('');
    const chatBusy = ref(false);
    const msgBox = ref(null);
    const op = reactive({ port: 0, host: '', apiKey: '', hasKey: false, info: '' });
    const tc = reactive({ configured: false, hint: '', secretId: '', secretKey: '', info: '' });
    const freeCmd = reactive({ enabled: false });
    async function loadFree() { Object.assign(freeCmd, await api('GET', '/api/settings/free-command')); }
    async function setFree(on) {
      if (on && !confirm('开启后，没有现成模板时 AI 可以现场编写命令。系统会替你检查、试运行、独立审查，并自动备份和设置 5 分钟保险，但做不到零风险。确定开启吗？')) return;
      await guarded(on ? '正在开启……' : '正在关闭……', async () => {
        Object.assign(freeCmd, await api('PUT', '/api/settings/free-command', { enabled: on }));
        notify(on ? '已开启 AI 自由命令' : '已关闭 AI 自由命令');
      });
    }
    const cloud = ref(null);          // Tencent Cloud instance behind the selected server
    const cloudList = ref([]);        // instances offered when adding a server
    const cloudPick = ref('');
    const suggestions = [
      '服务器现在的整体状况怎么样？',
      '内存占用是不是太高了？',
      '帮我做一次安全体检',
      '网站打不开，帮我排查一下',
    ];

    async function guarded(text, fn) {
      busy.value = true; busyText.value = text;
      try { return await fn(); } catch (e) { notify(e.message, 'error'); } finally { busy.value = false; }
    }

    async function loadServers() { servers.value = await api('GET', '/api/servers'); }
    async function loadSpend() { spend.value = await api('GET', '/api/usage'); }
    async function loadAI() {
      ai.value = await api('GET', '/api/settings/ai');
      Object.assign(aiForm, ai.value, { apiKey: '' });
    }
    async function select(id) {
      tab.value = 'servers';
      if (selectedId.value !== id) current.value = null;
      selectedId.value = id;
      cloud.value = null;
      if (tc.configured) {
        api('GET', `/api/servers/${id}/cloud`).then(r => { if (selectedId.value === id) cloud.value = r.instance || null; }).catch(() => {});
      }
      try {
        current.value = await api('GET', `/api/servers/${id}/profile`);
        if (current.value.server.adapter === '1panel') {
          Object.assign(op, await api('GET', `/api/servers/${id}/onepanel`), { apiKey: '', info: '' });
        }
      } catch (e) { notify(e.message, 'error'); }
    }
    async function saveOnePanel() {
      await guarded('正在保存……', async () => {
        const saved = await api('PUT', `/api/servers/${selectedId.value}/onepanel`, { port: op.port, host: op.host, apiKey: op.apiKey });
        Object.assign(op, saved, { apiKey: '', info: '' });
        notify('已保存');
      });
    }
    async function testOnePanel() {
      await guarded('正在连接 1Panel……', async () => {
        const r = await api('POST', `/api/servers/${selectedId.value}/onepanel/test`);
        op.info = '连接成功：' + r.info;
        notify('1Panel 接口可以正常使用');
      });
    }
    function setTencent(v) { Object.assign(tc, { configured: v.configured, hint: v.secretId || '', secretId: '', secretKey: '', info: '' }); }
    async function loadTencent() { setTencent(await api('GET', '/api/settings/tencent')); }
    async function saveTencent() {
      await guarded('正在保存……', async () => {
        setTencent(await api('PUT', '/api/settings/tencent', { secretId: tc.secretId, secretKey: tc.secretKey }));
        notify('已保存，正在测试……');
        await testTencent();
      });
    }
    async function testTencent() {
      await guarded('正在连接腾讯云……', async () => {
        const r = await api('POST', '/api/settings/tencent/test');
        tc.info = '连接成功：' + r.info;
        notify('腾讯云可以正常使用');
      });
    }
    async function clearTencent() {
      if (!confirm('确定要清除保存的腾讯云密钥吗？')) return;
      await guarded('正在清除……', async () => { setTencent(await api('DELETE', '/api/settings/tencent')); });
    }
    const seen = reactive({}); // pages opened at least once stay mounted
    function go(id) {
      tab.value = id;
      seen[id] = true;
      if (id === 'plans') api('GET', '/api/plans').then(v => { plans.value = v; }).catch(e => notify(e.message, 'error'));
      if (id === 'logs') loadAudit();
    }
    function loadAudit() { api('GET', '/api/audit').then(v => { audit.value = v; }).catch(e => notify(e.message, 'error')); }
    function openLog(id) { logView.value = 'exec'; logFocus.value = id; tab.value = 'logs'; }
    provide('openLog', openLog);

    function openAdd() {
      Object.assign(addForm, { name: '', host: '', port: 22, username: 'root', authKind: 'password', password: '', keyPath: '', keyPassphrase: '', instanceId: '', region: '' });
      showAdd.value = true;
      cloudPick.value = '';
      if (tc.configured) api('GET', '/api/tencent/servers').then(r => { cloudList.value = (r.servers || []).filter(s => s.publicIPs && s.publicIPs.length); }).catch(() => {});
    }
    function pickCloud() {
      const s = cloudList.value.find(x => x.id === cloudPick.value);
      if (!s) {
        Object.assign(addForm, { instanceId: '', region: '', authKind: addForm.authKind === 'tat' ? 'password' : addForm.authKind });
        return;
      }
      Object.assign(addForm, { name: s.name, host: s.publicIPs[0], username: /ubuntu/i.test(s.os) ? 'ubuntu' : 'root',
        instanceId: s.id, region: s.region, authKind: 'tat' });
    }
    function askAI(text) { draft.value = text; tab.value = 'chat'; newChat(); }
    const daysTo = t => t ? Math.floor((new Date(t) - Date.now()) / 86400000) : null;
    async function addServer() {
      await guarded('正在添加并测试连接……', async () => {
        const sv = await api('POST', '/api/servers', { ...addForm });
        showAdd.value = false;
        await loadServers();
        await select(sv.id);
        try {
          const r = await api('POST', `/api/servers/${sv.id}/test`);
          notify(`连接成功：${r.output}`);
          await select(sv.id);
        } catch (e) { notify('已添加，但连接失败：' + e.message, 'error'); }
      });
    }
    async function testConn() {
      await guarded('正在测试连接……', async () => {
        const r = await api('POST', `/api/servers/${selectedId.value}/test`);
        notify(`连接成功：${r.output}`);
        await select(selectedId.value);
      });
    }
    async function discover() {
      await guarded('正在识别服务器环境（只读，大约十几秒）……', async () => {
        current.value = await api('POST', `/api/servers/${selectedId.value}/discover`);
        await loadServers();
        notify('识别完成');
      });
    }
    async function removeServer() {
      if (!confirm(`确定要从 Miao Panel 里删除「${current.value.server.name}」吗？\n只是不再管理，不会影响服务器本身。`)) return;
      await guarded('正在删除……', async () => {
        await api('DELETE', `/api/servers/${selectedId.value}`);
        current.value = null; selectedId.value = null;
        await loadServers();
        if (servers.value.length) await select(servers.value[0].id);
      });
    }
    function askAbout() {
      draft.value = `帮我分析一下「${current.value.server.name}」这台服务器的状况，有没有需要处理的问题？`;
      tab.value = 'chat';
    }

    async function loadConvs() {
      try { convs.value = await api('GET', '/api/conversations'); } catch (e) { notify(e.message, 'error'); }
    }
    function viewMessage(m) {
      if (m.role !== 'assistant') return { id: m.id, role: m.role, text: m.text };
      return { id: m.id, role: 'assistant', text: m.text, steps: m.steps || [], usage: m.usage, cost: m.cost, currency: m.currency, plans: m.plans || [] };
    }
    async function openConv(id) {
      if (chatBusy.value) return;
      try {
        const c = await api('GET', `/api/conversations/${encodeURIComponent(id)}`);
        messages.value = c.messages.map(viewMessage);
        conversationId.value = c.id;
        showConvs.value = false;
        scrollChat();
      } catch (e) { notify(e.message, 'error'); }
    }
    async function deleteConv(c) {
      if (!confirm(`确定要删除对话「${c.title || '新对话'}」吗？\n里面生成过的清单仍然保留在「建议」里。`)) return;
      try {
        await api('DELETE', `/api/conversations/${encodeURIComponent(c.id)}`);
        if (c.id === conversationId.value) newChat();
        await loadConvs();
      } catch (e) { notify(e.message, 'error'); }
    }
    // The answer being streamed: its text so far, what the AI is looking
    // up (steps, and remarks it made between lookups) and its thinking.
    const live = ref(null);
    let controller = null;
    function onChatEvent(lv, e) {
      const findStep = state => lv.steps.find(s => s.kind === 'tool' && s.tool === e.tool && s.state === state);
      // Text written before a lookup was a remark on the way; it goes
      // above the lookup, in order.
      const toNote = () => {
        if (lv.text.trim()) lv.steps.push({ kind: 'note', text: lv.text });
        lv.text = '';
      };
      switch (e.type) {
        case 'start': conversationId.value = e.conversationId; break;
        case 'thinking': lv.thinking += e.text; if (!lv.text) lv.phase = 'think'; break;
        case 'text': lv.text += e.text; lv.phase = 'answer'; break;
        case 'reset': lv.text = ''; break;
        case 'round': toNote(); lv.thinking = ''; lv.phase = 'wait'; break;
        case 'prepare': toNote(); lv.steps.push({ kind: 'tool', tool: e.tool, args: '', state: 'prepare' }); lv.phase = 'tool'; break;
        case 'tool': {
          const st = findStep('prepare');
          if (st) Object.assign(st, { args: e.args, state: 'run' });
          else { toNote(); lv.steps.push({ kind: 'tool', tool: e.tool, args: e.args, state: 'run' }); }
          lv.phase = 'tool';
          break;
        }
        case 'tool_done': {
          const st = findStep('run');
          if (st) Object.assign(st, { state: e.error ? 'err' : 'ok', error: e.error || '' });
          lv.phase = 'wait';
          break;
        }
      }
    }
    const liveStatus = computed(() => {
      const lv = live.value;
      if (!lv) return '';
      if (lv.stopping) return '正在停止……';
      const busy = [...lv.steps].reverse().find(s => s.kind === 'tool' && (s.state === 'run' || s.state === 'prepare'));
      if (busy) return `正在${toolName(busy.tool)}……`;
      if (lv.phase === 'answer') return '';
      return lv.steps.length ? '正在整理查到的数据……' : '正在思考……';
    });
    const thinkTail = t => { const s = (t || '').replace(/\s+/g, ' ').trim(); return s.length > 140 ? '…' + s.slice(-140) : s; };
    function nearBottom() {
      const b = msgBox.value;
      return !b || b.scrollHeight - b.scrollTop - b.clientHeight < 120;
    }
    // A stream that ended without "done" (stopped, or the connection
    // dropped) still keeps what was shown.
    function keepLive(lv) {
      const steps = lv.steps.filter(s => s.kind === 'tool' && s.state !== 'prepare')
        .map(s => ({ tool: s.tool, args: s.args, output: '', error: s.error || '' }));
      if (lv.text || steps.length) messages.value.push({ role: 'assistant', text: lv.text, steps });
    }
    async function send() {
      const text = draft.value.trim();
      if (!text || chatBusy.value) return;
      messages.value.push({ role: 'user', text });
      draft.value = '';
      chatBusy.value = true;
      const lv = reactive({ text: '', thinking: '', steps: [], phase: 'wait', stopping: false });
      live.value = lv;
      controller = new AbortController();
      scrollChat();
      let final = null;
      try {
        const res = await fetch('/api/chat/stream', {
          method: 'POST', credentials: 'same-origin', signal: controller.signal,
          headers: { 'X-Miao': '1', 'Content-Type': 'application/json' },
          body: JSON.stringify({ conversationId: conversationId.value, message: text }),
        });
        if (!res.ok) {
          const d = await res.json().catch(() => ({}));
          throw new Error(d.error || `请求失败（${res.status}）`);
        }
        const reader = res.body.getReader();
        const dec = new TextDecoder();
        let buf = '';
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          const follow = nearBottom();
          buf += dec.decode(value, { stream: true });
          let i;
          while ((i = buf.indexOf('\n')) >= 0) {
            const line = buf.slice(0, i).trim();
            buf = buf.slice(i + 1);
            if (!line) continue;
            let e;
            try { e = JSON.parse(line); } catch { continue; }
            if (e.type === 'done') final = e.reply;
            else onChatEvent(lv, e);
          }
          if (follow) scrollChat();
        }
        if (!final) throw new Error('连接中断了，回答没有完整收到');
        const r = final;
        if (r.conversationId) conversationId.value = r.conversationId;
        const reply = r.reply || {};
        if (reply.text || (reply.steps && reply.steps.length) || (r.plans && r.plans.length) || !r.error)
          messages.value.push({ role: 'assistant', text: reply.text || '', steps: reply.steps || [], usage: reply.usage, cost: r.cost, currency: r.currency, plans: r.plans || [] });
        if (r.error) messages.value.push({ role: 'error', text: r.error });
      } catch (e) {
        keepLive(lv);
        messages.value.push({ role: 'error', text: e.name === 'AbortError' ? '已停止回答' : e.message });
      } finally {
        live.value = null;
        controller = null;
        chatBusy.value = false;
        loadSpend().catch(() => {});
        loadConvs();
        scrollChat();
      }
    }
    // Stops the answer on the server, which then sends what it has; if that
    // does not work, stops listening.
    async function stopAnswer() {
      const lv = live.value, ctl = controller;
      if (!lv || lv.stopping) return;
      lv.stopping = true;
      let stopped = false;
      if (conversationId.value) {
        try { stopped = (await api('POST', '/api/chat/stop', { conversationId: conversationId.value })).stopped; } catch { /* fall back below */ }
      }
      if (!stopped) ctl && ctl.abort();
      else setTimeout(() => { if (controller === ctl && ctl) ctl.abort(); }, 5000);
    }
    // Enter sends, but not while an input method (e.g. Chinese pinyin) is composing.
    function onEnter(e) {
      if (e.isComposing || e.keyCode === 229) return;
      e.preventDefault();
      send();
    }
    function newChat() { messages.value = []; conversationId.value = ''; showConvs.value = false; }
    // Today: 14:05; yesterday: 昨天 14:05; earlier: 9月24日.
    function relTime(t) {
      if (!t) return '';
      const d = new Date(t), now = new Date();
      const hm = d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false });
      if (d.toDateString() === now.toDateString()) return hm;
      const y = new Date(now); y.setDate(now.getDate() - 1);
      if (d.toDateString() === y.toDateString()) return '昨天 ' + hm;
      return d.getFullYear() === now.getFullYear() ? `${d.getMonth() + 1}月${d.getDate()}日` : `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日`;
    }
    function scrollChat() { nextTick(() => { if (msgBox.value) msgBox.value.scrollTop = msgBox.value.scrollHeight; }); }

    function applyPreset() {
      const pr = presets.value.find(p => p.id === aiForm.presetId);
      if (!pr) return;
      Object.assign(aiForm, { kind: pr.kind, baseUrl: pr.baseUrl, model: pr.model, inputPrice: pr.inputPrice, cachedPrice: pr.cachedPrice,
        outputPrice: pr.outputPrice, currency: pr.currency, echoReasoning: pr.echoReasoning, fallbackModel: pr.fallbackModel || '', name: pr.name, note: pr.note });
    }
    async function saveAI() {
      await guarded('正在保存……', async () => {
        const saved = await api('PUT', '/api/settings/ai', { ...aiForm });
        ai.value = saved;
        Object.assign(aiForm, saved, { apiKey: '' });
        conversationId.value = '';
        notify('已保存');
      });
    }
    async function testAI() {
      await guarded('正在测试 AI 模型……', async () => {
        const r = await api('POST', '/api/settings/ai/test');
        notify(`AI 模型可以正常使用，它回复：${r.reply}`);
      });
    }

    const p = computed(() => current.value && current.value.profile);
    const memPct = computed(() => {
      const m = p.value && p.value.memory;
      return m && m.totalMB ? Math.round((m.totalMB - m.availableMB) * 100 / m.totalMB) : 0;
    });
    const rootDisk = computed(() => {
      const d = (p.value && p.value.disks) || [];
      return d.find(x => x.mount === '/') || d[0] || null;
    });
    const envSub = computed(() => {
      const pn = p.value && p.value.panel;
      if (!pn || pn.kind === 'none') return '没有安装面板';
      return [pn.version, pn.port && `端口 ${pn.port}`].filter(Boolean).join(' · ') || '已安装';
    });
    const dockerText = computed(() => {
      const d = p.value && p.value.docker;
      if (!d || !d.status) return '未知';
      if (d.status === 'no') return '没有安装';
      if (d.status === 'unreachable') return '已安装，但服务没有运行';
      return `${d.status}，${(d.containers || []).length} 个容器`;
    });
    const spendText = computed(() => {
      const parts = Object.entries(spend.value || {}).map(([cur, v]) => money(v, cur));
      return parts.length ? parts.join(' + ') : '¥0';
    });
    const presetNote = computed(() => {
      const pr = presets.value.find(x => x.id === aiForm.presetId);
      return pr ? pr.note : '';
    });

    function money(v, cur) {
      const sym = cur === 'USD' ? '$' : '¥';
      if (!v) return sym + '0';
      return sym + (v < 0.01 ? v.toFixed(4) : v.toFixed(2));
    }
    const mb = v => v >= 1024 ? (v / 1024).toFixed(1) + ' GB' : (v || 0) + ' MB';
    const meterClass = v => v >= 90 ? 'crit' : v >= 80 ? 'warn' : '';
    const levelClass = l => ({ danger: 'crit', warn: 'warn' }[l] || 'info');
    const levelIcon = l => ({ danger: 'alert', warn: 'warn' }[l] || 'info');
    const levelName = l => ({ danger: '严重', warn: '注意', info: '提示' }[l] || l);
    const riskName = r => ({ R0: '只读', R1: '可撤销', R2: '影响线上', R3: '高风险' }[r] || '');
    const adapterName = a => ({ '1panel': '1Panel', bt: '宝塔', linux: '纯 Linux' }[a] || '未识别');
    const fmtTime = t => t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '';
    const serverName = id => id === 0 ? '腾讯云' : (servers.value.find(s => s.id === id) || { name: `服务器 ${id}` }).name;
    const parseSteps = s => { try { return JSON.parse(s); } catch { return []; } };
    const toolName = t => ({ list_servers: '查看服务器列表', get_server_profile: '读取服务器画像', refresh_server_profile: '重新识别服务器',
      run_check: '执行只读检查', propose_plan: '生成修改清单', tencent_dns: '查询 DNSPod 解析', tencent_eo: '查询 EdgeOne',
      tencent_servers: '查询腾讯云服务器', tencent_server_detail: '查看腾讯云服务器详情', tencent_eo_analytics: '分析网站访问数据',
      certificates: '查看 HTTPS 证书', tencent_eo_security: '查看 EdgeOne 安全防护', panel_websites: '查看 1Panel 网站' }[t] || t);
    // What a lookup is about, in a few words: the server, domain, checks.
    function toolDetail(tool, args) {
      let a;
      try { a = JSON.parse(args || '{}'); } catch { return ''; }
      if (!a || typeof a !== 'object') return '';
      const parts = [];
      if (a.server_id != null) parts.push(serverName(a.server_id));
      for (const k of ['domain', 'zone', 'instance', 'title']) if (a[k]) parts.push(String(a[k]));
      if (Array.isArray(a.checks) && a.checks.length) parts.push(a.checks.join('、'));
      return parts.join(' · ');
    }
    const actorName = a => ({ user: '你', ai: 'AI', system: '系统' }[a] || a);
    const actionName = a => ({ 'server.add': '添加服务器', 'server.delete': '删除服务器', 'server.test': '测试连接', 'server.discover': '识别环境',
      'server.hostkey.recorded': '记录服务器指纹', 'settings.ai': '修改 AI 设置', 'ai.chat': 'AI 对话', 'plan.propose': 'AI 生成清单',
      'plan.execute': '执行清单', 'plan.step': '执行步骤', 'plan.undo': '撤销步骤', 'exec.rollback': '回滚', 'onepanel.settings': '修改 1Panel 接口设置', 'settings.tencent': '修改腾讯云密钥' }[a] || a);

    onMounted(async () => {
      try {
        info.value = await api('GET', '/api/info');
        presets.value = await api('GET', '/api/ai/presets');
        await Promise.all([loadServers(), loadAI(), loadSpend(), loadConvs(), loadTencent(), loadFree()]);
        if (convs.value.length) await openConv(convs.value[0].id);
        if (servers.value.length) await select(servers.value[0].id);
      } catch (e) { notify(e.message, 'error'); }
    });

    return {
      tab, go, servers, selectedId, current, p, busy, busyText, toast, info, ai, presets, aiForm, presetNote,
      spendText, plans, audit, logView, logFocus, loadAudit, openLog, showAdd, addForm, messages, draft, chatBusy, msgBox, suggestions,
      select, openAdd, addServer, testConn, discover, removeServer, askAbout, send, onEnter, newChat, applyPreset, saveAI, testAI,
      convs, showConvs, conversationId, openConv, deleteConv, relTime,
      op, saveOnePanel, testOnePanel, tc, saveTencent, testTencent, clearTencent, freeCmd, setFree, seen,
      cloud, cloudList, cloudPick, pickCloud, askAI, daysTo, fmtBytes,
      memPct, rootDisk, envSub, dockerText, money, mb, meterClass, levelClass, levelIcon, levelName, riskName, adapterName,
      fmtTime, serverName, parseSteps, toolName, toolDetail, actorName, actionName, md, live, liveStatus, thinkTail, stopAnswer,
    };
  },
});

app.component('plan-card', PlanCard);
app.component('exec-log', ExecLog);
app.component('line-chart', LineChart);
app.component('eo-stats', EoStats);
app.component('cert-page', CertPage);
app.component('ui-icon', {
  props: { name: { type: String, required: true } },
  setup(props) { return { d: computed(() => ICONS[props.name] || '') }; },
  template: '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path :d="d"></path></svg>',
});

app.mount('#app');

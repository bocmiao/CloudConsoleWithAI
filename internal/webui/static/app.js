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
  if (!res.ok) {
    // The web edition's session ran out: back to the login page.
    if (res.status === 401 && data.code === 'login' && window.MIAO_MODE === 'server' && !/^\/api\/auth\//.test(path)) location.reload();
    const err = new Error(data.error || `请求失败（${res.status}）`);
    err.code = data.code || '';
    throw err;
  }
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
  up: 'M12 19V5M6 11l6-6 6 6',
  folder: 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z',
  file: 'M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8zM14 3v5h5',
  link: 'M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1',
  upload: 'M12 15V4M7 9l5-5 5 5M5 15v3a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-3',
  download: 'M12 4v11M7 10l5 5 5-5M5 15v3a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-3',
  copy: 'M9 9h10a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H9a1 1 0 0 1-1-1V10a1 1 0 0 1 1-1zM5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1',
  cut: 'M6 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM6 21a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM20 4L8.1 15.9M14.5 14.5L20 20M8.1 8.1L12 12',
  paste: 'M9 3h6v4H9zM9 5H6a1 1 0 0 0-1 1v14a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1h-3',
  pencil: 'M4 20h4L19 9l-4-4L4 16zM13.5 6.5l4 4',
  archive: 'M3 4h18v4H3zM5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8M10 12h4',
  cpu: 'M7 7h10v10H7zM10 10h4v4h-4zM9 3v4M15 3v4M9 17v4M15 17v4M3 9h4M3 15h4M17 9h4M17 15h4',
  memory: 'M3 8h18v8H3zM7 16v3M12 16v3M17 16v3M7 11v2M12 11v2M17 11v2',
  disk: 'M3 13h18v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2zM3 13l3-8h12l3 8M7 16.5h.01',
  terminal: 'M4 5h16a1 1 0 0 1 1 1v12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1zM7 10l3 2.5L7 15M12.5 15H17',
  undo: 'M9 14L4 9l5-5M4 9h10.5a5.5 5.5 0 0 1 0 11H11',
  eye: 'M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
  chart: 'M4 20V10M10 20V4M16 20v-7M22 20H2',
  lock: 'M6 11h12v10H6zM8 11V7a4 4 0 0 1 8 0v4',
  stop: 'M8 8h8v8H8z',
  prompt: 'M4 6l6 6-6 6M12 18h8',
  close: 'M6 6l12 12M18 6L6 18',
  menu: 'M4 7h16M4 12h16M4 17h16',
  globe: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3.6 9h16.8M3.6 15h16.8M12 3c-2.4 2.6-3.6 5.6-3.6 9s1.2 6.4 3.6 9M12 3c2.4 2.6 3.6 5.6 3.6 9s-1.2 6.4-3.6 9',
  bolt: 'M13 3L5 13.5h6L10 21l8-10.5h-6z',
  cloud: 'M7 18a4.5 4.5 0 0 1-.6-8.96A6 6 0 0 1 18 8.6 4.5 4.5 0 0 1 17.5 18z',
  bucket: 'M4 7h16l-1.6 12.2a2 2 0 0 1-2 1.8H7.6a2 2 0 0 1-2-1.8zM4 7c0-1.7 3.6-3 8-3s8 1.3 8 3',
  bell: 'M6 16V11a6 6 0 0 1 12 0v5l1.5 2h-15zM10 20.5a2 2 0 0 0 4 0',
  window: 'M4 5h16a1 1 0 0 1 1 1v12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1zM3 9h18M6 7h.01M9 7h.01',
  home: 'M4 10.5 12 4l8 6.5V19a1 1 0 0 1-1 1h-4.5v-5.5h-5V20H5a1 1 0 0 1-1-1z',
  inbox: 'M4 13.5 6.5 5h11L20 13.5V19a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1zM4 13.5h4.5l1.5 2.5h4l1.5-2.5H20',
  shield: 'M12 3.5l7 2.7v5.3c0 4.2-2.9 7.6-7 9-4.1-1.4-7-4.8-7-9V6.2z',
  history: 'M4 12a8 8 0 1 0 2.4-5.7L4 8.5M4 4v4.5h4.5M12 8v4.2l2.8 1.8',
  expand: 'M14 4h6v6M10 20H4v-6M20 4l-7 7M4 20l7-7',
  search: 'M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4',
  gear: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 13.5a7.6 7.6 0 0 0 0-3l2-1.6-2-3.4-2.4 1a7.6 7.6 0 0 0-2.6-1.5L14 2.5h-4l-.4 2.5a7.6 7.6 0 0 0-2.6 1.5l-2.4-1-2 3.4 2 1.6a7.6 7.6 0 0 0 0 3l-2 1.6 2 3.4 2.4-1a7.6 7.6 0 0 0 2.6 1.5l.4 2.5h4l.4-2.5a7.6 7.6 0 0 0 2.6-1.5l2.4 1 2-3.4z',
};

// Miao Panel's mark: the cat-ear M on a rounded tile, light or dark with
// the system.
const MIAO_MARK = 'M 103 438 C 79 438 64 422 66 398 L 79 174 C 81 136 88 99 103 73 C 107 66 114 65 120 72 L 246 221 Q 256 234 266 221 L 392 72 C 398 65 405 66 409 73 C 424 99 431 136 433 174 L 446 398 C 448 422 433 438 409 438 H 365 C 343 438 332 426 332 406 V 280 L 282 338 Q 256 365 230 338 L 180 280 V 406 C 180 426 169 438 147 438 Z';
const MiaoLogo = {
  template: `<svg viewBox="0 0 512 512" role="img" aria-label="Miao Panel"><rect class="logo-tile" width="512" height="512" rx="115"></rect>
    <rect class="logo-edge" x="4" y="4" width="504" height="504" rx="111" fill="none" stroke-width="8"></rect>
    <path class="logo-mark" transform="translate(96 99) scale(0.625)" :d="d"></path></svg>`,
  setup() { return { d: MIAO_MARK }; },
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
  emits: ['done'],
  setup(props, { emit }) {
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
          emit('done', p.value);
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
        emit('done', p.value);
      } catch (e) { notify(e.message, 'error'); await refresh(); } finally { busy.value = false; }
    }
    const undoable = computed(() => steps.value.filter(s => s.status === 'done' && s.reversible).length);
    async function undoAll() {
      if (!confirm(`确定要把这份清单里已执行的 ${undoable.value} 项全部撤销吗？会从最后一项开始，按相反的顺序逐项恢复。`)) return;
      busy.value = true;
      try {
        p.value = await api('POST', `/api/plans/${p.value.id}/undo`);
        notify('已全部撤销');
        emit('done', p.value);
      } catch (e) { notify(e.message, 'error'); await refresh(); } finally { busy.value = false; }
    }

    resetPicks();
    if (running.value) poll();
    onUnmounted(stop);
    // A fresher copy from the page (the 建议 list reloaded) replaces this one.
    watch(() => props.plan, v => {
      if (!v || v === p.value) return;
      p.value = v;
      resetPicks();
      if (v.status === 'running') poll();
    });

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
      <div class="row stack"><b>{{ p.title }}</b><div class="small secondary pre-line">{{ p.reason }}</div></div>
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
          <details class="free-more" v-for="d in s.diffs || []" :key="'c' + d.path" :open="!s.status">
            <summary><ui-icon name="chevron"></ui-icon>{{ d.path }} 的改动</summary>
            <div class="diff"><div v-for="(l, j) in diffLines(d.diff)" :key="j" :class="l.cls">{{ l.t }}</div></div>
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

const ORIGIN_NAME = { ai: 'AI 检查', user: '你操作的', plan: '清单（你确认后执行）', auto: '后台自动更新' };
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
  // server: only what was run on that server, for its workbench.
  props: { focus: { type: Number, default: 0 }, server: { type: Number, default: 0 } },
  setup(props) {
    const list = ref([]);
    const changesOnly = ref(false);
    const open = ref(0);
    const detail = reactive({});
    const busy = ref(false);
    const loading = ref(false);

    async function load() {
      loading.value = true;
      const q = new URLSearchParams();
      if (changesOnly.value) q.set('changes', '1');
      if (props.server) q.set('server', props.server);
      try { list.value = await api('GET', '/api/exec' + (q.toString() ? '?' + q : '')); }
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
              <span><code>{{ detail[e.id].rollbackFile }}</code><br><span class="small secondary">就算 Miao Panel 不在了，也可以在服务器上用 root 执行 <code>sh {{ detail[e.id].rollbackFile }}</code> 恢复到修改前</span></span>
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
    more: { type: Array, default: () => [] },  // [{label, points, format}] more rows in the tooltip
    span: { type: Number, default: 24 },       // hours shown, picks the time format
    partial: Boolean,                          // the last point is a period not over yet (dashed)
  },
  setup(props) {
    const box = ref(null);
    const width = ref(640);
    const height = 240;
    const hover = ref(-1);
    let ro = null;
    onMounted(() => {
      ro = new ResizeObserver(es => { width.value = Math.max(220, Math.floor(es[0].contentRect.width)); });
      ro.observe(box.value);
    });
    onUnmounted(() => ro && ro.disconnect());

    // Counts are whole: no 0.5 gridline labelled "1".
    const scale = computed(() => {
      const sc = niceScale(Math.max(0, ...props.points.map(p => p.v)));
      return props.format === fmtCount && sc.step < 1 ? { max: 4, step: 1 } : sc;
    });
    // The left margin fits the widest axis label (10.0 Kbps, 1.5万).
    const labels = computed(() => {
      const out = [];
      for (let v = 0; v <= scale.value.max + 1e-9; v += scale.value.step) out.push({ v, text: props.format(v) });
      return out;
    });
    const textWidth = t => [...t].reduce((w, c) => w + (c.charCodeAt(0) > 255 ? 11 : 6.4), 0);
    const m = reactive({ l: computed(() => Math.max(36, Math.ceil(Math.max(...labels.value.map(l => textWidth(l.text))) + 14))), r: 16, t: 12, b: 26 });
    const x = i => m.l + (props.points.length < 2 ? 0 : i * (width.value - m.l - m.r) / (props.points.length - 1));
    const y = v => m.t + (1 - v / scale.value.max) * (height - m.t - m.b);
    const path = list => list.map((p, i) => `${i ? 'L' : 'M'}${x(p.i).toFixed(1)},${y(p.v).toFixed(1)}`).join('');
    const indexed = computed(() => props.points.map((p, i) => ({ i, v: p.v })));
    const cut = computed(() => props.partial && props.points.length > 1 ? props.points.length - 1 : props.points.length);
    const line = computed(() => path(indexed.value.slice(0, cut.value)));
    const tail = computed(() => cut.value < props.points.length ? path(indexed.value.slice(cut.value - 1)) : '');
    const area = computed(() => props.points.length ? `${path(indexed.value)}L${x(props.points.length - 1).toFixed(1)},${y(0)}L${x(0).toFixed(1)},${y(0)}Z` : '');
    const yTicks = computed(() => labels.value.map(l => ({ v: l.v, text: l.text, y: y(l.v) })));
    const timeText = t => {
      const d = new Date(t * 1000);
      const hm = d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false });
      return props.span <= 24 ? hm : `${d.getMonth() + 1}-${d.getDate()}${props.span <= 72 ? ' ' + hm : ''}`;
    };
    // Labels at an even step (every day, every 2 days, ...), the last one
    // always shown.
    const xTicks = computed(() => {
      const n = props.points.length;
      if (!n) return [];
      const room = Math.max(2, Math.min(8, Math.floor((width.value - m.l - m.r) / 84) + 1));
      const step = Math.max(1, Math.ceil((n - 1) / (room - 1)));
      const idx = [];
      for (let i = n - 1; i >= 0; i -= step) idx.unshift(i);
      const gap = (width.value - m.l - m.r) / Math.max(1, n - 1);
      if (idx.length > 1 && idx[0] > 0 && idx[0] * gap >= 70) idx.unshift(0);
      return idx.map((i, k) => ({ x: x(i), text: timeText(props.points[i].t),
        anchor: i === 0 ? 'start' : i === n - 1 && k === idx.length - 1 ? 'end' : 'middle' }));
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
    // The tooltip names the point exactly: a day, or a time on a day.
    const tipTime = t => {
      const pts = props.points, step = pts.length > 1 ? pts[1].t - pts[0].t : 3600;
      if (props.span <= 24) return timeText(t);
      const d = new Date(t * 1000), day = `${d.getMonth() + 1}月${d.getDate()}日`;
      return step >= 86400 ? day : day + ' ' + d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false });
    };
    const tip = computed(() => {
      const i = hover.value;
      if (i < 0 || i >= props.points.length) return null;
      const px = x(i);
      return {
        x: px, y: y(props.points[i].v), time: tipTime(props.points[i].t) + (props.partial && i === props.points.length - 1 ? '（还没过完）' : ''),
        value: props.format(props.points[i].v),
        extra: props.extra[i] ? props.extraFormat(props.extra[i].v) : '', left: px > width.value * 0.6,
        more: props.more.map(m => ({ label: m.label, value: m.points[i] ? (m.format || fmtCount)(m.points[i].v) : '—' })),
      };
    });
    return { box, width, height, m, line, tail, area, yTicks, xTicks, onMove, onKey, hover, tip, fmtCount };
  },
  template: `
  <div class="lchart" ref="box" @pointermove="onMove" @pointerleave="hover = -1">
    <svg :width="width" :height="height" role="img" :aria-label="label + '走势图'" tabindex="0" @keydown="onKey" @blur="hover = -1">
      <g class="grid">
        <line v-for="t in yTicks" :key="t.v" :x1="m.l" :x2="width - m.r" :y1="t.y" :y2="t.y"></line>
      </g>
      <g class="ticks">
        <text v-for="t in yTicks" :key="'y' + t.v" :x="m.l - 8" :y="t.y + 4" text-anchor="end">{{ t.text }}</text>
        <text v-for="(t, i) in xTicks" :key="'x' + i" :x="t.x" :y="height - 6" :text-anchor="t.anchor">{{ t.text }}</text>
      </g>
      <path class="area" :d="area"></path>
      <path class="line" :d="line"></path>
      <path class="line partial" :d="tail" v-if="tail"></path>
      <template v-if="tip">
        <line class="cross" :x1="tip.x" :x2="tip.x" :y1="m.t" :y2="height - m.b"></line>
        <circle class="dot" :cx="tip.x" :cy="tip.y" r="4"></circle>
      </template>
    </svg>
    <div class="ctip" v-if="tip" :style="{left: tip.x + 'px', transform: tip.left ? 'translateX(calc(-100% - 12px))' : 'translateX(12px)'}">
      <div class="small secondary">{{ tip.time }}</div>
      <div class="ctip-row"><span class="key"></span><b>{{ tip.value }}</b><span class="secondary">{{ label }}</span></div>
      <div class="ctip-row" v-if="tip.extra"><span class="key none"></span><b>{{ tip.extra }}</b><span class="secondary">{{ extraLabel }}</span></div>
      <div class="ctip-row" v-for="r in tip.more" :key="r.label"><span class="key none"></span><b>{{ r.value }}</b><span class="secondary">{{ r.label }}</span></div>
    </div>
  </div>`,
};

const VISIT_RANGES = [{ d: 1, text: '今天' }, { d: 7, text: '7 天' }, { d: 30, text: '30 天' }];
const VISIT_SECTIONS = [{ id: 'overview', text: '概览' }, { id: 'visitors', text: '访客' }, { id: 'content', text: '内容' }, { id: 'security', text: '安全' }];
const RISK = { high: { text: '高风险', cls: 'crit' }, medium: { text: '中风险', cls: 'warn' }, low: { text: '低风险', cls: 'low' }, none: { text: '正常', cls: 'ok' } };
const VERDICT = { block: '建议封禁', watch: '继续观察', ignore: '不用管' };
// Reports already seen this session, by source.
const visitMemo = new Map();
const pref = (k, d) => { try { return localStorage.getItem(k) || d; } catch { return d; } };
const setPref = (k, v) => { try { localStorage.setItem(k, v); } catch { /* not kept */ } };

// One ranking: rows with a share bar; clicking a row picks its value.
const RankList = {
  props: { items: { type: Array, default: () => [] }, total: { type: Number, default: 0 }, limit: { type: Number, default: 10 },
    clickable: Boolean, empty: { type: String, default: '没有数据' }, expandable: { type: Boolean, default: true } },
  emits: ['pick'],
  setup(props) {
    const more = ref(false);
    const shown = computed(() => props.items.slice(0, more.value ? 20 : props.limit));
    const base = computed(() => props.total > 0 ? props.total : props.items.reduce((s, i) => s + i.count, 0));
    const share = n => base.value > 0 ? Math.min(1, n / base.value) : 0;
    return { more, shown, share, fmtCount };
  },
  template: `
  <div class="rank">
    <div class="rank-row" v-for="(t, i) in shown" :key="i + '|' + t.value" :class="{ clickable }" @click="clickable && $emit('pick', t.value)">
      <div class="rank-line">
        <span class="rank-key" :title="t.value">{{ t.value }}</span>
        <span class="rank-note" v-if="t.note" :title="t.note">{{ t.note }}</span>
        <span class="num">{{ fmtCount(t.count) }}</span>
        <span class="rank-share">{{ (share(t.count) * 100).toFixed(1) }}%</span>
      </div>
      <div class="share-bar"><div :style="{ width: Math.max(1, share(t.count) * 100) + '%' }"></div></div>
    </div>
    <div class="rank-empty" v-if="!items.length">{{ empty }}</div>
    <button class="link small rank-more" v-if="expandable && items.length > limit" @click="more = !more">{{ more ? '收起' : '显示前 20 名' }}</button>
  </div>`,
};

// 访问分析: visits counted from access logs (EdgeOne's or a server's),
// in four sections, with the IPs worth a look and blocking.
const VisitStats = {
  props: { servers: { type: Array, default: () => [] }, active: Boolean, tencent: Boolean, request: Object },
  emits: ['ask', 'section'],
  setup(props, { emit }) {
    const sources = ref([]);
    const source = ref(pref('miao.visitSource', ''));
    const days = ref(Number(pref('miao.visitDays', '7')) || 7);
    const site = ref('*');
    const section = ref(pref('miao.visitSection', 'overview'));
    const series = ref('pv');
    const data = ref(null);
    const loading = ref(false);
    const error = ref('');
    const judging = ref(false);
    const judgements = reactive({}); // source|days -> {summary, verdicts, cost, currency}
    const picked = ref(new Set());
    const showAll = ref(false);
    const drawer = ref(null); // IP profile shown on the side
    const plan = ref(null); // checklist being confirmed
    const planning = ref(false);
    const blocked = ref([]);
    let seq = 0;
    // Today is counted per hour for PV and requests only.
    watch(days, v => { setPref('miao.visitDays', String(v)); if (v === 1 && !['pv', 'requests'].includes(series.value)) series.value = 'pv'; }, { immediate: true });
    watch(section, v => { setPref('miao.visitSection', v); emit('section', v); });
    watch(() => props.request, r => { if (r && r.section) section.value = r.section; });

    async function loadSources() {
      try {
        sources.value = await api('GET', '/api/visits/sources');
        if (!sources.value.some(s => s.key === source.value)) source.value = sources.value.length ? sources.value[0].key : '';
      } catch (e) { error.value = e.message; }
    }
    // The server answers at once with the last report it has (even from
    // before a restart); if old it is shown while counting again.
    async function load(refresh) {
      if (!source.value) return;
      const memo = visitMemo.get(source.value);
      if (memo) data.value = memo;
      else if (!data.value || data.value.sourceKey !== source.value) data.value = null;
      const n = ++seq;
      loading.value = true; error.value = '';
      const base = `/api/visits?source=${encodeURIComponent(source.value)}`;
      try {
        let d = await api('GET', base + (refresh ? '&refresh=1' : ''));
        if (n !== seq) return;
        data.value = d; visitMemo.set(source.value, d);
        if (d.refreshing) {
          d = await api('GET', base + '&wait=1');
          if (n !== seq) return;
          data.value = d; visitMemo.set(source.value, d);
        }
      } catch (e) { if (n === seq) error.value = e.message; }
      finally { if (n === seq) loading.value = false; }
    }
    async function loadBlocked() {
      if (!props.tencent) { blocked.value = []; return; }
      try { blocked.value = await api('GET', '/api/visits/blocked'); } catch { blocked.value = []; }
      loadAuto();
    }
    // 自动封禁: the rule the user turns on, and what it did.
    const auto = ref(null);
    const autoEdit = ref(false);
    const autoBusy = ref(false);
    const autoForm = reactive({ enabled: true, level: 'high', requireAI: true, hours: 24, allowText: '' });
    async function loadAuto() {
      if (!props.tencent) { auto.value = null; return; }
      try { auto.value = await api('GET', '/api/autoblock'); } catch { auto.value = null; }
    }
    function editAuto() {
      const st = (auto.value && auto.value.settings) || {};
      Object.assign(autoForm, { enabled: true, level: st.level || 'high', requireAI: st.requireAI !== false, hours: st.hours ?? 24, allowText: (st.allow || []).join('\n') });
      autoEdit.value = true;
    }
    async function saveAuto(enabled) {
      autoBusy.value = true;
      try {
        auto.value = await api('PUT', '/api/autoblock', { enabled: enabled ?? autoForm.enabled, level: autoForm.level, requireAI: autoForm.requireAI,
          hours: Number(autoForm.hours), allow: autoForm.allowText.split(/[\s,，]+/).filter(Boolean) });
        autoEdit.value = false;
        notify(auto.value.settings.enabled ? '自动封禁已开启' : '自动封禁已关闭', 'ok');
      } catch (e) { notify(e.message, 'error'); }
      finally { autoBusy.value = false; }
    }
    async function turnOffAuto() {
      const st = auto.value.settings;
      Object.assign(autoForm, { level: st.level, requireAI: st.requireAI, hours: st.hours, allowText: (st.allow || []).join('\n') });
      await saveAuto(false);
    }
    async function runAuto() {
      autoBusy.value = true;
      try { auto.value = await api('POST', '/api/autoblock/run'); notify(auto.value.lastNote || '检查完了', 'ok'); loadBlocked(); }
      catch (e) { notify(e.message, 'error'); }
      finally { autoBusy.value = false; }
    }
    const autoRule = computed(() => {
      const st = auto.value && auto.value.settings;
      if (!st) return '';
      const who = (st.level === 'medium' ? '高风险和中风险 IP' : '高风险 IP') + (st.requireAI ? '（AI 也建议封禁的）' : '');
      const how = !st.hours ? '一直封禁，直到手动解封' : st.hours % 24 === 0 ? `封禁 ${st.hours / 24} 天` : `封禁 ${st.hours} 小时`;
      return who + '，' + how + ((st.allow || []).length ? `，不封 ${st.allow.length} 个你列出的 IP/网段` : '');
    });
    const autoUntil = computed(() => new Map(((auto.value && auto.value.blocked) || []).map(b => [b.ip, b.until])));
    const untilText = u => { if (!u) return '一直封禁'; const d = new Date(u); return isNaN(d) ? u : `${d.getMonth() + 1}月${d.getDate()}日 ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')} 解封`; };
    watch(source, v => { setPref('miao.visitSource', v); site.value = '*'; picked.value = new Set(); load(false); });
    // Picks belong to the list they were made in.
    watch([site, days], () => { picked.value = new Set(); });
    watch(() => props.active, on => { if (on) { loadSources().then(() => load(false)); loadBlocked(); } });
    watch(() => props.servers.length, () => loadSources());
    watch(() => props.tencent, () => { loadSources(); loadBlocked(); });
    onMounted(async () => { await loadSources(); load(false); loadBlocked(); });

    const siteNames = computed(() => ((data.value && data.value.sites) || []).map(s => s.name).filter(n => n !== '*'));
    watch(siteNames, names => { if (names.length && site.value !== '*' && !names.includes(site.value)) site.value = '*'; });
    const range = computed(() => data.value && data.value.ranges ? data.value.ranges[String(days.value)] : null);
    const cur = computed(() => range.value && range.value.sites ? (range.value.sites[site.value] || range.value.sites['*'] || null) : null);
    const total = computed(() => cur.value ? cur.value.total : {});
    const top = kind => (cur.value && cur.value.top && cur.value.top[kind]) || [];
    const siteInfo = computed(() => ((data.value && data.value.sites) || []).find(s => s.name === site.value) || null);
    const siteRows = computed(() => range.value ? siteNames.value.map(n => ({ name: n, total: (range.value.sites[n] || {}).total || {} })) : []);

    // IPs: those that touched the chosen site, riskiest first.
    const ips = computed(() => {
      const list = (range.value && range.value.ips) || [];
      return site.value === '*' ? list : list.filter(p => (p.sites || []).some(s => s.value === site.value));
    });
    const risky = computed(() => ips.value.filter(p => p.risk !== 'none'));
    const ipRows = computed(() => showAll.value ? ips.value : risky.value);
    const counts = computed(() => ({
      high: risky.value.filter(p => p.risk === 'high').length,
      medium: risky.value.filter(p => p.risk === 'medium').length,
      low: risky.value.filter(p => p.risk === 'low').length,
      // high-risk IPs not blocked yet: what still needs doing
      open: risky.value.filter(p => p.risk === 'high' && !blockedSet.value.has(p.ip)).length,
    }));
    const judgement = computed(() => judgements[source.value + '|' + days.value] || null);
    const verdictOf = ip => { const j = judgement.value; return j ? (j.verdicts || []).find(v => v.ip === ip) : null; };
    const blockable = p => !p.edgeOne && !p.crawler && !/^(10\.|127\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(p.ip);
    // Server logs say which requests came to the server itself rather than
    // through EdgeOne; blocking in EdgeOne does not stop those.
    const fromServer = computed(() => String(source.value).startsWith('server:'));
    const directRows = computed(() => fromServer.value ? ipRows.value.filter(p => p.direct > 0).length : 0);
    const blockedSet = computed(() => new Set(blocked.value.flatMap(z => z.ips)));
    function togglePick(ip) {
      const next = new Set(picked.value);
      next.has(ip) ? next.delete(ip) : next.add(ip);
      picked.value = next;
    }
    function pickSuggested() {
      picked.value = new Set(ips.value.filter(p => blockable(p) && !blockedSet.value.has(p.ip) &&
        (verdictOf(p.ip) ? verdictOf(p.ip).action === 'block' : p.risk === 'high')).map(p => p.ip));
    }
    async function judge() {
      judging.value = true;
      try {
        const want = picked.value.size ? [...picked.value] : [];
        judgements[source.value + '|' + days.value] = await api('POST', '/api/visits/judge', { source: source.value, days: days.value, ips: want });
        loadSpendSoon();
      } catch (e) { notify(e.message, 'error'); }
      finally { judging.value = false; }
    }
    const loadSpendSoon = inject('loadSpend', () => {});
    async function block(list) {
      planning.value = true;
      try { plan.value = await api('POST', '/api/visits/block', { source: source.value, ips: list }); }
      catch (e) { notify(e.message, 'error'); }
      finally { planning.value = false; }
    }
    async function unblock(zone, ip) {
      planning.value = true;
      try { plan.value = await api('POST', '/api/visits/unblock', { zone, ips: [ip] }); }
      catch (e) { notify(e.message, 'error'); }
      finally { planning.value = false; }
    }
    function planDone(p) {
      loadBlocked(); picked.value = new Set();
      // Once the server records real IPs, the warning about EdgeOne's nodes changes.
      if (p && (p.stepList || []).some(s => s.capability === 'nginx.realip')) load(true);
    }
    // realIP proposes the checklist that makes the server log real visitor IPs.
    async function realIP() {
      const id = (data.value || {}).serverId;
      if (!id) return;
      planning.value = true;
      try { plan.value = await api('POST', `/api/servers/${id}/realip`); }
      catch (e) { notify(e.message, 'error'); }
      finally { planning.value = false; }
    }
    const planServerName = computed(() => {
      const id = plan.value && plan.value.serverId;
      return id ? (props.servers.find(s => s.id === id) || { name: `服务器 ${id}` }).name : '腾讯云';
    });
    function openIP(ip) {
      const p = ((range.value && range.value.ips) || []).find(x => x.ip === ip);
      if (p) drawer.value = p;
      else notify('这个 IP 访问得不多，没有详细记录', 'ok');
    }

    // What needs attention, most important first.
    const alerts = computed(() => {
      const out = [];
      const d = data.value;
      if (!d) return out;
      if (d.problem) out.push({ level: 'info', text: d.problem });
      for (const s of d.sites || []) {
        if (s.warning && (site.value === '*' || s.name === site.value)) out.push({ level: 'warn', text: (site.value === '*' ? s.name + '：' : '') + s.warning, fix: s.fix });
      }
      for (const l of top('leak')) out.push({ level: 'crit', text: `敏感文件被成功下载：${l.value.replace(/^200 /, '')}（${l.count} 次）。里面的密钥或代码可能已经泄露，请删除或禁止访问这个文件，并更换其中的密码和密钥。`, ask: `我的网站 ${l.value.replace(/^200 /, '')} 返回了 200，可能泄露了敏感信息，帮我看看怎么处理` });
      const c = counts.value;
      if (c.open) out.push({ level: 'crit', text: `发现 ${c.open} 个高风险 IP 还没有封禁（扫描、攻击或猜密码）`, go: 'security' });
      else if (c.high) out.push({ level: 'info', text: `${c.high} 个高风险 IP 都已在 EdgeOne 封禁`, go: 'security' });
      if (c.medium) out.push({ level: 'warn', text: `有 ${c.medium} 个可疑 IP 值得看一看`, go: 'security' });
      const t = total.value;
      if (t.requests > 50 && t.e5xx / t.requests > 0.01) out.push({ level: 'warn', text: `服务器出错（5xx）${fmtCount(t.e5xx)} 次，占请求的 ${(t.e5xx / t.requests * 100).toFixed(1)}%`, go: 'content' });
      if (t.requests > 50 && t.bots / t.requests > 0.5) out.push({ level: 'info', text: `爬虫和程序占了请求的 ${Math.round(t.bots / t.requests * 100)}%` , go: 'visitors' });
      const dead = top('dead');
      if (dead.length) out.push({ level: 'info', text: `发现 ${dead.length >= 20 ? '20 多' : dead.length} 个死链：有人点链接打开却是 404`, go: 'content' });
      for (const n of d.notes || []) out.push({ level: 'info', text: n });
      return out;
    });

    // Trend: per hour today, per day otherwise.
    const dayTime = s => { const [y, m, dd] = s.split('-').map(Number); return new Date(y, m - 1, dd).getTime() / 1000; };
    const SERIES = { pv: 'PV', uv: 'UV', ip: 'IP', requests: '请求' };
    const trend = computed(() => {
      const d = data.value;
      if (!d) return { main: [], more: [] };
      if (days.value === 1) {
        const hours = (d.hours && d.hours[site.value]) || [];
        const base = d.today ? dayTime(d.today) : 0;
        const last = hours.reduce((m, h) => h.requests > 0 ? h.hour : m, 0);
        const list = hours.filter(h => h.hour <= Math.max(last, 1));
        const key = series.value === 'requests' ? 'requests' : 'pv';
        const pts = k => list.map(h => ({ t: base + h.hour * 3600, v: h[k] }));
        return { main: pts(key), more: [{ label: key === 'pv' ? '次请求' : 'PV', points: pts(key === 'pv' ? 'requests' : 'pv') }] };
      }
      const list = ((d.days && d.days[site.value]) || []).slice(-days.value);
      const pts = k => list.map(x => ({ t: dayTime(x.date), v: x[k] }));
      return { main: pts(series.value), more: Object.keys(SERIES).filter(k => k !== series.value).map(k => ({ label: SERIES[k], points: pts(k) })) };
    });
    const dayRows = computed(() => (((data.value && data.value.days) || {})[site.value] || []).slice(-days.value).reverse());
    const hourRows = computed(() => (((data.value && data.value.hours) || {})[site.value] || []).filter(h => h.requests > 0).reverse());
    // Full days only (today is not over): the last N days against the N
    // before them. Only PV and requests add up over days.
    const fullDays = computed(() => {
      const list = (data.value && data.value.days && data.value.days[site.value]) || [];
      return list.length && list[list.length - 1].date === data.value.today ? list.slice(0, -1) : list;
    });
    function delta(key) {
      const n = days.value, list = fullDays.value;
      if (n === 1 || list.length < n * 2) return null;
      const sum = arr => arr.reduce((s, x) => s + x[key], 0);
      const now = sum(list.slice(-n)), before = sum(list.slice(-2 * n, -n));
      return before ? Math.round((now - before) / before * 100) : null;
    }
    const yesterday = key => { const l = fullDays.value; return l.length ? l[l.length - 1][key] : null; };
    const pct = (a, b) => b > 0 ? (a / b * 100).toFixed(1) + '%' : '—';
    const rangeText = computed(() => (VISIT_RANGES.find(r => r.d === days.value) || {}).text);
    const sourceTitle = computed(() => (sources.value.find(s => s.key === source.value) || {}).title || '');
    function askAI() {
      const who = site.value === '*' ? '所有网站' : site.value;
      const src = source.value === 'edgeone' ? 'EdgeOne 日志（source=edgeone）' : `服务器日志（server_id=${(data.value || {}).serverId}）`;
      emit('ask', `根据${src}分析 ${who} ${days.value === 1 ? '今天' : '最近' + rangeText.value}的访问情况：PV、UV、IP 和地区有没有异常变化，访客主要看了什么、从哪里来，爬虫、错误和可疑 IP 多不多，有没有需要处理的问题？`);
    }
    function askIP(p) {
      emit('ask', `分析一下 IP ${p.ip}（${p.place || ''} ${p.isp || ''}）最近${rangeText.value}在我网站上的行为：它访问了什么、频率如何、是不是扫描或攻击，要不要封禁？`);
    }
    const shortUA = ua => (ua || '').length > 90 ? ua.slice(0, 90) + '…' : (ua || '—');
    // "blog.x.com/old ← /about": the missing address, and the page (or the
    // other website) whose link led there.
    const deadLinks = computed(() => top('dead').map(it => {
      const [to, from] = it.value.split(' ← ');
      return { value: to, count: it.count, note: from ? (from.startsWith('/') ? '站内 ' + from : '来自 ' + from) : '' };
    }));
    function askDead() {
      const list = top('dead').slice(0, 15).map(it => `${it.value}（${it.count} 次）`).join('\n');
      emit('ask', `我的网站有这些死链（有人点链接打开却是 404，← 后面是链接所在的页面或网站）：\n${list}\n帮我看看这些地址原来是什么、应该怎么修（改链接、做 301 跳转还是恢复页面）。`);
    }
    return { sources, source, days, site, section, series, data, loading, error, load, siteNames, range, cur, total, top, siteInfo, siteRows,
      ips, risky, ipRows, counts, judgement, verdictOf, blockable, blockedSet, picked, togglePick, pickSuggested, judge, judging, showAll,
      block, unblock, plan, planning, planDone, realIP, planServerName, fromServer, directRows, blocked, drawer, openIP, alerts, trend, dayRows, hourRows, delta, yesterday, pct, SERIES, sourceTitle,
      askAI, askIP, shortUA, deadLinks, askDead, auto, autoEdit, autoBusy, autoForm, editAuto, saveAuto, turnOffAuto, runAuto, autoRule, autoUntil, untilText, VISIT_RANGES, VISIT_SECTIONS, RISK, VERDICT, fmtCount, fmtBytes, whenText };
  },
  template: `
  <div class="vs">
    <div class="group" v-if="!sources.length && !error"><div class="row secondary">添加服务器或者填写腾讯云密钥后，这里会统计网站的访问日志。</div></div>
    <template v-else>
      <div class="stat-bar">
        <label class="field"><span>数据</span>
          <select v-model="source" aria-label="数据来源"><option v-for="s in sources" :key="s.key" :value="s.key">{{ s.title }}</option></select></label>
        <label class="field"><span>网站</span>
          <select v-model="site" aria-label="网站"><option value="*">全部网站</option><option v-for="n in siteNames" :key="n" :value="n">{{ n }}</option></select></label>
        <span class="segmented" role="tablist" aria-label="时间">
          <button v-for="r in VISIT_RANGES" :key="r.d" role="tab" :aria-selected="days === r.d" :class="{ on: days === r.d }" @click="days = r.d">{{ r.text }}</button>
        </span>
        <span class="grow"></span>
        <span class="small tertiary live-note" v-if="data && data.checkedAt">
          <span class="spinner inline" v-if="loading"></span>{{ loading ? '正在更新，下面是 ' : '统计于 ' }}{{ whenText(data.checkedAt) }}{{ loading ? ' 的结果' : '' }}
        </span>
        <button class="plain" @click="load(true)" :disabled="loading" title="重新统计"><ui-icon name="refresh"></ui-icon>刷新</button>
        <button class="primary" @click="askAI" :disabled="!cur"><ui-icon name="sparkles"></ui-icon>让 AI 分析</button>
      </div>
      <p class="source-note small tertiary" v-if="sources.length">{{ (sources.find(s => s.key === source) || {}).note }}</p>

      <nav class="subtabs" role="tablist">
        <button v-for="s in VISIT_SECTIONS" :key="s.id" role="tab" :aria-selected="section === s.id" :class="{ on: section === s.id }" @click="section = s.id">
          {{ s.text }}<span class="badge crit" v-if="s.id === 'security' && counts.open" :title="counts.open + ' 个高风险 IP 还没有封禁'">{{ counts.open }}</span>
        </button>
      </nav>

      <div class="notice" v-if="error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ error }}</div>
      <div class="notice" v-if="!data && loading"><span class="spinner"></span>正在统计访问日志（第一次要下载或读取日志，可能要一两分钟）……</div>

      <template v-if="cur">
        <!-- 概览 -->
        <template v-if="section === 'overview'">
          <div class="alerts" v-if="alerts.length">
            <div class="alert" v-for="(a, i) in alerts" :key="i" :class="'al-' + a.level">
              <ui-icon :name="a.level === 'crit' ? 'alert' : a.level === 'warn' ? 'warn' : 'info'"></ui-icon>
              <span class="grow">{{ a.text }}</span>
              <button class="link small" v-if="a.go" @click="section = a.go">查看</button>
              <button class="link small" v-if="a.ask" @click="$emit('ask', a.ask)">让 AI 处理</button>
              <button class="link small" v-if="a.fix === 'realip'" @click="realIP" :disabled="planning">让服务器记录真实 IP</button>
            </div>
          </div>
          <div class="kpi-group">
            <div class="kpi-title">访问</div>
            <div class="tiles four">
              <div class="tile"><div class="label">PV · 浏览量</div><div class="value">{{ fmtCount(total.pv) }}</div>
                <div class="sub" v-if="days === 1 && yesterday('pv') !== null">昨天全天 {{ fmtCount(yesterday('pv')) }}</div>
                <div class="sub" v-else-if="delta('pv') !== null" :title="'最近 ' + days + ' 个整天（不含今天）和再往前 ' + days + ' 天相比'">
                  <span :class="delta('pv') >= 0 ? 'up' : 'down'">{{ delta('pv') >= 0 ? '↑' : '↓' }} {{ Math.abs(delta('pv')) }}%</span> 较前 {{ days }} 天</div>
                <div class="sub" v-else>打开网页的次数</div></div>
              <div class="tile"><div class="label">UV · 访客</div><div class="value">{{ fmtCount(total.uv) }}</div><div class="sub">不同的 IP + 浏览器</div></div>
              <div class="tile"><div class="label">IP · 独立 IP</div><div class="value">{{ fmtCount(total.ip) }}</div><div class="sub">浏览过网页的 IP</div></div>
              <div class="tile"><div class="label">人均浏览</div><div class="value">{{ total.uv ? (total.pv / total.uv).toFixed(1) : '—' }}</div><div class="sub">每个访客看的页面数</div></div>
            </div>
          </div>
          <div class="kpi-group">
            <div class="kpi-title">请求与质量</div>
            <div class="tiles four">
              <div class="tile"><div class="label">请求数</div><div class="value">{{ fmtCount(total.requests) }}</div>
                <div class="sub" v-if="days === 1 && yesterday('requests') !== null">昨天全天 {{ fmtCount(yesterday('requests')) }}</div>
                <div class="sub" v-else-if="delta('requests') !== null" :title="'最近 ' + days + ' 个整天（不含今天）和再往前 ' + days + ' 天相比'">
                  <span :class="delta('requests') >= 0 ? 'up' : 'down'">{{ delta('requests') >= 0 ? '↑' : '↓' }} {{ Math.abs(delta('requests')) }}%</span> 较前 {{ days }} 天</div>
                <div class="sub" v-else>含图片、脚本、接口</div></div>
              <div class="tile"><div class="label">爬虫和程序</div><div class="value">{{ pct(total.bots, total.requests) }}</div><div class="sub">{{ fmtCount(total.bots) }} 次请求</div></div>
              <div class="tile"><div class="label">流量</div><div class="value">{{ fmtBytes(total.bytes) }}</div><div class="sub">{{ source === 'edgeone' ? 'EdgeOne 发给访客' : '服务器发出' }}</div></div>
              <div class="tile"><div class="label">错误率</div><div class="value">{{ pct(total.e4xx + total.e5xx, total.requests) }}</div><div class="sub">4xx {{ fmtCount(total.e4xx) }} · 5xx {{ fmtCount(total.e5xx) }}</div></div>
            </div>
          </div>

          <section class="card">
            <header class="card-head">
              <h3>{{ days === 1 ? '今天每小时' : '每天' }}</h3>
              <span class="segmented small">
                <button v-for="(t, k) in SERIES" :key="k" v-show="days > 1 || k === 'pv' || k === 'requests'" :class="{ on: series === k }" @click="series = k">{{ t }}</button>
              </span>
            </header>
            <line-chart :points="trend.main" :more="trend.more" :label="SERIES[series] || 'PV'" :span="days === 1 ? 24 : days * 24" partial></line-chart>
            <details class="card-foot">
              <summary><ui-icon name="chevron"></ui-icon>{{ days === 1 ? '每小时明细' : '每天明细' }}</summary>
              <div class="table-wrap">
                <table class="table visit-table" v-if="days > 1">
                  <thead><tr><th>日期</th><th class="num">PV</th><th class="num">UV</th><th class="num">IP</th><th class="num">请求</th><th class="num">爬虫</th><th class="num">流量</th><th class="num">4xx / 5xx</th></tr></thead>
                  <tbody><tr v-for="d in dayRows" :key="d.date"><td>{{ d.date.slice(5).replace('-', '月') }}日</td><td class="num">{{ fmtCount(d.pv) }}</td><td class="num">{{ fmtCount(d.uv) }}</td><td class="num">{{ fmtCount(d.ip) }}</td>
                    <td class="num">{{ fmtCount(d.requests) }}</td><td class="num">{{ fmtCount(d.bots) }}</td><td class="num">{{ fmtBytes(d.bytes) }}</td><td class="num">{{ d.e4xx }} / {{ d.e5xx }}</td></tr></tbody>
                </table>
                <table class="table visit-table" v-else>
                  <thead><tr><th>时间</th><th class="num">PV</th><th class="num">请求</th></tr></thead>
                  <tbody><tr v-for="h in hourRows" :key="h.hour"><td>{{ h.hour }}:00</td><td class="num">{{ fmtCount(h.pv) }}</td><td class="num">{{ fmtCount(h.requests) }}</td></tr></tbody>
                </table>
              </div>
            </details>
          </section>

          <div class="card-grid three">
            <section class="card"><header class="card-head"><h3>热门页面</h3><button class="link small" @click="section = 'content'">全部</button></header>
              <rank-list :items="top('page')" :total="total.pv" :limit="5" :expandable="false"></rank-list></section>
            <section class="card"><header class="card-head"><h3>来源</h3><button class="link small" @click="section = 'visitors'">全部</button></header>
              <rank-list :items="top('referer')" :total="total.pv" :limit="5" :expandable="false"></rank-list></section>
            <section class="card"><header class="card-head"><h3>访客地区</h3><button class="link small" @click="section = 'visitors'">全部</button></header>
              <rank-list :items="top('region')" :limit="5" :expandable="false"></rank-list></section>
          </div>

          <section class="card" v-if="site === '*' && siteRows.length > 1">
            <header class="card-head"><h3>各网站</h3><span class="small tertiary">点一行只看这个网站</span></header>
            <div class="table-wrap">
              <table class="table visit-table site-table">
                <thead><tr><th>网站</th><th class="num">PV</th><th class="num">UV</th><th class="num">IP</th><th class="num">请求</th><th class="num">流量</th><th></th></tr></thead>
                <tbody><tr v-for="s in siteRows" :key="s.name" class="clickable" @click="site = s.name">
                  <td>{{ s.name }}</td><td class="num">{{ fmtCount(s.total.pv) }}</td><td class="num">{{ fmtCount(s.total.uv) }}</td><td class="num">{{ fmtCount(s.total.ip) }}</td>
                  <td class="num">{{ fmtCount(s.total.requests) }}</td><td class="num">{{ fmtBytes(s.total.bytes) }}</td><td><ui-icon name="chevron" class="tertiary"></ui-icon></td></tr></tbody>
              </table>
            </div>
          </section>
        </template>

        <!-- 访客 -->
        <div class="card-grid" v-if="section === 'visitors'">
          <section class="card"><header class="card-head"><h3>访客地区</h3><span class="small tertiary">按独立 IP</span></header>
            <rank-list :items="top('region')"></rank-list></section>
          <section class="card"><header class="card-head"><h3>来源</h3><span class="small tertiary">打开网页前在哪里</span></header>
            <rank-list :items="top('referer')" :total="total.pv"></rank-list></section>
          <section class="card"><header class="card-head"><h3>访客 IP</h3><span class="small tertiary">点一行看它做了什么</span></header>
            <rank-list :items="top('ip')" :total="total.requests - total.bots" clickable @pick="openIP"></rank-list></section>
          <section class="card"><header class="card-head"><h3>运营商</h3><span class="small tertiary">按独立 IP</span></header>
            <rank-list :items="top('isp')"></rank-list></section>
          <section class="card"><header class="card-head"><h3>设备</h3></header>
            <rank-list :items="top('device')" :total="total.pv"></rank-list></section>
          <section class="card"><header class="card-head"><h3>爬虫和程序</h3><span class="small tertiary">{{ fmtCount(total.bots) }} 次请求</span></header>
            <rank-list :items="top('bot')" :total="total.bots"></rank-list></section>
        </div>

        <!-- 内容 -->
        <div class="card-grid" v-if="section === 'content'">
          <section class="card"><header class="card-head"><h3>受访页面</h3><span class="small tertiary">按 PV</span></header>
            <rank-list :items="top('page')" :total="total.pv"></rank-list></section>
          <section class="card"><header class="card-head"><h3>访问目录</h3><span class="small tertiary">按请求，含爬虫</span></header>
            <rank-list :items="top('dir')" :total="total.requests"></rank-list></section>
          <section class="card"><header class="card-head"><h3>出错的地址</h3><span class="small tertiary">404 多半是扫描或死链</span></header>
            <rank-list :items="top('errpage')" :total="total.e4xx + total.e5xx" empty="没有出错的请求"></rank-list></section>
          <section class="card"><header class="card-head"><h3>死链</h3>
              <span class="small tertiary">有人点链接打开却是 404，旁边是链接在哪</span>
              <button class="link small" v-if="deadLinks.length" @click="askDead">让 AI 看看</button></header>
            <rank-list :items="deadLinks" empty="没有发现死链"></rank-list></section>
          <section class="card"><header class="card-head"><h3>状态码</h3></header>
            <rank-list :items="top('status')" :total="total.requests"></rank-list></section>
        </div>

        <!-- 安全 -->
        <template v-if="section === 'security'">
          <div class="risk-summary">
            <span class="risk-chip crit">高风险 {{ counts.high }}</span>
            <span class="risk-chip warn">中风险 {{ counts.medium }}</span>
            <span class="risk-chip low">低风险 {{ counts.low }}</span>
            <span class="grow"></span>
            <label class="check small"><input type="checkbox" v-model="showAll"> 显示全部记录的 IP（{{ ips.length }}）</label>
          </div>
          <div class="alerts" v-if="source !== 'edgeone' && alerts.some(a => a.level === 'warn' && !a.go)">
            <div class="alert al-warn" v-for="(a, i) in alerts.filter(a => a.level === 'warn' && !a.go)" :key="i"><ui-icon name="warn"></ui-icon><span class="grow">{{ a.text }}</span>
              <button class="link small" v-if="a.fix === 'realip'" @click="realIP" :disabled="planning">让服务器记录真实 IP</button></div>
          </div>
          <div class="alert al-info judge-box" v-if="judgement">
            <ui-icon name="sparkles"></ui-icon>
            <span class="grow"><b>AI 研判：</b>{{ judgement.summary }}<span class="tertiary small" v-if="judgement.cost"> · 约 {{ judgement.currency === 'USD' ? '$' : '¥' }}{{ judgement.cost.toFixed(4) }}</span></span>
          </div>
          <section class="card">
            <header class="card-head">
              <h3>值得注意的 IP</h3>
              <span class="grow"></span>
              <button class="plain small" @click="pickSuggested" :disabled="!ips.length">选中建议封禁的</button>
              <button class="small" @click="judge" :disabled="judging || !ips.length"><span class="spinner inline" v-if="judging"></span><ui-icon name="sparkles" v-else></ui-icon>{{ picked.size ? 'AI 研判选中的 ' + picked.size + ' 个' : 'AI 研判' }}</button>
              <button class="primary small" @click="block([...picked])" :disabled="!picked.size || planning || !tencent" :title="tencent ? '' : '封禁通过 EdgeOne 进行，需要先填写腾讯云密钥'">封禁选中的 {{ picked.size }} 个</button>
            </header>
            <p class="small secondary card-note" v-if="directRows">{{ directRows }} 个 IP 标了「直连服务器」：它们直接访问服务器的 IP，没经过 EdgeOne。在 EdgeOne 封禁只能挡住它们经过 EdgeOne 的访问，挡不住直接访问服务器。</p>
            <div class="table-wrap">
              <table class="table ip-table" v-if="ipRows.length">
                <thead><tr><th></th><th>IP</th><th>风险</th><th>它做了什么</th><th class="num">请求</th><th class="num">404/403</th><th class="num">每分钟最多</th><th>最后访问</th></tr></thead>
                <tbody>
                  <tr v-for="p in ipRows" :key="p.ip" class="clickable" @click="drawer = p">
                    <td @click.stop><input type="checkbox" :checked="picked.has(p.ip)" :disabled="!blockable(p) || blockedSet.has(p.ip)" @change="togglePick(p.ip)" :aria-label="'选中 ' + p.ip"></td>
                    <td><div class="mono-ish">{{ p.ip }}</div><div class="small tertiary">{{ p.place || '未知' }} {{ p.isp }}</div>
                      <span class="tag direct-tag" v-if="fromServer && p.direct > 0" :title="'有 ' + p.direct + ' 次请求直接访问服务器 IP，没经过 EdgeOne'">直连服务器</span></td>
                    <td><span class="risk-chip" :class="RISK[p.risk].cls">{{ RISK[p.risk].text }}</span>
                      <div class="small verdict" v-if="verdictOf(p.ip)" :class="'v-' + verdictOf(p.ip).action">AI：{{ VERDICT[verdictOf(p.ip).action] }}</div>
                      <div class="small tertiary" v-if="blockedSet.has(p.ip)">已封禁</div></td>
                    <td class="small why">{{ (verdictOf(p.ip) && verdictOf(p.ip).reason) || (p.reasons || []).slice(0, 2).join('；') || (p.bot ? '程序：' + p.bot : '普通访问') }}</td>
                    <td class="num">{{ fmtCount(p.requests) }}</td><td class="num">{{ fmtCount(p.e4xx) }}</td><td class="num">{{ p.peakMin }}</td>
                    <td class="small">{{ p.last.slice(5, 16) }}</td>
                  </tr>
                </tbody>
              </table>
              <div class="rank-empty" v-else>{{ showAll ? '没有记录的 IP' : '这段时间没有发现可疑 IP' }}</div>
            </div>
          </section>
          <section class="card" v-if="tencent">
            <header class="card-head"><h3>已在 EdgeOne 封禁</h3><span class="small tertiary">封错了点 × 解封（也要确认）</span></header>
            <p class="small secondary card-note">封禁只在 EdgeOne 上挡住这些 IP 访问你的网站，不改服务器，也不影响你用 SSH、1Panel 和 Miao Panel 管理服务器。EdgeOne 的节点和已验证的搜索引擎爬虫不会被封，封禁前会再向 EdgeOne 核对一次。</p>
            <div class="blocked" v-if="blocked.length">
              <div v-for="z in blocked" :key="z.zone" class="blocked-zone">
                <div class="small secondary">站点 {{ z.zone }}</div>
                <span class="ip-chip" v-for="ip in z.ips" :key="ip" :title="autoUntil.has(ip) ? '自动封禁，' + untilText(autoUntil.get(ip)) : ''">{{ ip }}<span class="tag auto-tag" v-if="autoUntil.has(ip)">自动</span><button class="plain icon-only" title="解除封禁" :aria-label="'解除封禁 ' + ip" @click="unblock(z.zone, ip)" :disabled="planning"><ui-icon name="close"></ui-icon></button></span>
              </div>
            </div>
            <div class="rank-empty" v-else>还没有封禁的 IP</div>
          </section>
          <section class="card" v-if="tencent && auto">
            <header class="card-head"><h3>自动封禁 <span class="risk-chip" :class="auto.settings.enabled ? 'on' : ''">{{ auto.settings.enabled ? '已开启' : '已关闭' }}</span></h3>
              <button class="plain small" v-if="auto.settings.enabled && !autoEdit" @click="runAuto" :disabled="autoBusy"><span class="spinner inline" v-if="autoBusy"></span>立即检查一次</button>
              <button class="small" v-if="!autoEdit" @click="editAuto">{{ auto.settings.enabled ? '修改规则' : '开启…' }}</button>
            </header>
            <div class="auto-body" v-if="!autoEdit">
              <p class="small" v-if="auto.settings.enabled">规则：{{ autoRule }}。随统计每 20 分钟检查一次<template v-if="auto.lastRun">；上次 {{ whenText(auto.lastRun) }}：{{ auto.lastNote }}</template>。</p>
              <p class="small secondary" v-else>开启后，每次统计更新（每 20 分钟）都会按规则在 EdgeOne 封禁 IP，到期自动解封。每次封禁和解封都会生成清单，留在「建议」和执行日志里，可以撤销。EdgeOne 节点、内网地址、已验证的搜索引擎和你列出的 IP 不会被封。</p>
              <div class="table-wrap" v-if="auto.blocked.length">
                <table class="table auto-table">
                  <thead><tr><th>IP</th><th>站点</th><th>为什么</th><th>到期</th></tr></thead>
                  <tbody><tr v-for="b in auto.blocked" :key="b.zone + b.ip"><td class="mono-ish">{{ b.ip }}</td><td>{{ b.zone }}</td><td class="small secondary">{{ b.reason || '—' }}</td><td class="small">{{ untilText(b.until) }}</td></tr></tbody>
                </table>
              </div>
              <details class="auto-events" v-if="auto.events.length">
                <summary><ui-icon name="chevron"></ui-icon>最近的记录（{{ auto.events.length }}）</summary>
                <ul class="small"><li v-for="(e, i) in auto.events.slice().reverse()" :key="i"><span class="tertiary">{{ whenText(e.at) }}</span> {{ e.text }}</li></ul>
              </details>
            </div>
            <div class="auto-form" v-else>
              <div class="row form"><span class="k">封禁哪些 IP</span><span class="v">
                <select v-model="autoForm.level" aria-label="封禁哪些 IP"><option value="high">高风险的</option><option value="medium">高风险和中风险的</option></select></span></div>
              <div class="row form"><span class="k">AI 把关</span><span class="v"><label class="check"><input type="checkbox" v-model="autoForm.requireAI"> 只封 AI 也建议封禁的</label>
                <div class="small tertiary">每个 IP 每天最多问一次 AI，会产生少量费用；AI 没配置好时不会封禁。</div></span></div>
              <div class="row form"><span class="k">封多久</span><span class="v">
                <select v-model.number="autoForm.hours" aria-label="封多久"><option :value="1">1 小时</option><option :value="24">24 小时</option><option :value="168">7 天</option><option :value="720">30 天</option><option :value="0">一直（手动解封）</option></select>
                <div class="small tertiary">到期解封后，如果它又来了，会再次封禁。</div></span></div>
              <div class="row form"><span class="k">不要封的 IP</span><span class="v">
                <textarea v-model="autoForm.allowText" rows="3" placeholder="每行一个 IP 或网段，例如你自己的 IP、公司网段" aria-label="不要自动封禁的 IP"></textarea></span></div>
              <div class="row"><span class="grow"></span>
                <button class="plain" v-if="auto.settings.enabled" @click="turnOffAuto" :disabled="autoBusy">关闭自动封禁</button>
                <button @click="autoEdit = false">取消</button>
                <button class="primary" @click="saveAuto(true)" :disabled="autoBusy">{{ auto.settings.enabled ? '保存' : '开启' }}</button></div>
            </div>
          </section>
        </template>
      </template>

      <!-- One IP: where it is and what it did -->
      <div class="drawer-mask" v-if="drawer" @click.self="drawer = null">
        <aside class="drawer" role="dialog" :aria-label="'IP ' + drawer.ip">
          <header class="drawer-head">
            <div class="grow"><div class="drawer-ip mono-ish">{{ drawer.ip }}</div><div class="small secondary">{{ drawer.place || '未知地区' }} {{ drawer.isp }}</div></div>
            <span class="risk-chip" :class="RISK[drawer.risk].cls">{{ RISK[drawer.risk].text }} · {{ drawer.score }} 分</span>
            <button class="plain icon-only" @click="drawer = null" aria-label="关闭"><ui-icon name="close"></ui-icon></button>
          </header>
          <div class="drawer-body">
            <div class="facts">
              <span class="fact ok" v-if="drawer.crawler">已验证：{{ drawer.crawler }}</span>
              <span class="fact crit" v-if="drawer.fakeCrawler">冒充搜索引擎</span>
              <span class="fact warn" v-if="drawer.edgeOne">EdgeOne 节点（不是真实访客）</span>
              <span class="fact warn" v-if="fromServer && drawer.direct > 0">直接访问服务器 {{ fmtCount(drawer.direct) }} 次（没经过 EdgeOne，EdgeOne 封禁挡不住）</span>
              <span class="fact" v-if="drawer.bot && !drawer.crawler">程序：{{ drawer.bot }}</span>
            </div>
            <div class="kv-grid">
              <div><span>请求</span><b>{{ fmtCount(drawer.requests) }}</b></div>
              <div><span>浏览网页</span><b>{{ fmtCount(drawer.pv) }}</b></div>
              <div><span>404/403</span><b>{{ fmtCount(drawer.e4xx) }}</b></div>
              <div><span>5xx</span><b>{{ fmtCount(drawer.e5xx) }}</b></div>
              <div><span>POST</span><b>{{ fmtCount(drawer.posts) }}</b></div>
              <div><span>不同地址</span><b>{{ fmtCount(drawer.paths) }}</b></div>
              <div><span>每分钟最多</span><b>{{ drawer.peakMin }}</b></div>
              <div><span>登录失败</span><b>{{ drawer.login }}</b></div>
            </div>
            <div class="drawer-sec"><h4>时间</h4><p class="small">{{ drawer.first }} 到 {{ drawer.last }}</p></div>
            <div class="drawer-sec" v-if="(drawer.reasons || []).length"><h4>判断依据</h4><ul class="small"><li v-for="r in drawer.reasons" :key="r">{{ r }}</li></ul></div>
            <div class="drawer-sec" v-if="verdictOf(drawer.ip)"><h4>AI 研判</h4><p class="small"><b>{{ VERDICT[verdictOf(drawer.ip).action] }}</b>：{{ verdictOf(drawer.ip).reason }}</p></div>
            <div class="drawer-sec"><h4>访问的网站</h4><rank-list :items="drawer.sites || []" :total="drawer.requests" :limit="5"></rank-list></div>
            <div class="drawer-sec"><h4>常访问的地址</h4><rank-list :items="drawer.topPaths || []" :total="drawer.requests" :limit="5"></rank-list></div>
            <div class="drawer-sec"><h4>浏览器标识</h4><p class="small mono-ish ua">{{ drawer.ua || '—' }}</p></div>
          </div>
          <footer class="drawer-foot">
            <button @click="askIP(drawer)"><ui-icon name="sparkles"></ui-icon>让 AI 分析</button>
            <span class="grow"></span>
            <span class="small tertiary" v-if="blockedSet.has(drawer.ip)">已封禁</span>
            <button class="primary" v-else-if="blockable(drawer)" :disabled="planning || !tencent" @click="block([drawer.ip])">封禁这个 IP</button>
            <span class="small tertiary" v-else>这个 IP 不能封禁</span>
          </footer>
        </aside>
      </div>

      <!-- The checklist to confirm -->
      <div class="sheet-mask" v-if="plan" @click.self="plan = null">
        <div class="sheet plan-sheet" role="dialog" aria-label="确认清单">
          <h2>{{ plan.title }}</h2>
          <p>勾选后点「执行」，确认后才会生效；执行后可以在这里或「建议」页撤销。</p>
          <plan-card :plan="plan" :server-name="planServerName" @done="planDone"></plan-card>
          <div class="sheet-actions"><button @click="plan = null">关闭</button></div>
        </div>
      </div>
    </template>
  </div>`,
};

// xterm.js (MIT, vendored in xterm/) is only loaded when a terminal opens.
let xtermLoading = null;
function loadXterm() {
  if (!xtermLoading) {
    const script = src => new Promise((ok, bad) => {
      const el = document.createElement('script');
      el.src = src; el.onload = ok; el.onerror = () => bad(new Error('终端组件加载失败'));
      document.head.appendChild(el);
    });
    const css = document.createElement('link');
    css.rel = 'stylesheet'; css.href = 'xterm/xterm.css';
    document.head.appendChild(css);
    xtermLoading = script('xterm/xterm.js').then(() => script('xterm/addon-fit.js'));
    xtermLoading.catch(() => { xtermLoading = null; });
  }
  return xtermLoading;
}
const TERM_THEME = {
  background: '#1c1c1e', foreground: '#e8e8ed', cursor: '#0a84ff', cursorAccent: '#1c1c1e',
  selectionBackground: 'rgba(10, 132, 255, 0.35)',
};

// Terminals on servers, one tab each. They are the user's own hands on
// the server: nothing typed here goes through the AI or the checklists.
const TerminalPage = {
  props: { servers: { type: Array, default: () => [] }, active: Boolean, request: Object },
  setup(props) {
    const tabs = ref([]); // {key, id, serverId, name, state: connecting|open|ended|error, error}
    const current = ref('');
    const pick = ref(props.servers.length ? props.servers[0].id : 0);
    watch(() => props.servers, list => { if (!list.some(s => s.id === pick.value)) pick.value = list.length ? list[0].id : 0; });
    const live = new Map(); // key -> {term, fit, ctl, queue, sending, ro, el}
    let n = 0;
    const tabOf = key => tabs.value.find(t => t.key === key);

    function setBox(key, el) { if (el) { const l = live.get(key) || {}; l.el = el; live.set(key, l); } }
    function fitNow(key) {
      const l = live.get(key);
      if (l && l.fit && l.el && l.el.offsetWidth > 0) { try { l.fit.fit(); } catch { /* not laid out yet */ } }
    }
    async function makeTerm(key) {
      await loadXterm();
      await nextTick();
      const l = live.get(key);
      const term = new window.Terminal({
        fontFamily: '"SF Mono", Menlo, Consolas, "Cascadia Mono", "Microsoft YaHei Mono", monospace',
        fontSize: 13, lineHeight: 1.15, cursorBlink: true, scrollback: 5000, theme: TERM_THEME, allowProposedApi: false,
      });
      const fit = new window.FitAddon.FitAddon();
      term.loadAddon(fit);
      term.open(l.el);
      // Ctrl+C copies when something is selected (like Windows Terminal);
      // Ctrl+V pastes through the browser.
      term.attachCustomKeyEventHandler(e => {
        if (e.type !== 'keydown' || !e.ctrlKey) return true;
        const k = e.key.toLowerCase();
        if (k === 'c' && (term.hasSelection() || e.shiftKey)) {
          if (term.hasSelection() && navigator.clipboard) navigator.clipboard.writeText(term.getSelection()).catch(() => {});
          term.clearSelection();
          return false;
        }
        if (k === 'v') return false;
        return true;
      });
      Object.assign(l, { term, fit, queue: '', sending: false });
      l.ro = new ResizeObserver(() => fitNow(key));
      l.ro.observe(l.el);
      fitNow(key);
      let timer = null;
      term.onResize(({ cols, rows }) => {
        clearTimeout(timer);
        timer = setTimeout(() => {
          const t = tabOf(key);
          if (t && t.id && t.state === 'open') api('POST', `/api/terminals/${t.id}/resize`, { cols, rows }).catch(() => {});
        }, 150);
      });
      term.onData(d => send(key, d));
      return l;
    }
    function send(key, data) {
      const l = live.get(key), t = tabOf(key);
      // Typing still reaches the shell while the output reconnects.
      if (!l || !t || !(t.state === 'open' || t.state === 'reconnecting')) return;
      l.queue += data;
      if (!l.sending) flush(key);
    }
    async function flush(key) {
      const l = live.get(key), t = tabOf(key);
      l.sending = true;
      while (l.queue && t && (t.state === 'open' || t.state === 'reconnecting')) {
        const data = l.queue;
        l.queue = '';
        try { await api('POST', `/api/terminals/${t.id}/input`, { data }); }
        catch { l.queue = ''; break; }
      }
      l.sending = false;
    }
    // attach streams the terminal's output into the tab. The stream is
    // server-sent events: output in base64 with its offset as the id,
    // "reset" when the replay does not start where we asked, a ping every
    // 15 seconds, "end" when the shell has ended. When the stream drops
    // while the shell is still running (a proxy timing out, the network
    // changing, a laptop waking up) it picks up again from the last
    // offset, so nothing is lost or printed twice.
    async function attach(key) {
      const t = tabOf(key), l = live.get(key);
      if (!t || !l || !t.id) return;
      if (l.ctl) l.ctl.abort();
      clearTimeout(l.retry);
      const ctl = new AbortController();
      l.ctl = ctl;
      let heard = Date.now(), ended = false, gone = '';
      // A connection that went quiet without closing (pings stopped).
      const watch = setInterval(() => { if (Date.now() - heard > 40000) ctl.abort(); }, 5000);
      try {
        const from = l.offset == null ? -1 : l.offset;
        const res = await fetch(`/api/terminals/${t.id}/output?from=${from}`, { headers: { 'X-Miao': '1' }, credentials: 'same-origin', signal: ctl.signal });
        if (!res.ok) {
          const d = await res.json().catch(() => ({}));
          // 400: the terminal is gone on the server; anything else may pass.
          if (res.status === 400) gone = d.error || '会话已结束';
          else if (res.status === 401 && window.MIAO_MODE === 'server') { location.reload(); return; }
          throw new Error(d.error || `连接失败（${res.status}）`);
        }
        t.state = 'open'; t.error = ''; l.tries = 0;
        const reader = res.body.getReader(), dec = new TextDecoder();
        let text = '', ev = {};
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          heard = Date.now();
          text += dec.decode(value, { stream: true });
          let i;
          while ((i = text.indexOf('\n')) >= 0) {
            const line = text.slice(0, i);
            text = text.slice(i + 1);
            if (line === '') {
              if (ev.event === 'reset') l.term.reset();
              else if (ev.event === 'end') ended = true;
              else if (ev.data) {
                const bin = atob(ev.data), bytes = new Uint8Array(bin.length);
                for (let j = 0; j < bin.length; j++) bytes[j] = bin.charCodeAt(j);
                l.term.write(bytes);
                if (ev.id) l.offset = Number(ev.id);
              }
              ev = {};
            } else if (line[0] !== ':') {
              const c = line.indexOf(':');
              ev[c < 0 ? line : line.slice(0, c)] = c < 0 ? '' : line.slice(c + 1).replace(/^ /, '');
            }
          }
        }
      } catch (e) {
        if (e.name === 'AbortError' && l.ctl !== ctl) return; // replaced or closed
        if (!gone) t.error = e.name === 'AbortError' ? '连接没有响应' : e.message;
      } finally {
        clearInterval(watch);
      }
      if (l.ctl !== ctl || !tabOf(key)) return;
      if (ended || gone) { t.state = 'ended'; t.error = gone && !ended ? gone : ''; return; }
      // Dropped: try again, soon at first, then less often.
      t.state = 'reconnecting';
      const wait = Math.min(1000 * 2 ** (l.tries || 0), 15000);
      l.tries = (l.tries || 0) + 1;
      l.retry = setTimeout(() => { if (tabOf(key) && t.state === 'reconnecting') attach(key); }, wait);
    }
    // Back online or back to the page: reconnect now rather than after the wait.
    function retryNow() {
      for (const t of tabs.value) if (t.state === 'reconnecting') { const l = live.get(t.key); if (l) l.tries = 0; attach(t.key); }
    }
    const onVisible = () => { if (document.visibilityState === 'visible') retryNow(); };
    window.addEventListener('online', retryNow);
    document.addEventListener('visibilitychange', onVisible);
    async function open(serverId) {
      const sv = props.servers.find(s => s.id === serverId);
      if (!sv) return;
      const key = 'k' + (++n);
      tabs.value.push({ key, id: '', serverId, name: sv.name, state: 'connecting', error: '' });
      current.value = key;
      await connect(key);
    }
    async function connect(key) {
      const t = tabOf(key);
      t.state = 'connecting'; t.error = '';
      try {
        const l = live.get(key) && live.get(key).term ? live.get(key) : await makeTerm(key);
        const v = await api('POST', `/api/servers/${t.serverId}/terminal`, { cols: l.term.cols, rows: l.term.rows });
        // Closed while it was connecting: end the new one on the server too.
        if (!tabOf(key)) { api('DELETE', `/api/terminals/${v.id}`).catch(() => {}); return; }
        t.id = v.id;
        l.offset = 0; // a new shell: keep what the tab already shows
        l.tries = 0;
        attach(key);
        l.term.focus();
      } catch (e) { if (tabOf(key)) { t.state = 'error'; t.error = e.message; } }
    }
    // The button on a tab that ended or failed: pick up the same shell if
    // it is still running on the server, otherwise start a new one.
    async function reconnect(key) {
      const t = tabOf(key), l = live.get(key);
      if (!t) return;
      if (t.id && l && l.term && t.state !== 'ended') {
        l.tries = 0;
        t.state = 'reconnecting';
        attach(key);
        return;
      }
      if (l && l.term) l.term.write('\r\n\x1b[90m[重新连接……]\x1b[0m\r\n');
      connect(key);
    }
    async function close(key) {
      const t = tabOf(key);
      if (!t) return;
      if ((t.state === 'open' || t.state === 'reconnecting') && !confirm(`关闭「${t.name}」的终端？正在运行的命令会被结束。`)) return;
      const l = live.get(key);
      if (l) {
        clearTimeout(l.retry);
        const ctl = l.ctl;
        l.ctl = null;
        if (ctl) ctl.abort();
        if (l.ro) l.ro.disconnect();
        if (l.term) l.term.dispose();
        live.delete(key);
      }
      if (t.id && t.state !== 'ended') api('DELETE', `/api/terminals/${t.id}`).catch(() => {});
      const i = tabs.value.indexOf(t);
      tabs.value.splice(i, 1);
      if (current.value === key) current.value = tabs.value.length ? tabs.value[Math.max(0, i - 1)].key : '';
    }
    function show(key) {
      current.value = key;
      nextTick(() => { fitNow(key); const l = live.get(key); if (l && l.term) l.term.focus(); });
    }
    // Terminals still open on the server (e.g. after the page reloaded).
    onMounted(async () => {
      try {
        const list = await api('GET', '/api/terminals');
        for (const v of list.filter(v => !v.ended)) {
          const key = 'k' + (++n);
          tabs.value.push({ key, id: v.id, serverId: v.serverId, name: v.serverName, state: 'connecting', error: '' });
          current.value = key;
          await makeTerm(key);
          attach(key);
        }
      } catch { /* none to restore */ }
      if (props.request) onRequest(props.request);
    });
    function onRequest(r) {
      if (!r) return;
      const existing = tabs.value.find(t => t.serverId === r.serverId && (t.state === 'open' || t.state === 'reconnecting'));
      if (existing) show(existing.key); else open(r.serverId);
    }
    watch(() => props.request, onRequest);
    watch(() => props.active, on => { if (on && current.value) show(current.value); });
    onUnmounted(() => {
      window.removeEventListener('online', retryNow);
      document.removeEventListener('visibilitychange', onVisible);
      for (const [, l] of live) { clearTimeout(l.retry); const ctl = l.ctl; l.ctl = null; if (ctl) ctl.abort(); if (l.ro) l.ro.disconnect(); if (l.term) l.term.dispose(); }
    });
    const stateText = t => ({ connecting: '正在连接……', reconnecting: '重新连接中……', ended: '已结束', error: '出错' }[t.state] || '');
    return { tabs, current, pick, setBox, open, close, show, reconnect, stateText };
  },
  template: `
  <div class="term-page">
    <div class="term-bar">
      <div class="term-tabs" role="tablist">
        <div v-for="t in tabs" :key="t.key" class="term-tab" :class="{on: current === t.key, off: t.state !== 'open'}" role="tab" :aria-selected="current === t.key" @click="show(t.key)">
          <ui-icon name="prompt"></ui-icon><span class="name">{{ t.name }}</span><span class="small tertiary" v-if="stateText(t)">{{ stateText(t) }}</span>
          <button class="plain icon-only" title="关闭" aria-label="关闭终端" @click.stop="close(t.key)"><ui-icon name="close"></ui-icon></button>
        </div>
      </div>
      <span class="grow"></span>
      <select v-model="pick" aria-label="服务器" v-if="servers.length > 1"><option v-for="s in servers" :key="s.id" :value="s.id">{{ s.name }}</option></select>
      <button @click="open(pick)" :disabled="!pick"><ui-icon name="plus"></ui-icon>新建终端</button>
    </div>
    <div class="term-body">
      <div class="term-empty" v-if="!tabs.length">
        <ui-icon name="prompt" class="lg"></ui-icon>
        <p v-if="servers.length">选择服务器，打开一个命令行终端（SSH），可以直接在服务器上敲命令。</p>
        <p v-else>先在左边添加服务器。</p>
        <div class="term-picks"><button v-for="s in servers" :key="s.id" @click="open(s.id)">{{ s.name }}</button></div>
        <p class="small tertiary">终端里的命令直接在服务器上执行，不经过 AI 检查，也不会自动备份，请确认后再回车。</p>
      </div>
      <div v-for="t in tabs" :key="t.key" class="term-wrap" v-show="current === t.key">
        <div class="term-box" :ref="el => setBox(t.key, el)"></div>
        <div class="term-note" v-if="t.state === 'reconnecting'" role="status">连接断了，正在重新连接{{ t.error ? '（' + t.error + '）' : '' }}……<button class="plain" @click="reconnect(t.key)">现在重试</button></div>
        <div class="term-over" v-if="t.state === 'error' || t.state === 'ended'">
          <span :class="t.state === 'error' ? 'st-crit' : ''">{{ t.state === 'error' ? t.error : (t.error || '会话已结束') }}</span>
          <button @click="reconnect(t.key)"><ui-icon name="refresh"></ui-icon>重新连接</button>
        </div>
      </div>
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

const LEVEL_ICON = { ok: 'check', warn: 'warn', crit: 'alert', info: 'info' };
const LEVEL_RANK = { crit: 0, warn: 1, info: 2, ok: 3 };
const rankOf = l => LEVEL_RANK[l] ?? 2; // unknown counts as info
const CERT_SHOWS = [{ id: 'all', text: '全部' }, { id: 'attention', text: '需要处理' }, { id: 'manual', text: '不会自动续签' }];
const CERT_SORTS = [{ id: 'urgent', text: '最紧急在前' }, { id: 'expiry', text: '最先到期' }, { id: 'domain', text: '按域名' }];
const byStr = (a, b) => a < b ? -1 : a > b ? 1 : 0;
const nullsLast = (a, b) => a == null ? (b == null ? 0 : 1) : b == null ? -1 : a - b;
const expiryMs = x => { const t = x && x.notAfter ? Date.parse(x.notAfter) : NaN; return Number.isFinite(t) ? t : null; };
// Domains sort by the registered name, a parent before its subdomains:
// *.miao.club, miao.club, www.miao.club, blog.miao.club, sakura.vin, ...
function domainKey(d) {
  let h = String(d || '').toLowerCase().trim().replace(/\.$/, '');
  if (h.startsWith('*.')) h = h.slice(2);
  if (h.startsWith('www.')) h = h.slice(4);
  const p = h.split('.');
  const n = p.length > 2 && /^(com|net|org|gov|edu|ac|co)\.[a-z]{2}$/.test(p.slice(-2).join('.')) ? 3 : 2;
  return [p.slice(-n).join('.'), p.slice(0, -n).reverse().join('.')];
}
const cmpDomain = (a, b) => {
  const x = domainKey(a), y = domainKey(b);
  return byStr(x[0], y[0]) || byStr(x[1], y[1]) || byStr(String(a).toLowerCase(), String(b).toLowerCase()) || byStr(a, b);
};
// coversHost is covers() in certgroups.go: *.miao.club covers blog.miao.club.
function coversHost(names, host) {
  if (!host.includes('.') || host.includes('*')) return false;
  return (names || []).some(n => {
    n = String(n).toLowerCase();
    if (n === host) return true;
    if (!n.startsWith('*.') || !host.endsWith(n.slice(1))) return false;
    const rest = host.slice(0, host.length - (n.length - 1));
    return rest !== '' && !rest.includes('.');
  });
}
// normNeedle makes a pasted address searchable: https://Blog.Sakura.vin:443/x → blog.sakura.vin.
function normNeedle(q) {
  let s = String(q || '').trim().toLowerCase().replace(/[。．]/g, '.');
  s = s.replace(/^[a-z][a-z0-9+.-]*:\/\//, '');
  s = s.split(/[/?#]/)[0];
  if (s.includes('.')) s = s.replace(/:\d+$/, '');
  return s.trim();
}
const leftText = d => d == null ? '' : d > 0 ? `剩 ${d} 天` : d === 0 ? '不到 1 天' : '已过期';

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
    const groups = computed(() => (data.value && data.value.groups) || []);
    const live = computed(() => (data.value && data.value.live) || []);
    const noHttps = computed(() => (data.value && data.value.noHttps) || []);
    const inUse = computed(() => groups.value.flatMap(g => g.certs).filter(c => c.inUse));
    const counts = computed(() => ({
      total: inUse.value.length,
      auto: inUse.value.filter(c => c.autoRenew).length,
      soon: inUse.value.filter(c => c.daysLeft != null && c.daysLeft >= 0 && c.daysLeft < 30 && !c.autoRenew).length,
      // A site whose bad certificate is already counted is not counted again.
      bad: groups.value.filter(g => g.level === 'crit').length + live.value.filter(l => l.level === 'crit' && !problemServed.value.has(l.domain)).length,
    }));

    // Filters are not remembered, so one left on can never hide a new
    // problem after a restart; the sort is.
    const q = ref('');
    const show = ref('all');
    const s0 = pref('miao.certSort', 'urgent');
    const sort = ref(CERT_SORTS.some(s => s.id === s0) ? s0 : 'urgent');
    watch(sort, v => setPref('miao.certSort', v));
    const needle = computed(() => normNeedle(q.value));
    const filtered = computed(() => needle.value !== '' || show.value !== 'all');
    const reveal = ref(new Set()); // domains whose card also shows the rows filtered out
    watch([needle, show], () => { reveal.value = new Set(); });
    function toggleReveal(d) { const s = new Set(reveal.value); s.has(d) ? s.delete(d) : s.add(d); reveal.value = s; }
    function clearFilters() { q.value = ''; show.value = 'all'; }

    // What a search looks at: the certificate's domains, and (without
    // spanning fields) its issuer, status, renewal and where it is kept, so
    // server names, EdgeOne, 腾讯云, 过期 all work.
    const hostHay = (g, c) => [g.domain, ...(c.names || []), ...(c.usedBy || []), ...(c.edgeOne || []), ...(c.servedOn || [])].join('\n').toLowerCase();
    const infoHay = c => [c.issuer || '', c.status || '', c.renew || '', ...(c.copies || []).map(p => p.where || '')].join('\n').toLowerCase();
    const matchCert = (g, c) => { const n = needle.value; return !n || hostHay(g, c).includes(n) || infoHay(c).includes(n) || coversHost(c.names, n); };
    // The rows that put a domain under 需要处理 (as in certgroups.go).
    const isProblem = (g, c) => (c.level === 'crit' || c.level === 'warn') && (c.inUse || !g.certs.some(x => x.inUse));
    const isManual = c => c.inUse && !c.autoRenew;
    const passShow = (g, c) => show.value === 'all' || (show.value === 'attention' ? isProblem(g, c) : isManual(c));
    const pass = (g, c) => matchCert(g, c) && passShow(g, c);

    // A card per domain with its matching certificates; sections follow the
    // whole domain, so a card never jumps while typing.
    function viewOf(g) {
      const ok = g.certs.filter(c => pass(g, c));
      const open = reveal.value.has(g.domain);
      const used = ok.filter(c => c.inUse);
      const times = (used.length ? used : ok).map(expiryMs).filter(t => t != null);
      return { g, ok: ok.length, certs: open ? g.certs : ok, open, hidden: g.certs.length - ok.length, when: times.length ? Math.min(...times) : null };
    }
    function cmpView(a, b) {
      const lv = rankOf(a.g.level) - rankOf(b.g.level), dm = cmpDomain(a.g.domain, b.g.domain);
      if (sort.value === 'expiry') return nullsLast(a.when, b.when) || lv || dm;
      if (sort.value === 'domain') return dm || lv;
      return lv || nullsLast(a.when, b.when) || dm;
    }
    const sectionOf = g => g.level === 'crit' || g.level === 'warn' ? 'attention' : g.certs.some(c => c.inUse) ? 'fine' : 'unused';
    const views = computed(() => groups.value.map(viewOf).filter(v => v.ok > 0));
    const attentionV = computed(() => views.value.filter(v => sectionOf(v.g) === 'attention').sort(cmpView));
    const fineV = computed(() => views.value.filter(v => sectionOf(v.g) === 'fine').sort(cmpView));
    const unusedV = computed(() => views.value.filter(v => sectionOf(v.g) === 'unused').sort(cmpView));
    const unusedOpen = ref(false);
    watch(() => !!needle.value && unusedV.value.length > 0, on => { if (on) unusedOpen.value = true; });

    // 实际访问到的证书: a search for a server name, issuer or status also
    // lists the sites its certificates serve.
    const linked = computed(() => {
      const n = needle.value, out = new Set();
      if (!n) return out;
      for (const g of groups.value) for (const c of g.certs) {
        if (infoHay(c).includes(n)) [...(c.usedBy || []), ...(c.edgeOne || []), ...(c.servedOn || [])].forEach(d => out.add(d));
      }
      return out;
    });
    const manualServed = computed(() => {
      const out = new Set();
      for (const g of groups.value) for (const c of g.certs) if (matchCert(g, c) && isManual(c)) (c.servedOn || []).forEach(d => out.add(d));
      return out;
    });
    const liveMatch = l => { const n = needle.value; return !n || [l.domain, l.issuer || '', l.status || ''].join('\n').toLowerCase().includes(n) || linked.value.has(l.domain); };
    const livePass = l => liveMatch(l) && (show.value === 'all' || (show.value === 'attention' ? l.level === 'crit' || l.level === 'warn' : manualServed.value.has(l.domain)));
    function cmpLive(a, b) {
      const lv = rankOf(a.level) - rankOf(b.level), dm = cmpDomain(a.domain, b.domain), ex = nullsLast(expiryMs(a), expiryMs(b));
      if (sort.value === 'expiry') return ex || lv || dm;
      if (sort.value === 'domain') return dm;
      return lv || ex || dm;
    }
    const liveShown = computed(() => live.value.filter(livePass).sort(cmpLive));
    const noHttpsShown = computed(() => show.value !== 'all' ? [] : noHttps.value
      .filter(e => !needle.value || (e.domain + '\n' + (e.where || '')).toLowerCase().includes(needle.value))
      .sort((a, b) => cmpDomain(a.domain, b.domain)));

    // Problems counted once each: a problem certificate, or a site with a
    // bad certificate that no problem certificate explains (one it is used
    // on or was served by).
    const problemServed = computed(() => {
      const s = new Set();
      for (const g of groups.value) for (const c of g.certs) {
        if (isProblem(g, c)) [...(c.usedBy || []), ...(c.edgeOne || []), ...(c.servedOn || [])].forEach(d => s.add(d));
      }
      return s;
    });
    const liveOnly = l => (l.level === 'crit' || l.level === 'warn') && !problemServed.value.has(l.domain);
    const tally = computed(() => {
      let attention = 0, manual = 0;
      for (const g of groups.value) for (const c of g.certs) {
        if (!matchCert(g, c)) continue;
        if (isProblem(g, c)) attention++;
        if (isManual(c)) manual++;
      }
      return { attention: attention + live.value.filter(l => liveOnly(l) && liveMatch(l)).length, manual };
    });
    // A revealed card shows its hidden rows only while it is drawn at all
    // (a refresh can leave it with nothing that matches).
    const shownOpen = g => reveal.value.has(g.domain) && g.certs.some(x => pass(g, x));
    const hiddenProblems = computed(() => {
      let n = 0;
      for (const g of groups.value) for (const c of g.certs) if (isProblem(g, c) && !pass(g, c) && !shownOpen(g)) n++;
      return n + live.value.filter(l => liveOnly(l) && !livePass(l)).length;
    });
    const totalCount = computed(() => groups.value.reduce((s, g) => s + g.certs.length, 0));
    const shownCount = computed(() => groups.value.reduce((s, g) => s + g.certs.filter(c => pass(g, c)).length, 0));

    const date = t => t ? new Date(t).toLocaleDateString('zh-CN') : '—';
    const others = g => (g.names || []).filter(n => n !== g.domain).join('、');
    const cut = (list, n) => list.length > n ? list.slice(0, n).join('、') + ` 等 ${list.length} 个` : list.join('、');
    function uses(c) {
      const out = [];
      if (c.usedBy && c.usedBy.length) out.push('网站 ' + cut(c.usedBy, 4));
      if (c.edgeOne && c.edgeOne.length) out.push('EdgeOne ' + cut(c.edgeOne, 4));
      const served = (c.servedOn || []).filter(d => !(c.usedBy || []).includes(d) && !(c.edgeOne || []).includes(d));
      if (served.length) out.push('访问 ' + cut(served, 3) + ' 时看到的就是它');
      return out.join('；');
    }
    const copyText = p => p.where + (p.autoRenew ? '（自动续签）' : '');
    function action(g, c) {
      const panel = c.copies.find(p => p.source === '1panel' && p.canRenew);
      if (panel && c.inUse && (c.level === 'crit' || c.level === 'warn')) return ['让 AI 续签', `帮我立即续签 1Panel 里 ${g.domain} 的证书，并看看自动续签为什么没有成功`];
      if (panel && c.inUse && !c.autoRenew) return ['开启自动续签', `帮我开启 1Panel 里 ${g.domain} 证书的自动续签`];
      if (c.level === 'crit' || c.level === 'warn') return ['让 AI 处理', `${g.domain} 的证书${c.status}，帮我看看怎么处理`];
      return null;
    }
    const ask = text => emit('ask', text);
    return { data, loading, error, load, whenText, groups, live, noHttps, counts, date, others, uses, copyText, action, ask,
      q, show, sort, filtered, tally, hiddenProblems, attentionV, fineV, unusedV, unusedOpen, liveShown, noHttpsShown, shownCount, totalCount,
      toggleReveal, clearFilters, leftText, CERT_SHOWS, CERT_SORTS, icon: l => LEVEL_ICON[l] || 'info' };
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
        <div class="tile"><div class="label"><ui-icon name="lock"></ui-icon>证书</div><div class="value">{{ counts.total }}</div><div class="sub">在用的证书</div></div>
        <div class="tile"><div class="label"><ui-icon name="refresh"></ui-icon>自动续签</div><div class="value">{{ counts.auto }}</div><div class="sub">由 EdgeOne 或 1Panel 自动续签</div></div>
        <div class="tile"><div class="label"><ui-icon name="clock"></ui-icon>30 天内到期</div><div class="value">{{ counts.soon }}</div><div class="sub">而且不会自动续签</div></div>
        <div class="tile"><div class="label"><ui-icon name="alert"></ui-icon>有问题</div><div class="value">{{ counts.bad }}</div><div class="sub">已过期、申请失败或访问异常</div></div>
      </div>

      <div class="stat-bar cert-bar" v-if="groups.length || live.length || noHttps.length">
        <span class="segmented" role="group" aria-label="显示">
          <button v-for="s in CERT_SHOWS" :key="s.id" :aria-pressed="show === s.id" :class="{ on: show === s.id }" @click="show = s.id">{{ s.text }}
            <span class="badge crit" v-if="s.id === 'attention' && tally.attention">{{ tally.attention }}</span>
            <span class="tertiary" v-if="s.id === 'manual'">{{ tally.manual }}</span></button>
        </span>
        <label class="field cert-search"><span>搜索</span>
          <input type="search" v-model="q" placeholder="域名或服务器名" autocomplete="off" autocapitalize="off" spellcheck="false" enterkeyhint="search" @keydown.esc="q = ''"></label>
        <span class="grow"></span>
        <label class="field" title="需要处理的始终排在最上面"><span>排序</span>
          <select v-model="sort"><option v-for="s in CERT_SORTS" :key="s.id" :value="s.id">{{ s.text }}</option></select></label>
      </div>
      <!-- Its own line, so the bar does not rewrap while typing; the status
           stays mounted so screen readers hear each change. -->
      <div class="cert-result" :class="{ on: filtered }" v-if="groups.length || live.length || noHttps.length">
        <span class="small secondary" role="status">{{ filtered ? '显示 ' + shownCount + ' / ' + totalCount + ' 张证书' : '' }}<span class="sr-only" v-if="filtered && hiddenProblems">，还有 {{ hiddenProblems }} 个需要处理的问题被筛选隐藏了</span></span>
        <button class="link small" v-if="filtered" @click="clearFilters">清除筛选</button>
      </div>
      <div class="alert al-warn cert-hint" v-if="hiddenProblems">
        <ui-icon name="warn"></ui-icon><span class="grow">还有 {{ hiddenProblems }} 个需要处理的问题被筛选隐藏了</span>
        <button class="link small" @click="q = ''; show = 'attention'">查看</button>
      </div>

      <template v-for="sec in [{ title: '需要处理', list: attentionV }, { title: '证书', list: fineV }]" :key="sec.title">
        <template v-if="sec.list.length">
          <div class="group-title">{{ sec.title }}</div>
          <div class="cert-group" v-for="v in sec.list" :key="v.g.domain">
            <div class="cert-head">
              <ui-icon name="lock" class="lg"></ui-icon>
              <div class="grow"><div class="cert-domain">{{ v.g.domain }}</div><div class="small tertiary" v-if="others(v.g)">也包括 {{ others(v.g) }}</div></div>
              <span class="cert-st" :class="'st-' + v.g.level"><ui-icon :name="icon(v.g.level)"></ui-icon>{{ v.g.status }}</span>
            </div>
            <div class="cert-row" v-for="(c, i) in v.certs" :key="i" :class="{ muted: !c.inUse }">
              <div class="grow">
                <div class="cert-line"><b>{{ c.issuer || '证书' }}</b><span class="tag" :class="c.inUse ? 'on' : ''">{{ c.inUse ? '在用' : '没发现在用' }}</span></div>
                <div class="small secondary">到期 {{ date(c.notAfter) }}<template v-if="c.daysLeft != null">（{{ leftText(c.daysLeft) }}）</template> · {{ c.renew }}</div>
                <div class="small tertiary" v-if="uses(c)">用在 {{ uses(c) }}</div>
                <div class="small tertiary">存放在 {{ c.copies.map(copyText).join('、') }}</div>
                <div class="small st-crit" v-if="c.renewError">{{ c.renewError }}</div>
              </div>
              <div class="cert-side">
                <span class="cert-st small" :class="'st-' + c.level"><ui-icon :name="icon(c.level)"></ui-icon>{{ c.status }}</span>
                <button class="link small" v-if="action(v.g, c)" @click="ask(action(v.g, c)[1])">{{ action(v.g, c)[0] }}</button>
              </div>
            </div>
            <div class="cert-row cert-rest" v-if="v.hidden">
              <button class="link small" :aria-expanded="v.open" @click="toggleReveal(v.g.domain)">{{ v.open ? '收起不符合筛选的证书' : '另有 ' + v.hidden + ' 张证书不符合筛选，显示' }}</button>
            </div>
          </div>
        </template>
      </template>
      <div class="group" v-if="!groups.length && !loading"><div class="row small secondary">还没有找到证书。{{ configured ? '' : '在「设置 → 腾讯云」填好密钥后可以看到 EdgeOne 和腾讯云的证书；' }}配置了 1Panel 接口的服务器会显示 1Panel 里的证书。</div></div>
      <div class="group" v-if="groups.length && filtered && !attentionV.length && !fineV.length && !unusedV.length">
        <div class="row" v-if="q.trim()"><div class="grow">没有和「{{ q.trim() }}」有关的证书。</div><button class="link small" @click="clearFilters">清除筛选</button></div>
        <div class="row" v-else-if="show === 'attention'"><ui-icon name="check" class="st-ok"></ui-icon>
          <div class="grow"><div>没有需要处理的证书。</div><div class="small tertiary">已过期、快到期、续签或申请失败的证书，都会出现在这里。</div>
            <div class="small secondary" v-if="liveShown.length">下面「实际访问到的证书」里还有访问时有问题的网址。</div></div></div>
        <div class="row" v-else><div class="grow">在用的证书都会自动续签，不用手动处理。</div><button class="link small" @click="show = 'all'">显示全部</button></div>
      </div>

      <details class="cert-more" v-if="unusedV.length" :open="unusedOpen" @toggle="unusedOpen = $event.target.open">
        <summary><ui-icon name="chevron"></ui-icon>没发现在用的证书（{{ unusedV.length }} 个域名）</summary>
        <p class="small tertiary">没有网站、EdgeOne 域名在用，访问时也没看到。已经过期的可以在腾讯云或 1Panel 里删除；如果它们用在负载均衡、CDN 等别的地方，请忽略这个提示。</p>
        <div class="cert-group" v-for="v in unusedV" :key="v.g.domain">
          <div class="cert-head">
            <ui-icon name="lock" class="lg"></ui-icon>
            <div class="grow"><div class="cert-domain">{{ v.g.domain }}</div><div class="small tertiary" v-if="others(v.g)">也包括 {{ others(v.g) }}</div></div>
          </div>
          <div class="cert-row muted" v-for="(c, i) in v.certs" :key="i">
            <div class="grow">
              <div class="cert-line"><b>{{ c.issuer || '证书' }}</b></div>
              <div class="small secondary">到期 {{ date(c.notAfter) }} · 存放在 {{ c.copies.map(copyText).join('、') }}</div>
            </div>
            <div class="cert-side"><span class="cert-st small st-info"><ui-icon name="info"></ui-icon>{{ c.status }}</span></div>
          </div>
          <div class="cert-row cert-rest" v-if="v.hidden">
            <button class="link small" :aria-expanded="v.open" @click="toggleReveal(v.g.domain)">{{ v.open ? '收起不符合筛选的证书' : '另有 ' + v.hidden + ' 张证书不符合筛选，显示' }}</button>
          </div>
        </div>
      </details>

      <template v-if="noHttpsShown.length">
        <div class="group-title">没有开启 HTTPS 的 EdgeOne 域名</div>
        <div class="group">
          <div class="row" v-for="e in noHttpsShown" :key="e.domain">
            <div class="grow"><div>{{ e.domain }}</div><div class="small tertiary">访问只能用 http://</div></div>
            <button class="link small" @click="ask('帮 ' + e.domain + ' 开启 HTTPS（EdgeOne 免费证书，自动续签）')">开启 HTTPS</button>
          </div>
        </div>
      </template>

      <template v-if="liveShown.length">
        <div class="group-title">实际访问到的证书<span v-if="filtered && liveShown.length < live.length">（显示 {{ liveShown.length }} / {{ live.length }}）</span></div>
        <div class="group table-wrap">
          <table class="table cert-table">
            <thead><tr>
              <th scope="col" :aria-sort="sort === 'domain' ? 'ascending' : null"><button class="th-sort" :class="{ on: sort === 'domain' }" @click="sort = 'domain'" title="按域名排序">网址<ui-icon name="arrow-up"></ui-icon></button></th>
              <th scope="col">签发</th>
              <th scope="col" :aria-sort="sort === 'expiry' ? 'ascending' : null"><button class="th-sort" :class="{ on: sort === 'expiry' }" @click="sort = 'expiry'" title="按到期时间排序，最早到期的在前">到期<ui-icon name="arrow-up"></ui-icon></button></th>
              <th scope="col" :aria-sort="sort === 'urgent' ? 'ascending' : null"><button class="th-sort" :class="{ on: sort === 'urgent' }" @click="sort = 'urgent'" title="按紧急程度排序，最紧急的在前">状态<ui-icon name="arrow-up"></ui-icon></button></th>
            </tr></thead>
            <tbody>
              <tr v-for="l in liveShown" :key="l.domain">
                <td>https://{{ l.domain }}</td>
                <td class="small">{{ l.issuer || '—' }}</td>
                <td class="num">{{ date(l.notAfter) }}<div class="small tertiary" v-if="l.daysLeft != null">{{ leftText(l.daysLeft) }}</div></td>
                <td><span class="cert-st" :class="'st-' + l.level"><ui-icon :name="icon(l.level)"></ui-icon>{{ l.status }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="small tertiary" style="margin: -16px 4px 22px">从 Miao Panel 所在的机器直接访问每个网站得到的证书，和访问者看到的一致。</p>
      </template>
      <div class="notice" v-for="n in data.notes || []" :key="n"><ui-icon name="info"></ui-icon>没能读取：{{ n }}</div>
    </template>
  </div>`,
};

// 解析: DNSPod records, with what EdgeOne does for each name. Changes are
// proposed as checklists, confirmed in a sheet, and can be undone.
const RECORD_TYPES = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'CAA', 'SRV'];
const TTLS = [60, 120, 300, 600, 1800, 3600, 86400];
const ttlText = n => n >= 86400 && n % 86400 === 0 ? `${n / 86400} 天` : n >= 3600 && n % 3600 === 0 ? `${n / 3600} 小时` : n >= 60 && n % 60 === 0 ? `${n / 60} 分钟` : `${n} 秒`;
const VALUE_HINT = {
  A: 'IPv4 地址，例如 1.2.3.4', AAAA: 'IPv6 地址，例如 2001:db8::1', CNAME: '另一个域名，例如 example.github.io',
  MX: '邮件服务器，例如 mxbiz1.qq.com', TXT: '文本，例如 v=spf1 include:spf.mail.qq.com ~all', NS: '域名服务器，例如 ns1.example.net',
  CAA: '例如 0 issue "letsencrypt.org"', SRV: '优先级 权重 端口 目标，例如 5 0 5060 sip.example.com',
};
const EO_AREAS = [{ id: 'mainland', text: '中国大陆（域名要已备案）' }, { id: 'overseas', text: '全球（不含中国大陆）' }, { id: 'global', text: '全球（含中国大陆，要备案）' }];
const dnsMemo = new Map();

const DnsPage = {
  props: { configured: Boolean, active: Boolean, servers: { type: Array, default: () => [] } },
  emits: ['settings'],
  setup(props) {
    const domains = ref([]);
    const eoListError = ref(''); // reading the sites for the domain list
    // Either call may fail to read EdgeOne; the records call says so too.
    const eoError = computed(() => eoListError.value || (data.value && data.value.eoError) || '');
    const domain = ref(pref('miao.dnsDomain', ''));
    const data = ref(null);
    const loading = ref(false);
    const error = ref('');
    const q = ref('');
    const typeFilter = ref('');
    const plan = ref(null);
    const planning = ref(false);
    const lines = ref([]);
    const formError = ref('');
    let seq = 0;
    watch(domain, v => { if (v) setPref('miao.dnsDomain', v); load(); });

    const domainsLoaded = ref(false);
    async function loadDomains() {
      if (!props.configured) return;
      try {
        const r = await api('GET', '/api/dns/domains');
        domains.value = r.domains; eoListError.value = r.eoError || '';
        if (!r.domains.some(d => d.name === domain.value)) domain.value = r.domains.length ? r.domains[0].name : '';
        else load();
      } catch (e) { error.value = e.message; } finally { domainsLoaded.value = true; }
    }
    async function load(fresh) {
      const d = domain.value;
      if (!d) { data.value = null; return; }
      const n = ++seq;
      const memo = dnsMemo.get(d);
      if (memo && !fresh) data.value = memo; else if (!memo) data.value = null;
      loading.value = true; error.value = '';
      try {
        const r = await api('GET', '/api/dns/records?domain=' + encodeURIComponent(d));
        if (n !== seq) return;
        dnsMemo.set(d, r); data.value = r;
      } catch (e) { if (n === seq) error.value = e.message; }
      finally { if (n === seq) loading.value = false; }
    }
    // Coming back to the page shows what it had at once, then refreshes.
    watch(() => props.active, v => { if (v) domains.value.length ? load() : loadDomains(); }, { immediate: true });
    watch(() => props.configured, v => { if (v) loadDomains(); });

    const current = computed(() => domains.value.find(d => d.name === domain.value) || null);
    const zone = computed(() => data.value && data.value.edgeone);
    const eoUsable = computed(() => !eoError.value && !(zone.value && (zone.value.type === 'full' || zone.value.paused)));
    const shown = computed(() => {
      if (!data.value) return [];
      const k = q.value.trim().toLowerCase();
      return data.value.records.filter(r => (!typeFilter.value || r.type === typeFilter.value) &&
        (!k || [r.name, r.full, r.value, r.remark || '', r.line].some(s => s.toLowerCase().includes(k))));
    });

    async function propose(body) {
      planning.value = true; formError.value = '';
      try {
        plan.value = await api('POST', '/api/dns/plan', { domain: domain.value, ...body });
        editor.open = false; quick.open = false;
        return true;
      } catch (e) {
        if (editor.open || quick.open) formError.value = e.message; else notify(e.message, 'error');
        return false;
      } finally { planning.value = false; }
    }
    function planDone() { load(true); loadDomains(); }
    // Closed while it still runs: refresh the records once it has finished.
    async function closePlan() {
      const p = plan.value;
      plan.value = null;
      if (!p) return;
      for (let i = 0; i < 200; i++) {
        await new Promise(r => setTimeout(r, 3000));
        try {
          const now = await api('GET', `/api/plans/${p.id}`);
          if (now.status === 'running') continue;
          planDone();
        } catch { /* checked again next time the page loads */ }
        return;
      }
    }

    // Adding or changing one record.
    const editor = reactive({ open: false, id: 0, sub: '', type: 'A', value: '', line: '默认', ttl: 600, mx: 10, remark: '' });
    async function loadLines() {
      try { lines.value = await api('GET', '/api/dns/lines?domain=' + encodeURIComponent(domain.value)); }
      catch { lines.value = ['默认']; }
    }
    function openEditor(r) {
      formError.value = '';
      Object.assign(editor, r
        ? { open: true, id: r.id, sub: r.name, type: r.type, value: r.value, line: r.line, ttl: r.ttl, mx: r.mx || 10, remark: r.remark || '' }
        : { open: true, id: 0, sub: '', type: 'A', value: '', line: '默认', ttl: 600, mx: 10, remark: '' });
      loadLines();
    }
    const editorTTLs = computed(() => TTLS.includes(editor.ttl) ? TTLS : [...TTLS, editor.ttl].sort((a, b) => a - b));
    function submitEditor() {
      const body = { op: editor.id ? 'modify' : 'add', id: editor.id, sub: editor.sub.trim() || '@', type: editor.type, value: editor.value.trim(),
        line: editor.line, ttl: editor.ttl, remark: editor.remark.trim() };
      if (editor.type === 'MX') body.mx = editor.mx;
      propose(body);
    }

    // One click: a name to a server, an IP or a host, through EdgeOne if wanted.
    const quick = reactive({ open: false, sub: '', target: 'server', serverId: 0, value: '', edgeone: false, https: true, protocol: 'HTTP', area: 'mainland' });
    function openQuick(pre) {
      formError.value = '';
      const s = props.servers[0];
      Object.assign(quick, { open: true, sub: '', target: s ? 'server' : 'ip', serverId: s ? s.id : 0, value: '',
        edgeone: !!(zone.value && zone.value.type === 'partial' && eoUsable.value), https: true, protocol: 'HTTP', area: 'mainland' }, pre || {});
    }
    function submitQuick() {
      propose({ op: 'quick', sub: quick.sub.trim() || '@', target: quick.target, serverId: quick.serverId, value: quick.value.trim(),
        edgeone: quick.edgeone, https: quick.https, protocol: quick.protocol, area: quick.area });
    }
    // An error is about what was sent; editing the form clears it.
    watch(() => JSON.stringify([editor, quick]), () => { if (!planning.value) formError.value = ''; });
    const quickName = computed(() => ((quick.sub.trim() || '@') === '@' ? '' : quick.sub.trim() + '.') + domain.value);

    const del = r => propose({ op: 'delete', id: r.id });
    const toggle = r => propose({ op: 'status', id: r.id, status: r.enabled ? 'disable' : 'enable' });
    const eoPoint = sub => propose({ op: 'eo_point', sub });
    const eoOff = r => propose({ op: 'eo_off', id: r.id });
    const eoOn = r => openQuick({ sub: r.name, target: r.type === 'CNAME' ? 'host' : 'ip', value: r.value, edgeone: true });
    const canEO = r => r.enabled && !r.edgeone && !r.system && ['A', 'AAAA', 'CNAME'].includes(r.type) && zone.value && zone.value.type === 'partial' && eoUsable.value && !/\.eo\.dnse|\.edgeone\.app/.test(r.value);
    const planServerName = computed(() => '腾讯云');

    return { RECORD_TYPES, EO_AREAS, VALUE_HINT, domains, domainsLoaded, eoError, domain, data, loading, error, q, typeFilter, plan, planning, lines, formError,
      current, zone, eoUsable, shown, load, planDone, closePlan, editor, openEditor, editorTTLs, submitEditor, quick, openQuick, submitQuick, quickName,
      del, toggle, eoPoint, eoOff, eoOn, canEO, ttlText, planServerName };
  },
  template: `
  <div>
    <div class="group" v-if="!configured">
      <div class="row"><ui-icon name="info" class="lg" style="color: var(--accent)"></ui-icon><div class="grow">域名解析在腾讯云 DNSPod，需要先填写腾讯云密钥。</div><button @click="$emit('settings')">去设置</button></div>
    </div>
    <template v-else>
      <div class="page-head"><p>DNSPod 里的域名解析。每次修改都会先生成一份清单，确认后才执行，执行后可以撤销；经过 EdgeOne 的网站会标出来。</p></div>
      <div class="stat-bar dns-bar">
        <label class="field"><span>域名</span>
          <select v-model="domain" aria-label="域名" :disabled="!domains.length"><option v-for="d in domains" :key="d.name" :value="d.name">{{ d.name }}</option></select></label>
        <label class="field dns-search"><span>搜索</span>
          <input type="search" v-model="q" placeholder="主机记录、记录值或备注" autocomplete="off" autocapitalize="off" spellcheck="false" enterkeyhint="search" @keydown.esc="q = ''"></label>
        <label class="field"><span>类型</span>
          <select v-model="typeFilter" aria-label="记录类型"><option value="">全部</option><option v-for="t in RECORD_TYPES" :key="t" :value="t">{{ t }}</option></select></label>
        <span class="grow"></span>
        <button class="plain icon-only" @click="load(true)" :disabled="loading || !domain" title="刷新" aria-label="刷新"><ui-icon name="refresh"></ui-icon></button>
        <button @click="openEditor()" :disabled="!domain"><ui-icon name="plus"></ui-icon>添加记录</button>
        <button class="primary" @click="openQuick()" :disabled="!domain"><ui-icon name="bolt"></ui-icon>一键解析</button>
      </div>

      <div class="notice" v-if="error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ error }}</div>
      <div class="group" v-if="domainsLoaded && !domains.length && !error && !loading"><div class="row secondary">DNSPod 里还没有域名。</div></div>
      <div class="notice" v-if="!domainsLoaded && !error"><span class="spinner"></span>正在读取域名……</div>
      <div class="notice" v-if="loading && !data"><span class="spinner"></span>正在读取解析记录……</div>

      <div class="alerts dns-alerts" v-if="data">
        <div class="alert al-crit" v-if="current && !current.dnsOk"><ui-icon name="alert"></ui-icon>
          <span class="grow">{{ domain }} 的 DNS 服务器不是 DNSPod，这里的解析不会生效。到域名注册商那里，把 DNS 服务器改成 DNSPod 的地址。</span></div>
        <div class="alert al-warn" v-if="current && current.status && current.status !== 'ENABLE'"><ui-icon name="warn"></ui-icon>
          <span class="grow">{{ domain }} 在 DNSPod 里不是启用状态（{{ current.status }}），解析可能不生效。</span></div>
        <div class="alert al-info" v-if="zone && zone.type === 'full'"><ui-icon name="info"></ui-icon>
          <span class="grow">{{ domain }} 用 NS 方式接入了 EdgeOne，线上生效的解析在 EdgeOne 里管理，这里的 DNSPod 记录不起作用。</span></div>
        <div class="alert al-warn" v-for="p in data.pending" :key="p.name"><ui-icon name="warn"></ui-icon>
          <span class="grow">EdgeOne 里有加速域名 <b>{{ p.name }}</b>（回源到 {{ p.origin }}），但解析还没指过去，访客没有经过 EdgeOne<template v-if="p.current">；现在解析到 {{ p.current }}</template>。</span>
          <button class="link small" @click="eoPoint(p.sub)" :disabled="planning">解析到 EdgeOne</button></div>
        <div class="alert al-info" v-if="eoError"><ui-icon name="info"></ui-icon>
          <span class="grow">读不到 EdgeOne 的信息（{{ eoError }}），经过 EdgeOne 的网站不会标出来。</span></div>
      </div>

      <div class="group dns-group" v-if="data">
        <div class="dns-sum small secondary">
          <span>{{ data.records.length }} 条记录<template v-if="shown.length !== data.records.length">，显示 {{ shown.length }} 条</template></span>
          <span v-if="zone && zone.type === 'partial'"><ui-icon name="cloud"></ui-icon>已接入 EdgeOne（CNAME 方式）</span>
          <span v-else-if="!zone && !eoError">没有接入 EdgeOne</span>
          <span v-if="loading"><span class="spinner inline"></span>正在更新</span>
        </div>
        <div class="table-wrap">
          <table class="table dns-table">
            <thead><tr><th>主机记录</th><th>类型</th><th>线路</th><th>记录值</th><th>TTL</th><th>状态</th><th><span class="sr-only">操作</span></th></tr></thead>
            <tbody>
              <tr v-for="r in shown" :key="r.id" :class="{ off: !r.enabled }">
                <td class="c-name"><div class="dns-name">{{ r.name }}</div><div class="small tertiary">{{ r.full }}</div></td>
                <td class="c-type"><span class="tag">{{ r.type }}</span></td>
                <td class="c-line nowrap">{{ r.line }}</td>
                <td class="c-value dns-value"><span class="mono">{{ r.value }}</span><div class="small tertiary" v-if="r.type === 'MX'">优先级 {{ r.mx }}</div>
                  <div class="small tertiary" v-if="r.weight != null">权重 {{ r.weight }}</div><div class="small tertiary" v-if="r.remark">{{ r.remark }}</div></td>
                <td class="c-ttl nowrap">{{ ttlText(r.ttl) }}</td>
                <td class="c-state dns-state">
                  <span class="tag warn" v-if="!r.enabled">已暂停</span>
                  <span class="tag" v-if="r.system">DNSPod 自带</span>
                  <template v-if="r.edgeone">
                    <template v-if="r.edgeone.points"><span class="tag on">经过 EdgeOne</span><div class="small tertiary">回源到 {{ r.edgeone.origin }}<template v-if="r.edgeone.https === 'eofreecert'"> · 免费证书</template></div></template>
                    <template v-else><span class="tag warn">没走 EdgeOne</span><div class="small tertiary">{{ r.line === '默认' ? 'EdgeOne 里有这个域名，解析没指过去' : '这条线路的访客不经过 EdgeOne' }}</div></template>
                  </template>
                  <span class="tertiary" v-if="r.enabled && !r.system && !r.edgeone">—</span>
                </td>
                <td class="c-ops dns-ops">
                  <template v-if="!r.system">
                    <button class="link small" @click="openEditor(r)" :aria-label="'修改 ' + r.full + ' 的 ' + r.type + ' 记录'">修改</button>
                    <button class="link small" @click="toggle(r)" :disabled="planning" :aria-label="(r.enabled ? '暂停 ' : '启用 ') + r.full + ' 的 ' + r.type + ' 记录'">{{ r.enabled ? '暂停' : '启用' }}</button>
                    <button class="link small danger" @click="del(r)" :disabled="planning" :aria-label="'删除 ' + r.full + ' 的 ' + r.type + ' 记录'">删除</button>
                    <button class="link small" v-if="r.edgeone && r.edgeone.points" @click="eoOff(r)" :disabled="planning">不走 EdgeOne</button>
                    <button class="link small" v-else-if="r.edgeone && r.line === '默认'" @click="eoPoint(r.name)" :disabled="planning">解析到 EdgeOne</button>
                    <button class="link small" v-else-if="canEO(r)" @click="eoOn(r)">开启 EdgeOne</button>
                  </template>
                </td>
              </tr>
              <tr v-if="!shown.length"><td colspan="7" class="secondary">{{ data.records.length ? '没有符合条件的记录' : '还没有解析记录，点「一键解析」或「添加记录」开始' }}</td></tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <!-- One click -->
    <div class="sheet-mask" v-if="quick.open" @click.self="quick.open = false">
      <div class="sheet" role="dialog" aria-label="一键解析">
        <h2>一键解析</h2>
        <p>填好名字和要指向的地方，会生成一份清单：确认后执行，每一步都能撤销。</p>
        <div class="group">
          <div class="row form"><span class="k">名字</span><span class="v dns-sub"><input v-model="quick.sub" placeholder="www；主域名本身填 @" aria-label="主机记录" autocapitalize="off" spellcheck="false"><span class="secondary">.{{ domain }}</span></span></div>
          <div class="row form"><span class="k">指向</span><span class="v"><span class="segmented">
            <button :class="{on: quick.target === 'server'}" @click="quick.target = 'server'" :disabled="!servers.length">我的服务器</button>
            <button :class="{on: quick.target === 'ip'}" @click="quick.target = 'ip'">IP 地址</button>
            <button :class="{on: quick.target === 'host'}" @click="quick.target = 'host'">另一个域名</button></span></span></div>
          <div class="row form" v-if="quick.target === 'server'"><span class="k">服务器</span><span class="v">
            <select v-model.number="quick.serverId" aria-label="服务器"><option v-for="s in servers" :key="s.id" :value="s.id">{{ s.name }} · {{ s.host }}</option></select></span></div>
          <div class="row form" v-else><span class="k">{{ quick.target === 'ip' ? 'IP 地址' : '域名' }}</span><span class="v">
            <input v-model="quick.value" :placeholder="quick.target === 'ip' ? '例如 1.2.3.4' : '例如 example.github.io'" :aria-label="quick.target === 'ip' ? 'IP 地址' : '域名'" autocapitalize="off" spellcheck="false"></span></div>
        </div>
        <div class="group">
          <label class="row form dns-check"><input type="checkbox" v-model="quick.edgeone" :disabled="!eoUsable">
            <span class="grow"><b>经过 EdgeOne</b><span class="small secondary block">访客先到 EdgeOne 节点：加速、防护、隐藏服务器 IP，还能用免费 HTTPS 证书。{{ !eoUsable ? (zone && zone.type === 'full' ? '这个域名用 NS 方式接入 EdgeOne，请在 EdgeOne 里管理解析。' : '现在读不到 EdgeOne。') : '' }}</span></span></label>
          <template v-if="quick.edgeone">
            <div class="row form" v-if="!zone"><span class="k">加速区域</span><span class="v">
              <select v-model="quick.area" aria-label="加速区域"><option v-for="a in EO_AREAS" :key="a.id" :value="a.id">{{ a.text }}</option></select>
              <span class="small secondary block">{{ domain }} 还没有接入 EdgeOne，会先接入（CNAME 方式，用账号里还能绑定站点的套餐）。</span></span></div>
            <div class="row form"><span class="k">回源协议</span><span class="v"><span class="segmented">
              <button :class="{on: quick.protocol === 'HTTP'}" @click="quick.protocol = 'HTTP'">HTTP</button>
              <button :class="{on: quick.protocol === 'HTTPS'}" @click="quick.protocol = 'HTTPS'">HTTPS</button></span>
              <span class="small secondary block">服务器上这个网站没有证书就选 HTTP。</span></span></div>
            <label class="row form dns-check"><input type="checkbox" v-model="quick.https"><span class="grow">申请 EdgeOne 免费证书，开启 HTTPS<span class="small secondary block">自动续签；要等解析生效后才申请得下来。</span></span></label>
          </template>
        </div>
        <div class="dns-preview small secondary">{{ quickName }} → {{ quick.target === 'server' ? ((servers.find(s => s.id === quick.serverId) || {}).host || '') : (quick.value || '…') }}{{ quick.edgeone ? '（经过 EdgeOne）' : '' }}</div>
        <div class="notice" v-if="formError"><ui-icon name="alert" class="st-crit"></ui-icon>{{ formError }}</div>
        <div class="sheet-actions"><button @click="quick.open = false">取消</button>
          <button class="primary" @click="submitQuick" :disabled="planning || (quick.target !== 'server' && !quick.value.trim())">{{ planning ? '正在生成……' : '生成清单' }}</button></div>
      </div>
    </div>

    <!-- One record -->
    <div class="sheet-mask" v-if="editor.open" @click.self="editor.open = false">
      <div class="sheet" role="dialog" :aria-label="editor.id ? '修改记录' : '添加记录'">
        <h2>{{ editor.id ? '修改记录' : '添加记录' }}</h2>
        <p>确认清单后才会生效，可以一键撤销。</p>
        <div class="group">
          <div class="row form"><span class="k">主机记录</span><span class="v dns-sub"><input v-model="editor.sub" placeholder="www；主域名本身填 @；泛解析填 *" aria-label="主机记录" autocapitalize="off" spellcheck="false"><span class="secondary">.{{ domain }}</span></span></div>
          <div class="row form"><span class="k">类型</span><span class="v"><select v-model="editor.type" aria-label="记录类型"><option v-for="t in RECORD_TYPES" :key="t" :value="t">{{ t }}</option></select></span></div>
          <div class="row form"><span class="k">线路</span><span class="v"><select v-model="editor.line" aria-label="线路">
            <option v-for="l in (lines.includes(editor.line) ? lines : [editor.line, ...lines])" :key="l" :value="l">{{ l }}</option></select></span></div>
          <div class="row form"><span class="k">记录值</span><span class="v"><input v-model="editor.value" :placeholder="VALUE_HINT[editor.type]" aria-label="记录值" autocapitalize="off" spellcheck="false"></span></div>
          <div class="row form" v-if="editor.type === 'MX'"><span class="k">MX 优先级</span><span class="v"><input type="number" min="1" max="65535" v-model.number="editor.mx" aria-label="MX 优先级"><span class="small secondary block">数字越小越优先</span></span></div>
          <div class="row form"><span class="k">TTL</span><span class="v"><select v-model.number="editor.ttl" aria-label="TTL"><option v-for="t in editorTTLs" :key="t" :value="t">{{ ttlText(t) }}</option></select>
            <span class="small secondary block">DNSPod 免费版最小 10 分钟</span></span></div>
          <div class="row form"><span class="k">备注</span><span class="v"><input v-model="editor.remark" placeholder="可不填" aria-label="备注"></span></div>
        </div>
        <div class="notice" v-if="formError"><ui-icon name="alert" class="st-crit"></ui-icon>{{ formError }}</div>
        <div class="sheet-actions"><button @click="editor.open = false">取消</button>
          <button class="primary" @click="submitEditor" :disabled="planning || !editor.value.trim()">{{ planning ? '正在生成……' : '生成清单' }}</button></div>
      </div>
    </div>

    <!-- The checklist to confirm -->
    <div class="sheet-mask" v-if="plan" @click.self="closePlan">
      <div class="sheet plan-sheet" role="dialog" aria-label="确认清单">
        <h2>{{ plan.title }}</h2>
        <p>勾选后点「执行」，确认后才会生效；执行后可以在这里或「建议」页撤销。</p>
        <plan-card :plan="plan" :server-name="planServerName" @done="planDone"></plan-card>
        <div class="sheet-actions"><button @click="closePlan">关闭</button></div>
      </div>
    </div>
  </div>`,
};

// ⌘K / Ctrl+K: search servers, sites and pages, or type what to do
// ("终端 blog", "新建网站"); anything else goes to the AI.
const PALETTE_PAGES = [
  { tab: 'home', label: '总览', icon: 'home', keys: 'home overview zonglan' },
  { tab: 'chat', label: 'AI 助手', icon: 'sparkles', keys: 'ai chat' },
  { tab: 'inbox', label: '待处理', icon: 'inbox', keys: 'inbox todo 建议 通知 清单 plans notices' },
  { tab: 'sites', label: '网站管理', icon: 'window', keys: 'sites website 网站 1panel nginx https 反向代理 证书' },
  { stats: ['logs', 'overview'], label: '访问统计', icon: 'chart', keys: 'stats visits pv uv 统计 流量' },
  { stats: ['logs', 'security'], label: '安全', icon: 'shield', keys: 'security 封禁 ip 攻击' },
  { tab: 'certs', label: '证书', icon: 'lock', keys: 'certs ssl https 证书 续签' },
  { tab: 'cloud', label: '云服务器', icon: 'cloud', keys: 'cloud lighthouse cvm 轻量 云服务器 实例 防火墙 快照 开机 关机 重启 到期' },
  { stats: ['eo'], label: 'EdgeOne', icon: 'bolt', keys: 'edgeone eo cdn 缓存 cache' },
  { tab: 'dns', label: '解析', icon: 'globe', keys: 'dns dnspod 解析 域名' },
  { tab: 'storage', label: '存储', icon: 'bucket', keys: 'cos storage bucket 存储桶' },
  { tab: 'terminal', label: '终端', icon: 'prompt', keys: 'terminal ssh shell 终端' },
  { tab: 'files', label: '文件', icon: 'folder', keys: 'files sftp 文件' },
  { tab: 'logs', label: '记录', icon: 'history', keys: 'logs history 日志 记录 撤销' },
  { tab: 'settings', label: '设置', icon: 'gear', keys: 'settings 设置 api key 模型' },
];
const CommandPalette = {
  props: { open: Boolean, servers: { type: Array, default: () => [] } },
  emits: ['close', 'go', 'stats', 'server', 'terminal', 'files', 'site', 'ask', 'add', 'newsite'],
  setup(props, { emit }) {
    const q = ref(''), pick = ref(0), input = ref(null), sites = ref([]);
    let loaded = 0;
    watch(() => props.open, v => {
      if (!v) return;
      q.value = ''; pick.value = 0;
      nextTick(() => input.value && input.value.focus());
      if (Date.now() - loaded > 60000) {
        loaded = Date.now();
        api('GET', '/api/websites').then(r => { sites.value = r.servers.flatMap(s => s.sites.map(x => ({ ...x, serverId: s.id, serverName: s.name }))); }).catch(() => {});
      }
    });
    const norm = s => String(s || '').toLowerCase();
    const items = computed(() => {
      const raw = q.value.trim(), k = norm(raw);
      const words = k.split(/\s+/).filter(Boolean);
      const has = (...fields) => words.every(w => fields.some(f => norm(f).includes(w)));
      const out = [];
      // "终端 blog", "文件 blog": a tool on a server.
      const tool = words[0] === '终端' || words[0] === 'ssh' ? 'terminal' : words[0] === '文件' ? 'files' : '';
      const rest = tool ? words.slice(1) : words;
      for (const s of props.servers) {
        const hit = rest.every(w => norm(s.name).includes(w) || norm(s.host).includes(w));
        if (tool && hit) out.push({ kind: tool, id: s.id, icon: tool === 'terminal' ? 'prompt' : 'folder', label: `${tool === 'terminal' ? '打开终端' : '管理文件'}：${s.name}`, note: s.host });
        else if (!tool && (!k || has(s.name, s.host, '服务器 server'))) out.push({ kind: 'server', id: s.id, icon: 'server', label: s.name, note: '服务器 · ' + s.host });
      }
      if (!tool) {
        for (const x of sites.value) if (k && has(x.domain, x.remark, x.serverName, '网站 site')) out.push({ kind: 'site', id: x.id, serverId: x.serverId, icon: 'window', label: x.domain, note: '网站 · ' + x.serverName });
        for (const p of PALETTE_PAGES) if (!k || has(p.label, p.keys)) out.push({ kind: 'page', page: p, icon: p.icon, label: p.label, note: '页面' });
        if (!k || has('新建网站 new site 建站')) out.push({ kind: 'newsite', icon: 'plus', label: '新建网站', note: '1Panel' });
        if (!k || has('添加服务器 add server')) out.push({ kind: 'add', icon: 'plus', label: '添加服务器', note: '' });
        if (raw) out.push({ kind: 'ask', icon: 'sparkles', label: '问 AI：' + raw, note: 'Enter', text: raw });
      }
      return out.slice(0, 40);
    });
    watch(items, () => { pick.value = 0; });
    function run(it) {
      if (!it) return;
      emit('close');
      if (it.kind === 'server') emit('server', it.id);
      else if (it.kind === 'terminal') emit('terminal', it.id);
      else if (it.kind === 'files') emit('files', it.id);
      else if (it.kind === 'site') emit('site', it.serverId, it.id);
      else if (it.kind === 'page') it.page.stats ? emit('stats', ...it.page.stats) : emit('go', it.page.tab);
      else if (it.kind === 'ask') emit('ask', it.text);
      else if (it.kind === 'add') emit('add');
      else if (it.kind === 'newsite') emit('newsite');
    }
    function key(e) {
      const n = items.value.length;
      if (e.key === 'ArrowDown') { e.preventDefault(); pick.value = (pick.value + 1) % Math.max(n, 1); scroll(); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); pick.value = (pick.value - 1 + n) % Math.max(n, 1); scroll(); }
      else if (e.key === 'Enter') { e.preventDefault(); run(items.value[pick.value]); }
      else if (e.key === 'Escape') { e.preventDefault(); emit('close'); }
    }
    const list = ref(null);
    function scroll() { nextTick(() => { const el = list.value && list.value.querySelector('.pal-item.on'); if (el) el.scrollIntoView({ block: 'nearest' }); }); }
    return { q, pick, input, items, run, key, list };
  },
  template: `
  <div class="sheet-mask pal-mask" v-if="open" @click.self="$emit('close')">
    <div class="palette" role="dialog" aria-label="搜索和跳转">
      <div class="pal-input"><ui-icon name="search"></ui-icon>
        <input ref="input" v-model="q" @keydown="key" placeholder="搜服务器、网站、页面，或者输入「终端 blog」" aria-label="搜索" autocomplete="off" autocapitalize="off" spellcheck="false">
        <kbd>Esc</kbd></div>
      <div class="pal-list" ref="list" role="listbox">
        <button v-for="(it, i) in items" :key="it.kind + (it.id || it.label)" class="pal-item" :class="{ on: i === pick }" role="option" :aria-selected="i === pick"
          @click="run(it)" @mousemove="pick = i">
          <ui-icon :name="it.icon"></ui-icon><span class="grow">{{ it.label }}</span><span class="small tertiary">{{ it.note }}</span></button>
        <div class="pal-empty small secondary" v-if="!items.length">没有找到</div>
      </div>
      <div class="pal-foot small tertiary"><span>↑ ↓ 选择</span><span>Enter 打开</span><span>找不到的话，直接说想做什么，交给 AI</span></div>
    </div>
  </div>`,
};

// 总览: servers, sites, certificates and security at a glance, what needs
// the user, and what changed lately. Built from what Miao Panel already
// knows, so it opens at once.
const HOME_ASKS = ['今天的访问量怎么样？', '有没有人在扫描我的网站？', '哪台服务器最需要处理？'];
const TODO_ICON = { crit: 'alert', warn: 'warn', plan: 'sparkles', info: 'info' };
const levelDot = l => ({ ok: 'good', warn: 'warn', crit: 'crit' }[l] || 'off');
const HomePage = {
  props: { overview: Object, servers: { type: Array, default: () => [] }, aiReady: Boolean, active: Boolean },
  emits: ['refresh', 'ask', 'go', 'server', 'stats', 'plan', 'add', 'log'],
  setup(props, { emit }) {
    const draft = ref('');
    const ov = computed(() => props.overview);
    const today = computed(() => new Date().toLocaleDateString('zh-CN', { month: 'long', day: 'numeric', weekday: 'short' }));
    const ask = text => { const t = (text || draft.value).trim(); if (t) { emit('ask', t); draft.value = ''; } };
    const patrol = () => emit('ask', '帮我把所有服务器、网站、证书和安全情况巡检一遍：先查数据，再按轻重缓急告诉我哪些需要处理，能修的给我清单。');

    const cards = computed(() => {
      const o = ov.value;
      if (!o) return [];
      const bad = o.servers.filter(s => s.level === 'warn' || s.level === 'crit');
      const worst = o.servers.some(s => s.level === 'crit') ? 'crit' : bad.length ? 'warn' : 'ok';
      const out = [{ id: 'servers', icon: 'server', label: '服务器', value: o.servers.length, unit: '台', level: worst,
        pill: bad.length ? `${bad.length} 台注意` : '正常', sub: bad.length ? bad.map(s => `${s.name} ${s.note}`).join(' · ') : o.servers.map(s => s.name).join('、') }];
      const v = o.visits;
      const change = v && v.pvBefore > 0 ? Math.round((v.pv - v.pvBefore) * 100 / v.pvBefore) : null;
      out.push({ id: 'sites', icon: 'chart', label: '网站', value: v ? v.sites : '—', unit: v ? '个' : '', level: v ? 'ok' : 'off', pill: v ? '今天' : '没有数据',
        sub: v ? `今天 PV ${fmtCount(v.pv)}${change == null ? '' : ` · 比昨天同期 ${change >= 0 ? '↑' : '↓'}${Math.abs(change)}%`}` : '在「访问统计」里选择数据来源' });
      const c = o.certs;
      out.push({ id: 'certs', icon: 'lock', label: '证书', value: c ? c.total : '—', unit: c ? '张' : '', level: c ? (c.attention ? c.level : 'ok') : 'off',
        pill: c ? (c.attention ? `${c.attention} 张要处理` : '正常') : '没有数据', sub: c && c.attention ? `${c.soonest}${c.days != null ? (c.days < 0 ? ' 已过期' : ` ${c.days} 天后到期`) : ''}` : (c ? '都在自动续签或有效期充足' : '腾讯云或 1Panel 的证书会显示在这里') });
      const sct = o.security;
      out.push({ id: 'security', icon: 'shield', label: '安全', value: sct ? sct.high : '—', unit: sct ? '个高风险 IP' : '', level: sct ? (sct.known && sct.unblocked ? 'crit' : 'ok') : 'off',
        pill: sct ? (sct.known ? (sct.unblocked ? `${sct.unblocked} 个未封禁` : '都已处理') : '最近 24 小时') : '没有数据', sub: sct ? '扫描后台、猜密码或攻击 · 最近 24 小时' : '经过 EdgeOne 或服务器日志统计后显示' });
      return out;
    });
    function openCard(id) {
      if (id === 'servers') { const s = ov.value.servers.find(x => x.level === 'warn' || x.level === 'crit') || ov.value.servers[0]; if (s) emit('server', s.id); }
      if (id === 'sites') emit('stats', 'logs', 'overview');
      if (id === 'certs') emit('go', 'certs');
      if (id === 'security') emit('stats', 'logs', 'security');
    }
    function doTodo(t) {
      if (t.kind === 'plan') emit('plan', t.id);
      else if (t.kind === 'cert') emit('go', 'certs');
      else if (t.kind === 'security') emit('stats', 'logs', 'security');
      else if (t.kind === 'notice') emit('go', 'inbox');
      else if (t.kind === 'server') {
        const s = ov.value.servers.find(x => x.id === t.id);
        emit('ask', `服务器 ${s ? s.name : ''} 提示「${s ? s.note : ''}」，帮我看看是怎么回事，要不要处理，怎么处理？`);
      }
    }

    // Today's views by hour, as a small line.
    const spark = computed(() => {
      const v = ov.value && ov.value.visits;
      if (!v || !v.hours || !v.hours.length) return null;
      const W = 300, H = 72, max = Math.max(...v.hours, 1);
      const X = i => (i * W / 23).toFixed(1), Y = n => (H - 4 - n / max * (H - 14)).toFixed(1);
      const line = v.hours.map((n, i) => (i ? 'L' : 'M') + X(i) + ' ' + Y(n)).join(' ');
      const last = v.hours.length - 1;
      return { line, area: line + ` L${X(last)} ${H} L0 ${H} Z`, x: X(last), y: Y(v.hours[last]), now: `0:00 → ${last}:59` };
    });
    const bar = v => v == null ? null : { w: Math.min(v, 100) + '%', cls: v >= 85 ? 'warn' : '' };
    return { draft, ov, today, ask, patrol, cards, openCard, doTodo, spark, bar, HOME_ASKS, TODO_ICON, levelDot, fmtCount, whenText };
  },
  template: `
  <div class="home">
    <div class="home-head">
      <div class="grow secondary">{{ today }}</div>
      <button class="plain" @click="$emit('refresh')"><ui-icon name="refresh"></ui-icon>刷新</button>
      <button class="primary" @click="patrol" :disabled="!aiReady || !servers.length"><ui-icon name="sparkles"></ui-icon>让 AI 巡检一遍</button>
    </div>

    <!-- First time -->
    <div class="group home-start" v-if="!servers.length">
      <div class="row stack"><b>三步开始</b><div class="small secondary">看状态、找问题、问 AI，任何修改都要你确认后才执行。</div></div>
      <div class="row"><span class="step-num" :class="{done: aiReady}">1</span><div class="grow"><b>设置 AI 模型</b><div class="small secondary">推荐 DeepSeek V4.1 Flash，便宜好用</div></div>
        <button @click="$emit('go', 'settings')">去设置</button></div>
      <div class="row"><span class="step-num">2</span><div class="grow"><b>添加服务器</b><div class="small secondary">填 IP、用户名和密码，或者选腾讯云上的服务器</div></div>
        <button class="primary" @click="$emit('add')">添加服务器</button></div>
      <div class="row"><span class="step-num">3</span><div class="grow"><b>识别环境</b><div class="small secondary">自动看出装了什么：1Panel、宝塔、网站、数据库……只读，不会改动任何东西</div></div></div>
    </div>

    <template v-if="ov && servers.length">
      <div class="home-cards">
        <button class="home-card" v-for="c in cards" :key="c.id" @click="openCard(c.id)">
          <span class="home-card-top"><ui-icon :name="c.icon"></ui-icon><span class="grow">{{ c.label }}</span>
            <span class="pill"><span class="sdot" :class="levelDot(c.level)"></span>{{ c.pill }}</span></span>
          <span class="home-card-value">{{ c.value }}<small>{{ c.unit }}</small></span>
          <span class="home-card-sub"><span class="sdot phone-only" :class="levelDot(c.level)"></span>{{ c.sub }}</span>
        </button>
      </div>

      <div class="home-row">
        <section class="card home-todo">
          <header class="card-head"><h3>需要你处理</h3><span class="side-count" v-if="ov.todo.length">{{ ov.todo.length }}</span><span class="grow"></span>
            <button class="link small" @click="$emit('go', 'inbox')">全部 ›</button></header>
          <div class="home-empty" v-if="!ov.todo.length"><span class="sdot good"></span>现在没有要处理的事</div>
          <div class="todo-row" v-for="(t, i) in ov.todo" :key="i">
            <span class="todo-icon" :class="'lv-' + t.level"><ui-icon :name="TODO_ICON[t.level] || 'info'"></ui-icon></span>
            <div class="grow"><div class="todo-title">{{ t.title }}</div><div class="small secondary" v-if="t.meta">{{ t.meta }}</div></div>
            <button :class="t.kind === 'plan' ? 'primary' : ''" @click="doTodo(t)">{{ t.action }}</button>
          </div>
        </section>
        <section class="card home-ask">
          <header class="card-head"><ui-icon name="sparkles" class="home-ask-icon"></ui-icon><h3>问 AI</h3></header>
          <p class="small secondary">用大白话说想做的事，它先查清楚再回答；要改东西会先给你清单。</p>
          <textarea v-model="draft" rows="3" placeholder="比如：今天下午流量为什么涨了？" aria-label="问 AI" @keydown.enter.exact.prevent="ask()"></textarea>
          <div class="home-asks"><button v-for="q in HOME_ASKS" :key="q" class="chip" @click="ask(q)">{{ q }}</button></div>
          <div class="home-ask-foot"><span class="grow"></span>
            <button class="primary icon-only round" @click="ask()" :disabled="!draft.trim()" title="发送" aria-label="发送"><ui-icon name="arrow-up"></ui-icon></button></div>
        </section>
      </div>

      <div class="home-row three">
        <section class="card">
          <header class="card-head"><h3>服务器</h3></header>
          <div class="srv-grid srv-head small tertiary"><span>名称</span><span>CPU</span><span>内存</span><span>磁盘</span></div>
          <button class="srv-grid srv-row" v-for="s in ov.servers" :key="s.id" @click="$emit('server', s.id)">
            <span class="srv-name"><span class="sdot" :class="levelDot(s.level)"></span><span><b>{{ s.name }}</b><span class="small tertiary block">{{ s.level === 'unknown' ? s.note : ({ '1panel': '1Panel', bt: '宝塔', linux: 'Linux' }[s.adapter] || '') }}</span></span></span>
            <span v-for="(m, k) in [s.cpuPct, s.memPct, s.diskPct]" :key="k" class="srv-metric">
              <template v-if="m != null"><span class="mini-bar"><span :class="bar(m).cls" :style="{ width: bar(m).w }"></span></span><span :class="{ 'warn-text': m >= 85 }">{{ m }}%</span></template>
              <span class="tertiary" v-else>—</span>
            </span>
          </button>
        </section>
        <section class="card">
          <header class="card-head"><h3>今天的访问</h3><span class="grow"></span><button class="link small" @click="$emit('stats', 'logs', 'overview')">统计 ›</button></header>
          <template v-if="ov.visits">
            <div class="home-pv"><b>{{ fmtCount(ov.visits.pv) }}</b><span class="secondary">PV</span>
              <span class="small secondary" v-if="ov.visits.pvBefore">比昨天同期 {{ ov.visits.pv >= ov.visits.pvBefore ? '↑' : '↓' }}{{ Math.abs(Math.round((ov.visits.pv - ov.visits.pvBefore) * 100 / ov.visits.pvBefore)) }}%</span></div>
            <svg v-if="spark" class="home-spark" viewBox="0 0 300 72" preserveAspectRatio="none" role="img" aria-label="今天每小时的访问量">
              <path :d="spark.area" class="spark-area"></path><path :d="spark.line" class="spark-line"></path>
              <circle :cx="spark.x" :cy="spark.y" r="3.5" class="spark-dot"></circle></svg>
            <div class="small tertiary" v-if="spark">{{ ov.visits.title }} · {{ spark.now }}</div>
          </template>
          <div class="home-empty" v-else>还没有访问数据，打开「访问统计」选择数据来源。</div>
        </section>
        <section class="card">
          <header class="card-head"><h3>最近的变更</h3><span class="grow"></span><button class="link small" @click="$emit('go', 'logs')">记录 ›</button></header>
          <div class="home-empty" v-if="!ov.changes.length">还没有执行过修改</div>
          <button class="change-row" v-for="c in ov.changes" :key="c.id" @click="$emit('log', c.id)">
            <span class="small tertiary change-when">{{ whenText(c.startedAt) }}</span>
            <span class="grow"><span class="ellipsis block" :title="c.note || c.title">{{ c.note || c.title }}</span><span class="small secondary">{{ c.serverName }}{{ c.status === 'undone' || c.undoneBy ? ' · 已撤销' : c.status !== 'done' ? ' · 没有成功' : '' }}</span></span>
          </button>
        </section>
      </div>
    </template>
    <div class="notice" v-else-if="servers.length"><span class="spinner"></span>正在汇总……</div>
  </div>`,
};

// The server workbench: one server's page, in tabs. 终端 and 文件 stay pages
// of their own (their sessions and uploads outlive the tab), opened from
// the buttons at the top.
const SERVER_TABS = [{ id: 'overview', text: '概览' }, { id: 'sites', text: '网站' }, { id: 'apps', text: '应用' },
  { id: 'history', text: '执行记录' }, { id: 'connection', text: '连接设置' }];

// What a finding's button offers, by its title. Every one goes to the AI,
// which looks first and proposes a checklist.
const FINDING_FIX = [
  [/没有 swap/, '加 swap', '给这台服务器加一个大小合适的 swap'],
  [/内存/, '优化内存', '看看是什么占用了内存，能怎么优化'],
  [/磁盘/, '清理磁盘', '看看是什么占用了磁盘空间，哪些可以安全清理'],
  [/SSH/, '加固 SSH', '在不把我锁在外面的前提下加固 SSH 登录'],
  [/服务启动失败|Docker/, '排查', '查一下原因并修复'],
];
const findingFix = f => {
  if (f.level === 'info' && /不是 root/.test(f.title)) return null;
  const hit = FINDING_FIX.find(([re]) => re.test(f.title));
  return hit ? { label: hit[1], ask: hit[2] } : { label: '让 AI 处理', ask: '看看怎么处理' };
};

// A ring gauge, like the widgets on a phone.
const RING_C = 2 * Math.PI * 23;
const ServerRing = {
  props: { pct: Number, level: String },
  setup(props) {
    const dash = computed(() => `${(RING_C * Math.min(100, Math.max(0, props.pct || 0)) / 100).toFixed(1)} ${RING_C.toFixed(1)}`);
    return { dash };
  },
  template: `<svg class="ring" viewBox="0 0 56 56" aria-hidden="true">
    <circle cx="28" cy="28" r="23" class="ring-track"></circle>
    <circle cx="28" cy="28" r="23" class="ring-value" :class="level" :stroke-dasharray="dash" transform="rotate(-90 28 28)"></circle>
  </svg>`,
};

const gbText = mb => mb >= 1024 ? (mb / 1024).toFixed(mb >= 10240 ? 0 : 1) + ' GB' : mb + ' MB';

const ServerOverview = {
  props: { view: Object, cloud: Object, active: Boolean, busy: Boolean },
  emits: ['ask', 'tab', 'site', 'discover', 'log'],
  setup(props, { emit }) {
    const p = computed(() => props.view && props.view.profile);
    const sv = computed(() => props.view && props.view.server);
    const sites = ref(null), sitesError = ref(''), recent = ref([]);
    let seq = 0;
    async function load() {
      const s = sv.value;
      if (!s) return;
      const n = ++seq;
      api('GET', `/api/exec?server=${s.id}&changes=1`).then(r => { if (n === seq) recent.value = r.slice(0, 5); }).catch(() => {});
      if (s.adapter !== '1panel') { sites.value = null; sitesError.value = ''; return; }
      try {
        const r = await api('GET', `/api/websites?server=${s.id}`);
        if (n !== seq) return;
        const x = r.servers[0];
        sites.value = x && !x.noPanel && !x.error ? x.sites : null;
        sitesError.value = !x ? '' : x.noPanel ? '还没有配置 1Panel 接口' : x.error || '';
      } catch (e) { if (n === seq) sitesError.value = e.message; }
    }
    watch(() => [props.active, sv.value && sv.value.id, props.view && props.view.collectedAt], () => { if (props.active) load(); }, { immediate: true });

    const level = v => v >= 90 ? 'crit' : v >= 80 ? 'warn' : '';
    const metrics = computed(() => {
      const x = p.value, out = [];
      if (!x) return out;
      const load1 = parseFloat(String(x.load || '').split(/\s+/)[0]);
      if (x.cpuCores && !isNaN(load1)) {
        const v = Math.min(100, Math.round(load1 * 100 / x.cpuCores));
        out.push({ label: 'CPU', pct: v, level: level(v), sub: `${x.cpuCores} 核 · 负载 ${load1}` });
      }
      const m = x.memory;
      if (m && m.totalMB) {
        const v = Math.round((m.totalMB - m.availableMB) * 100 / m.totalMB);
        const noSwap = !m.swapTotalMB;
        out.push({ label: '内存', pct: v, level: level(v) || (noSwap && v >= 70 ? 'warn' : ''),
          sub: `${gbText(m.totalMB - m.availableMB)} / ${gbText(m.totalMB)}` + (noSwap ? ' · 没有 swap' : '') });
      }
      const d = (x.disks || []).find(d => d.mount === '/');
      if (d) out.push({ label: '磁盘', pct: d.usePct, level: level(d.usePct), sub: `${d.used} / ${d.size}` });
      const c = props.cloud;
      if (c && c.trafficTotal) {
        const v = Math.round(c.trafficUsed * 100 / c.trafficTotal);
        out.push({ label: '流量包', pct: v, level: level(v), sub: `本月 ${fmtBytes(c.trafficUsed)} / ${fmtBytes(c.trafficTotal)}` });
      }
      return out;
    });
    const findings = computed(() => (p.value && p.value.findings || []).map(f => ({ ...f, fix: findingFix(f) })));
    const tone = l => ({ danger: 'crit', warn: 'warn' }[l] || 'info');
    function fix(f) {
      emit('ask', `服务器「${sv.value.name}」：${f.title}（${f.detail}）。${f.fix.ask}，能改的话给我一份清单。`);
    }
    const programs = computed(() => {
      const list = (p.value && p.value.programs || []).slice(0, 5);
      const top = Math.max(1, ...list.map(g => g.memMB));
      return list.map(g => ({ ...g, w: Math.max(2, Math.round(g.memMB * 100 / top)) + '%' }));
    });
    const plainSites = computed(() => p.value && p.value.websites || []);
    const shownSites = computed(() => (sites.value || []).slice(0, 6));
    return { p, sv, sites, sitesError, recent, metrics, findings, tone, fix, programs, plainSites, shownSites,
      SITE_TYPES, certLeft, certLevel, whenText, gbText, fmtTime: t => t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '' };
  },
  template: `
  <div class="wb-overview">
    <div class="wb-rings" v-if="metrics.length">
      <div class="wb-ring card" v-for="m in metrics" :key="m.label">
        <div class="wb-ring-gauge"><server-ring :pct="m.pct" :level="m.level"></server-ring><b>{{ m.pct }}<small>%</small></b></div>
        <div class="grow"><div class="wb-ring-label">{{ m.label }}</div><div class="small" :class="m.level ? 'st-' + m.level + '-text' : 'secondary'">{{ m.sub }}</div></div>
      </div>
    </div>

    <section class="card wb-checks">
      <header class="card-head"><h3>体检结果</h3><span class="small tertiary" v-if="view.collectedAt">{{ whenText(view.collectedAt) }} · 只读检查</span>
        <span class="grow"></span><button class="plain small" @click="$emit('discover')" :disabled="busy"><ui-icon name="refresh"></ui-icon>重新识别</button></header>
      <div class="home-empty" v-if="!findings.length"><span class="sdot good"></span>一切正常，没有发现需要处理的问题</div>
      <div class="wb-check" v-for="(f, i) in findings" :key="i">
        <span class="wb-check-icon" :class="'st-' + tone(f.level)"><ui-icon :name="f.level === 'danger' ? 'alert' : f.level === 'warn' ? 'warn' : 'info'"></ui-icon></span>
        <div class="grow"><div>{{ f.title }}</div><div class="small secondary">{{ f.detail }}</div></div>
        <button v-if="f.fix" @click="fix(f)">{{ f.fix.label }}</button>
      </div>
    </section>

    <div class="wb-row">
      <section class="card">
        <header class="card-head"><h3>网站</h3><span class="grow"></span>
          <button class="link small" v-if="sv.adapter === '1panel'" @click="$emit('tab', 'sites')">全部 ›</button></header>
        <template v-if="sv.adapter === '1panel'">
          <div class="home-empty" v-if="sitesError"><span class="sdot warn"></span>{{ sitesError }}
            <button class="link small" v-if="sitesError.includes('1Panel 接口')" @click="$emit('tab', 'connection')">去填写</button></div>
          <div class="home-empty" v-else-if="!sites"><span class="spinner inline"></span>正在读取……</div>
          <div class="home-empty" v-else-if="!sites.length">这台服务器上还没有网站</div>
          <button class="wb-site" v-for="x in shownSites" :key="x.id" @click="$emit('site', x.id)">
            <span class="sdot" :class="x.running ? 'good' : 'off'"></span>
            <span class="grow"><span class="block ellipsis">{{ x.domain }}</span><span class="small tertiary">{{ SITE_TYPES[x.type] || x.type }}{{ x.app ? ' · ' + x.app : '' }}</span></span>
            <span class="small" v-if="x.https" :class="certLevel(x.certDays) === 'good' ? 'secondary' : 'st-' + certLevel(x.certDays) + '-text'">HTTPS {{ certLeft(x.certDays) }}</span>
            <span class="small st-warn-text" v-else>没开 HTTPS</span>
          </button>
          <div class="small tertiary wb-more" v-if="sites && sites.length > shownSites.length">还有 {{ sites.length - shownSites.length }} 个</div>
        </template>
        <template v-else>
          <div class="home-empty" v-if="!plainSites.length">没有发现网站</div>
          <div class="wb-site" v-for="w in plainSites.slice(0, 8)" :key="w"><span class="grow ellipsis">{{ w }}</span></div>
          <div class="small tertiary wb-more">网站管理目前支持 1Panel；这里是识别时在 Nginx 配置里看到的网站</div>
        </template>
      </section>

      <section class="card">
        <header class="card-head"><h3>应用和服务</h3><span class="small tertiary">按内存</span><span class="grow"></span>
          <button class="link small" @click="$emit('tab', 'apps')">全部 ›</button></header>
        <div class="home-empty" v-if="!programs.length">重新识别后显示占内存最多的程序</div>
        <div class="wb-app" v-for="g in programs" :key="g.name">
          <div class="wb-app-top"><span class="grow ellipsis">{{ g.name }}<span class="small tertiary" v-if="g.procs > 1"> · {{ g.procs }} 个进程</span></span><span class="small secondary num">{{ gbText(g.memMB) }}</span></div>
          <div class="wb-app-bar"><div :style="{ width: g.w }"></div></div>
        </div>
      </section>
    </div>

    <section class="card">
      <header class="card-head"><h3>最近在这台服务器上</h3><span class="grow"></span><button class="link small" @click="$emit('tab', 'history')">执行记录 ›</button></header>
      <div class="home-empty" v-if="!recent.length">还没有执行过修改</div>
      <button class="change-row" v-for="c in recent" :key="c.id" @click="$emit('log', c.id)">
        <span class="small tertiary change-when">{{ whenText(c.startedAt) }}</span>
        <span class="grow"><span class="ellipsis block" :title="c.note || c.title">{{ c.note || c.title }}</span>
          <span class="small secondary">{{ c.status === 'undone' || c.undoneBy ? '已撤销' : c.status === 'done' ? '已完成' : '没有成功' }}</span></span>
      </button>
    </section>
  </div>`,
};

const ServerApps = {
  props: { profile: Object, collectedAt: String },
  emits: ['ask'],
  setup(props) {
    const p = computed(() => props.profile || {});
    const programs = computed(() => p.value.programs || []);
    const containers = computed(() => (p.value.docker && p.value.docker.list) || []);
    const services = computed(() => p.value.services || { running: [], failed: [] });
    const panelApps = computed(() => (p.value.panel && p.value.panel.apps) || []);
    const runtimes = computed(() => (p.value.panel && p.value.panel.runtimes) || []);
    return { p, programs, containers, services, panelApps, runtimes, gbText, whenText };
  },
  template: `
  <div class="wb-apps">
    <p class="small tertiary">来自{{ collectedAt ? ' ' + whenText(collectedAt) + '的' : '最近一次' }}识别（只读）。要更新，点「重新识别」。</p>

    <div class="group-title">占内存最多的程序</div>
    <div class="group">
      <div class="row secondary" v-if="!programs.length">重新识别后显示</div>
      <div class="table-wrap" v-else>
        <table class="table">
          <thead><tr><th>程序</th><th class="num">进程</th><th class="num">内存</th><th class="num">CPU</th><th>用户</th></tr></thead>
          <tbody><tr v-for="g in programs" :key="g.name"><td>{{ g.name }}</td><td class="num">{{ g.procs }}</td><td class="num nowrap">{{ gbText(g.memMB) }}</td>
            <td class="num">{{ g.cpu.toFixed(1) }}%</td><td class="secondary">{{ g.user }}</td></tr></tbody>
        </table>
      </div>
    </div>

    <template v-if="containers.length || (p.docker && p.docker.status && p.docker.status !== 'no')">
      <div class="group-title">Docker 容器</div>
      <div class="group">
        <div class="row secondary" v-if="p.docker.status === 'unreachable'">Docker 已安装但连接不上，服务可能没有运行</div>
        <div class="row secondary" v-else-if="!containers.length">没有容器</div>
        <div class="table-wrap" v-else>
          <table class="table">
            <thead><tr><th>容器</th><th>镜像</th><th>状态</th><th class="num">内存</th><th class="num">CPU</th></tr></thead>
            <tbody><tr v-for="c in containers" :key="c.name"><td>{{ c.name }}<div class="small tertiary" v-if="c.ports">{{ c.ports }}</div></td><td class="secondary">{{ c.image }}</td>
              <td class="nowrap"><span class="sdot" :class="c.running ? 'good' : 'off'"></span>{{ c.status }}</td>
              <td class="num nowrap">{{ c.mem ? c.mem.split(' / ')[0] : '—' }}</td><td class="num">{{ c.cpu || '—' }}</td></tr></tbody>
          </table>
        </div>
      </div>
    </template>

    <div class="group-title">系统服务</div>
    <div class="group">
      <div class="row" v-if="services.failed && services.failed.length"><span class="k"><span class="sdot crit"></span>启动失败</span>
        <span class="v">{{ services.failed.join('、') }}</span>
        <button @click="$emit('ask', '这台服务器上的服务 ' + services.failed.join('、') + ' 启动失败，帮我查一下原因并修复')">让 AI 排查</button></div>
      <div class="row"><span class="k">正在运行</span><span class="v"><span class="tags" v-if="services.running && services.running.length"><span class="tag" v-for="s in services.running" :key="s">{{ s }}</span></span><template v-else>—</template></span></div>
    </div>

    <template v-if="panelApps.length || runtimes.length || (p.databases && p.databases.length) || (p.wordpress && p.wordpress.length) || (p.java && p.java.length)">
      <div class="group-title">面板应用和运行环境</div>
      <div class="group">
        <div class="row" v-if="panelApps.length"><span class="k">面板应用</span><span class="v"><span class="tags"><span class="tag" v-for="a in panelApps" :key="a">{{ a }}</span></span></span></div>
        <div class="row" v-if="runtimes.length"><span class="k">PHP 运行环境</span><span class="v">{{ runtimes.join('、') }}</span></div>
        <div class="row" v-if="p.databases && p.databases.length"><span class="k">数据库</span><span class="v">{{ p.databases.join('、') }}</span></div>
        <div class="row" v-for="w in p.wordpress" :key="w"><span class="k">WordPress</span><span class="v small">{{ w }}</span></div>
        <div class="row" v-for="j in p.java" :key="j"><span class="k">Java</span><span class="v small">{{ j }}</span></div>
      </div>
    </template>
  </div>`,
};

// 腾讯云 › 云服务器: the Lighthouse and CVM instances in the account, their
// expiry and traffic package, and one instance's monitoring, firewall and
// snapshots. Starting, stopping, rebooting, snapshots and firewall changes
// are checklists, confirmed first.
const CLOUD_STATES = { RUNNING: ['good', '运行中'], STOPPED: ['off', '已关机'], STARTING: ['warn', '正在开机'], STOPPING: ['warn', '正在关机'],
  REBOOTING: ['warn', '正在重启'], PENDING: ['warn', '创建中'], SHUTDOWN: ['off', '已关机'], TERMINATING: ['off', '正在退还'] };
const cloudState = s => CLOUD_STATES[s] || ['off', s || '未知'];
const cloudKind = k => k === 'lighthouse' ? '轻量应用服务器' : '云服务器 CVM';

const CloudPage = {
  props: { active: Boolean, request: Object, servers: { type: Array, default: () => [] }, configured: Boolean },
  emits: ['server', 'add', 'settings', 'ask'],
  setup(props, { emit }) {
    const list = ref(null), loading = ref(false), error = ref('');
    const open = ref(null); // { region, id }
    const detail = ref(null), dLoading = ref(false), dError = ref('');
    const plan = ref(null), planning = ref(false), formError = ref('');
    const fw = reactive({ open: false, port: '', protocol: 'TCP', who: 'all', cidr: '', description: '' });
    const snap = reactive({ open: false, name: '' });
    let seq = 0;

    async function loadList(refresh) {
      if (!props.configured) return;
      loading.value = true; error.value = '';
      try { list.value = await api('GET', '/api/tencent/servers' + (refresh ? '?refresh=1' : '')); }
      catch (e) { error.value = e.message; } finally { loading.value = false; }
    }
    async function loadDetail() {
      const o = open.value;
      if (!o) return;
      const n = ++seq;
      dLoading.value = true; dError.value = '';
      try {
        const d = await api('GET', `/api/tencent/servers/${encodeURIComponent(o.region)}/${encodeURIComponent(o.id)}`);
        if (n === seq) detail.value = d;
      } catch (e) { if (n === seq) dError.value = e.message; }
      finally { if (n === seq) dLoading.value = false; }
    }
    function openOne(s) { open.value = { region: s.region, id: s.id }; detail.value = null; loadDetail(); }
    function back() { open.value = null; detail.value = null; loadList(); }
    watch(() => props.active, v => { if (v) { if (!list.value) loadList(); if (open.value) loadDetail(); } }, { immediate: true });
    watch(() => props.configured, v => { if (v && props.active) loadList(); });
    watch(() => props.request, r => { if (r && r.id) openOne(r); }, { immediate: true });

    const servers = computed(() => (list.value && list.value.servers) || []);
    const serverOf = id => props.servers.find(s => s.id === id);
    const inst = computed(() => detail.value && detail.value.instance);
    const daysTo = t => t ? Math.floor((new Date(t) - Date.now()) / 86400000) : null;
    function expiry(s) {
      if (!s.expiredTime) return { text: s.chargeType === 'POSTPAID_BY_HOUR' ? '按量计费' : '', level: '' };
      const d = daysTo(s.expiredTime);
      const auto = s.renewFlag === 'NOTIFY_AND_AUTO_RENEW';
      const text = d < 0 ? '已过期' : `还剩 ${d} 天`;
      return { text: text + (auto ? ' · 自动续费' : ''), level: d < 0 ? 'crit' : d < 15 && !auto ? 'warn' : '', date: new Date(s.expiredTime).toLocaleDateString('zh-CN') };
    }
    const traffic = s => s.trafficTotal ? Math.min(100, Math.round(s.trafficUsed * 100 / s.trafficTotal)) : null;
    const trafficLevel = v => v >= 90 ? 'crit' : v >= 80 ? 'warn' : '';

    async function propose(body) {
      planning.value = true; formError.value = '';
      try {
        plan.value = await api('POST', '/api/tencent/servers/plan', { instance: open.value.id, region: open.value.region, ...body });
        fw.open = false; snap.open = false;
      } catch (e) { formError.value = e.message; if (!fw.open && !snap.open) notify(e.message, 'error'); }
      finally { planning.value = false; }
    }
    const power = op => propose({ op });
    function openFirewall() { Object.assign(fw, { open: true, port: '', protocol: 'TCP', who: 'all', cidr: '', description: '' }); formError.value = ''; }
    function submitFirewall() {
      propose({ op: 'firewall_open', port: fw.port.trim(), protocol: fw.protocol, cidr: fw.who === 'all' ? '0.0.0.0/0' : fw.cidr.trim(), description: fw.description.trim() });
    }
    function closeRule(r) {
      propose({ op: 'firewall_close', port: r.port, protocol: r.protocol, cidr: r.source });
    }
    function openSnap() { Object.assign(snap, { open: true, name: '' }); formError.value = ''; }
    function submitSnap() { propose({ op: 'snapshot', name: snap.name.trim() }); }
    // Closed while it still runs: look again once it has finished.
    async function closePlan() {
      const p = plan.value;
      plan.value = null;
      if (!p) return;
      for (let i = 0; i < 100; i++) {
        await new Promise(r => setTimeout(r, 3000));
        try {
          const now = await api('GET', `/api/plans/${p.id}`);
          if (now.status === 'running') continue;
          await loadList(true); loadDetail();
        } catch { /* looked at again when the page opens */ }
        return;
      }
    }
    function planDone() { loadList(true); loadDetail(); }
    const loginPort = r => /^(22|3389)$/.test(String(r.port)) || /\bALL\b/i.test(String(r.port));
    const metricFormat = m => v => (Math.round(v * 10) / 10) + ' ' + (m.unit === '%' ? '%' : m.unit || '');
    const snapState = s => ({ NORMAL: '可用', CREATING: '创建中', ROLLBACKING: '回滚中' }[s.state] || s.state);
    function ask() {
      const s = inst.value;
      emit('ask', `帮我看看腾讯云${cloudKind(s.kind)}「${s.name || s.id}」（${s.id}，${s.regionName}）的状况：监控、防火墙和到期，有没有需要处理的？`);
    }
    return { list, loading, error, open, detail, dLoading, dError, plan, planning, formError, fw, snap, servers, serverOf, inst,
      loadList, loadDetail, openOne, back, expiry, traffic, trafficLevel, power, openFirewall, submitFirewall, closeRule, openSnap, submitSnap,
      closePlan, planDone, loginPort, metricFormat, snapState, ask, cloudState, cloudKind, fmtBytes };
  },
  template: `
  <div class="cloud-page">
    <div class="group" v-if="!configured">
      <div class="row"><ui-icon name="cloud" class="lg" style="color: var(--accent)"></ui-icon>
        <div class="grow">还没有配置腾讯云密钥<span class="small secondary block">在「设置 → 腾讯云」填写 SecretId 和 SecretKey，就能在这里看到账号里的轻量应用服务器和云服务器。</span></div>
        <button class="primary" @click="$emit('settings')">去设置</button></div>
    </div>

    <!-- The instances -->
    <template v-else-if="!open">
      <div class="page-head"><p>腾讯云账号里的轻量应用服务器和云服务器 CVM：到期、流量包、监控、防火墙和快照。开关机、快照和防火墙的修改都会先生成一份清单，确认后才执行。</p></div>
      <div class="stat-bar">
        <span class="small tertiary" v-if="list">{{ servers.length }} 台<span v-if="list.fetchedAt"> · 更新于 {{ new Date(list.fetchedAt).toLocaleTimeString('zh-CN', { hour12: false }) }}</span></span>
        <span class="grow"></span>
        <button class="plain" @click="loadList(true)" :disabled="loading"><ui-icon name="refresh"></ui-icon>刷新</button>
      </div>
      <div class="notice" v-if="error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ error }}</div>
      <div class="notice" v-for="e in (list && list.errors) || []" :key="e"><ui-icon name="warn" class="st-warn"></ui-icon>{{ e }}</div>
      <div class="notice" v-if="loading && !list"><span class="spinner"></span>正在读取各个地域的服务器……</div>
      <div class="group" v-if="list && !servers.length"><div class="row secondary">这个账号里还没有轻量应用服务器或云服务器</div></div>
      <div class="group cloud-list" v-if="servers.length">
        <div class="table-wrap">
          <table class="table cloud-table">
            <thead><tr><th>服务器</th><th>状态</th><th>配置</th><th>公网 IP</th><th>到期</th><th>流量包</th><th>Miao Panel</th></tr></thead>
            <tbody>
              <tr v-for="s in servers" :key="s.id" class="site-row" @click="openOne(s)">
                <td><div class="site-name">{{ s.name || s.id }}</div><div class="small tertiary">{{ cloudKind(s.kind) }} · {{ s.regionName }}</div></td>
                <td class="nowrap"><span class="sdot" :class="cloudState(s.state)[0]"></span>{{ cloudState(s.state)[1] }}</td>
                <td class="nowrap">{{ s.cpu }} 核 · {{ s.memoryGB }} GB<div class="small tertiary">{{ s.diskGB }} GB 硬盘<span v-if="s.bandwidthMbps"> · {{ s.bandwidthMbps }} Mbps</span></div></td>
                <td class="nowrap mono small">{{ (s.publicIPs || [])[0] || '—' }}</td>
                <td class="nowrap"><span :class="expiry(s).level ? 'st-' + expiry(s).level + '-text' : ''">{{ expiry(s).text || '—' }}</span><div class="small tertiary" v-if="expiry(s).date">{{ expiry(s).date }}</div></td>
                <td class="cloud-traffic"><template v-if="traffic(s) != null">
                  <div class="small nowrap">{{ fmtBytes(s.trafficUsed) }} / {{ fmtBytes(s.trafficTotal) }}</div>
                  <div class="meter" :class="trafficLevel(traffic(s))" role="meter" :aria-valuenow="traffic(s)" aria-valuemin="0" aria-valuemax="100" aria-label="流量包用量"><div :style="{ width: traffic(s) + '%' }"></div></div></template>
                  <span class="tertiary" v-else>—</span></td>
                <td class="nowrap" @click.stop>
                  <button class="link small" v-if="s.serverId && serverOf(s.serverId)" @click="$emit('server', s.serverId)">{{ serverOf(s.serverId).name }} ›</button>
                  <button class="link small" v-else-if="(s.publicIPs || []).length" @click="$emit('add', s)">添加</button>
                  <span class="tertiary" v-else>—</span></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <!-- One instance -->
    <template v-else>
      <div class="site-head">
        <button class="plain" @click="back"><ui-icon name="chevron" class="flip"></ui-icon>云服务器</button>
        <template v-if="inst">
          <div class="grow site-title">
            <h2>{{ inst.name || inst.id }}</h2>
            <div class="small secondary"><span class="sdot" :class="cloudState(inst.state)[0]"></span>{{ cloudState(inst.state)[1] }} · {{ cloudKind(inst.kind) }} · {{ inst.regionName }} · {{ inst.id }}</div>
          </div>
          <div class="site-actions">
            <button v-if="inst.serverId && serverOf(inst.serverId)" @click="$emit('server', inst.serverId)"><ui-icon name="server"></ui-icon>服务器页</button>
            <button @click="ask"><ui-icon name="sparkles"></ui-icon>让 AI 检查</button>
            <button v-if="inst.state === 'STOPPED'" class="primary" @click="power('start')" :disabled="planning">开机</button>
            <template v-else-if="inst.state === 'RUNNING'">
              <button @click="power('reboot')" :disabled="planning">重启</button>
              <button class="destructive" @click="power('stop')" :disabled="planning">关机</button>
            </template>
          </div>
        </template>
        <span class="grow" v-else></span>
        <span class="spinner inline" v-if="dLoading"></span>
      </div>
      <div class="notice" v-if="dError"><ui-icon name="alert" class="st-crit"></ui-icon>{{ dError }}<button class="plain" @click="loadDetail">重试</button></div>
      <div class="notice" v-if="dLoading && !detail"><span class="spinner"></span>正在读取……</div>

      <template v-if="detail">
        <div class="group">
          <div class="row"><span class="k">配置</span><span class="v">{{ inst.cpu }} 核 · {{ inst.memoryGB }} GB 内存 · 系统盘 {{ inst.diskGB }} GB<span v-if="inst.bandwidthMbps"> · 带宽 {{ inst.bandwidthMbps }} Mbps</span></span></div>
          <div class="row"><span class="k">系统</span><span class="v">{{ inst.os || '—' }}</span></div>
          <div class="row"><span class="k">IP</span><span class="v mono small">{{ (inst.publicIPs || []).join('、') || '—' }}<span class="tertiary" v-if="(inst.privateIPs || []).length"> · 内网 {{ inst.privateIPs.join('、') }}</span></span></div>
          <div class="row"><span class="k">到期</span><span class="v"><span :class="expiry(inst).level ? 'st-' + expiry(inst).level + '-text' : ''">{{ expiry(inst).date ? expiry(inst).date + '（' + expiry(inst).text + '）' : expiry(inst).text || '—' }}</span></span></div>
          <div class="row stack" v-if="traffic(inst) != null">
            <div style="display: flex"><span>本月流量包</span><span class="secondary" style="margin-left: auto">已用 {{ fmtBytes(inst.trafficUsed) }} / {{ fmtBytes(inst.trafficTotal) }}</span></div>
            <div class="meter" :class="trafficLevel(traffic(inst))" role="meter" :aria-valuenow="traffic(inst)" aria-valuemin="0" aria-valuemax="100" aria-label="流量包用量"><div :style="{ width: traffic(inst) + '%' }"></div></div>
          </div>
        </div>

        <div class="group-title">最近 24 小时</div>
        <div class="notice" v-if="detail.metricError"><ui-icon name="warn" class="st-warn"></ui-icon>监控：{{ detail.metricError }}</div>
        <div class="cloud-metrics" v-if="detail.metrics.length">
          <section class="card" v-for="m in detail.metrics" :key="m.label">
            <header class="card-head"><h3>{{ m.label }}</h3><span class="small secondary num">现在 {{ metricFormat(m)(m.latest) }} · 平均 {{ metricFormat(m)(m.avg) }} · 最高 {{ metricFormat(m)(m.max) }}</span></header>
            <line-chart v-if="m.points.length" :points="m.points" :label="m.label" :format="metricFormat(m)" :span="24"></line-chart>
            <div class="home-empty" v-else>没有数据</div>
          </section>
        </div>

        <div class="group-title cloud-title">{{ inst.kind === 'lighthouse' ? '防火墙' : '安全组' }}<span class="tertiary small" v-if="detail.group">{{ detail.group }}（同一安全组的服务器共用这些规则）</span>
          <span class="grow"></span><button class="plain small" @click="openFirewall" :disabled="planning"><ui-icon name="plus"></ui-icon>放行端口</button></div>
        <div class="group">
          <div class="row" v-if="detail.firewallError"><ui-icon name="alert" class="st-crit"></ui-icon><span class="grow secondary">{{ detail.firewallError }}</span></div>
          <div class="row secondary" v-else-if="!detail.firewall.length">没有入站规则</div>
          <div class="table-wrap" v-else>
            <table class="table">
              <thead><tr><th>端口</th><th>协议</th><th>来源</th><th>策略</th><th>备注</th><th><span class="sr-only">操作</span></th></tr></thead>
              <tbody><tr v-for="(r, i) in detail.firewall" :key="i">
                <td class="mono">{{ r.port }}</td><td>{{ r.protocol }}</td>
                <td>{{ r.everyone ? '所有人' : r.source }}<span class="tertiary small cloud-cidr" v-if="r.everyone">{{ r.source }}</span></td>
                <td><span class="sdot" :class="r.action === 'ACCEPT' ? 'good' : 'off'"></span>{{ r.action === 'ACCEPT' ? '允许' : '拒绝' }}</td>
                <td class="secondary">{{ r.description || '' }}</td>
                <td class="site-ops"><button class="link small destructive" v-if="r.action === 'ACCEPT' && !loginPort(r)" @click="closeRule(r)" :disabled="planning">关闭</button>
                  <span class="small tertiary" v-else-if="loginPort(r)" title="远程登录用的端口，关掉会连不上">登录用</span></td>
              </tr></tbody>
            </table>
          </div>
        </div>

        <div class="group-title cloud-title">系统盘快照<span class="grow"></span><button class="plain small" @click="openSnap" :disabled="planning"><ui-icon name="plus"></ui-icon>创建快照</button></div>
        <div class="group">
          <div class="row" v-if="detail.snapshotError"><ui-icon name="alert" class="st-crit"></ui-icon><span class="grow secondary">{{ detail.snapshotError }}</span></div>
          <div class="row secondary" v-else-if="!detail.snapshots.length">还没有快照。大改之前做一个，出问题可以在腾讯云控制台回滚。</div>
          <div class="row" v-for="sn in detail.snapshots" :key="sn.id">
            <div class="grow"><div>{{ sn.name || sn.id }}</div><div class="small tertiary">{{ sn.id }}<span v-if="sn.created"> · {{ new Date(sn.created).toLocaleString('zh-CN', { hour12: false }) }}</span><span v-if="sn.sizeGB"> · {{ sn.sizeGB }} GB</span></div></div>
            <span class="small"><span class="sdot" :class="sn.state === 'NORMAL' ? 'good' : 'warn'"></span>{{ snapState(sn) }}<span v-if="sn.state === 'CREATING' && sn.percent"> {{ sn.percent }}%</span></span>
          </div>
        </div>
      </template>
    </template>

    <!-- Opening a port -->
    <div class="sheet-mask" v-if="fw.open" @click.self="fw.open = false">
      <div class="sheet" role="dialog" aria-label="放行端口">
        <h2>放行端口</h2>
        <p>在{{ inst && inst.kind === 'lighthouse' ? '防火墙' : '安全组' }}里加一条允许访问的规则。只给自己用的端口（数据库、面板）最好只允许自己的 IP。</p>
        <div class="group">
          <div class="row form"><span class="k">端口</span><span class="v"><input v-model="fw.port" placeholder="如 8080，或 8000-8100" aria-label="端口" autocomplete="off"></span></div>
          <div class="row form"><span class="k">协议</span><span class="v"><span class="segmented"><button :class="{on: fw.protocol === 'TCP'}" @click="fw.protocol = 'TCP'">TCP</button><button :class="{on: fw.protocol === 'UDP'}" @click="fw.protocol = 'UDP'">UDP</button></span></span></div>
          <div class="row form"><span class="k">允许谁</span><span class="v"><span class="segmented"><button :class="{on: fw.who === 'all'}" @click="fw.who = 'all'">所有人</button><button :class="{on: fw.who === 'ip'}" @click="fw.who = 'ip'">指定 IP</button></span></span></div>
          <div class="row form" v-if="fw.who === 'ip'"><span class="k">IP 或网段</span><span class="v"><input v-model="fw.cidr" placeholder="如 203.0.113.7 或 203.0.113.0/24" aria-label="IP 或网段" autocomplete="off"></span></div>
          <div class="row form"><span class="k">备注</span><span class="v"><input v-model="fw.description" placeholder="可以不填" aria-label="备注" autocomplete="off"></span></div>
        </div>
        <div class="notice" v-if="formError"><ui-icon name="alert" class="st-crit"></ui-icon>{{ formError }}</div>
        <div class="sheet-actions"><button @click="fw.open = false">取消</button>
          <button class="primary" @click="submitFirewall" :disabled="planning || !fw.port.trim() || (fw.who === 'ip' && !fw.cidr.trim())">{{ planning ? '正在生成……' : '生成清单' }}</button></div>
      </div>
    </div>

    <!-- Taking a snapshot -->
    <div class="sheet-mask" v-if="snap.open" @click.self="snap.open = false">
      <div class="sheet" role="dialog" aria-label="创建快照">
        <h2>创建快照</h2>
        <p>给系统盘做一个整盘备份，不影响运行。{{ inst && inst.kind === 'cvm' ? '云服务器的快照按容量收费。' : '轻量应用服务器有免费的快照额度。' }}</p>
        <div class="group"><div class="row form"><span class="k">名称</span><span class="v"><input v-model="snap.name" placeholder="不填自动生成，如 升级前" aria-label="快照名称" autocomplete="off"></span></div></div>
        <div class="notice" v-if="formError"><ui-icon name="alert" class="st-crit"></ui-icon>{{ formError }}</div>
        <div class="sheet-actions"><button @click="snap.open = false">取消</button>
          <button class="primary" @click="submitSnap" :disabled="planning">{{ planning ? '正在生成……' : '生成清单' }}</button></div>
      </div>
    </div>

    <!-- The checklist to confirm -->
    <div class="sheet-mask" v-if="plan" @click.self="closePlan">
      <div class="sheet plan-sheet" role="dialog" aria-label="确认清单">
        <h2>{{ plan.title }}</h2>
        <p>勾选后点「执行」，确认后才会生效。</p>
        <plan-card :plan="plan" server-name="腾讯云" @done="planDone"></plan-card>
        <div class="sheet-actions"><button @click="closePlan">关闭</button></div>
      </div>
    </div>
  </div>`,
};

// 待处理: checklists waiting for the user, alerts and daily reports, and
// every checklist so far.
const INBOX_VIEWS = [{ id: 'todo', text: '等你确认' }, { id: 'notices', text: '提醒和日报' }, { id: 'plans', text: '全部清单' }];
const InboxPage = {
  props: { active: Boolean, servers: { type: Array, default: () => [] }, focus: Object },
  emits: ['unread', 'changed'],
  setup(props, { emit }) {
    const view = ref(pref('miao.inboxView', 'todo'));
    watch(view, v => setPref('miao.inboxView', v));
    const plans = ref(null), error = ref('');
    async function load() {
      try { plans.value = await api('GET', '/api/plans'); error.value = ''; }
      catch (e) { error.value = e.message; }
    }
    watch(() => props.active, v => { if (v) load(); }, { immediate: true });
    watch(() => props.focus, f => { if (f && f.view) view.value = f.view; });
    const waiting = computed(() => (plans.value || []).filter(p => p.status === 'proposed'));
    const serverName = id => id ? ((props.servers.find(s => s.id === id) || {}).name || '已删除的服务器') : '腾讯云';
    const fmt = t => t ? new Date(t).toLocaleString('zh-CN', { hour12: false, month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '';
    const done = () => { load(); emit('changed'); };
    return { INBOX_VIEWS, view, plans, error, waiting, serverName, fmt, done, load };
  },
  template: `
  <div class="inbox">
    <div class="inbox-bar">
      <span class="segmented" role="tablist" aria-label="待处理">
        <button v-for="v in INBOX_VIEWS" :key="v.id" role="tab" :aria-selected="view === v.id" :class="{ on: view === v.id }" @click="view = v.id">{{ v.text }}<span class="side-count" v-if="v.id === 'todo' && waiting.length"> {{ waiting.length }}</span></button>
      </span>
    </div>
    <div class="notice" v-if="error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ error }}</div>
    <template v-if="view === 'todo'">
      <p class="small secondary inbox-lead">AI 和各个页面生成、还没有执行的清单。勾选后执行，执行后可以撤销；不需要的可以不管它。</p>
      <plan-card v-for="pl in waiting" :key="pl.id" :plan="pl" :server-name="serverName(pl.serverId) + ' · ' + fmt(pl.createdAt)" @done="done"></plan-card>
      <div class="group" v-if="plans && !waiting.length"><div class="row secondary"><span class="sdot good"></span>没有等你确认的清单。</div></div>
    </template>
    <notice-page v-if="view === 'notices'" :active="active && view === 'notices'" @unread="n => $emit('unread', n)"></notice-page>
    <template v-if="view === 'plans'">
      <p class="small secondary inbox-lead">所有清单都在这里，可以随时回来执行或撤销。</p>
      <plan-card v-for="pl in plans || []" :key="pl.id" :plan="pl" :server-name="serverName(pl.serverId) + ' · ' + fmt(pl.createdAt)" @done="done"></plan-card>
      <div class="group" v-if="plans && !plans.length"><div class="row secondary">还没有清单。在「AI 助手」里说出你想做什么，AI 会把修改方案整理成清单。</div></div>
    </template>
  </div>`,
};

// 网站: the sites on each 1Panel server, and one site's domains, HTTPS,
// reverse proxies, rewrite rules, config file and logs. Every change is
// a checklist, confirmed first and undoable.
const SITE_TYPES = { static: '静态网站', proxy: '反向代理', deployment: '一键部署', runtime: '运行环境', subsite: '子网站', stream: 'TCP/UDP 代理' };
const HTTP_MODES = [
  { id: 'HTTPAlso', text: 'HTTP 和 HTTPS 都能访问', hint: '网站在 EdgeOne 后面、EdgeOne 用 HTTP 回源时选这个' },
  { id: 'HTTPToHTTPS', text: 'HTTP 自动跳转到 HTTPS', hint: '最常用：访客输入 http:// 也会进入 https://' },
  { id: 'HTTPSOnly', text: '只能用 HTTPS', hint: 'http:// 直接打不开' },
];
const REWRITE_TEMPLATES = ['default', 'wordpress', 'wp2', 'thinkphp', 'laravel5', 'yii2', 'typecho', 'typecho2', 'zblog', 'emlog', 'discuz', 'discuzx',
  'discuzx2', 'discuzx3', 'dedecms', 'phpcms', 'phpwind', 'ecshop', 'shopex', 'shopwind', 'niushop', 'crmeb', 'maccms', 'seacms', 'empirecms', 'edusoho',
  'sablog', 'dbshop', 'dabr', 'drupal', 'mvc'];
const SITE_SECTIONS = [{ id: 'overview', text: '概览' }, { id: 'domains', text: '域名' }, { id: 'https', text: 'HTTPS' }, { id: 'proxy', text: '反向代理' },
  { id: 'rewrite', text: '伪静态' }, { id: 'conf', text: '配置文件' }, { id: 'logs', text: '日志' }, { id: 'cache', text: 'EdgeOne 缓存' }];
const siteMemo = new Map(); // "server/site" → detail, shown at once when coming back
const certLeft = d => d == null ? '' : d > 0 ? `剩 ${d} 天` : d === 0 ? '今天到期' : '已过期';
const certLevel = d => d == null ? 'off' : d < 0 ? 'crit' : d <= 15 ? 'warn' : 'good';

// Clearing or warming EdgeOne's cache for some domains; used on a site and
// on the EdgeOne statistics page.
const EoCacheForm = {
  props: { domains: { type: Array, default: () => [] }, serverId: { type: Number, default: 0 } },
  emits: ['plan'],
  setup(props, { emit }) {
    const f = reactive({ domain: props.domains[0] || '', type: 'url', method: 'invalidate', targets: '', warm: '' });
    watch(() => props.domains, v => { if (!v.includes(f.domain)) f.domain = v[0] || ''; });
    const busy = ref(''), error = ref('');
    const lines = s => s.split(/[\n,，\s]+/).map(x => x.trim()).filter(Boolean);
    async function send(body) {
      busy.value = body.op; error.value = '';
      try { emit('plan', await api('POST', '/api/eo/cache/plan', { serverId: props.serverId, domain: f.domain, ...body })); }
      catch (e) { error.value = e.message; } finally { busy.value = ''; }
    }
    const purge = () => send({ op: 'purge', type: f.type, method: f.method, targets: ['url', 'prefix'].includes(f.type) ? lines(f.targets) : [] });
    const warm = () => send({ op: 'prefetch', targets: lines(f.warm) });
    return { f, busy, error, purge, warm, lines };
  },
  template: `
  <div class="eo-cache">
    <div class="group">
      <div class="row form" v-if="domains.length > 1"><span class="k">域名</span><span class="v">
        <select v-model="f.domain" aria-label="域名"><option v-for="d in domains" :key="d" :value="d">{{ d }}</option></select></span></div>
      <div class="row stack">
        <b>清除缓存</b>
        <div class="small secondary">网站更新了文件、改版后，让访客马上看到新内容。一般几分钟内在全部节点生效，不中断访问。</div>
      </div>
      <div class="row form"><span class="k">范围</span><span class="v"><span class="segmented wrap">
        <button :class="{on: f.type === 'url'}" @click="f.type = 'url'">指定网址</button>
        <button :class="{on: f.type === 'prefix'}" @click="f.type = 'prefix'">目录</button>
        <button :class="{on: f.type === 'host'}" @click="f.type = 'host'">整个域名</button>
        <button :class="{on: f.type === 'all'}" @click="f.type = 'all'">整个站点</button></span>
        <span class="small secondary block" v-if="f.type === 'host'">清除 {{ f.domain }} 下的所有缓存，之后一段时间回源会变多。</span>
        <span class="small secondary block" v-if="f.type === 'all'">清除 EdgeOne 站点里所有域名的缓存，源站压力会突然变大，尽量少用。</span></span></div>
      <div class="row form" v-if="f.type === 'url' || f.type === 'prefix'"><span class="k">{{ f.type === 'url' ? '网址' : '目录' }}</span><span class="v">
        <textarea v-model="f.targets" rows="4" spellcheck="false" autocapitalize="off" :aria-label="f.type === 'url' ? '要清除的网址' : '要清除的目录'"
          :placeholder="f.type === 'url' ? '每行一个，例如\\n/css/app.css\\nhttps://' + (f.domain || 'example.com') + '/index.html' : '每行一个，例如\\n/static/\\n/uploads/2026/'"></textarea>
        <span class="small secondary block">可以只写路径，会自动加上 https://{{ f.domain }}</span></span></div>
      <div class="row form"><span class="k">方式</span><span class="v"><span class="segmented">
        <button :class="{on: f.method === 'invalidate'}" @click="f.method = 'invalidate'">标记过期</button>
        <button :class="{on: f.method === 'delete'}" @click="f.method = 'delete'">直接删除</button></span>
        <span class="small secondary block">{{ f.method === 'invalidate' ? '节点下次访问时向源站确认有没有更新，没变就继续用（推荐）' : '下次访问一定重新从源站拉取' }}</span></span></div>
      <div class="row"><span class="grow"></span>
        <button class="primary" @click="purge" :disabled="!!busy || !f.domain || ((f.type === 'url' || f.type === 'prefix') && !lines(f.targets).length)">{{ busy === 'purge' ? '正在生成……' : '生成清除清单' }}</button></div>
    </div>
    <div class="group">
      <div class="row stack">
        <b>预热</b>
        <div class="small secondary">提前把文件缓存到 EdgeOne 节点，访客第一次访问也很快。适合发布新版本、大文件或活动页面之前。</div>
      </div>
      <div class="row form"><span class="k">网址</span><span class="v">
        <textarea v-model="f.warm" rows="4" spellcheck="false" autocapitalize="off" aria-label="要预热的网址" :placeholder="'每行一个，例如\\n/download/app.zip\\nhttps://' + (f.domain || 'example.com') + '/'"></textarea></span></div>
      <div class="row"><span class="grow"></span>
        <button class="primary" @click="warm" :disabled="!!busy || !f.domain || !lines(f.warm).length">{{ busy === 'prefetch' ? '正在生成……' : '生成预热清单' }}</button></div>
    </div>
    <div class="notice" v-if="error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ error }}</div>
  </div>`,
};

const SitePage = {
  // server: only that server's sites, for its workbench.
  props: { servers: { type: Array, default: () => [] }, active: Boolean, request: Object, server: { type: Number, default: 0 } },
  emits: ['ask', 'server', 'context'],
  setup(props, { emit }) {
    const list = ref(null), loading = ref(false), error = ref('');
    const serverFilter = ref(pref('miao.siteServer', ''));
    watch(serverFilter, v => setPref('miao.siteServer', v));
    const q = ref('');
    const open = ref(null); // { serverId, siteId }
    const detail = ref(null), dLoading = ref(false), dError = ref('');
    const section = ref('overview');
    const plan = ref(null), planning = ref(false), formError = ref('');
    let seq = 0;

    async function loadList() {
      loading.value = true; error.value = '';
      try { list.value = await api('GET', '/api/websites' + (props.server ? '?server=' + props.server : '')); }
      catch (e) { error.value = e.message; } finally { loading.value = false; }
    }
    watch(() => props.active, v => { if (v) { loadList(); if (open.value) loadDetail(); } }, { immediate: true });

    const panels = computed(() => list.value ? list.value.servers : []);
    const shownServers = computed(() => {
      const k = q.value.trim().toLowerCase();
      return panels.value.filter(s => props.server || !serverFilter.value || String(s.id) === serverFilter.value).map(s => ({
        ...s, shown: s.sites.filter(x => !k || [x.domain, x.remark || '', x.alias, x.app || '', x.runtime || ''].some(v => v.toLowerCase().includes(k))),
      }));
    });
    const total = computed(() => panels.value.reduce((n, s) => n + s.sites.length, 0));
    const usable = computed(() => panels.value.filter(s => !s.noPanel && !s.error));

    function openSite(serverId, siteId, sec) {
      open.value = { serverId, siteId };
      section.value = sec || 'overview';
      // What was being edited belongs to the site left behind.
      Object.assign(rw, { content: '', base: '', hash: '' });
      Object.assign(conf, { content: '', base: '', hash: '' });
      Object.assign(domainForm, { domain: '', port: 80 });
      Object.assign(log, { text: '', path: '', error: '' });
      const memo = siteMemo.get(serverId + '/' + siteId);
      detail.value = memo || null;
      if (memo) fill(memo);
      loadDetail();
    }
    function back() { open.value = null; detail.value = null; loadList(); }
    async function loadDetail() {
      const o = open.value;
      if (!o) return;
      const n = ++seq;
      dLoading.value = true; dError.value = '';
      try {
        const d = await api('GET', `/api/servers/${o.serverId}/websites/${o.siteId}`);
        if (n !== seq) return;
        siteMemo.set(o.serverId + '/' + o.siteId, d);
        detail.value = d; fill(d);
        if (section.value === 'logs') loadLog();
      } catch (e) { if (n === seq) dError.value = e.message; }
      finally { if (n === seq) dLoading.value = false; }
    }

    // The forms start from what the site has now.
    const httpsForm = reactive({ enabled: false, cert: '', mode: 'HTTPAlso', hsts: false, http3: false });
    const domainForm = reactive({ domain: '', port: 80 });
    const rw = reactive({ content: '', base: '', hash: '', template: 'default', name: '' });
    const conf = reactive({ content: '', base: '', hash: '', path: '' });
    function fill(d) {
      Object.assign(httpsForm, { enabled: d.https.enable, cert: d.https.cert || (d.certs[0] && d.certs[0].domain) || '', mode: d.https.mode || 'HTTPAlso',
        hsts: d.https.hsts, http3: d.https.http3 });
      // Text being edited is kept when the file itself has not changed.
      if (rw.hash !== d.rewriteHash || rw.content === rw.base) Object.assign(rw, { content: d.rewrite, base: d.rewrite, hash: d.rewriteHash });
      rw.name = d.rewriteName || 'default';
      if (!REWRITE_TEMPLATES.includes(rw.template)) rw.template = 'default';
      if (conf.hash !== d.confHash || conf.content === conf.base) Object.assign(conf, { content: d.conf, base: d.conf, hash: d.confHash });
      conf.path = d.confPath;
    }

    const site = computed(() => detail.value && detail.value.site);
    const sections = computed(() => SITE_SECTIONS.filter(s => s.id !== 'cache' || (detail.value && detail.value.edgeone)).filter(s =>
      !(site.value && site.value.type === 'stream' && ['proxy', 'rewrite'].includes(s.id))));
    watch(section, v => { if (v === 'logs' && !log.text) loadLog(); });

    async function propose(body) {
      planning.value = true; formError.value = '';
      try {
        const o = open.value || {};
        plan.value = await api('POST', '/api/websites/plan', { serverId: o.serverId, site: site.value ? site.value.domain : '', ...body });
        proxyEd.open = false; certForm.open = false; createForm.open = false;
        return true;
      } catch (e) {
        if (proxyEd.open || certForm.open || createForm.open) formError.value = e.message; else notify(e.message, 'error');
        return false;
      } finally { planning.value = false; }
    }
    function planDone() { if (open.value) loadDetail(); loadList(); }
    async function closePlan() {
      const p = plan.value;
      plan.value = null;
      if (!p) return;
      for (let i = 0; i < 200; i++) {
        await new Promise(r => setTimeout(r, 3000));
        try {
          const now = await api('GET', `/api/plans/${p.id}`);
          if (now.status === 'running') continue;
          planDone();
        } catch { /* checked again next time */ }
        return;
      }
    }
    const planServerName = computed(() => {
      if (plan.value && plan.value.serverId === 0) return '腾讯云';
      return detail.value ? detail.value.serverName : ((props.servers.find(s => s.id === createForm.serverId) || {}).name || '');
    });

    // Status and removal.
    const setRunning = run => propose({ op: run ? 'start' : 'stop' });
    function removeSite() {
      if (!confirm(`确定要删除网站 ${site.value.domain} 吗？会先生成一份清单：删除前在 1Panel 里备份网站目录和配置，网站用的应用和数据库保留。删除后不能一键撤销。`)) return;
      propose({ op: 'delete' });
    }
    function ask() {
      const d = detail.value;
      emit('ask', `帮我检查一下服务器「${d.serverName}」上的网站 ${d.site.domain}：配置、HTTPS 证书、反向代理和最近的错误日志有没有问题，需要修改的话给我一份清单。`);
    }

    // Domains.
    const addDomain = () => propose({ op: 'domain_add', domain: domainForm.domain.trim(), port: Number(domainForm.port) || 80 });
    const removeDomain = d => propose({ op: 'domain_remove', domain: d.domain, port: d.port });

    // HTTPS.
    const certOptions = computed(() => detail.value ? detail.value.certs.filter(c => c.ready) : []);
    const httpsChanged = computed(() => {
      const h = detail.value && detail.value.https;
      if (!h) return false;
      if (!httpsForm.enabled) return h.enable;
      return !h.enable || httpsForm.cert !== h.cert || httpsForm.mode !== (h.mode || 'HTTPAlso') || httpsForm.hsts !== h.hsts || httpsForm.http3 !== h.http3;
    });
    // Only what was changed goes in the checklist; the rest stays as it is.
    function saveHTTPS() {
      const h = detail.value.https;
      if (!httpsForm.enabled) return propose({ op: 'https', enabled: false });
      const body = { op: 'https', enabled: true };
      if (!h.enable || httpsForm.cert !== h.cert) body.cert = httpsForm.cert;
      if (!h.enable || httpsForm.mode !== h.mode) body.httpMode = httpsForm.mode;
      if (httpsForm.hsts !== h.hsts) body.hsts = httpsForm.hsts;
      if (httpsForm.http3 !== h.http3) body.http3 = httpsForm.http3;
      return propose(body);
    }
    const certForm = reactive({ open: false, picked: [], email: '', mode: 'HTTPAlso' });
    const certNames = computed(() => {
      if (!detail.value) return [];
      const names = [detail.value.site.domain];
      for (const d of detail.value.domains) if (!names.includes(d.domain) && !d.domain.startsWith('*.')) names.push(d.domain);
      return names;
    });
    function openCert() {
      formError.value = '';
      Object.assign(certForm, { open: true, picked: [...certNames.value], email: '', mode: detail.value.edgeone ? 'HTTPAlso' : 'HTTPToHTTPS' });
    }
    function toggleCertName(n) {
      const i = certForm.picked.indexOf(n);
      if (i >= 0) certForm.picked.splice(i, 1); else certForm.picked.push(n);
    }
    const issueCert = () => {
      const [first, ...rest] = certNames.value.filter(n => certForm.picked.includes(n));
      propose({ op: 'cert', domain: first, otherDomains: rest, email: certForm.email.trim(), httpMode: certForm.mode });
    };

    // Reverse proxies.
    const proxyEd = reactive({ open: false, editing: false, name: '', path: '/', target: '', hostMode: '$host', host: '' });
    function openProxy(p) {
      formError.value = '';
      if (p) {
        const hostMode = ['$host', '$proxy_host'].includes(p.host) ? p.host : 'custom';
        Object.assign(proxyEd, { open: true, editing: true, name: p.name, path: p.path, target: p.target, hostMode, host: hostMode === 'custom' ? p.host : '' });
      } else {
        const used = new Set(detail.value.proxies.map(x => x.name));
        let name = 'proxy';
        for (let i = 2; used.has(name); i++) name = 'proxy' + i;
        Object.assign(proxyEd, { open: true, editing: false, name, path: '/', target: 'http://127.0.0.1:', hostMode: '$host', host: '' });
      }
    }
    const saveProxy = () => propose({ op: 'proxy_set', name: proxyEd.name.trim(), path: proxyEd.path.trim() || '/', target: proxyEd.target.trim(),
      host: proxyEd.hostMode === 'custom' ? proxyEd.host.trim() : proxyEd.hostMode });
    const toggleProxy = p => propose({ op: p.enabled ? 'proxy_off' : 'proxy_on', name: p.name });
    function removeProxy(p) {
      if (!confirm(`删除反向代理规则 ${p.name}（${p.path} → ${p.target}）？会先生成清单，确认后才删除，可以撤销。`)) return;
      propose({ op: 'proxy_remove', name: p.name });
    }

    // Rewrite rules and the config file.
    const rwDirty = computed(() => rw.content !== rw.base);
    const confDirty = computed(() => conf.content !== conf.base);
    async function useTemplate() {
      if (rwDirty.value && !confirm('套用模板会替换现在编辑框里的内容，继续吗？')) return;
      try {
        const o = open.value;
        const r = await api('GET', `/api/servers/${o.serverId}/websites/${o.siteId}/rewrite?name=${encodeURIComponent(rw.template)}`);
        rw.content = r.content;
      } catch (e) { notify(e.message, 'error'); }
    }
    const saveRewrite = () => propose({ op: 'rewrite', content: rw.content, template: rw.template !== 'default' && rw.content !== rw.base ? rw.template : '', baseHash: rw.hash });
    const saveConf = () => propose({ op: 'conf', content: conf.content, baseHash: conf.hash });
    function onConfKey(e) {
      if ((e.ctrlKey || e.metaKey) && e.key === 's') { e.preventDefault(); if (confDirty.value && !planning.value) saveConf(); }
      if (e.key === 'Tab' && !e.shiftKey) { // indent instead of leaving the box
        e.preventDefault();
        const t = e.target, s = t.selectionStart;
        t.setRangeText('    ', s, t.selectionEnd, 'end');
        t.dispatchEvent(new Event('input'));
      }
    }

    // Logs.
    const log = reactive({ kind: 'access', lines: 200, text: '', path: '', enabled: true, loading: false, error: '' });
    async function loadLog() {
      const o = open.value;
      if (!o) return;
      log.loading = true; log.error = '';
      try {
        const r = await api('GET', `/api/servers/${o.serverId}/websites/${o.siteId}/log?type=${log.kind}&lines=${log.lines}`);
        Object.assign(log, { text: r.lines, path: r.path, enabled: r.enabled });
      } catch (e) { log.error = e.message; } finally { log.loading = false; }
    }
    watch(() => [log.kind, log.lines], loadLog);

    // A new site.
    const createForm = reactive({ open: false, serverId: 0, domain: '', type: 'proxy', proxy: 'http://127.0.0.1:' });
    function openCreate() {
      formError.value = '';
      const s = usable.value.find(x => String(x.id) === serverFilter.value) || usable.value[0];
      Object.assign(createForm, { open: true, serverId: s ? s.id : 0, domain: '', type: 'proxy', proxy: 'http://127.0.0.1:' });
    }
    async function create() {
      planning.value = true; formError.value = '';
      try {
        plan.value = await api('POST', '/api/websites/plan', { serverId: createForm.serverId, op: 'create', domain: createForm.domain.trim(),
          type: createForm.type, proxy: createForm.type === 'proxy' ? createForm.proxy.trim() : '' });
        createForm.open = false;
      } catch (e) { formError.value = e.message; } finally { planning.value = false; }
    }
    watch(() => JSON.stringify([proxyEd, certForm, createForm]), () => { if (!planning.value) formError.value = ''; });

    const visitURL = computed(() => site.value ? (site.value.https ? 'https://' : 'http://') + site.value.domain : '');
    // What is open, for the AI side panel.
    watch(() => [open.value, site.value && site.value.domain, section.value], () => {
      if (!open.value || !site.value) { emit('context', ''); return; }
      const sec = (SITE_SECTIONS.find(x => x.id === section.value) || {}).text;
      emit('context', `网站 ${site.value.domain}（服务器 ${detail.value.serverName}）· ${sec}`);
    });
    const eoDomains = computed(() => detail.value && detail.value.edgeone ? detail.value.edgeone.domains.map(d => d.name) : []);
    const onCachePlan = p => { plan.value = p; };
    // Opening a site or the new-site sheet from elsewhere (search, the overview).
    watch(() => props.request, r => {
      if (r && r.serverId && r.siteId) openSite(r.serverId, r.siteId, r.section);
      if (r && r.create) { open.value = null; detail.value = null; loadList().then(() => { if (usable.value.length) openCreate(); }); }
    }, { immediate: true });
    const modeText = m => (HTTP_MODES.find(x => x.id === m) || { text: '已开启' }).text;
    const modeHint = m => (HTTP_MODES.find(x => x.id === m) || { hint: '' }).hint;

    return { SITE_TYPES, HTTP_MODES, REWRITE_TEMPLATES, list, loading, error, serverFilter, q, panels, shownServers, total, usable, open, detail, dLoading, dError,
      section, sections, site, plan, planning, formError, loadList, openSite, back, loadDetail, planDone, closePlan, planServerName, setRunning, removeSite, ask,
      domainForm, addDomain, removeDomain, httpsForm, certOptions, httpsChanged, saveHTTPS, certForm, certNames, openCert, toggleCertName, issueCert,
      proxyEd, openProxy, saveProxy, toggleProxy, removeProxy, rw, rwDirty, useTemplate, saveRewrite, conf, confDirty, saveConf, onConfKey,
      log, loadLog, createForm, openCreate, create, visitURL, eoDomains, onCachePlan, modeText, modeHint, certLeft, certLevel };
  },
  template: `
  <div class="site-page">
    <!-- The list -->
    <template v-if="!open">
      <div class="page-head" v-if="!server"><p>1Panel 上的网站：域名、HTTPS 证书、反向代理、伪静态和 Nginx 配置。每次修改都会先生成一份清单，确认后才执行，执行后可以撤销。</p></div>
      <div class="stat-bar site-bar">
        <label class="field" v-if="panels.length > 1 && !server"><span>服务器</span>
          <select v-model="serverFilter" aria-label="服务器"><option value="">全部</option><option v-for="s in panels" :key="s.id" :value="String(s.id)">{{ s.name }}</option></select></label>
        <label class="field site-search"><span>搜索</span>
          <input type="search" v-model="q" placeholder="域名或备注" autocomplete="off" autocapitalize="off" spellcheck="false" enterkeyhint="search" @keydown.esc="q = ''"></label>
        <span class="grow"></span>
        <span class="small tertiary" v-if="list">{{ total }} 个网站</span>
        <button class="plain icon-only" @click="loadList" :disabled="loading" title="刷新" aria-label="刷新"><ui-icon name="refresh"></ui-icon></button>
        <button class="primary" @click="openCreate" :disabled="!usable.length"><ui-icon name="plus"></ui-icon>新建网站</button>
      </div>
      <div class="notice" v-if="error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ error }}</div>
      <div class="notice" v-if="loading && !list"><span class="spinner"></span>正在读取各台服务器上的网站……</div>
      <div class="group" v-if="list && !panels.length">
        <div class="row"><ui-icon name="info" class="lg" style="color: var(--accent)"></ui-icon><div class="grow">还没有装了 1Panel 的服务器。网站管理目前支持 1Panel：添加服务器并识别环境后，在服务器页面填写 1Panel 的 API 密钥。
          <span class="small secondary block" v-if="list.others && list.others.length">{{ list.others.join('、') }} 的网站请先在面板里管理。</span></div></div>
      </div>

      <section class="site-server" v-for="s in shownServers" :key="s.id">
        <div class="group-title site-server-title">
          <template v-if="!server"><ui-icon name="server"></ui-icon><span>{{ s.name }}</span></template>
          <span class="tertiary small" v-if="s.openresty && s.openresty.installed">OpenResty {{ s.openresty.version }}<span class="sdot" :class="s.openresty.running ? 'good' : 'crit'"></span>{{ s.openresty.running ? '运行中' : '没有运行' }}</span>
          <span class="tertiary small" v-else-if="s.openresty">没有安装 OpenResty</span>
          <span v-if="loading" class="spinner inline"></span>
        </div>
        <div class="group" v-if="s.noPanel">
          <div class="row"><ui-icon name="plug" class="lg" style="color: var(--accent)"></ui-icon>
            <div class="grow">还没有配置 1Panel 接口<span class="small secondary block">在服务器的「连接设置」里填写 1Panel 的端口和 API 密钥，就能在这里管理网站。</span></div>
            <button @click="$emit('server', s.id)">去填写</button></div>
        </div>
        <div class="group" v-else-if="s.error"><div class="row"><ui-icon name="alert" class="st-crit"></ui-icon><div class="grow secondary">{{ s.error }}</div>
          <button class="plain" @click="loadList">重试</button></div></div>
        <div class="group site-group" v-else>
          <div class="table-wrap">
            <table class="table site-table">
              <thead><tr><th>网站</th><th>类型</th><th>状态</th><th>HTTPS</th><th><span class="sr-only">操作</span></th></tr></thead>
              <tbody>
                <tr v-for="x in s.shown" :key="x.id" class="site-row" @click="openSite(s.id, x.id)">
                  <td><div class="site-name">{{ x.domain }}</div><div class="small tertiary" v-if="x.remark || x.app || x.runtime">{{ [x.remark, x.app, x.runtime].filter(Boolean).join(' · ') }}</div></td>
                  <td class="nowrap">{{ SITE_TYPES[x.type] || x.type }}</td>
                  <td class="nowrap"><span class="sdot" :class="x.running ? 'good' : 'off'"></span>{{ x.running ? '运行中' : '已停止' }}</td>
                  <td class="nowrap"><template v-if="x.https"><span class="sdot" :class="certLevel(x.certDays)"></span>{{ certLeft(x.certDays) || '已开启' }}</template>
                    <span class="tertiary" v-else>未开启</span></td>
                  <td class="site-ops"><button class="link small" @click.stop="openSite(s.id, x.id)" :aria-label="'管理 ' + x.domain">管理</button></td>
                </tr>
                <tr v-if="!s.shown.length"><td colspan="5" class="secondary">{{ s.sites.length ? '没有符合条件的网站' : '这台服务器上还没有网站，点「新建网站」开始' }}</td></tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>
    </template>

    <!-- One site -->
    <template v-else>
      <div class="site-head">
        <button class="plain" @click="back"><ui-icon name="chevron" class="flip"></ui-icon>网站</button>
        <template v-if="site">
          <div class="grow site-title">
            <h2>{{ site.domain }}</h2>
            <div class="small secondary"><span class="sdot" :class="site.running ? 'good' : 'off'"></span>{{ site.running ? '运行中' : '已停止' }} · {{ SITE_TYPES[site.type] || site.type }} · {{ detail.serverName }}</div>
          </div>
          <div class="site-actions">
            <a class="btn plain" :href="visitURL" target="_blank" rel="noopener"><ui-icon name="link"></ui-icon>访问</a>
            <button @click="ask"><ui-icon name="sparkles"></ui-icon>让 AI 检查</button>
            <button @click="setRunning(!site.running)" :disabled="planning">{{ site.running ? '停止' : '启动' }}</button>
            <button class="plain destructive icon-only" @click="removeSite" :disabled="planning" title="删除网站" aria-label="删除网站"><ui-icon name="trash"></ui-icon></button>
          </div>
        </template>
        <span class="grow" v-else></span>
        <span class="spinner inline" v-if="dLoading"></span>
      </div>
      <div class="notice" v-if="dError"><ui-icon name="alert" class="st-crit"></ui-icon>{{ dError }}<button class="plain" @click="loadDetail">重试</button></div>
      <div class="notice" v-if="dLoading && !detail"><span class="spinner"></span>正在读取网站……</div>

      <template v-if="detail">
        <nav class="subtabs site-tabs" role="tablist" aria-label="网站设置">
          <button v-for="s in sections" :key="s.id" role="tab" :aria-selected="section === s.id" :class="{on: section === s.id}" @click="section = s.id">{{ s.text }}</button>
        </nav>
        <div class="alerts" v-if="detail.problems && detail.problems.length">
          <div class="alert al-warn" v-for="p in detail.problems" :key="p"><ui-icon name="warn"></ui-icon><span class="grow">{{ p }}</span></div>
        </div>

        <!-- Overview -->
        <div v-if="section === 'overview'">
          <div class="group">
            <div class="row"><span class="k">状态</span><span class="v"><span class="sdot" :class="site.running ? 'good' : 'off'"></span>{{ site.running ? '运行中' : '已停止（访客看到「网站已停止」页面）' }}</span></div>
            <div class="row"><span class="k">类型</span><span class="v">{{ SITE_TYPES[site.type] || site.type }}<template v-if="site.runtime"> · {{ site.runtime }}</template></span></div>
            <div class="row" v-if="detail.proxy"><span class="k">后端</span><span class="v mono">{{ detail.proxy }}</span></div>
            <div class="row"><span class="k">域名</span><span class="v">{{ detail.domains.map(d => d.port === 80 ? d.domain : d.domain + ':' + d.port).join('、') }}</span></div>
            <div class="row"><span class="k">HTTPS</span><span class="v">
              <template v-if="detail.https.enable"><span class="sdot" :class="certLevel(detail.https.days)"></span>{{ modeText(detail.https.mode) }} · 证书{{ certLeft(detail.https.days) }}{{ detail.https.autoRenew ? '，自动续签' : '' }}</template>
              <template v-else>未开启</template></span></div>
            <div class="row" v-if="detail.edgeone"><span class="k">EdgeOne</span><span class="v">{{ detail.edgeone.domains.map(d => d.name).join('、') }} 经过 EdgeOne（站点 {{ detail.edgeone.zone }}）</span></div>
            <div class="row"><span class="k">网站目录</span><span class="v mono">{{ site.sitePath || '—' }}</span></div>
            <div class="row"><span class="k">日志</span><span class="v">访问日志{{ detail.accessLog ? '开' : '关' }} · 错误日志{{ detail.errorLog ? '开' : '关' }}</span></div>
          </div>
          <div class="alerts">
            <div class="alert al-warn" v-if="detail.https.enable && detail.https.uncovered && detail.https.uncovered.length"><ui-icon name="warn"></ui-icon>
              <span class="grow">证书不包含 {{ detail.https.uncovered.join('、') }}，用 https:// 访问它们时浏览器会报证书错误。</span>
              <button class="link small" @click="section = 'https'; openCert()">重新申请证书</button></div>
            <div class="alert al-warn" v-if="detail.https.enable && detail.https.days != null && detail.https.days <= 15 && !detail.https.autoRenew"><ui-icon name="warn"></ui-icon>
              <span class="grow">证书{{ certLeft(detail.https.days) }}，而且不会自动续签。</span><button class="link small" @click="section = 'https'">去处理</button></div>
            <div class="alert al-info" v-if="!detail.https.enable"><ui-icon name="info"></ui-icon><span class="grow">这个网站还没有开启 HTTPS。</span>
              <button class="link small" @click="section = 'https'">开启 HTTPS</button></div>
          </div>
        </div>

        <!-- Domains -->
        <div v-if="section === 'domains'">
          <div class="group">
            <div class="row" v-for="d in detail.domains" :key="d.id">
              <span class="grow"><b>{{ d.domain }}</b><span class="small tertiary"> · 端口 {{ d.port }}</span><span class="tag" v-if="d.domain === site.domain">主域名</span></span>
              <button class="link small danger" v-if="d.domain !== site.domain" @click="removeDomain(d)" :disabled="planning" :aria-label="'删除域名 ' + d.domain">删除</button>
            </div>
          </div>
          <div class="group-title">添加域名</div>
          <div class="group">
            <div class="row form"><span class="k">域名</span><span class="v"><input v-model="domainForm.domain" placeholder="例如 www.example.com" aria-label="要添加的域名" autocapitalize="off" spellcheck="false" @keydown.enter="domainForm.domain.trim() && addDomain()"></span></div>
            <div class="row form"><span class="k">端口</span><span class="v"><input type="number" min="1" max="65535" v-model.number="domainForm.port" aria-label="端口"><span class="small secondary block">一般用 80；HTTPS 会自动加上 443</span></span></div>
            <div class="row"><span class="grow small secondary">域名还要解析到这台服务器（或 EdgeOne）才能访问。</span>
              <button class="primary" @click="addDomain" :disabled="planning || !domainForm.domain.trim()">生成清单</button></div>
          </div>
        </div>

        <!-- HTTPS -->
        <div v-if="section === 'https'">
          <div class="group" v-if="detail.https.enable">
            <div class="row"><span class="k">证书</span><span class="v">{{ (detail.https.certNames || []).join('、') }}</span></div>
            <div class="row"><span class="k">到期</span><span class="v"><span class="sdot" :class="certLevel(detail.https.days)"></span>{{ detail.https.expires ? detail.https.expires.slice(0, 10) : '—' }}（{{ certLeft(detail.https.days) }}）</span></div>
            <div class="row"><span class="k">自动续签</span><span class="v">{{ detail.https.autoRenew ? '开（1Panel 在到期前自动续签）' : '关' }}</span></div>
          </div>
          <div class="group">
            <label class="row form site-check"><input type="checkbox" v-model="httpsForm.enabled"><span class="grow"><b>开启 HTTPS</b>
              <span class="small secondary block">用 1Panel 里的证书。</span></span></label>
            <template v-if="httpsForm.enabled">
              <div class="row form"><span class="k">证书</span><span class="v">
                <select v-model="httpsForm.cert" aria-label="证书" v-if="certOptions.length">
                  <option v-for="c in certOptions" :key="c.domain" :value="c.domain">{{ c.names.join('、') }}（{{ certLeft(c.days) }}{{ c.covers < detail.domains.length ? '，不包含全部域名' : '' }}）</option></select>
                <span class="small secondary block" v-else>1Panel 里没有能用于这个网站的证书。</span>
                <button class="link small" @click="openCert">申请免费证书（Let's Encrypt）</button></span></div>
              <div class="row form"><span class="k">访问方式</span><span class="v">
                <select v-model="httpsForm.mode" aria-label="访问方式"><option v-for="m in HTTP_MODES" :key="m.id" :value="m.id">{{ m.text }}</option></select>
                <span class="small secondary block">{{ modeHint(httpsForm.mode) }}</span></span></div>
              <label class="row form site-check"><input type="checkbox" v-model="httpsForm.hsts"><span class="grow">HSTS<span class="small secondary block">浏览器记住以后只用 HTTPS 访问。开启后，就算关掉 HTTPS，访问过的浏览器也会在一段时间内打不开网站。</span></span></label>
              <label class="row form site-check"><input type="checkbox" v-model="httpsForm.http3"><span class="grow">HTTP/3<span class="small secondary block">更快的新协议，需要防火墙放行 UDP 443 端口。</span></span></label>
            </template>
            <div class="alert al-warn site-inline" v-if="detail.edgeone && httpsForm.enabled && httpsForm.mode !== 'HTTPAlso'"><ui-icon name="warn"></ui-icon>
              <span class="grow">这个网站经过 EdgeOne。如果 EdgeOne 用 HTTP 回源，跳转到 HTTPS 会让访问陷入循环，请选「HTTP 和 HTTPS 都能访问」。</span></div>
            <div class="row"><span class="grow"></span>
              <button class="primary" @click="saveHTTPS" :disabled="planning || !httpsChanged || (httpsForm.enabled && !httpsForm.cert)">生成清单</button></div>
          </div>
        </div>

        <!-- Reverse proxies -->
        <div v-if="section === 'proxy'">
          <div class="group">
            <div class="row" v-if="!detail.proxies.length"><span class="grow secondary">还没有反向代理规则。反向代理可以把某个路径（比如 /api）转给另一个程序。</span></div>
            <div class="row site-proxy" v-for="p in detail.proxies" :key="p.name" :class="{off: !p.enabled}">
              <div class="grow">
                <div><b class="mono">{{ p.path }}</b> → <span class="mono">{{ p.target }}</span></div>
                <div class="small tertiary">规则 {{ p.name }}{{ p.name === 'root' && site.type === 'proxy' ? '（网站的主规则）' : '' }} · Host {{ p.host }}<template v-if="!p.enabled"> · 已停用</template><template v-if="p.cache"> · 开了缓存</template><template v-if="p.cors"> · 允许跨域</template></div>
              </div>
              <button class="link small" @click="openProxy(p)" v-if="p.enabled">修改</button>
              <button class="link small" @click="toggleProxy(p)" :disabled="planning">{{ p.enabled ? '停用' : '启用' }}</button>
              <button class="link small danger" @click="removeProxy(p)" :disabled="planning">删除</button>
            </div>
          </div>
          <div class="site-toolbar"><button class="primary" @click="openProxy()"><ui-icon name="plus"></ui-icon>添加规则</button></div>
        </div>

        <!-- Rewrite rules -->
        <div v-if="section === 'rewrite'">
          <div class="stat-bar site-bar">
            <label class="field"><span>模板</span><select v-model="rw.template" aria-label="伪静态模板"><option v-for="t in REWRITE_TEMPLATES" :key="t" :value="t">{{ t }}</option></select></label>
            <button @click="useTemplate">套用模板</button>
            <span class="grow small tertiary">现在用的是 {{ rw.name }}</span>
            <button class="plain" @click="rw.content = rw.base" :disabled="!rwDirty">还原</button>
            <button class="primary" @click="saveRewrite" :disabled="planning || !rwDirty">生成清单</button>
          </div>
          <textarea class="site-code" v-model="rw.content" rows="14" spellcheck="false" autocomplete="off" autocapitalize="off" wrap="off" aria-label="伪静态规则"
            placeholder="还没有伪静态规则。可以选一个模板套用，或者直接写 Nginx 的 location / rewrite 规则"></textarea>
          <p class="small secondary">WordPress、ThinkPHP 这类程序需要伪静态规则，链接才能正常打开。1Panel 会先检查规则，有错误会拒绝并保留原来的。</p>
        </div>

        <!-- Config file -->
        <div v-if="section === 'conf'">
          <div class="stat-bar site-bar">
            <span class="grow small tertiary mono ellipsis" :title="conf.path">{{ conf.path }}</span>
            <span class="tag dirty" v-if="confDirty">未保存</span>
            <button class="plain" @click="conf.content = conf.base" :disabled="!confDirty">还原</button>
            <button class="primary" @click="saveConf" :disabled="planning || !confDirty" title="Ctrl+S">生成清单</button>
          </div>
          <textarea class="site-code tall" v-model="conf.content" spellcheck="false" autocomplete="off" autocapitalize="off" wrap="off" aria-label="Nginx 配置文件" @keydown="onConfKey"></textarea>
          <p class="small secondary">这是 1Panel 为这个网站生成的 Nginx 配置。保存前会显示改了哪几行；1Panel 会先用 nginx -t 检查，不通过会自动恢复原文件。HTTPS、反向代理这些用上面对应的页面改更稳妥。</p>
        </div>

        <!-- Logs -->
        <div v-if="section === 'logs'">
          <div class="stat-bar site-bar">
            <span class="segmented"><button :class="{on: log.kind === 'access'}" @click="log.kind = 'access'">访问日志</button><button :class="{on: log.kind === 'error'}" @click="log.kind = 'error'">错误日志</button></span>
            <label class="field"><span>最后</span><select v-model.number="log.lines" aria-label="行数"><option :value="100">100 行</option><option :value="200">200 行</option><option :value="500">500 行</option><option :value="2000">2000 行</option></select></label>
            <span class="grow small tertiary mono ellipsis" :title="log.path">{{ log.path }}</span>
            <button class="plain icon-only" @click="loadLog" :disabled="log.loading" title="刷新" aria-label="刷新"><ui-icon name="refresh"></ui-icon></button>
          </div>
          <div class="notice" v-if="log.error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ log.error }}</div>
          <div class="notice" v-if="!log.enabled"><ui-icon name="info"></ui-icon>这个网站关掉了{{ log.kind === 'access' ? '访问' : '错误' }}日志，可以在 1Panel 里打开。</div>
          <pre class="site-log" v-if="log.text">{{ log.text }}</pre>
          <div class="notice" v-else-if="!log.loading && !log.error"><ui-icon name="info"></ui-icon>日志是空的。</div>
          <div class="notice" v-if="log.loading && !log.text"><span class="spinner"></span>正在读取日志……</div>
        </div>

        <!-- EdgeOne cache -->
        <div v-if="section === 'cache' && detail.edgeone">
          <p class="small secondary site-lead">{{ eoDomains.join('、') }} 经过 EdgeOne。清除或预热会先生成清单，确认后提交给 EdgeOne。</p>
          <eo-cache-form :domains="eoDomains" :server-id="detail.serverId" @plan="onCachePlan"></eo-cache-form>
        </div>
      </template>
    </template>

    <!-- New site -->
    <div class="sheet-mask" v-if="createForm.open" @click.self="createForm.open = false">
      <div class="sheet" role="dialog" aria-label="新建网站">
        <h2>新建网站</h2>
        <p>由 1Panel 的 OpenResty 提供服务。会先生成清单，确认后才创建，可以撤销。</p>
        <div class="group">
          <div class="row form" v-if="usable.length > 1"><span class="k">服务器</span><span class="v"><select v-model.number="createForm.serverId" aria-label="服务器">
            <option v-for="s in usable" :key="s.id" :value="s.id">{{ s.name }}</option></select></span></div>
          <div class="row form"><span class="k">域名</span><span class="v"><input v-model="createForm.domain" placeholder="例如 blog.example.com" aria-label="域名" autocapitalize="off" spellcheck="false"></span></div>
          <div class="row form"><span class="k">类型</span><span class="v"><span class="segmented">
            <button :class="{on: createForm.type === 'proxy'}" @click="createForm.type = 'proxy'">反向代理</button>
            <button :class="{on: createForm.type === 'static'}" @click="createForm.type = 'static'">静态网站</button></span>
            <span class="small secondary block">{{ createForm.type === 'proxy' ? '转给服务器上的一个程序，比如 Docker 里的 Halo、Node 应用' : '直接提供 HTML、图片等文件' }}</span></span></div>
          <div class="row form" v-if="createForm.type === 'proxy'"><span class="k">后端地址</span><span class="v"><input v-model="createForm.proxy" placeholder="http://127.0.0.1:8090" aria-label="后端地址" autocapitalize="off" spellcheck="false"></span></div>
        </div>
        <div class="notice" v-if="formError"><ui-icon name="alert" class="st-crit"></ui-icon>{{ formError }}</div>
        <div class="sheet-actions"><button @click="createForm.open = false">取消</button>
          <button class="primary" @click="create" :disabled="planning || !createForm.domain.trim() || !createForm.serverId">{{ planning ? '正在生成……' : '生成清单' }}</button></div>
      </div>
    </div>

    <!-- A reverse proxy rule -->
    <div class="sheet-mask" v-if="proxyEd.open" @click.self="proxyEd.open = false">
      <div class="sheet" role="dialog" :aria-label="proxyEd.editing ? '修改反向代理' : '添加反向代理'">
        <h2>{{ proxyEd.editing ? '修改反向代理' : '添加反向代理' }}</h2>
        <p>访问这个网站的某个路径时，由 OpenResty 转给后端程序。</p>
        <div class="group">
          <div class="row form"><span class="k">路径</span><span class="v"><input v-model="proxyEd.path" placeholder="/api；/ 表示整个网站" aria-label="路径" autocapitalize="off" spellcheck="false"></span></div>
          <div class="row form"><span class="k">后端地址</span><span class="v"><input v-model="proxyEd.target" placeholder="http://127.0.0.1:8080" aria-label="后端地址" autocapitalize="off" spellcheck="false"></span></div>
          <div class="row form"><span class="k">Host</span><span class="v"><span class="segmented wrap">
            <button :class="{on: proxyEd.hostMode === '$host'}" @click="proxyEd.hostMode = '$host'">访客访问的域名</button>
            <button :class="{on: proxyEd.hostMode === '$proxy_host'}" @click="proxyEd.hostMode = '$proxy_host'">后端地址里的域名</button>
            <button :class="{on: proxyEd.hostMode === 'custom'}" @click="proxyEd.hostMode = 'custom'">自定义</button></span>
            <input v-if="proxyEd.hostMode === 'custom'" v-model="proxyEd.host" placeholder="例如 api.example.com" aria-label="自定义 Host" autocapitalize="off" spellcheck="false">
            <span class="small secondary block">后端是自己服务器上的程序选第一个；代理到别人的网站（例如 CDN、对象存储）选第二个。</span></span></div>
          <div class="row form"><span class="k">规则名称</span><span class="v"><input v-model="proxyEd.name" :disabled="proxyEd.editing" aria-label="规则名称" autocapitalize="off" spellcheck="false">
            <span class="small secondary block">字母、数字、_ 和 -</span></span></div>
        </div>
        <div class="notice" v-if="formError"><ui-icon name="alert" class="st-crit"></ui-icon>{{ formError }}</div>
        <div class="sheet-actions"><button @click="proxyEd.open = false">取消</button>
          <button class="primary" @click="saveProxy" :disabled="planning || !proxyEd.target.trim() || !proxyEd.name.trim()">{{ planning ? '正在生成……' : '生成清单' }}</button></div>
      </div>
    </div>

    <!-- A free certificate -->
    <div class="sheet-mask" v-if="certForm.open" @click.self="certForm.open = false">
      <div class="sheet" role="dialog" aria-label="申请免费证书">
        <h2>申请免费证书</h2>
        <p>让 1Panel 向 Let's Encrypt 申请，签发后自动开启 HTTPS，到期前 1Panel 会自动续签。</p>
        <div class="group">
          <div class="row form"><span class="k">包含的域名</span><span class="v">
            <label class="site-pick" v-for="n in certNames" :key="n"><input type="checkbox" :checked="certForm.picked.includes(n)" @change="toggleCertName(n)">{{ n }}</label></span></div>
          <div class="row form"><span class="k">访问方式</span><span class="v">
            <select v-model="certForm.mode" aria-label="访问方式"><option v-for="m in HTTP_MODES" :key="m.id" :value="m.id">{{ m.text }}</option></select></span></div>
          <div class="row form"><span class="k">邮箱</span><span class="v"><input v-model="certForm.email" type="email" placeholder="1Panel 里还没有 Let's Encrypt 账号时需要" aria-label="邮箱" autocapitalize="off" spellcheck="false"></span></div>
        </div>
        <p class="small secondary">Let's Encrypt 会访问 http://域名/.well-known/ 来验证，所以这些域名要已经解析到这台服务器（经过 EdgeOne 也可以），80 端口要能访问。</p>
        <div class="notice" v-if="formError"><ui-icon name="alert" class="st-crit"></ui-icon>{{ formError }}</div>
        <div class="sheet-actions"><button @click="certForm.open = false">取消</button>
          <button class="primary" @click="issueCert" :disabled="planning || !certForm.picked.length">{{ planning ? '正在生成……' : '生成清单' }}</button></div>
      </div>
    </div>

    <!-- The checklist to confirm -->
    <div class="sheet-mask" v-if="plan" @click.self="closePlan">
      <div class="sheet plan-sheet" role="dialog" aria-label="确认清单">
        <h2>{{ plan.title }}</h2>
        <p>勾选后点「执行」，确认后才会生效；执行后可以在这里或「建议」页撤销。</p>
        <plan-card :plan="plan" :server-name="planServerName" @done="planDone"></plan-card>
        <div class="sheet-actions"><button @click="closePlan">关闭</button></div>
      </div>
    </div>
  </div>`,
};

// 存储: COS buckets — their files, their settings, and how exposed each
// one is. Files change at once (and go in the log); a setting saved here
// runs as a one-step checklist, so it can be undone.
const COS_REGIONS = [
  ['ap-beijing', '北京'], ['ap-nanjing', '南京'], ['ap-shanghai', '上海'], ['ap-guangzhou', '广州'], ['ap-chengdu', '成都'], ['ap-chongqing', '重庆'],
  ['ap-hongkong', '中国香港'], ['ap-singapore', '新加坡'], ['ap-tokyo', '东京'], ['ap-seoul', '首尔'], ['ap-bangkok', '曼谷'], ['ap-jakarta', '雅加达'],
  ['na-siliconvalley', '硅谷'], ['na-ashburn', '弗吉尼亚'], ['eu-frankfurt', '法兰克福'], ['sa-saopaulo', '圣保罗'],
];
const COS_REGION_NAME = Object.fromEntries(COS_REGIONS);
const regionText = r => COS_REGION_NAME[r] || r;
const COS_ACL = {
  private: { text: '私有读写', note: '只有你和你授权的账号能访问；分享文件用临时链接。', cls: 'on' },
  'public-read': { text: '公有读私有写', note: '任何人都能下载和列出文件，适合放网站图片；建议同时开启防盗链。', cls: 'warn' },
  'public-read-write': { text: '公有读写', note: '任何人都能上传、覆盖和删除文件，非常危险。', cls: 'crit' },
};
const COS_CLASS = { STANDARD: '标准', STANDARD_IA: '低频', ARCHIVE: '归档', DEEP_ARCHIVE: '深度归档', INTELLIGENT_TIERING: '智能分层',
  MAZ_STANDARD: '多 AZ 标准', MAZ_STANDARD_IA: '多 AZ 低频' };
const COS_LIFE_TEMPLATES = [
  { id: 'abort-7d', text: '清理没传完的碎片', note: '上传中断留下的分块 7 天后清理，它们也按存储量收费', rule: { prefix: '', abortDays: 7 } },
  { id: 'backup-30d', text: '备份只留 30 天', note: 'backup/ 里的文件 30 天后删除', rule: { prefix: 'backup/', expireDays: 30 } },
  { id: 'logs-archive', text: '日志转归档', note: 'logs/ 里的文件 30 天后转归档存储（便宜很多），180 天后删除', rule: { prefix: 'logs/', archiveDays: 30, expireDays: 180 } },
  { id: 'cold-ia', text: '不常用的转低频', note: '30 天后转低频存储，适合很少读取的资料', rule: { prefix: '', iaDays: 30 } },
  { id: 'old-versions', text: '历史版本 30 天后删除', note: '开了版本控制才有用，免得旧版本一直收费', rule: { prefix: '', noncurrentDays: 30 } },
];
const COS_LIFE_FIELDS = [
  ['iaDays', '转低频存储'], ['archiveDays', '转归档存储'], ['deepDays', '转深度归档'], ['expireDays', '删除文件'],
  ['noncurrentDays', '删除历史版本'], ['abortDays', '清理上传碎片'],
];
const COS_METHODS = ['GET', 'PUT', 'POST', 'DELETE', 'HEAD'];
const COS_SECTIONS = [{ id: 'files', text: '文件' }, { id: 'settings', text: '设置' }, { id: 'security', text: '安全与用量' }];
const COS_LINK_TIMES = [{ s: 3600, text: '1 小时' }, { s: 86400, text: '1 天' }, { s: 7 * 86400, text: '7 天' }];
const COS_MAX_UPLOAD = 5 * 1024 * 1024 * 1024;
const shortBucket = n => String(n).replace(/-\d+$/, '');
const cosMemo = new Map(); // bucket → detail, shown at once when coming back
const cosLines = s => String(s || '').split(/[\n,，\s]+/).map(x => x.trim()).filter(Boolean);
const cosSleep = ms => new Promise(r => setTimeout(r, ms));

const StoragePage = {
  props: { configured: Boolean, active: Boolean },
  emits: ['settings', 'ask'],
  setup(props, { emit }) {
    const list = ref(null); // {buckets, appId}
    const listError = ref('');
    const listLoading = ref(false);
    const name = ref(pref('miao.cosBucket', ''));
    const section = ref(pref('miao.cosSection', 'files'));
    const bucket = computed(() => (list.value && list.value.buckets.find(b => b.name === name.value)) || null);
    const where = () => ({ bucket: bucket.value.name, region: bucket.value.region });
    const q = s => `bucket=${encodeURIComponent(bucket.value.name)}&region=${encodeURIComponent(bucket.value.region)}${s || ''}`;
    watch(section, v => setPref('miao.cosSection', v));

    async function loadBuckets(pick) {
      if (!props.configured) return;
      listLoading.value = true;
      try {
        const r = await api('GET', '/api/cos/buckets');
        list.value = r; listError.value = '';
        const want = pick || name.value;
        if (r.buckets.some(b => b.name === want)) { if (name.value !== want) name.value = want; }
        else name.value = r.buckets.length ? r.buckets[0].name : '';
      } catch (e) { listError.value = e.message; } finally { listLoading.value = false; }
    }
    watch(() => props.active, v => { if (v) list.value ? (loadBuckets(), bucket.value && loadDetail()) : loadBuckets(); }, { immediate: true });
    watch(() => props.configured, v => { if (v) loadBuckets(); });
    watch(bucket, (b, old) => {
      if (b && old && b.name === old.name) return;
      if (b) setPref('miao.cosBucket', b.name);
      detail.value = b ? cosMemo.get(b.name) || null : null;
      detailError.value = '';
      usage.value = null; prefix.value = ''; files.value = null; picked.value = []; filter.value = '';
      if (b) { loadDetail(); loadFiles(); if (section.value === 'security') loadUsage(); }
    });
    watch(section, v => { if (v === 'security' && bucket.value && !usage.value) loadUsage(); });

    // ---- The bucket's settings ----
    const detail = ref(null), detailError = ref(''), detailLoading = ref(false);
    let dseq = 0;
    async function loadDetail() {
      if (!bucket.value) return;
      const n = ++dseq, b = bucket.value.name;
      detailLoading.value = true;
      try {
        const d = await api('GET', '/api/cos/bucket?' + q());
        if (n !== dseq) return;
        cosMemo.set(b, d); detail.value = d; detailError.value = '';
      } catch (e) { if (n === dseq) detailError.value = e.message; } finally { if (n === dseq) detailLoading.value = false; }
    }
    const acl = computed(() => (detail.value && detail.value.acl && detail.value.acl.canned) || (bucket.value && bucket.value.acl) || '');
    const readable = computed(() => /^public-read/.test(acl.value) || !!(detail.value && detail.value.policyPublic));
    const grants = computed(() => Object.keys((detail.value && detail.value.acl && detail.value.acl.grants) || {}).length);
    const errOf = k => (detail.value && detail.value.errors && detail.value.errors[k]) || '';
    const refererText = computed(() => {
      const r = detail.value && detail.value.referer;
      if (!r || r.status !== 'Enabled') return '没有开启：任何网站都能引用这个桶的文件';
      return `${r.type === 'Black-List' ? '黑名单' : '白名单'}：${(r.domains || []).join('、')}；${r.emptyRefer === 'Deny' ? '拒绝' : '允许'}空 Referer`;
    });
    const policyCount = computed(() => {
      const p = detail.value && detail.value.policy;
      if (!p) return 0;
      try { const j = JSON.parse(p); return ((j.Statement || j.statement) || []).length; } catch { return 1; }
    });
    const levelIconOf = l => l === 'crit' ? 'alert' : l === 'warn' ? 'warn' : 'info';

    // A change runs at once; the last one stays on the page with its undo.
    const recent = ref(null); // {bucket, plan}
    const saving = ref(false);
    const step0 = computed(() => recent.value && recent.value.plan.stepList ? recent.value.plan.stepList[0] : null);
    async function change(body, opts = {}) {
      if (!bucket.value && !opts.bucket) return null;
      if (opts.confirm && !confirm(opts.confirm)) return null;
      saving.value = true; ed.error = '';
      try {
        const target = opts.bucket || where();
        const p = await api('POST', '/api/cos/plan', { ...target, ...body, run: true });
        ed.kind = '';
        recent.value = { bucket: target.bucket, plan: p };
        return await follow(p, opts.quiet);
      } catch (e) {
        if (ed.kind) ed.error = e.message; else notify(e.message, 'error');
        return null;
      } finally { saving.value = false; }
    }
    async function follow(p, quiet) {
      for (let i = 0; i < 150 && p.status === 'running'; i++) {
        await cosSleep(i < 5 ? 400 : 1000);
        try { p = await api('GET', `/api/plans/${p.id}`); } catch { /* try again */ }
        if (recent.value && recent.value.plan.id === p.id) recent.value = { ...recent.value, plan: p };
      }
      const s = (p.stepList || [])[0] || {};
      if (!quiet) {
        if (s.status === 'done') notify('已保存');
        else {
          const why = (s.log || []).filter(Boolean).slice(-1)[0];
          notify(((STEP_STATUS[s.status] || {}).text || '没有完成') + (why ? '：' + why : ''), 'error');
        }
      }
      await refreshAfter();
      return s.status === 'done' ? p : null;
    }
    // After a change: the list (a bucket may be gone, or safer now), then
    // the open bucket's settings if it is still the one shown.
    async function refreshAfter() {
      const before = name.value;
      await loadBuckets();
      if (bucket.value && bucket.value.name === before) await loadDetail();
    }
    async function undoRecent() {
      const r = recent.value;
      if (!r || !confirm(`撤销「${step0.value.summary}」？`)) return;
      saving.value = true;
      try {
        recent.value = { ...r, plan: await api('POST', `/api/plans/${r.plan.id}/steps/0/undo`) };
        notify('已撤销');
        await refreshAfter();
      } catch (e) { notify(e.message, 'error'); } finally { saving.value = false; }
    }

    // ---- Editors, one sheet each ----
    const ed = reactive({ kind: '', error: '', acl: 'private', type: 'white', domains: '', allowEmpty: true,
      cors: [], life: [], keep: [], fixed: [], index: 'index.html', errorPage: '', https: false, policy: '',
      short: '', region: 'ap-guangzhou', template: '', confirmName: '' });
    function openEd(kind) {
      const d = detail.value || {};
      Object.assign(ed, { kind, error: '' });
      if (kind === 'acl') ed.acl = acl.value || 'private';
      if (kind === 'referer') {
        const r = d.referer || {};
        Object.assign(ed, { type: r.type === 'Black-List' ? 'black' : 'white', domains: (r.domains || []).join('\n'), allowEmpty: r.emptyRefer !== 'Deny' });
      }
      if (kind === 'cors') ed.cors = (d.cors || []).map(r => ({ origins: (r.origins || []).join('\n'), methods: [...(r.methods || [])],
        headers: (r.headers || []).join('\n'), expose: (r.expose || []).join('\n'), maxAge: r.maxAge || 600 }));
      if (kind === 'lifecycle') {
        // Days not set show as empty boxes, not 0.
        ed.life = (d.lifecycle || []).filter(r => r.editable).map(r => {
          const o = { ...r };
          for (const [k] of COS_LIFE_FIELDS) o[k] = r[k] || null;
          return o;
        });
        ed.fixed = (d.lifecycle || []).filter(r => !r.editable);
        ed.keep = ed.fixed.map(r => r.id);
      }
      if (kind === 'website') {
        const w = d.website || {};
        Object.assign(ed, { index: w.index || 'index.html', errorPage: w.error || '', https: !!w.https });
      }
      if (kind === 'policy') ed.policy = d.policy ? prettyJSON(d.policy) : '';
      if (kind === 'create') Object.assign(ed, { short: '', region: bucket.value ? bucket.value.region : 'ap-guangzhou', acl: 'private', template: 'abort-7d' });
      if (kind === 'deleteBucket') ed.confirmName = '';
    }
    const closeEd = () => { if (!saving.value) ed.kind = ''; };
    watch(() => JSON.stringify([ed.acl, ed.domains, ed.cors, ed.life, ed.policy, ed.short]), () => { if (!saving.value) ed.error = ''; });
    function prettyJSON(s) { try { return JSON.stringify(JSON.parse(s), null, 2); } catch { return s; } }

    function saveACL() {
      const risky = ed.acl === 'public-read-write' ? '改成「公有读写」后，任何人都能上传、覆盖和删除这个桶里的文件。确定吗？'
        : ed.acl === 'private' && /^public/.test(acl.value) ? '改成私有后，网站里直接引用这个桶文件的地方会显示不出来。确定吗？' : '';
      change({ op: 'acl', acl: ed.acl }, { confirm: risky });
    }
    function saveReferer(on) {
      const domains = cosLines(ed.domains);
      if (on && !domains.length) { ed.error = '至少填一个域名'; return; }
      change({ op: 'referer', status: on ? 'on' : 'off', refererType: ed.type, domains, allowEmpty: ed.allowEmpty });
    }
    const corsTemplate = () => ({ origins: '', methods: ['GET', 'PUT', 'POST', 'HEAD'], headers: '*', expose: 'ETag', maxAge: 600 });
    function saveCORS() {
      const rules = ed.cors.map(r => ({ origins: cosLines(r.origins), methods: r.methods, headers: cosLines(r.headers), expose: cosLines(r.expose), maxAge: Number(r.maxAge) || 0 }));
      if (rules.some(r => !r.origins.length || !r.methods.length)) { ed.error = '每条规则都要填来源和至少一种方法'; return; }
      change({ op: 'cors', cors: rules });
    }
    function addLife(t) {
      const base = { id: '', prefix: '', enabled: true, iaDays: null, archiveDays: null, deepDays: null, expireDays: null, noncurrentDays: null, abortDays: null };
      let id = t ? t.id : 'rule';
      for (let i = 2; ed.life.some(r => r.id === id) || ed.fixed.some(r => r.id === id); i++) id = (t ? t.id : 'rule') + '-' + i;
      ed.life.push({ ...base, ...(t ? t.rule : {}), id });
    }
    function saveLife() {
      const rules = ed.life.map(r => {
        const o = { id: r.id.trim(), prefix: r.prefix.trim(), enabled: r.enabled };
        for (const [k] of COS_LIFE_FIELDS) if (Number(r[k]) > 0) o[k] = Number(r[k]);
        return o;
      });
      if (rules.some(r => Object.keys(r).length === 3)) { ed.error = '每条规则至少设置一项，比如多少天后删除'; return; }
      const deletes = rules.some(r => r.enabled && (r.expireDays || r.noncurrentDays));
      change({ op: 'lifecycle', lifecycle: rules, keep: ed.keep },
        { confirm: deletes ? '有规则会到期删除文件，删掉的文件找不回（撤销只能恢复规则本身）。确定保存吗？' : '' });
    }
    const lifeText = r => {
      const parts = COS_LIFE_FIELDS.filter(([k]) => Number(r[k]) > 0).map(([k, t]) => `${r[k]} 天后${t}`);
      return (r.prefix ? r.prefix : '整个桶') + '：' + (parts.join('，') || '还没设置');
    };
    const setVersioning = on => change({ op: 'versioning', status: on ? 'on' : 'suspend' },
      { confirm: on ? '' : '暂停后，之后覆盖和删除的文件不再保留历史版本。确定吗？' });
    const setEncryption = on => change({ op: 'encryption', status: on ? 'on' : 'off' });
    const saveWebsite = on => change({ op: 'website', status: on ? 'on' : 'off', index: ed.index.trim(), error: ed.errorPage.trim(), https: ed.https });
    function savePolicy(remove) {
      const p = remove ? '' : ed.policy.trim();
      if (p) { try { JSON.parse(p); } catch { ed.error = '存储桶策略要是 JSON，检查一下括号和引号'; return; } }
      change({ op: 'policy', policy: p }, { confirm: remove ? '删除存储桶策略后，靠它授权访问的账号和程序会访问不了。确定吗？'
        : '存储桶策略改错可能让文件被公开，或者让你的程序访问不了（可以撤销）。确定保存吗？' });
    }
    const fixPolicy = () => change({ op: 'policy_public_off' }, { confirm: '去掉存储桶策略里允许任何人访问的规则？其他规则不变，可以撤销。' });

    // New and deleted buckets.
    const appId = computed(() => (list.value && list.value.appId) || '');
    const newName = computed(() => ed.short.trim().toLowerCase() + (appId.value ? '-' + appId.value : ''));
    async function createBucket() {
      if (!/^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$/.test(ed.short.trim())) { ed.error = '名字只能用小写字母、数字和 -，不能以 - 开头或结尾'; return; }
      if (!appId.value) { ed.error = '读不到账号的 APPID，请稍后再试'; return; }
      const target = { bucket: newName.value, region: ed.region };
      const t = COS_LIFE_TEMPLATES.find(x => x.id === ed.template);
      const ok = await change({ op: 'create', acl: ed.acl }, { bucket: target });
      if (!ok) return;
      await loadBuckets(target.bucket);
      if (t) {
        const life = await change({ op: 'lifecycle', lifecycle: [{ id: t.id, enabled: true, ...t.rule }], keep: [] }, { bucket: target, quiet: true });
        notify(life ? `已新建 ${target.bucket}，并设置了「${t.text}」` : `已新建 ${target.bucket}，但生命周期规则没有设置成功`, life ? 'ok' : 'error');
      }
    }
    async function deleteBucket() {
      if (ed.confirmName.trim() !== bucket.value.name) { ed.error = '输入的名字不对'; return; }
      const ok = await change({ op: 'delete' });
      if (ok) { cosMemo.delete(ed.confirmName.trim()); await loadBuckets(); }
    }

    // ---- Security and usage ----
    const usage = ref(null), usageLoading = ref(false);
    let useq = 0;
    async function loadUsage() {
      if (!bucket.value) return;
      const n = ++useq;
      usageLoading.value = true;
      try {
        const u = await api('GET', '/api/cos/usage?' + q());
        if (n === useq) usage.value = u;
      } catch (e) { if (n === useq) usage.value = { available: false, note: e.message, hourly: [] }; }
      finally { if (n === useq) usageLoading.value = false; }
    }
    const trafficChange = computed(() => {
      const u = usage.value;
      return u && u.trafficPrev > 0 ? Math.round((u.traffic24h - u.trafficPrev) / u.trafficPrev * 100) : null;
    });
    function fix(f) {
      if (f.fix === 'private') { ed.acl = 'private'; saveACL(); }
      else if (f.fix === 'referer') { openEd('referer'); section.value = 'settings'; }
      else if (f.fix === 'policy_public_off') fixPolicy();
      else if (f.fix === 'versioning') setVersioning(true);
      else if (f.fix === 'cors') { openEd('cors'); section.value = 'settings'; }
    }
    const fixText = { private: '改为私有', referer: '设置防盗链', policy_public_off: '去掉公开规则', versioning: '开启版本控制', cors: '修改跨域规则' };
    function showFile(k) {
      section.value = 'files';
      const dir = k.includes('/') ? k.slice(0, k.lastIndexOf('/') + 1) : '';
      openPrefix(dir, k);
    }
    const ask = () => emit('ask', `帮我检查一下 COS 存储桶 ${bucket.value.name}（${regionText(bucket.value.region)}）的安全设置和用量：访问权限、防盗链、存储桶策略、跨域规则、生命周期合不合理，有没有文件被公开的风险，流量有没有异常？需要修改的话给我一份清单。`);

    // ---- Files ----
    const prefix = ref('');
    const files = ref(null); // {prefix, entries, next}
    const filesLoading = ref(false), filesError = ref('');
    const filter = ref('');
    const picked = ref([]); // keys
    let fseq = 0, flash = '';
    async function loadFiles(more) {
      if (!bucket.value) return;
      const n = ++fseq, p = prefix.value;
      filesLoading.value = true;
      try {
        const r = await api('GET', '/api/cos/objects?' + q(`&prefix=${encodeURIComponent(p)}${more && files.value ? '&marker=' + encodeURIComponent(files.value.next) : ''}`));
        if (n !== fseq) return;
        if (more && files.value) r.entries = [...files.value.entries, ...r.entries];
        files.value = r; filesError.value = '';
        const here = new Set(r.entries.map(e => e.key));
        picked.value = flash && here.has(flash) ? [flash] : picked.value.filter(k => here.has(k));
        flash = '';
      } catch (e) { if (n === fseq) filesError.value = e.message; } finally { if (n === fseq) filesLoading.value = false; }
    }
    function openPrefix(p, select) { prefix.value = p; files.value = null; picked.value = []; filter.value = ''; flash = select || ''; loadFiles(); }
    const crumbs = computed(() => {
      const parts = prefix.value.split('/').filter(Boolean);
      return [{ p: '', text: shortBucket(name.value) }, ...parts.map((x, i) => ({ p: parts.slice(0, i + 1).join('/') + '/', text: x }))];
    });
    const shown = computed(() => {
      if (!files.value) return [];
      const k = filter.value.trim().toLowerCase();
      return files.value.entries.filter(e => !k || e.name.toLowerCase().includes(k));
    });
    const pickSet = computed(() => new Set(picked.value));
    const pickedEntries = computed(() => shown.value.filter(e => pickSet.value.has(e.key)));
    const one = computed(() => pickedEntries.value.length === 1 ? pickedEntries.value[0] : null);
    const allOn = computed(() => shown.value.length > 0 && shown.value.every(e => pickSet.value.has(e.key)));
    const toggleAll = () => { picked.value = allOn.value ? [] : shown.value.map(e => e.key); };
    function togglePick(e) { const s = new Set(picked.value); s.has(e.key) ? s.delete(e.key) : s.add(e.key); picked.value = [...s]; }
    function openEntry(e) { if (e.folder) openPrefix(e.key); else download(e); }
    // A click on a row picks just that one; with Ctrl or ⌘ it adds to the picks.
    function clickRow(e, ev) {
      if (ev.target.closest('button, a, input')) return;
      if (ev.ctrlKey || ev.metaKey) togglePick(e); else picked.value = [e.key];
    }
    const cold = e => /ARCHIVE/.test(e.storageClass || '');

    async function download(e) {
      if (cold(e)) { notify(`${e.name} 在${COS_CLASS[e.storageClass] || '归档'}存储里，要先在腾讯云控制台「恢复」后才能下载`, 'error'); return; }
      try {
        const r = await api('POST', '/api/cos/link', { ...where(), key: e.key, expires: 600, download: true });
        const a = document.createElement('a');
        a.href = r.url; a.download = e.name; a.rel = 'noopener';
        document.body.appendChild(a); a.click(); a.remove();
        notify('正在下载 ' + e.name);
      } catch (err) { notify(err.message, 'error'); }
    }
    const link = reactive({ open: false, entry: null, expires: 3600, url: '', public: '', busy: false, error: '' });
    async function makeLink() {
      link.busy = true; link.error = '';
      try {
        const r = await api('POST', '/api/cos/link', { ...where(), key: link.entry.key, expires: link.expires });
        link.url = r.url; link.public = r.public;
      } catch (e) { link.error = e.message; } finally { link.busy = false; }
    }
    function openLink(e) { Object.assign(link, { open: true, entry: e, url: '', public: '', error: '' }); makeLink(); }
    watch(() => link.expires, () => { if (link.open) makeLink(); });
    function copy(text, what) {
      const done = () => notify('已复制' + what);
      if (navigator.clipboard) navigator.clipboard.writeText(text).then(done, () => notify(text));
      else notify(text);
    }

    const dlg = reactive({ kind: '', name: '', entry: null, busy: false, error: '' });
    const dlgInput = ref(null);
    function openDlg(kind, entry) {
      Object.assign(dlg, { kind, entry: entry || null, name: entry ? entry.name : '', busy: false, error: '' });
      nextTick(() => { const el = dlgInput.value; if (el) { el.focus(); el.setSelectionRange(0, entry && !entry.folder ? stemOf(el.value).length : el.value.length); } });
    }
    async function dlgSubmit() {
      const n = dlg.name.trim();
      if (!n) { dlg.error = '请填写名称'; return; }
      if (n.includes('/')) { dlg.error = '名称里不能有 /'; return; }
      dlg.busy = true; dlg.error = '';
      try {
        if (dlg.kind === 'mkdir') {
          await api('POST', '/api/cos/folder', { ...where(), key: prefix.value + n + '/' });
          flash = prefix.value + n + '/';
        } else {
          const e = dlg.entry, to = prefix.value + n + (e.folder ? '/' : '');
          if (to === e.key) { dlg.kind = ''; return; }
          await api('POST', '/api/cos/rename', { ...where(), from: e.key, to });
          flash = to;
        }
        dlg.kind = '';
        notify(dlg.entry ? '已改名' : '已新建文件夹');
        loadFiles();
      } catch (e) { dlg.error = e.message; } finally { dlg.busy = false; }
    }
    async function remove() {
      const items = pickedEntries.value;
      if (!items.length) return;
      const kept = detail.value && detail.value.versioning === 'Enabled';
      const inside = items.some(e => e.folder) ? '文件夹里的所有文件也会一起删除。' : '';
      if (!confirm(`删除 ${namesText(items.map(e => e.name))}？\n${inside}${kept ? '这个桶开了版本控制，删掉的文件还能在历史版本里找回。' : '删除后不能恢复。'}`)) return;
      try {
        const r = await api('POST', '/api/cos/delete', { ...where(), keys: items.map(e => e.key) });
        notify(`已删除 ${namesText(items.map(e => e.name))}` + (items.some(e => e.folder) ? `（共 ${r.deleted} 个文件）` : ''));
      } catch (e) { notify(e.message, 'error'); }
      loadFiles();
    }

    // Uploads: one file at a time, straight into the bucket.
    const uploads = ref([]); // {n, file, name, key, size, loaded, state: wait|up|done|error|cancelled|conflict, error, bucket, region, overwrite, xhr}
    const fileInput = ref(null);
    const dragging = ref(false);
    let dragDepth = 0, upN = 0, pumping = false;
    const hasFiles = ev => ev.dataTransfer && [...ev.dataTransfer.types].includes('Files');
    function onDragEnter(ev) { if (hasFiles(ev) && section.value === 'files' && bucket.value) { dragDepth++; dragging.value = true; } }
    function onDragLeave(ev) { if (hasFiles(ev)) { dragDepth = Math.max(0, dragDepth - 1); if (!dragDepth) dragging.value = false; } }
    function onDrop(ev) {
      dragDepth = 0; dragging.value = false;
      if (!hasFiles(ev) || section.value !== 'files' || !bucket.value) return;
      if ([...(ev.dataTransfer.items || [])].some(it => it.webkitGetAsEntry && (it.webkitGetAsEntry() || {}).isDirectory)) {
        notify('不能直接上传文件夹：先新建文件夹，再把里面的文件拖进去', 'error');
        return;
      }
      queue([...ev.dataTransfer.files]);
    }
    function onPicked(ev) { queue([...ev.target.files]); ev.target.value = ''; }
    function queue(fs) {
      if (!bucket.value) return;
      for (const f of fs) {
        const u = { n: ++upN, file: f, name: f.name, key: prefix.value + f.name, size: f.size, loaded: 0, state: 'wait', error: '',
          bucket: bucket.value.name, region: bucket.value.region, overwrite: false, xhr: null };
        if (f.size > COS_MAX_UPLOAD) Object.assign(u, { state: 'error', error: '超过 5 GB，请用腾讯云的 COSBrowser 上传' });
        uploads.value.push(u);
      }
      pump();
    }
    async function pump() {
      if (pumping) return;
      pumping = true;
      for (let u; (u = uploads.value.find(x => x.state === 'wait'));) {
        await sendOne(u);
        if (bucket.value && bucket.value.name === u.bucket && u.key.startsWith(prefix.value) && section.value === 'files') loadFiles();
      }
      pumping = false;
    }
    function sendOne(u) {
      return new Promise(resolve => {
        const x = new XMLHttpRequest();
        u.xhr = x; u.state = 'up'; u.loaded = 0;
        x.open('PUT', `/api/cos/object?bucket=${encodeURIComponent(u.bucket)}&region=${encodeURIComponent(u.region)}&key=${encodeURIComponent(u.key)}${u.overwrite ? '&overwrite=1' : ''}`);
        x.setRequestHeader('X-Miao', '1');
        x.setRequestHeader('Content-Type', u.file.type || 'application/octet-stream');
        x.upload.onprogress = e => { if (e.lengthComputable) u.loaded = e.loaded; };
        x.onload = () => {
          if (x.status === 200) { u.state = 'done'; u.loaded = u.size; } else {
            let m = '', code = '';
            try { const j = JSON.parse(x.responseText); m = j.error; code = j.code; } catch { /* not JSON */ }
            u.state = code === 'conflict' ? 'conflict' : 'error'; u.error = m || `上传失败（${x.status}）`;
          }
          resolve();
        };
        x.onerror = () => { u.state = 'error'; u.error = '上传中断了'; resolve(); };
        x.onabort = () => { u.state = 'cancelled'; resolve(); };
        x.send(u.file);
      });
    }
    function overwrite(u) { u.overwrite = true; u.state = 'wait'; u.error = ''; pump(); }
    function cancelUpload(u) { if (u.state === 'wait' || u.state === 'conflict') u.state = 'cancelled'; else if (u.state === 'up' && u.xhr) u.xhr.abort(); }
    const clearUploads = () => { uploads.value = uploads.value.filter(u => ['wait', 'up', 'conflict'].includes(u.state)); };
    const upLeft = computed(() => uploads.value.filter(u => u.state === 'wait' || u.state === 'up').length);
    const upClash = computed(() => uploads.value.filter(u => u.state === 'conflict').length);
    const upPct = u => u.size ? Math.min(100, Math.round(u.loaded / u.size * 100)) : 100;
    const upState = u => ({
      wait: '等待中', done: '已上传', cancelled: '已取消', error: u.error, conflict: '已经有同名的文件',
      up: u.loaded >= u.size ? '正在写入存储桶……' : `${fmtBytes(u.loaded)} / ${fmtBytes(u.size)}`,
    }[u.state]);
    const warnLeave = ev => { if (upLeft.value) { ev.preventDefault(); ev.returnValue = ''; } };
    onMounted(() => window.addEventListener('beforeunload', warnLeave));
    onUnmounted(() => window.removeEventListener('beforeunload', warnLeave));

    return {
      COS_REGIONS, COS_ACL, COS_CLASS, COS_LIFE_TEMPLATES, COS_LIFE_FIELDS, COS_METHODS, COS_SECTIONS, COS_LINK_TIMES,
      list, listError, listLoading, name, section, bucket, loadBuckets, detail, detailError, detailLoading, loadDetail, acl, readable, grants, errOf,
      refererText, policyCount, levelIconOf, recent, saving, step0, undoRecent, STEP_STATUS,
      ed, openEd, closeEd, saveACL, saveReferer, corsTemplate, saveCORS, addLife, saveLife, lifeText, setVersioning, setEncryption, saveWebsite,
      savePolicy, fixPolicy, appId, newName, createBucket, deleteBucket,
      usage, usageLoading, loadUsage, trafficChange, fix, fixText, showFile, ask,
      prefix, files, filesLoading, filesError, filter, picked, pickSet, pickedEntries, one, allOn, toggleAll, togglePick, clickRow, openEntry, openPrefix, loadFiles, crumbs, shown, cold,
      download, link, openLink, copy, dlg, dlgInput, openDlg, dlgSubmit, remove,
      uploads, fileInput, dragging, onDragEnter, onDragLeave, onDrop, onPicked, overwrite, cancelUpload, clearUploads, upLeft, upClash, upPct, upState,
      regionText, shortBucket, fmtBytes, fileTime,
    };
  },
  template: `
  <div class="cos" @dragenter="onDragEnter" @dragleave="onDragLeave" @dragover.prevent @drop.prevent="onDrop">
    <div class="group" v-if="!configured">
      <div class="row"><ui-icon name="info" class="lg" style="color: var(--accent)"></ui-icon><div class="grow">存储桶在腾讯云 COS，需要先填写腾讯云密钥（子账号要有 COS 的权限）。</div><button @click="$emit('settings')">去设置</button></div>
    </div>
    <template v-else>
      <div class="page-head"><p>腾讯云 COS 里的存储桶。文件的上传、改名和删除直接执行，会记在「记录」里；设置保存后马上生效，可以撤销。</p></div>
      <div class="notice" v-if="listError"><ui-icon name="alert" class="st-crit"></ui-icon><span class="grow">{{ listError }}</span><button class="small" @click="loadBuckets()">重试</button></div>
      <div class="notice" v-else-if="!list"><span class="spinner"></span>正在读取存储桶……</div>

      <div class="cos-layout" v-if="list">
        <!-- Buckets -->
        <aside class="cos-side">
          <div class="cos-side-head"><b>存储桶</b><span class="small tertiary">{{ list.buckets.length }} 个</span><span class="grow"></span>
            <button class="plain icon-only" @click="loadBuckets()" :disabled="listLoading" title="刷新" aria-label="刷新存储桶"><ui-icon name="refresh"></ui-icon></button>
            <button class="small" @click="openEd('create')"><ui-icon name="plus"></ui-icon>新建</button></div>
          <select class="cos-pick" v-model="name" aria-label="存储桶" v-if="list.buckets.length">
            <option v-for="b in list.buckets" :key="b.name" :value="b.name">{{ shortBucket(b.name) }} · {{ regionText(b.region) }}{{ b.level === 'crit' ? ' · 有风险' : '' }}</option></select>
          <div class="cos-blist" role="listbox" aria-label="存储桶">
            <button v-for="b in list.buckets" :key="b.name" role="option" :aria-selected="b.name === name" class="cos-bucket" :class="{ on: b.name === name }" @click="name = b.name">
              <ui-icon name="bucket"></ui-icon>
              <span class="grow"><span class="cos-bname">{{ shortBucket(b.name) }}</span><span class="small tertiary">{{ regionText(b.region) }}</span></span>
              <span class="tag" :class="b.level === 'crit' ? 'crit' : b.level === 'warn' ? 'warn' : ''" v-if="b.acl">{{ (COS_ACL[b.acl] || {}).text || b.acl }}</span>
            </button>
          </div>
          <div class="cos-empty small secondary" v-if="!list.buckets.length">还没有存储桶，点「新建」创建一个。</div>
        </aside>

        <!-- One bucket -->
        <section class="cos-main" v-if="bucket">
          <div class="cos-head">
            <div class="grow"><h2>{{ bucket.name }}</h2>
              <div class="small secondary">{{ regionText(bucket.region) }}（{{ bucket.region }}）<template v-if="detail"> · <span class="mono">{{ detail.host }}</span></template></div></div>
            <span class="tag" :class="acl === 'private' ? 'on' : acl === 'public-read-write' ? 'crit' : 'warn'" v-if="acl">{{ (COS_ACL[acl] || {}).text }}</span>
          </div>
          <nav class="subtabs cos-tabs" role="tablist">
            <button v-for="s in COS_SECTIONS" :key="s.id" role="tab" :aria-selected="section === s.id" :class="{ on: section === s.id }" @click="section = s.id">{{ s.text }}
              <span class="badge crit" v-if="s.id === 'security' && detail && detail.findings.some(f => f.level === 'crit')">!</span></button>
          </nav>

          <div class="alert cos-recent" v-if="recent && recent.bucket === bucket.name && step0 && section !== 'files'"
            :class="step0.status === 'done' || step0.status === 'undone' ? 'al-info' : step0.status === 'running' || step0.status === 'queued' ? 'al-info' : 'al-warn'">
            <span class="spinner" v-if="step0.status === 'running' || step0.status === 'queued'"></span>
            <ui-icon v-else :name="(STEP_STATUS[step0.status] || {}).icon || 'info'"></ui-icon>
            <span class="grow">{{ step0.summary }}：{{ (STEP_STATUS[step0.status] || {}).text || step0.status }}
              <span class="block small secondary" v-for="(l, i) in (step0.status !== 'done' ? step0.log || [] : [])" :key="i">{{ l }}</span></span>
            <button class="link small" v-if="step0.status === 'done' && step0.reversible" @click="undoRecent" :disabled="saving">撤销</button>
            <button class="plain icon-only" @click="recent = null" aria-label="关闭"><ui-icon name="close"></ui-icon></button>
          </div>

          <!-- Files -->
          <div v-show="section === 'files'" class="cos-files">
            <div class="cos-fbar">
              <div class="cos-crumbs">
                <template v-for="(c, i) in crumbs" :key="c.p"><span class="sep" v-if="i"><ui-icon name="chevron"></ui-icon></span>
                  <button class="crumb" :class="{ last: i === crumbs.length - 1 }" @click="openPrefix(c.p)">{{ c.text }}</button></template>
              </div>
              <input class="cos-filter" type="search" v-model="filter" placeholder="筛选当前文件夹" aria-label="筛选当前文件夹" autocomplete="off" spellcheck="false" @keydown.esc="filter = ''">
              <button class="plain icon-only" @click="loadFiles()" :disabled="filesLoading" title="刷新" aria-label="刷新文件"><ui-icon name="refresh"></ui-icon></button>
            </div>
            <div class="fm-actions cos-actions">
              <button @click="fileInput.click()" title="上传文件（也可以把文件拖进来）"><ui-icon name="upload"></ui-icon><span class="lbl">上传</span></button>
              <input type="file" multiple ref="fileInput" class="sr-only" tabindex="-1" aria-hidden="true" @change="onPicked">
              <button @click="openDlg('mkdir')"><ui-icon name="folder"></ui-icon><span class="lbl">新建文件夹</span></button>
              <span class="fm-divider"></span>
              <button class="plain" @click="download(one)" :disabled="!one || one.folder" title="下载到电脑"><ui-icon name="download"></ui-icon><span class="lbl">下载</span></button>
              <button class="plain" @click="openLink(one)" :disabled="!one || one.folder" title="生成分享链接"><ui-icon name="link"></ui-icon><span class="lbl">链接</span></button>
              <button class="plain" @click="openDlg('rename', one)" :disabled="!one" title="重命名"><ui-icon name="pencil"></ui-icon><span class="lbl">重命名</span></button>
              <button class="plain destructive" @click="remove" :disabled="!pickedEntries.length" title="删除"><ui-icon name="trash"></ui-icon><span class="lbl">删除</span></button>
            </div>
            <div class="group cos-list">
              <div class="notice" v-if="filesError"><ui-icon name="alert" class="st-crit"></ui-icon><span class="grow">{{ filesError }}</span><button class="small" @click="loadFiles()">重试</button></div>
              <div class="fm-msg tertiary" v-else-if="!files">正在读取文件……</div>
              <div class="table-wrap" v-else>
                <table class="table cos-table">
                  <thead><tr>
                    <th class="ck"><input type="checkbox" :checked="allOn" :indeterminate="picked.length > 0 && !allOn" @change="toggleAll" aria-label="全选"></th>
                    <th>名称</th><th class="num">大小</th><th class="hide-sm">修改时间</th><th class="hide-sm">存储类型</th><th><span class="sr-only">操作</span></th></tr></thead>
                  <tbody>
                    <tr v-for="e in shown" :key="e.key" :class="{ on: pickSet.has(e.key) }" @click="clickRow(e, $event)">
                      <td class="ck" @click.stop><input type="checkbox" :checked="pickSet.has(e.key)" @change="togglePick(e)" :aria-label="'选择 ' + e.name"></td>
                      <td class="cos-name"><button class="cos-open" @click="openEntry(e)" :title="e.folder ? '打开' : '下载'"><ui-icon :name="e.folder ? 'folder' : 'file'" :class="{ dir: e.folder }"></ui-icon><span>{{ e.name }}</span></button></td>
                      <td class="num nowrap">{{ e.folder ? '' : fmtBytes(e.size) }}</td>
                      <td class="hide-sm nowrap">{{ e.modified ? fileTime(e.modified) : '' }}</td>
                      <td class="hide-sm nowrap"><span :class="{ 'st-warn': cold(e) }">{{ e.folder ? '' : COS_CLASS[e.storageClass] || e.storageClass || '标准' }}</span></td>
                      <td class="cos-ops nowrap"><template v-if="!e.folder">
                        <button class="link small" @click="openLink(e)" :aria-label="'分享 ' + e.name">链接</button></template></td>
                    </tr>
                    <tr v-if="!shown.length"><td colspan="6" class="secondary cos-none">
                      <template v-if="filter">没有名字里带「{{ filter }}」的文件</template>
                      <template v-else>这里还没有文件，点「上传」或者把文件拖进来</template></td></tr>
                  </tbody>
                </table>
              </div>
              <div class="cos-more" v-if="files && files.next"><button @click="loadFiles(true)" :disabled="filesLoading">{{ filesLoading ? '正在读取……' : '加载更多' }}</button></div>
            </div>
            <div class="small tertiary cos-foot" v-if="files">{{ files.entries.length }}{{ files.next ? '+' : '' }} 项<template v-if="picked.length"> · 已选 {{ picked.length }} 项</template>
              <template v-if="detail && detail.versioning === 'Enabled'"> · 开了版本控制，覆盖和删除的文件能找回</template></div>
            <div class="fm-drop cos-drop" v-if="dragging"><ui-icon name="upload" class="lg"></ui-icon>松开鼠标，上传到 {{ shortBucket(name) }}/{{ prefix }}</div>
          </div>

          <!-- Settings -->
          <div v-show="section === 'settings'">
            <div class="notice" v-if="detailError"><ui-icon name="alert" class="st-crit"></ui-icon><span class="grow">{{ detail ? '刷新失败（下面是之前读到的）：' : '' }}{{ detailError }}</span><button class="small" @click="loadDetail">重试</button></div>
            <div class="notice" v-if="!detail && !detailError"><span class="spinner"></span>正在读取设置……</div>
            <template v-if="detail">
              <div class="group-title">访问</div>
              <div class="group cos-set">
                <div class="row"><div class="grow"><div>访问权限</div><div class="small tertiary">{{ errOf('acl') || (COS_ACL[acl] || {}).note }}<template v-if="grants"> 另外单独授权给了 {{ grants }} 个账号。</template></div></div>
                  <span class="tag" :class="acl === 'private' ? 'on' : acl === 'public-read-write' ? 'crit' : 'warn'">{{ (COS_ACL[acl] || {}).text || '—' }}</span>
                  <button class="small" @click="openEd('acl')">修改</button></div>
                <div class="row"><div class="grow"><div>防盗链</div><div class="small tertiary">{{ errOf('referer') || refererText }}</div></div>
                  <span class="tag" :class="{ on: detail.referer.status === 'Enabled' }">{{ detail.referer.status === 'Enabled' ? '已开启' : '未开启' }}</span>
                  <button class="small" @click="openEd('referer')">设置</button></div>
                <div class="row"><div class="grow"><div>存储桶策略</div><div class="small tertiary">{{ errOf('policy') || (policyCount ? policyCount + ' 条规则' : '没有设置，按访问权限生效') }}
                  <span class="st-crit" v-if="detail.policyPublic">；其中有允许任何人{{ detail.policyPublic === 'write' ? '写入' : '读取' }}的规则</span></div></div>
                  <button class="small" @click="openEd('policy')">{{ policyCount ? '编辑' : '添加' }}</button></div>
                <div class="row"><div class="grow"><div>跨域访问（CORS）</div><div class="small tertiary">{{ errOf('cors') || (detail.cors.length ? detail.cors.map(r => (r.origins || []).join('、') + ' 可以 ' + (r.methods || []).join('/')).join('；') : '没有规则：网页脚本不能直接访问这个桶') }}</div></div>
                  <button class="small" @click="openEd('cors')">设置</button></div>
              </div>
              <div class="group-title">文件管理</div>
              <div class="group cos-set">
                <div class="row"><div class="grow"><div>生命周期</div>
                  <div class="small tertiary" v-if="errOf('lifecycle')">{{ errOf('lifecycle') }}</div>
                  <div class="small tertiary" v-else-if="!detail.lifecycle.length">没有规则：文件一直按原来的存储类型保存</div>
                  <div class="small secondary" v-for="r in detail.lifecycle" :key="r.id">{{ r.summary }}<span class="tertiary" v-if="!r.enabled">（已停用）</span></div></div>
                  <button class="small" @click="openEd('lifecycle')">设置</button></div>
                <div class="row"><div class="grow"><div>版本控制</div><div class="small tertiary">{{ errOf('versioning') || (detail.versioning === 'Enabled' ? '覆盖和删除的文件会保留历史版本，误删能找回' : detail.versioning === 'Suspended' ? '已暂停：之后的改动不再保留历史版本' : '没有开启：覆盖和删除的文件找不回') }}</div></div>
                  <span class="tag" :class="{ on: detail.versioning === 'Enabled' }">{{ detail.versioning === 'Enabled' ? '已开启' : detail.versioning === 'Suspended' ? '已暂停' : '未开启' }}</span>
                  <button class="small" @click="setVersioning(detail.versioning !== 'Enabled')" :disabled="saving">{{ detail.versioning === 'Enabled' ? '暂停' : '开启' }}</button></div>
                <div class="row"><div class="grow"><div>服务端加密</div><div class="small tertiary">{{ errOf('encryption') || '文件在 COS 的磁盘上加密保存（SSE-COS），下载时自动解密；只影响之后上传的文件' }}</div></div>
                  <span class="tag" :class="{ on: detail.encryption }">{{ detail.encryption ? '已开启' : '未开启' }}</span>
                  <button class="small" @click="setEncryption(!detail.encryption)" :disabled="saving">{{ detail.encryption ? '关闭' : '开启' }}</button></div>
                <div class="row"><div class="grow"><div>静态网站</div><div class="small tertiary">
                  <template v-if="errOf('website')">{{ errOf('website') }}</template>
                  <template v-else-if="detail.website.enabled">首页 {{ detail.website.index }}<template v-if="detail.website.error">，出错页 {{ detail.website.error }}</template><template v-if="detail.website.endpoint"> · <span class="mono">{{ detail.website.endpoint }}</span></template></template>
                  <template v-else>没有开启</template></div></div>
                  <span class="tag" :class="{ on: detail.website.enabled }">{{ detail.website.enabled ? '已开启' : '未开启' }}</span>
                  <button class="small" @click="openEd('website')">设置</button></div>
              </div>
              <div class="group cos-set">
                <div class="row"><div class="grow"><div>删除存储桶</div><div class="small tertiary">只能删除空的存储桶，删除后找不回</div></div>
                  <button class="small destructive" @click="openEd('deleteBucket')">删除</button></div>
              </div>
            </template>
          </div>

          <!-- Security and usage -->
          <div v-show="section === 'security'">
            <div class="cos-sec-head"><span class="grow small secondary">按设置检查这个桶有没有被公开的风险，以及最近的用量。</span>
              <button class="plain" @click="loadDetail(); loadUsage()" :disabled="detailLoading || usageLoading"><ui-icon name="refresh"></ui-icon>刷新</button>
              <button class="primary" @click="ask"><ui-icon name="sparkles"></ui-icon>让 AI 检查</button></div>
            <div class="notice" v-if="detailError"><ui-icon name="alert" class="st-crit"></ui-icon><span class="grow">{{ detailError }}</span><button class="small" @click="loadDetail">重试</button></div>
            <div class="notice" v-if="!detail && !detailError"><span class="spinner"></span>正在检查……</div>
            <div class="alerts" v-if="detail">
              <div class="alert" v-for="(f, i) in detail.findings" :key="i" :class="'al-' + f.level">
                <ui-icon :name="levelIconOf(f.level)"></ui-icon>
                <span class="grow">{{ f.text }}
                  <span class="block cos-flist" v-if="f.files && f.files.length"><button class="link small mono" v-for="k in f.files.slice(0, 8)" :key="k" @click="showFile(k)">{{ k }}</button></span></span>
                <button class="link small" v-if="f.fix && fixText[f.fix]" @click="fix(f)" :disabled="saving">{{ fixText[f.fix] }}</button>
              </div>
              <div class="alert al-info" v-if="!detail.findings.length"><ui-icon name="check"></ui-icon><span class="grow">没有发现问题：桶是私有的，也没有允许任何人访问的策略。</span></div>
            </div>

            <div class="kpi-group" v-if="usage && usage.available">
              <div class="kpi-title">用量</div>
              <div class="tiles three">
                <div class="tile"><div class="label">存储量</div><div class="value">{{ fmtBytes(usage.storageBytes) }}</div><div class="sub">标准存储，按天统计</div></div>
                <div class="tile"><div class="label">外网下行流量</div><div class="value">{{ fmtBytes(usage.traffic24h) }}</div>
                  <div class="sub" v-if="trafficChange !== null"><span :class="trafficChange >= 0 ? 'up' : 'down'">{{ trafficChange >= 0 ? '↑' : '↓' }} {{ Math.abs(trafficChange) }}%</span> 较前 24 小时</div>
                  <div class="sub" v-else>最近 24 小时</div></div>
                <div class="tile"><div class="label">前 24 小时</div><div class="value">{{ fmtBytes(usage.trafficPrev) }}</div><div class="sub">外网下行流量</div></div>
              </div>
            </div>
            <div class="alert al-warn cos-spike" v-if="usage && usage.spike"><ui-icon name="warn"></ui-icon>
              <span class="grow">最近 24 小时的外网流量是之前的好几倍。如果不是你自己的访问，可能是文件被盗链或被人刷流量：检查防盗链，或者把桶改成私有。</span></div>
            <section class="card" v-if="usage && usage.available && usage.hourly.length">
              <header class="card-head"><h3>外网下行流量</h3><span class="small tertiary">最近 48 小时，每小时</span></header>
              <line-chart :points="usage.hourly" label="流量" :format="fmtBytes" :span="48"></line-chart>
            </section>
            <div class="notice" v-if="usageLoading && !usage"><span class="spinner"></span>正在读取用量……</div>
            <div class="small tertiary cos-note" v-if="usage && !usage.available">{{ usage.note }}</div>
          </div>
        </section>
      </div>
    </template>

    <!-- Uploads -->
    <div class="fm-uploads cos-uploads" v-if="uploads.length" role="status">
      <div class="fm-up-head"><b>上传到存储桶</b><span class="small tertiary">{{ upLeft ? '还剩 ' + upLeft + ' 个' : upClash ? upClash + ' 个同名，等你决定' : '已完成' }}</span><span class="grow"></span>
        <button class="plain" v-if="uploads.length > upLeft" @click="clearUploads">清除已完成</button></div>
      <div class="fm-up" v-for="u in uploads" :key="u.n">
        <div class="line"><ui-icon :name="u.state === 'done' ? 'check' : u.state === 'error' ? 'alert' : u.state === 'conflict' ? 'warn' : 'file'" :class="{'st-ok': u.state === 'done', 'st-crit': u.state === 'error', 'st-warn': u.state === 'conflict'}"></ui-icon>
          <span class="grow ellipsis" :title="u.bucket + '/' + u.key">{{ u.name }}</span>
          <button class="link small" v-if="u.state === 'conflict'" @click="overwrite(u)">覆盖</button>
          <button class="plain icon-only" v-if="['wait', 'up', 'conflict'].includes(u.state)" title="取消" aria-label="取消上传" @click="cancelUpload(u)"><ui-icon name="close"></ui-icon></button></div>
        <div class="bar" v-if="u.state === 'up' || u.state === 'wait'"><i :style="{width: upPct(u) + '%'}"></i></div>
        <div class="small" :class="u.state === 'error' ? 'st-crit' : 'tertiary'">{{ upState(u) }}</div>
      </div>
    </div>

    <!-- New folder, rename -->
    <div class="sheet-mask" v-if="dlg.kind" @click.self="!dlg.busy && (dlg.kind = '')">
      <div class="sheet" role="dialog" :aria-label="dlg.kind === 'mkdir' ? '新建文件夹' : '重命名'">
        <h2>{{ dlg.kind === 'mkdir' ? '新建文件夹' : '重命名' }}</h2>
        <p>位置：{{ shortBucket(name) }}/{{ prefix }}<template v-if="dlg.entry && dlg.entry.folder">。文件夹改名会逐个复制里面的文件，文件多的话要等一会儿。</template></p>
        <div class="group"><div class="row form"><span class="k">名称</span><span class="v">
          <input ref="dlgInput" v-model="dlg.name" @keydown.enter="!$event.isComposing && dlgSubmit()" aria-label="名称" spellcheck="false" autocomplete="off" autocapitalize="off"></span></div></div>
        <div class="notice" v-if="dlg.error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ dlg.error }}</div>
        <div class="sheet-actions"><button @click="dlg.kind = ''" :disabled="dlg.busy">取消</button>
          <button class="primary" @click="dlgSubmit" :disabled="dlg.busy">{{ dlg.busy ? '正在处理……' : dlg.kind === 'mkdir' ? '新建' : '改名' }}</button></div>
      </div>
    </div>

    <!-- Share link -->
    <div class="sheet-mask" v-if="link.open" @click.self="link.open = false">
      <div class="sheet" role="dialog" aria-label="分享链接">
        <h2>分享链接</h2>
        <p>{{ link.entry.key }}</p>
        <div class="group">
          <div class="row form"><span class="k">有效期</span><span class="v"><span class="segmented">
            <button v-for="t in COS_LINK_TIMES" :key="t.s" :class="{ on: link.expires === t.s }" @click="link.expires = t.s">{{ t.text }}</button></span></span></div>
          <div class="row stack"><div class="small secondary">临时链接（到期后失效，桶是私有的也能用）</div>
            <div class="cos-url"><span class="mono">{{ link.busy ? '正在生成……' : link.url }}</span><button class="small" @click="copy(link.url, '临时链接')" :disabled="!link.url">复制</button></div></div>
          <div class="row stack" v-if="readable"><div class="small secondary">公开地址（一直有效，因为这个桶允许公开读取）</div>
            <div class="cos-url"><span class="mono">{{ link.public }}</span><button class="small" @click="copy(link.public, '公开地址')" :disabled="!link.public">复制</button></div></div>
        </div>
        <div class="notice" v-if="link.error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ link.error }}</div>
        <div class="sheet-actions"><button @click="link.open = false">关闭</button></div>
      </div>
    </div>

    <!-- Settings editors -->
    <div class="sheet-mask" v-if="ed.kind" @click.self="closeEd">
      <div class="sheet cos-ed" role="dialog" :aria-label="{acl: '访问权限', referer: '防盗链', cors: '跨域访问', lifecycle: '生命周期', website: '静态网站', policy: '存储桶策略', create: '新建存储桶', deleteBucket: '删除存储桶'}[ed.kind]">
        <template v-if="ed.kind === 'acl'">
          <h2>访问权限</h2><p>{{ bucket.name }}。保存后马上生效，可以撤销。</p>
          <div class="group">
            <label class="row form dns-check" v-for="(a, k) in COS_ACL" :key="k"><input type="radio" v-model="ed.acl" :value="k">
              <span class="grow"><b>{{ a.text }}</b><span class="small secondary block">{{ a.note }}</span></span></label>
          </div>
          <div class="hint" v-if="grants">单独授权给其他账号的 {{ grants }} 项会保留。</div>
        </template>

        <template v-else-if="ed.kind === 'referer'">
          <h2>防盗链</h2><p>限制哪些网站可以引用这个桶里的文件（按浏览器带的 Referer 判断），防止别人盗用你的图片、刷你的流量。</p>
          <div class="group">
            <div class="row form"><span class="k">名单类型</span><span class="v"><span class="segmented">
              <button :class="{ on: ed.type === 'white' }" @click="ed.type = 'white'">白名单</button>
              <button :class="{ on: ed.type === 'black' }" @click="ed.type = 'black'">黑名单</button></span>
              <span class="small secondary block">{{ ed.type === 'white' ? '只有名单里的网站能引用' : '名单里的网站不能引用' }}</span></span></div>
            <div class="row form"><span class="k">域名</span><span class="v"><textarea v-model="ed.domains" rows="4" placeholder="每行一个，例如&#10;example.com&#10;*.example.com" aria-label="域名" spellcheck="false" autocapitalize="off"></textarea></span></div>
            <label class="row form dns-check"><input type="checkbox" v-model="ed.allowEmpty"><span class="grow">允许空 Referer<span class="small secondary block">直接在浏览器打开链接、App 和下载工具通常不带 Referer；不允许的话它们会被拒绝。</span></span></label>
          </div>
        </template>

        <template v-else-if="ed.kind === 'cors'">
          <h2>跨域访问（CORS）</h2><p>网页里的脚本直接读写这个桶（比如浏览器直传）时才需要。只放图片、给人下载的桶不用设置。</p>
          <div class="group" v-for="(r, i) in ed.cors" :key="i">
            <div class="row"><b class="grow">规则 {{ i + 1 }}</b><button class="link small danger" @click="ed.cors.splice(i, 1)">删除这条</button></div>
            <div class="row form"><span class="k">来源</span><span class="v"><textarea v-model="r.origins" rows="2" placeholder="每行一个，例如 https://www.example.com；* 表示所有网站" aria-label="来源" spellcheck="false" autocapitalize="off"></textarea></span></div>
            <div class="row form"><span class="k">方法</span><span class="v cos-methods">
              <label v-for="m in COS_METHODS" :key="m"><input type="checkbox" :value="m" v-model="r.methods">{{ m }}</label></span></div>
            <div class="row form"><span class="k">允许的请求头</span><span class="v"><input v-model="r.headers" placeholder="* 表示全部" aria-label="允许的请求头" spellcheck="false" autocapitalize="off"></span></div>
            <div class="row form"><span class="k">暴露的响应头</span><span class="v"><input v-model="r.expose" placeholder="例如 ETag" aria-label="暴露的响应头" spellcheck="false" autocapitalize="off"></span></div>
            <div class="row form"><span class="k">缓存（秒）</span><span class="v"><input type="number" min="0" max="86400" v-model.number="r.maxAge" aria-label="缓存秒数"></span></div>
          </div>
          <div class="cos-add"><button class="small" @click="ed.cors.push(corsTemplate())"><ui-icon name="plus"></ui-icon>添加规则</button>
            <span class="small tertiary" v-if="!ed.cors.length">没有规则时保存，会删除这个桶的跨域设置。</span></div>
        </template>

        <template v-else-if="ed.kind === 'lifecycle'">
          <h2>生命周期</h2><p>按上传后的天数自动转存储类型或删除文件，每天执行一次。转低频、归档能省存储费，但读取要另外收费、归档的还要先恢复。</p>
          <div class="cos-templates"><span class="small secondary">常用：</span>
            <button class="small" v-for="t in COS_LIFE_TEMPLATES" :key="t.id" @click="addLife(t)" :title="t.note"><ui-icon name="plus"></ui-icon>{{ t.text }}</button></div>
          <div class="group" v-for="(r, i) in ed.life" :key="i">
            <div class="row"><label class="grow fm-check"><input type="checkbox" v-model="r.enabled"><b>{{ lifeText(r) }}</b></label>
              <button class="link small danger" @click="ed.life.splice(i, 1)">删除这条</button></div>
            <div class="row form"><span class="k">名称</span><span class="v"><input v-model="r.id" aria-label="规则名称" spellcheck="false" autocapitalize="off"></span></div>
            <div class="row form"><span class="k">作用于</span><span class="v"><input v-model="r.prefix" placeholder="文件夹，例如 logs/；不填表示整个桶" aria-label="作用的文件夹" spellcheck="false" autocapitalize="off"></span></div>
            <div class="row form cos-days"><span class="k">上传后多少天</span><span class="v">
              <label v-for="f in COS_LIFE_FIELDS" :key="f[0]"><span>{{ f[1] }}</span><input type="number" min="0" max="36500" v-model.number="r[f[0]]" :aria-label="f[1] + '（天）'" placeholder="—"></label></span></div>
          </div>
          <div class="group" v-if="ed.fixed.length">
            <label class="row form dns-check" v-for="r in ed.fixed" :key="r.id"><input type="checkbox" :value="r.id" v-model="ed.keep">
              <span class="grow">保留「{{ r.id }}」<span class="small secondary block">{{ r.summary }}</span></span></label>
          </div>
          <div class="cos-add"><button class="small" @click="addLife()"><ui-icon name="plus"></ui-icon>自定义规则</button>
            <span class="small tertiary" v-if="!ed.life.length && !ed.keep.length">没有规则时保存，会删除这个桶的生命周期设置。</span></div>
        </template>

        <template v-else-if="ed.kind === 'website'">
          <h2>静态网站</h2><p>用 COS 直接托管静态网页（比如 Vue、Hexo 生成的网站）。桶要是公有读，自己的域名建议通过 EdgeOne 接入。</p>
          <div class="group">
            <div class="row form"><span class="k">首页</span><span class="v"><input v-model="ed.index" placeholder="index.html" aria-label="首页" spellcheck="false" autocapitalize="off"></span></div>
            <div class="row form"><span class="k">出错页</span><span class="v"><input v-model="ed.errorPage" placeholder="例如 404.html，可不填" aria-label="出错页" spellcheck="false" autocapitalize="off"></span></div>
            <label class="row form dns-check"><input type="checkbox" v-model="ed.https"><span class="grow">强制 HTTPS<span class="small secondary block">HTTP 访问会跳转到 HTTPS</span></span></label>
          </div>
        </template>

        <template v-else-if="ed.kind === 'policy'">
          <h2>存储桶策略</h2><p>用 JSON 给其他账号、子账号或所有人授权，比访问权限更细。不熟悉的话，可以让 AI 帮你写好再粘贴进来。</p>
          <textarea class="cos-policy mono" v-model="ed.policy" rows="14" spellcheck="false" autocapitalize="off" aria-label="存储桶策略" placeholder='{"version": "2.0", "Statement": [...]}'></textarea>
        </template>

        <template v-else-if="ed.kind === 'create'">
          <h2>新建存储桶</h2><p>默认私有读写，最安全；要放网站图片可以选公有读，并在设置里开启防盗链。</p>
          <div class="group">
            <div class="row form"><span class="k">名字</span><span class="v dns-sub"><input v-model="ed.short" placeholder="例如 backup、img" aria-label="存储桶名字" spellcheck="false" autocapitalize="off"><span class="secondary">-{{ appId || 'APPID' }}</span></span></div>
            <div class="row form"><span class="k">地域</span><span class="v"><select v-model="ed.region" aria-label="地域"><option v-for="r in COS_REGIONS" :key="r[0]" :value="r[0]">{{ r[1] }}（{{ r[0] }}）</option></select>
              <span class="small secondary block">选离服务器近的地域；同地域的腾讯云服务器可以走内网，不收流量费。</span></span></div>
            <div class="row form"><span class="k">访问权限</span><span class="v"><span class="segmented">
              <button :class="{ on: ed.acl === 'private' }" @click="ed.acl = 'private'">私有读写</button>
              <button :class="{ on: ed.acl === 'public-read' }" @click="ed.acl = 'public-read'">公有读私有写</button></span></span></div>
            <div class="row form"><span class="k">生命周期</span><span class="v"><select v-model="ed.template" aria-label="生命周期"><option value="">先不设置</option>
              <option v-for="t in COS_LIFE_TEMPLATES" :key="t.id" :value="t.id">{{ t.text }}</option></select>
              <span class="small secondary block" v-if="ed.template">{{ (COS_LIFE_TEMPLATES.find(t => t.id === ed.template) || {}).note }}</span></span></div>
          </div>
          <div class="hint">完整名字：{{ newName }}。名字在整个腾讯云里不能重复，建好后不能改。</div>
        </template>

        <template v-else-if="ed.kind === 'deleteBucket'">
          <h2>删除存储桶</h2><p>只能删除空的存储桶（先把文件删完）。删除后找不回，名字也可能被别人用掉。</p>
          <div class="group"><div class="row form"><span class="k">输入名字确认</span><span class="v"><input v-model="ed.confirmName" :placeholder="bucket.name" aria-label="输入存储桶名字确认" spellcheck="false" autocapitalize="off"></span></div></div>
        </template>

        <div class="notice" v-if="ed.error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ ed.error }}</div>
        <div class="sheet-actions">
          <button @click="closeEd" :disabled="saving">取消</button>
          <template v-if="ed.kind === 'acl'"><button class="primary" @click="saveACL" :disabled="saving || ed.acl === acl">{{ saving ? '正在保存……' : '保存' }}</button></template>
          <template v-else-if="ed.kind === 'referer'">
            <button v-if="detail.referer.status === 'Enabled'" @click="saveReferer(false)" :disabled="saving">关闭防盗链</button>
            <button class="primary" @click="saveReferer(true)" :disabled="saving">{{ saving ? '正在保存……' : '保存并开启' }}</button></template>
          <template v-else-if="ed.kind === 'cors'"><button class="primary" @click="saveCORS" :disabled="saving">{{ saving ? '正在保存……' : '保存' }}</button></template>
          <template v-else-if="ed.kind === 'lifecycle'"><button class="primary" @click="saveLife" :disabled="saving">{{ saving ? '正在保存……' : '保存' }}</button></template>
          <template v-else-if="ed.kind === 'website'">
            <button v-if="detail.website.enabled" @click="saveWebsite(false)" :disabled="saving">关闭静态网站</button>
            <button class="primary" @click="saveWebsite(true)" :disabled="saving">{{ saving ? '正在保存……' : '保存并开启' }}</button></template>
          <template v-else-if="ed.kind === 'policy'">
            <button class="destructive" v-if="detail.policy" @click="savePolicy(true)" :disabled="saving">删除策略</button>
            <button class="primary" @click="savePolicy(false)" :disabled="saving || !ed.policy.trim()">{{ saving ? '正在保存……' : '保存' }}</button></template>
          <template v-else-if="ed.kind === 'create'"><button class="primary" @click="createBucket" :disabled="saving || !ed.short.trim()">{{ saving ? '正在新建……' : '新建' }}</button></template>
          <template v-else-if="ed.kind === 'deleteBucket'"><button class="primary danger" @click="deleteBucket" :disabled="saving || ed.confirmName.trim() !== bucket.name">{{ saving ? '正在删除……' : '删除' }}</button></template>
        </div>
      </div>
    </div>
  </div>`,
};

const EO_SECTIONS = [{ id: 'overview', text: '概览' }, { id: 'visitors', text: '访客' }, { id: 'content', text: '内容' }];
const EO_SERIES = {
  requests: { text: '请求', key: 'series', label: '次请求', format: fmtCount },
  flux: { text: '流量', key: 'flux', label: '流量', format: fmtBytes },
  bandwidth: { text: '带宽', key: 'bandwidth', label: '带宽', format: fmtBits },
  resp: { text: '响应时间', key: 'resp', label: '平均响应', format: v => Math.round(v) + ' ms' },
};

// EdgeOne 实时: EdgeOne's own analytics (a few minutes behind) for a site
// or one of its domains, in the same sections as 访问分析.
const EoStats = {
  props: { configured: Boolean, active: Boolean },
  emits: ['ask', 'settings'],
  setup(props, { emit }) {
    const sites = ref([]);
    const domain = ref(pref('miao.eoDomain', ''));
    const hours = ref(Number(pref('miao.eoHours', '24')) || 24);
    const section = ref(pref('miao.eoSection', 'overview'));
    const series = ref('requests');
    const data = ref(null);
    const loading = ref(false);    // nothing to show yet
    const refreshing = ref(false); // updating what is shown, quietly
    const error = ref('');
    const updatedAt = ref(0);
    let seq = 0;
    watch(hours, v => setPref('miao.eoHours', String(v)));
    watch(section, v => setPref('miao.eoSection', v));
    watch(domain, v => { if (v) setPref('miao.eoDomain', v); });

    async function loadSites() {
      if (!props.configured) return;
      try {
        sites.value = await api('GET', '/api/eo/sites');
        if (!sites.value.includes(domain.value)) domain.value = sites.value.length ? sites.value[0] : '';
      } catch (e) { error.value = e.message; }
    }
    const url = (d, h, extra) => `/api/eo/analytics?domain=${encodeURIComponent(d)}&hours=${h}${extra || ''}`;
    function remember(d, h, r) {
      const at = Date.parse(r.checkedAt) || Date.now();
      eoMemo.set(d + '|' + h, { data: r, at });
      return at;
    }
    // load shows what is remembered for this view at once (or the copy the
    // server keeps warm), then fetches a fresh one unless it is only
    // seconds old; force skips every cache.
    async function load(force) {
      if (!domain.value) return;
      const d = domain.value, h = hours.value;
      let memo = eoMemo.get(d + '|' + h);
      data.value = memo ? memo.data : null;
      updatedAt.value = memo ? memo.at : 0;
      const mine = ++seq;
      // A request this one replaces never clears its flags: start clean.
      error.value = ''; loading.value = false; refreshing.value = false;
      if (!memo && !force) {
        loading.value = true;
        try {
          const r = await api('GET', url(d, h, '&cached=1'));
          if (mine !== seq) return;
          updatedAt.value = remember(d, h, r);
          data.value = r;
          memo = eoMemo.get(d + '|' + h);
        } catch (e) {
          if (mine === seq) { error.value = e.message; loading.value = false; }
          return;
        }
        loading.value = false;
      }
      if (!force && memo && Date.now() - memo.at < EO_FRESH_MS) { prefetch(); return; }
      (data.value ? refreshing : loading).value = true;
      try {
        const r = await api('GET', url(d, h, force ? '&refresh=1' : ''));
        const at = remember(d, h, r);
        if (mine === seq) { data.value = r; updatedAt.value = at; }
      } catch (e) { if (mine === seq) error.value = e.message; }
      finally { if (mine === seq) { loading.value = false; refreshing.value = false; } }
      prefetch();
    }
    // The other ranges load one after another in the background, so
    // switching between them is instant.
    let prefetching = false;
    async function prefetch() {
      if (prefetching || !domain.value) return;
      prefetching = true;
      const d = domain.value;
      try {
        for (const r of EO_RANGES) {
          const m = eoMemo.get(d + '|' + r.h);
          if (r.h === hours.value || (m && Date.now() - m.at < 5 * 60 * 1000)) continue;
          try { remember(d, r.h, await api('GET', url(d, r.h, '&cached=1'))); } catch { break; }
        }
      } finally { prefetching = false; }
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
    onMounted(async () => { await loadSites(); if (props.active) { load(false); start(); } });
    onUnmounted(stop);

    const rangeText = computed(() => (EO_RANGES.find(r => r.h === hours.value) || {}).text || '');
    const tops = k => (data.value && data.value.tops && data.value.tops[k]) || [];
    // Rankings as rank-list rows: codes read in words (US is 美国, 404 is 找不到).
    function rank(k) {
      return tops(k).map(t => {
        if (k === 'country' || k === 'device') return { value: t.label || t.key, count: t.value, note: t.label && k === 'country' ? t.key : '' };
        if (k === 'referer' && (t.key === '' || t.key === '-')) return { value: '（直接访问）', count: t.value, note: '' };
        return { value: t.key, count: t.value, note: t.label || '' };
      });
    }
    const errors = computed(() => {
      let e4 = 0, e5 = 0;
      for (const t of tops('status')) { const c = Number(t.key); if (c >= 400 && c < 500) e4 += t.value; else if (c >= 500) e5 += t.value; }
      return { e4, e5 };
    });
    const hit = computed(() => data.value && data.value.hitRatio >= 0 ? Math.round(data.value.hitRatio * 1000) / 10 : null);
    // Against the same length of time just before.
    function change(key, prevKey) {
      const d = data.value;
      if (!d || !(d[prevKey] > 0)) return null;
      return Math.round((d[key] - d[prevKey]) / d[prevKey] * 100);
    }
    const pct = (a, b) => b > 0 ? (a / b * 100).toFixed(1) + '%' : '—';
    const alerts = computed(() => {
      const d = data.value, out = [];
      if (!d || !d.requests) return out;
      const { e5 } = errors.value;
      if (d.requests > 100 && e5 / d.requests > 0.01) out.push({ level: 'warn', text: `源站出错（5xx）${fmtCount(e5)} 次，占请求的 ${pct(e5, d.requests)}，看看源站是不是有问题`, go: 'content' });
      const up = change('requests', 'prevRequests');
      if (up !== null && up >= 200 && d.requests > 1000) out.push({ level: 'warn', text: `请求数比前 ${rangeText.value}多了 ${up}%，看看是不是被刷或者被攻击`, go: 'visitors' });
      if (d.avgRespMs > 1500) out.push({ level: 'warn', text: `平均响应 ${d.avgRespMs} ms，访问偏慢` });
      if (hit.value !== null && hit.value < 30 && d.bytes > 100 * 1024 * 1024) out.push({ level: 'info', text: `缓存命中率只有 ${hit.value}%：网页等动态内容不缓存是正常的；如果流量主要是图片、脚本，可以检查 EdgeOne 的缓存规则` });
      return out;
    });
    const chart = computed(() => {
      const d = data.value, s = EO_SERIES[series.value];
      if (!d) return { points: [], more: [] };
      return {
        points: d[s.key] || [], label: s.label, format: s.format,
        more: Object.entries(EO_SERIES).filter(([k]) => k !== series.value).map(([, o]) => ({ label: o.label, points: d[o.key] || [], format: o.format })),
      };
    });
    const rows = computed(() => {
      const d = data.value;
      if (!d || !d.series) return [];
      return d.series.map((p, i) => ({ t: p.t, requests: p.v, flux: d.flux && d.flux[i] ? d.flux[i].v : null,
        bandwidth: d.bandwidth && d.bandwidth[i] ? d.bandwidth[i].v : null, resp: d.resp && d.resp[i] ? d.resp[i].v : null })).reverse();
    });
    const pickDomain = v => { if (sites.value.includes(v)) domain.value = v; else notify(v + ' 不在站点列表里', 'ok'); };
    function ask() {
      emit('ask', `分析一下 ${domain.value} 最近${rangeText.value}的 EdgeOne 访问情况：请求和流量有没有异常变化，缓存命中率和响应时间怎么样，出错多不多，主要是谁在访问、访问了什么，有没有需要处理的问题？`);
    }
    const askIP = ip => emit('ask', `IP ${ip} 最近${rangeText.value}访问 ${domain.value} 很多，帮我看看它在做什么，是正常访问还是爬虫、攻击，要不要封禁？`);
    const timeText = t => new Date(t * 1000).toLocaleString('zh-CN', { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
    // Clearing or warming the cache of the site shown.
    const cacheOpen = ref(false), cachePlan = ref(null);
    const onCachePlan = p => { cacheOpen.value = false; cachePlan.value = p; };
    return { sites, domain, hours, section, series, data, loading, refreshing, error, updatedAt, every, load, ask, askIP, hit, errors, change, pct,
      alerts, chart, rows, rank, tops, pickDomain, rangeText, EO_RANGES, EO_SECTIONS, EO_SERIES, fmtCount, fmtBytes, fmtBits, timeText, clockText,
      cacheOpen, cachePlan, onCachePlan };
  },
  template: `
  <div class="vs">
    <div class="group" v-if="!configured">
      <div class="row"><ui-icon name="info" class="lg" style="color: var(--accent)"></ui-icon><div class="grow">EdgeOne 实时统计来自腾讯云，需要先填写腾讯云密钥。</div><button @click="$emit('settings')">去设置</button></div>
    </div>
    <template v-else>
      <div class="stat-bar">
        <label class="field"><span>站点</span>
          <select v-model="domain" aria-label="站点或域名" :disabled="!sites.length"><option v-for="s in sites" :key="s" :value="s">{{ s }}</option></select></label>
        <span class="segmented" role="tablist" aria-label="时间">
          <button v-for="r in EO_RANGES" :key="r.h" role="tab" :aria-selected="hours === r.h" :class="{ on: hours === r.h }" @click="hours = r.h">{{ r.text }}</button>
        </span>
        <span class="grow"></span>
        <span class="small tertiary live-note" v-if="updatedAt">
          <span class="spinner inline" v-if="refreshing"></span>更新于 {{ clockText(updatedAt) }} · {{ every === 30000 ? '每 30 秒' : '每分钟' }}自动更新
        </span>
        <button class="plain" @click="load(true)" :disabled="loading || refreshing || !domain" title="重新读取"><ui-icon name="refresh"></ui-icon>刷新</button>
        <button @click="cacheOpen = true" :disabled="!domain"><ui-icon name="bolt"></ui-icon>清除 / 预热缓存</button>
        <button class="primary" @click="ask" :disabled="!domain"><ui-icon name="sparkles"></ui-icon>让 AI 分析</button>
      </div>
      <p class="source-note small tertiary">腾讯云 EdgeOne 自己的统计，有几分钟延迟{{ hours === 1 ? '（最近 1 小时按分钟统计，最右边几分钟可能还在补齐）' : '' }}。访客地区、IP 行为和风险在「访问分析」里。</p>

      <nav class="subtabs" role="tablist">
        <button v-for="s in EO_SECTIONS" :key="s.id" role="tab" :aria-selected="section === s.id" :class="{ on: section === s.id }" @click="section = s.id">{{ s.text }}</button>
      </nav>

      <div class="notice" v-if="error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ error }}</div>
      <div class="group" v-if="!sites.length && !error && !loading"><div class="row secondary">EdgeOne 里还没有站点。</div></div>
      <div class="notice" v-if="!data && loading"><span class="spinner"></span>正在读取 EdgeOne 数据……</div>

      <template v-if="data">
        <template v-if="section === 'overview'">
          <div class="alerts" v-if="alerts.length">
            <div class="alert" v-for="(a, i) in alerts" :key="i" :class="'al-' + a.level">
              <ui-icon :name="a.level === 'warn' ? 'warn' : 'info'"></ui-icon><span class="grow">{{ a.text }}</span>
              <button class="link small" v-if="a.go" @click="section = a.go">查看</button>
            </div>
          </div>
          <div class="kpi-group">
            <div class="kpi-title">访问量</div>
            <div class="tiles three">
              <div class="tile"><div class="label">请求数</div><div class="value">{{ fmtCount(data.requests) }}</div>
                <div class="sub" v-if="change('requests', 'prevRequests') !== null" :title="'和再往前 ' + rangeText + '相比'">
                  <span :class="change('requests', 'prevRequests') >= 0 ? 'up' : 'down'">{{ change('requests', 'prevRequests') >= 0 ? '↑' : '↓' }} {{ Math.abs(change('requests', 'prevRequests')) }}%</span> 较前 {{ rangeText }}</div>
                <div class="sub" v-else>{{ data.domain }}</div></div>
              <div class="tile"><div class="label">流量</div><div class="value">{{ fmtBytes(data.bytes) }}</div>
                <div class="sub" v-if="change('bytes', 'prevBytes') !== null">
                  <span :class="change('bytes', 'prevBytes') >= 0 ? 'up' : 'down'">{{ change('bytes', 'prevBytes') >= 0 ? '↑' : '↓' }} {{ Math.abs(change('bytes', 'prevBytes')) }}%</span> 较前 {{ rangeText }}</div>
                <div class="sub" v-else>EdgeOne 发给访客</div></div>
              <div class="tile"><div class="label">峰值带宽</div><div class="value">{{ fmtBits(data.peakBps) }}</div><div class="sub">这段时间最高</div></div>
            </div>
          </div>
          <div class="kpi-group">
            <div class="kpi-title">质量</div>
            <div class="tiles three">
              <div class="tile"><div class="label">缓存命中率</div><div class="value">{{ hit === null ? '—' : hit + '%' }}</div>
                <div class="meter" v-if="hit !== null" role="meter" :aria-valuenow="hit" aria-valuemin="0" aria-valuemax="100" aria-label="缓存命中率"><div :style="{ width: hit + '%' }"></div></div>
                <div class="sub">按流量，越高源站越轻松</div></div>
              <div class="tile"><div class="label">平均响应</div><div class="value">{{ data.avgRespMs }} ms</div><div class="sub">EdgeOne 回应访客用的时间</div></div>
              <div class="tile"><div class="label">错误率</div><div class="value">{{ pct(errors.e4 + errors.e5, data.requests) }}</div><div class="sub">4xx {{ fmtCount(errors.e4) }} · 5xx {{ fmtCount(errors.e5) }}</div></div>
            </div>
          </div>

          <section class="card">
            <header class="card-head">
              <h3>走势</h3>
              <span class="segmented small">
                <button v-for="(o, k) in EO_SERIES" :key="k" :class="{ on: series === k }" @click="series = k">{{ o.text }}</button>
              </span>
            </header>
            <line-chart v-if="chart.points.length" :points="chart.points" :more="chart.more" :label="chart.label" :format="chart.format" :span="hours" partial></line-chart>
            <div class="rank-empty" v-else>这段时间没有访问数据</div>
            <details class="card-foot" v-if="rows.length">
              <summary><ui-icon name="chevron"></ui-icon>明细</summary>
              <div class="table-wrap">
                <table class="table visit-table">
                  <thead><tr><th>时间</th><th class="num">请求</th><th class="num">流量</th><th class="num">带宽</th><th class="num">平均响应</th></tr></thead>
                  <tbody><tr v-for="r in rows" :key="r.t"><td>{{ timeText(r.t) }}</td><td class="num">{{ fmtCount(r.requests) }}</td>
                    <td class="num">{{ r.flux === null ? '—' : fmtBytes(r.flux) }}</td><td class="num">{{ r.bandwidth === null ? '—' : fmtBits(r.bandwidth) }}</td>
                    <td class="num">{{ r.resp === null ? '—' : Math.round(r.resp) + ' ms' }}</td></tr></tbody>
                </table>
              </div>
            </details>
          </section>

          <div class="card-grid three">
            <section class="card"><header class="card-head"><h3>热门路径</h3><button class="link small" @click="section = 'content'">全部</button></header>
              <rank-list :items="rank('url')" :total="data.requests" :limit="5" :expandable="false"></rank-list></section>
            <section class="card"><header class="card-head"><h3>国家/地区</h3><button class="link small" @click="section = 'visitors'">全部</button></header>
              <rank-list :items="rank('country')" :total="data.requests" :limit="5" :expandable="false"></rank-list></section>
            <section class="card"><header class="card-head"><h3>状态码</h3><button class="link small" @click="section = 'content'">全部</button></header>
              <rank-list :items="rank('status')" :total="data.requests" :limit="5" :expandable="false"></rank-list></section>
          </div>
          <section class="card" v-if="tops('domain').length > 1">
            <header class="card-head"><h3>各域名</h3><span class="small tertiary">按请求数，点一行只看这个域名</span></header>
            <rank-list :items="rank('domain')" :total="data.requests" clickable @pick="pickDomain"></rank-list>
          </section>
        </template>

        <div class="card-grid" v-if="section === 'visitors'">
          <section class="card"><header class="card-head"><h3>国家/地区</h3><span class="small tertiary">按请求数</span></header>
            <rank-list :items="rank('country')" :total="data.requests"></rank-list></section>
          <section class="card"><header class="card-head"><h3>访问最多的 IP</h3><span class="small tertiary">点一行让 AI 看看它在做什么</span></header>
            <rank-list :items="rank('ip')" :total="data.requests" clickable @pick="askIP"></rank-list></section>
          <section class="card"><header class="card-head"><h3>来源</h3><span class="small tertiary">Referer，按请求数</span></header>
            <rank-list :items="rank('referer')" :total="data.requests"></rank-list></section>
          <section class="card"><header class="card-head"><h3>设备</h3></header>
            <rank-list :items="rank('device')" :total="data.requests"></rank-list></section>
          <section class="card"><header class="card-head"><h3>浏览器</h3></header>
            <rank-list :items="rank('browser')" :total="data.requests"></rank-list></section>
        </div>

        <div class="card-grid" v-if="section === 'content'">
          <section class="card"><header class="card-head"><h3>热门路径</h3><span class="small tertiary">按请求数，含图片和脚本</span></header>
            <rank-list :items="rank('url')" :total="data.requests"></rank-list></section>
          <section class="card"><header class="card-head"><h3>状态码</h3><span class="small tertiary">4xx 多半是扫描或死链，5xx 是源站出错</span></header>
            <rank-list :items="rank('status')" :total="data.requests"></rank-list></section>
          <section class="card" v-if="tops('domain').length"><header class="card-head"><h3>各域名</h3><span class="small tertiary">点一行只看这个域名</span></header>
            <rank-list :items="rank('domain')" :total="data.requests" clickable @pick="pickDomain"></rank-list></section>
        </div>
      </template>
    </template>

    <div class="sheet-mask" v-if="cacheOpen" @click.self="cacheOpen = false">
      <div class="sheet" role="dialog" aria-label="清除或预热缓存">
        <h2>{{ domain }} 的缓存</h2>
        <p>会先生成清单，确认后才提交给 EdgeOne。</p>
        <eo-cache-form :domains="[domain]" @plan="onCachePlan"></eo-cache-form>
        <div class="sheet-actions"><button @click="cacheOpen = false">关闭</button></div>
      </div>
    </div>
    <div class="sheet-mask" v-if="cachePlan" @click.self="cachePlan = null">
      <div class="sheet plan-sheet" role="dialog" aria-label="确认清单">
        <h2>{{ cachePlan.title }}</h2>
        <p>勾选后点「执行」，确认后才会提交。</p>
        <plan-card :plan="cachePlan" server-name="腾讯云"></plan-card>
        <div class="sheet-actions"><button @click="cachePlan = null">关闭</button></div>
      </div>
    </div>
  </div>`,
};

// 通知: the daily report and alerts, and where they are pushed.
const NoticePage = {
  props: { active: Boolean },
  emits: ['unread', 'ask'],
  setup(props, { emit }) {
    const data = ref(null);
    const error = ref('');
    const busy = ref('');
    const form = reactive({ daily: true, dailyAt: '09:00', alertRisk: true, alertLeak: true, alertCert: true, alertBlock: true });
    const hook = reactive({ url: '', secret: '', editing: false });
    // The form follows what is saved, unless it has changes not saved yet
    // (coming back to the page reloads the list, not your edits).
    let saved = '';
    const formNow = () => JSON.stringify(form);
    function take(v, force) {
      data.value = v;
      if (force || !saved || formNow() === saved) {
        Object.assign(form, { daily: v.settings.daily, dailyAt: v.settings.dailyAt, alertRisk: v.settings.alertRisk, alertLeak: v.settings.alertLeak,
          alertCert: v.settings.alertCert, alertBlock: v.settings.alertBlock });
        saved = formNow();
      }
      emit('unread', v.unread);
    }
    async function load() {
      try { take(await api('GET', '/api/notices')); error.value = ''; } catch (e) { error.value = e.message; }
    }
    // Marked read after a moment on the page, if still on it.
    async function markRead() {
      if (!props.active || !data.value || !data.value.unread) return;
      try {
        await api('POST', '/api/notices/read');
        emit('unread', 0);
        data.value.unread = 0;
        for (const n of data.value.notices || []) n.read = true;
      } catch { /* next time */ }
    }
    onMounted(async () => { await load(); if (props.active) setTimeout(markRead, 1500); });
    watch(() => props.active, async on => { if (on) { await load(); setTimeout(markRead, 1500); } });
    async function run(label, fn) {
      busy.value = label;
      try { await fn(); } catch (e) { notify(e.message, 'error'); } finally { busy.value = ''; }
    }
    const saveSettings = () => run('settings', async () => { take(await api('PUT', '/api/notices/settings', { ...form }), true); notify('已保存', 'ok'); });
    const saveHook = () => run('hook', async () => {
      take(await api('PUT', '/api/notices/webhook', { url: hook.url, secret: hook.secret }));
      hook.url = ''; hook.secret = ''; hook.editing = false;
      notify('推送地址已保存，可以点「发送测试」试一下', 'ok');
    });
    const clearHook = () => run('hook', async () => {
      if (!confirm('不再推送到这个地址？日报和提醒仍会保存在这一页。')) return;
      take(await api('PUT', '/api/notices/webhook', { url: '', secret: '' }));
    });
    const testHook = () => run('test', async () => { await api('POST', '/api/notices/test'); notify('测试消息已发送，去群里或手机上看看', 'ok'); });
    const reportNow = () => run('report', async () => { take(await api('POST', '/api/notices/report')); notify('日报已生成', 'ok'); });
    const needsSecret = computed(() => /\/robot\/send|\/open-apis\/bot\//.test(hook.url));
    const kindText = k => ({ report: '日报', alert: '提醒', test: '测试' }[k] || k);
    return { data, error, busy, form, hook, needsSecret, saveSettings, saveHook, clearHook, testHook, reportNow, kindText, whenText, load };
  },
  template: `
  <div>
    <div class="page-head"><p>每天的访问日报，以及需要你看一眼的提醒：新的高风险 IP、敏感文件被下载、证书快到期、自动封禁做了什么。都保存在这里，也可以推送到企业微信、钉钉、飞书群或者微信（Server酱）。</p></div>
    <div class="notice" v-if="error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ error }}</div>
    <template v-if="data">
      <div class="group-title">推送到</div>
      <div class="group notice-settings">
        <div class="row" v-if="data.settings.webhook && !hook.editing">
          <div class="grow"><div>{{ data.settings.webhookKind }}</div><div class="small tertiary mono-ish">{{ data.settings.webhook }}<span v-if="data.settings.hasSecret"> · 已设置加签密钥</span></div></div>
          <button @click="testHook" :disabled="!!busy"><span class="spinner inline" v-if="busy === 'test'"></span>发送测试</button>
          <button class="plain" @click="hook.editing = true">更换</button>
          <button class="plain destructive" @click="clearHook" :disabled="!!busy">不推送</button>
        </div>
        <template v-else>
          <div class="row form"><span class="k">推送地址</span><span class="v">
            <input v-model="hook.url" placeholder="粘贴群机器人的 Webhook 地址，或 Server酱的 SendKey 地址" autocomplete="off" spellcheck="false" aria-label="推送地址"></span></div>
          <div class="row form" v-if="needsSecret"><span class="k">加签密钥</span><span class="v">
            <input v-model="hook.secret" type="password" placeholder="机器人开了「加签」才填（SEC 开头）" autocomplete="off" aria-label="加签密钥"></span></div>
          <div class="row"><div class="grow small tertiary">
              企业微信：群设置 → 群机器人 → 添加，复制 Webhook 地址。钉钉：群设置 → 机器人 → 自定义，安全设置选「加签」。飞书：群设置 → 群机器人 → 自定义机器人。微信：在 Server酱官网拿到 SendKey，地址是 https://sctapi.ftqq.com/SendKey.send。</div>
            <button class="plain" v-if="hook.editing" @click="hook.editing = false">取消</button>
            <button class="primary" @click="saveHook" :disabled="!hook.url.trim() || !!busy">保存</button></div>
        </template>
      </div>

      <div class="group-title">发送什么</div>
      <div class="group notice-settings">
        <div class="row"><label class="check grow"><input type="checkbox" v-model="form.daily"> 每日日报：前一天的 PV、UV、IP 和变化，热门页面、地区，安全和证书情况</label>
          <input type="time" v-model="form.dailyAt" class="time-input" aria-label="日报时间" :disabled="!form.daily"></div>
        <div class="row"><label class="check"><input type="checkbox" v-model="form.alertRisk"> 发现新的高风险 IP（还没有封禁的）</label></div>
        <div class="row"><label class="check"><input type="checkbox" v-model="form.alertLeak"> 敏感文件（如 .env、数据库备份）被下载</label></div>
        <div class="row"><label class="check"><input type="checkbox" v-model="form.alertCert"> 证书快到期、已过期或申请失败</label></div>
        <div class="row"><label class="check"><input type="checkbox" v-model="form.alertBlock"> 自动封禁和解封了 IP</label></div>
        <div class="row"><div class="grow small tertiary">提醒随统计每 20 分钟检查一次，同一个问题一周内只提醒一次（高风险 IP 一天）。日报在设定时间后的第一次检查时生成；Miao Panel 没开着就等下次打开。</div>
          <button @click="reportNow" :disabled="!!busy"><span class="spinner inline" v-if="busy === 'report'"></span>现在生成一份日报</button>
          <button class="primary" @click="saveSettings" :disabled="!!busy">保存</button></div>
      </div>

      <div class="group-title">消息</div>
      <div class="group" v-if="!data.notices.length"><div class="row secondary">还没有消息。</div></div>
      <article class="notice-item" v-for="n in data.notices" :key="n.id" :class="{ unread: !n.read }">
        <header><span class="tag" :class="n.kind === 'alert' ? 'warn' : 'on'">{{ kindText(n.kind) }}</span><b>{{ n.title }}</b>
          <span class="grow"></span><span class="small tertiary">{{ whenText(n.at) }}</span></header>
        <div class="notice-text small">{{ n.text.replace(/\\*\\*/g, '') }}</div>
        <div class="small" v-if="n.push" :class="n.push === 'ok' ? 'tertiary' : 'st-crit'">{{ n.push === 'ok' ? '已推送' : '推送失败：' + n.push }}</div>
      </article>
    </template>
  </div>`,
};

// 文件: a server's files over SFTP, like a file manager on the desktop.
const FILE_PLACES = {
  common: [
    { path: '', text: '主目录' }, { path: '/', text: '根目录 /' }, { path: '/etc', text: '/etc（系统配置）' },
    { path: '/var/log', text: '/var/log（系统日志）' }, { path: '/tmp', text: '/tmp（临时文件）' },
  ],
  '1panel': [{ path: '/opt/1panel/www/sites', text: '1Panel 网站' }, { path: '/opt/1panel/apps', text: '1Panel 应用' }],
  bt: [{ path: '/www/wwwroot', text: '宝塔网站' }, { path: '/www/wwwlogs', text: '宝塔网站日志' }, { path: '/www/server/panel/vhost/nginx', text: '宝塔 Nginx 配置' }],
  linux: [{ path: '/var/www', text: '/var/www（网站）' }, { path: '/etc/nginx', text: 'Nginx 配置' }],
};
// What the server can unpack: IsArchive in files.go.
const ARCHIVE_RE = /\.(zip|tar|tgz|tbz2|txz|tar\.zst|gz|bz2|xz|7z|rar)$/i;
const FILE_EDIT_MAX = 5 << 20;
const FILE_SHOW = 500; // rows drawn at first in a big folder
const PERM_WHO = [{ text: '所有者', shift: 6 }, { text: '用户组', shift: 3 }, { text: '其他人', shift: 0 }];
const PERM_BITS = [{ text: '读', bit: 4 }, { text: '写', bit: 2 }, { text: '执行', bit: 1 }];
const baseName = p => String(p).replace(/\/+$/, '').split('/').pop() || '/';
const joinPath = (dir, name) => (dir === '/' ? '' : dir) + '/' + name;
const dirOf = p => p.replace(/\/[^/]*$/, '') || '/';
// stemOf is the name without its extension: site.tar.gz → site.
const stemOf = n => { const m = String(n).match(/^(.+?)(\.tar\.(gz|bz2|xz|zst)|\.[^.]+)$/); return m ? m[1] : n; };
const namesText = l => l.length <= 3 ? l.join('、') : `${l.slice(0, 3).join('、')} 等 ${l.length} 项`;
const archiveName = (name, format) => format === 'zip'
  ? (/\.zip$/i.test(name) ? name : name + '.zip')
  : (/\.(tar\.gz|tgz)$/i.test(name) ? name : name + '.tar.gz');
function fileTime(t) {
  const d = new Date(t);
  if (isNaN(d)) return '';
  const p = n => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
// indentOf: what the Tab key types, following the file's own indentation.
function indentOf(text) {
  const head = text.slice(0, 20000);
  if (/^\t/m.test(head)) return '\t';
  const m = head.match(/^ +(?=\S)/gm);
  if (!m) return '    ';
  return Math.min(...m.map(s => s.length)) === 2 ? '  ' : '    ';
}
const nameCmp = new Intl.Collator('zh-CN', { numeric: true, sensitivity: 'base' }).compare;

const FilePage = {
  props: { servers: { type: Array, default: () => [] }, active: Boolean, request: Object },
  setup(props) {
    // Servers reached through the Tencent Cloud automation agent have no SSH.
    const usable = computed(() => props.servers.filter(s => s.authKind !== 'tat'));
    const saved = (() => { try { return JSON.parse(localStorage.getItem('miao.files') || '{}') || {}; } catch { return {}; } })();
    const remember = () => { try { localStorage.setItem('miao.files', JSON.stringify(saved)); } catch { /* not kept */ } };
    const sid = ref(0);
    const list = ref(null); // {path, parent, home, user, entries}
    const loading = ref(false), error = ref('');
    const addr = ref(''), editingAddr = ref(false), addrEl = ref(null), pathEl = ref(null);
    const q = ref('');
    const sort = reactive({ key: 'name', desc: false });
    const sel = ref([]); // selected paths
    const limit = ref(FILE_SHOW);
    let anchor = '';
    const clip = reactive({ op: '', serverId: 0, from: '', paths: [] });
    const busy = ref(false);
    let seq = 0;

    const server = computed(() => usable.value.find(s => s.id === sid.value));
    const places = computed(() => [...FILE_PLACES.common, ...(FILE_PLACES[server.value && server.value.adapter] || [])]);
    const url = (tail = '') => `/api/servers/${sid.value}/files${tail}`;

    async function load(p, opts = {}) {
      if (!sid.value) return;
      const n = ++seq;
      loading.value = true;
      try {
        const v = await api('GET', url(`?path=${encodeURIComponent(p || '')}`));
        if (n !== seq) return;
        const same = list.value && list.value.path === v.path;
        list.value = v; error.value = ''; addr.value = v.path; editingAddr.value = false;
        const here = new Set(v.entries.map(e => e.path));
        sel.value = opts.select ? opts.select.filter(x => here.has(x)) : same && opts.keep ? sel.value.filter(x => here.has(x)) : [];
        if (!same) { q.value = ''; limit.value = FILE_SHOW; }
        nextTick(() => { if (pathEl.value) pathEl.value.scrollLeft = pathEl.value.scrollWidth; });
        saved[sid.value] = v.path; remember();
      } catch (e) {
        if (n !== seq) return;
        if (opts.fallback && p) { loading.value = false; return load('', {}); }
        if (list.value) notify(e.message, 'error'); else error.value = e.message;
      } finally { if (n === seq) loading.value = false; }
    }
    const refresh = () => list.value ? load(list.value.path, { keep: true }) : pickServer(sid.value);
    let refreshTimer = null;
    const refreshSoon = () => { clearTimeout(refreshTimer); refreshTimer = setTimeout(refresh, 300); };
    function pickServer(id, path) {
      sid.value = id; list.value = null; error.value = ''; sel.value = [];
      if (!id) return;
      saved.last = id; remember();
      load(path != null ? path : saved[id] || '', { fallback: path == null });
    }
    watch(usable, l => { if (!l.some(s => s.id === sid.value)) pickServer(l.length ? l[0].id : 0); });
    function onRequest(r) {
      if (!r || !usable.value.some(s => s.id === r.serverId)) return;
      if (r.serverId !== sid.value) pickServer(r.serverId, r.path);
      else if (r.path) load(r.path);
    }
    watch(() => props.request, onRequest);
    watch(() => props.active, on => { if (on && list.value && !loading.value) refresh(); });

    // What the table shows: folders first, then by the chosen column.
    const shown = computed(() => {
      if (!list.value) return [];
      const needle = q.value.trim().toLowerCase();
      const out = list.value.entries.filter(e => !needle || e.name.toLowerCase().includes(needle));
      const k = sort.key, d = sort.desc ? -1 : 1;
      return out.sort((a, b) => (b.dir - a.dir)
        || d * (k === 'size' ? a.size - b.size : k === 'modTime' ? byStr(a.modTime, b.modTime) : 0)
        || d * nameCmp(a.name, b.name));
    });
    const visible = computed(() => shown.value.slice(0, limit.value));
    watch(q, () => { const s = new Set(shown.value.map(e => e.path)); sel.value = sel.value.filter(p => s.has(p)); });
    const ariaSort = k => sort.key === k ? (sort.desc ? 'descending' : 'ascending') : null;
    function sortBy(k) {
      if (sort.key === k) sort.desc = !sort.desc; else { sort.key = k; sort.desc = k !== 'name'; }
    }
    const selSet = computed(() => new Set(sel.value));
    const selected = computed(() => list.value ? list.value.entries.filter(e => selSet.value.has(e.path)) : []);
    const one = computed(() => selected.value.length === 1 ? selected.value[0] : null);
    const selSize = computed(() => {
      const files = selected.value.filter(e => !e.dir);
      return files.length ? ' · ' + fmtBytes(files.reduce((s, e) => s + e.size, 0)) : '';
    });
    const allOn = computed(() => shown.value.length > 0 && shown.value.every(e => selSet.value.has(e.path)));
    const toggleAll = () => { sel.value = allOn.value ? [] : shown.value.map(e => e.path); };
    function toggle(e) {
      const s = new Set(sel.value);
      if (s.has(e.path)) s.delete(e.path); else s.add(e.path);
      sel.value = [...s]; anchor = e.path;
    }
    function clickRow(e, ev) {
      if (ev.shiftKey && anchor) {
        const order = shown.value.map(x => x.path), a = order.indexOf(anchor), b = order.indexOf(e.path);
        if (a >= 0 && b >= 0) {
          const range = order.slice(Math.min(a, b), Math.max(a, b) + 1);
          sel.value = ev.ctrlKey || ev.metaKey ? [...new Set([...sel.value, ...range])] : range;
          return;
        }
      }
      if (ev.ctrlKey || ev.metaKey) { toggle(e); return; }
      sel.value = [e.path]; anchor = e.path;
      // A tap on the name opens it on touch screens, where there is no double click.
      if (ev.target.closest('.fname') && window.matchMedia('(pointer: coarse)').matches) openEntry(e);
    }
    const crumbs = computed(() => {
      if (!list.value) return [];
      const parts = list.value.path.split('/').filter(Boolean);
      return [{ path: '/', text: '根目录' }, ...parts.map((x, i) => ({ path: '/' + parts.slice(0, i + 1).join('/'), text: x }))];
    });
    function editAddr() {
      if (!list.value) return;
      addr.value = list.value.path; editingAddr.value = true;
      nextTick(() => { if (addrEl.value) { addrEl.value.focus(); addrEl.value.select(); } });
    }
    function goAddr() {
      const p = addr.value.trim();
      if (!p) { editingAddr.value = false; return; }
      if (!p.startsWith('/')) { notify('路径要以 / 开头，比如 /etc/nginx', 'error'); return; }
      load(p);
    }
    function goPlace(ev) { const p = ev.target.value; ev.target.value = '-'; load(p); }
    const up = () => { if (list.value && list.value.parent) load(list.value.parent, { select: [list.value.path] }); };
    const isArchive = e => !e.dir && ARCHIVE_RE.test(e.name);
    const iconOf = e => e.dir ? 'folder' : isArchive(e) ? 'archive' : e.link ? 'link' : 'file';
    const ownerText = e => e.group && e.group !== e.owner ? `${e.owner}:${e.group}` : e.owner;
    const isCut = e => clip.op === 'move' && clip.serverId === sid.value && clip.paths.includes(e.path);

    function openEntry(e) {
      if (e.dir) { load(e.path); return; }
      if (!'-l'.includes(e.mode[0])) { notify(`${e.name} 不是普通文件，不能打开`, 'error'); return; }
      if (isArchive(e)) { askExtract(e); return; }
      if (e.size > FILE_EDIT_MAX) {
        if (confirm(`${e.name} 有 ${fmtBytes(e.size)}，太大了，不能在这里编辑。下载到电脑？`)) download(e);
        return;
      }
      openEditor(e.path);
    }
    async function op(body) { return api('POST', url('/op'), body); }

    // ---- Copy, cut, paste, delete ----
    function toClip(kind) {
      if (!selected.value.length) return;
      Object.assign(clip, { op: kind, serverId: sid.value, from: list.value.path, paths: selected.value.map(e => e.path) });
      notify(`已${kind === 'copy' ? '复制' : '剪切'} ${clip.paths.length} 项，打开要放的文件夹后点「粘贴」`);
    }
    const clipText = computed(() => clip.paths.length ? `${clip.op === 'copy' ? '复制' : '剪切'}了 ${namesText(clip.paths.map(baseName))}` : '');
    const clearClip = () => Object.assign(clip, { op: '', serverId: 0, from: '', paths: [] });
    async function paste(conflict = '') {
      if (!clip.paths.length || !list.value || busy.value) return;
      if (clip.serverId !== sid.value) { notify('剪贴板里是另一台服务器的文件，只能粘贴到同一台服务器', 'error'); return; }
      const dir = list.value.path, kind = clip.op;
      if (!conflict && clip.from === dir) {
        if (kind === 'move') { notify('已经在这个文件夹里了'); return; }
        conflict = 'rename'; // pasting a copy next to itself keeps both
      }
      busy.value = true; dlg.busy = true;
      try {
        const r = await op({ op: kind, paths: clip.paths, dir, conflict });
        const select = clip.paths.map(p => joinPath(dir, baseName(p)));
        if (kind === 'move') clearClip();
        dlg.kind = '';
        notify(r.done);
        await load(dir, { select });
      } catch (e) {
        if (e.code === 'conflict' && !conflict) openDlg('paste', { text: e.message });
        else if (dlg.kind) dlg.error = e.message;
        else notify(e.message, 'error');
      } finally { busy.value = false; dlg.busy = false; }
    }
    async function remove() {
      const items = selected.value;
      if (!items.length || busy.value) return;
      const inside = items.some(e => e.dir) ? '文件夹里的所有内容也会一起删除，' : '';
      if (!confirm(`删除 ${namesText(items.map(e => e.name))}？\n${inside}删除后不能恢复。`)) return;
      busy.value = true;
      try {
        const r = await op({ op: 'delete', paths: items.map(e => e.path) });
        notify(r.done);
        clip.paths = clip.paths.filter(p => !items.some(e => e.path === p));
        await load(list.value.path);
      } catch (e) { notify(e.message, 'error'); } finally { busy.value = false; }
    }
    async function download(e) {
      try {
        const r = await api('POST', url('/link'), { path: e.path });
        const a = document.createElement('a');
        a.href = r.url; a.download = '';
        document.body.appendChild(a); a.click(); a.remove();
        notify(e.dir ? `正在把 ${e.name} 打包成 ${e.name}.tar.gz 下载` : `正在下载 ${e.name}`);
      } catch (err) { notify(err.message, 'error'); }
    }
    function copyPath(p) {
      if (navigator.clipboard) navigator.clipboard.writeText(p).then(() => notify('已复制路径：' + p), () => notify(p));
    }

    // ---- Dialogs: new, rename, compress, extract, permissions, name clashes ----
    const dlg = reactive({ kind: '', name: '', text: '', paths: [], files: [], clash: [], format: 'tar.gz', dest: '', here: '', sub: '',
      perm: '', owner: '', doPerm: false, doOwner: false, recursive: false, hasDir: false, busy: false, error: '' });
    const dlgInput = ref(null);
    const DLG = {
      mkdir: { title: '新建文件夹', ok: '新建' }, touch: { title: '新建文件', ok: '新建' }, rename: { title: '重命名', ok: '改名' },
      compress: { title: '压缩', ok: '压缩' }, extract: { title: '解压', ok: '解压' }, perm: { title: '权限和所有者', ok: '修改' },
      paste: { title: '有同名的文件' }, upload: { title: '有同名的文件' },
    };
    function openDlg(kind, extra = {}) {
      Object.assign(dlg, { kind, name: '', text: '', error: '', busy: false }, extra);
      nextTick(() => {
        const el = dlgInput.value;
        if (!el) return;
        el.focus();
        if (kind === 'rename' && !extra.dir) el.setSelectionRange(0, stemOf(el.value).length); else el.select();
      });
    }
    const dlgClose = () => { if (!dlg.busy) dlg.kind = ''; };
    const askNew = kind => { if (list.value) openDlg(kind, { name: kind === 'mkdir' ? '新建文件夹' : '新建文件.txt' }); };
    const askRename = () => { const e = one.value; if (e) openDlg('rename', { paths: [e.path], name: e.name, dir: e.dir }); };
    function askCompress() {
      const items = selected.value;
      if (!items.length) return;
      const folder = baseName(list.value.path);
      const name = items.length === 1 ? (items[0].dir ? items[0].name : stemOf(items[0].name)) : folder === '/' ? 'archive' : folder;
      openDlg('compress', { paths: items.map(e => e.path), text: namesText(items.map(e => e.name)), name, format: dlg.format || 'tar.gz' });
    }
    function askExtract(e) {
      const here = list.value.path;
      openDlg('extract', { paths: [e.path], text: e.name, here, sub: joinPath(here, stemOf(e.name)), dest: here });
    }
    function askPerm() {
      const items = selected.value;
      if (!items.length) return;
      const same = f => items.every(e => f(e) === f(items[0])) ? f(items[0]) : '';
      openDlg('perm', { paths: items.map(e => e.path), text: namesText(items.map(e => e.name)), perm: same(e => e.perm),
        owner: same(e => `${e.owner}:${e.group}`), doPerm: false, doOwner: false, recursive: false, hasDir: items.some(e => e.dir) });
    }
    const permOn = (shift, bit) => ((parseInt(dlg.perm || '0', 8) || 0) >> shift & bit) !== 0;
    function flipPerm(shift, bit) {
      const v = (parseInt(dlg.perm || '0', 8) || 0) ^ (bit << shift);
      dlg.perm = v.toString(8).padStart(3, '0'); dlg.doPerm = true;
    }
    async function dlgSubmit() {
      const d = dlg, dir = list.value.path;
      if (d.busy) return;
      let body, select;
      const name = d.name.trim();
      switch (d.kind) {
        case 'mkdir': case 'touch': body = { op: d.kind, dir, name }; select = [joinPath(dir, name)]; break;
        case 'rename': body = { op: 'rename', paths: d.paths, name }; select = [joinPath(dir, name)]; break;
        case 'compress': body = { op: 'compress', paths: d.paths, name, format: d.format }; select = [joinPath(dir, archiveName(name, d.format))]; break;
        case 'extract': body = { op: 'extract', paths: d.paths, dir: d.dest.trim() }; select = [d.dest.trim()]; break;
        case 'perm': return savePerm();
        default: return;
      }
      if (d.kind !== 'extract' && !name) { d.error = '请填写名称'; return; }
      d.busy = true; d.error = '';
      try {
        const r = await op(body);
        d.busy = false; d.kind = '';
        notify(r.done);
        await load(dir, { select });
        if (body.op === 'touch') openEditor(select[0]);
      } catch (e) { d.error = e.message; } finally { d.busy = false; }
    }
    async function savePerm() {
      const d = dlg;
      if (!d.doPerm && !d.doOwner) { d.error = '勾选要修改的项'; return; }
      if (d.doPerm && !/^[0-7]{3,4}$/.test(d.perm)) { d.error = '权限要写成 755 这样的三位数字'; return; }
      if (d.doOwner && !d.owner.trim()) { d.error = '请填写所有者'; return; }
      d.busy = true; d.error = '';
      const done = [];
      try {
        if (d.doPerm) done.push((await op({ op: 'chmod', paths: d.paths, mode: d.perm, recursive: d.recursive })).done);
        if (d.doOwner) done.push((await op({ op: 'chown', paths: d.paths, owner: d.owner.trim(), recursive: d.recursive })).done);
        d.busy = false; d.kind = '';
        notify(done.join('，'));
      } catch (e) { d.error = (done.length ? done.join('，') + '；' : '') + e.message; } finally {
        d.busy = false;
        if (done.length) load(list.value.path, { keep: true });
      }
    }

    // ---- Uploads: one file at a time, with progress ----
    const uploads = ref([]); // {key, file, name, size, loaded, state: wait|up|done|error|cancelled, error, serverId, dir, overwrite, xhr}
    const fileInput = ref(null);
    const dragging = ref(false);
    let dragDepth = 0, upN = 0, pumping = false;
    const hasFiles = ev => ev.dataTransfer && [...ev.dataTransfer.types].includes('Files');
    function onDragEnter(ev) { if (hasFiles(ev) && list.value) { dragDepth++; dragging.value = true; } }
    function onDragLeave(ev) { if (hasFiles(ev)) { dragDepth = Math.max(0, dragDepth - 1); if (!dragDepth) dragging.value = false; } }
    function onDrop(ev) {
      dragDepth = 0; dragging.value = false;
      if (!hasFiles(ev) || !list.value) return;
      const items = [...(ev.dataTransfer.items || [])];
      if (items.some(it => it.webkitGetAsEntry && (it.webkitGetAsEntry() || {}).isDirectory)) {
        notify('不能直接上传文件夹：先在电脑上压缩成 zip，上传后在这里解压', 'error');
        return;
      }
      startUpload([...ev.dataTransfer.files]);
    }
    function onPicked(ev) { startUpload([...ev.target.files]); ev.target.value = ''; }
    function startUpload(files) {
      if (!files.length || !list.value) return;
      const names = new Set(list.value.entries.map(e => e.name));
      const clash = files.filter(f => names.has(f.name));
      if (clash.length) { openDlg('upload', { files, clash: clash.map(f => f.name), text: namesText(clash.map(f => f.name)) }); return; }
      queueUpload(files, false);
    }
    function uploadChoice(how) {
      const files = how === 'skip' ? dlg.files.filter(f => !dlg.clash.includes(f.name)) : dlg.files;
      dlg.kind = '';
      queueUpload(files, how === 'overwrite');
    }
    function queueUpload(files, overwrite) {
      for (const f of files) {
        uploads.value.push({ key: ++upN, file: f, name: f.name, size: f.size, loaded: 0, state: 'wait', error: '', serverId: sid.value, dir: list.value.path, overwrite, xhr: null });
      }
      pump();
    }
    async function pump() {
      if (pumping) return;
      pumping = true;
      for (let u; (u = uploads.value.find(x => x.state === 'wait'));) {
        await sendOne(u);
        if (list.value && sid.value === u.serverId && list.value.path === u.dir) refreshSoon();
      }
      pumping = false;
    }
    function sendOne(u) {
      return new Promise(resolve => {
        const x = new XMLHttpRequest();
        u.xhr = x; u.state = 'up';
        x.open('POST', `/api/servers/${u.serverId}/files/upload?dir=${encodeURIComponent(u.dir)}${u.overwrite ? '&overwrite=1' : ''}`);
        x.setRequestHeader('X-Miao', '1');
        x.upload.onprogress = e => { if (e.lengthComputable) u.loaded = e.loaded; };
        x.onload = () => {
          if (x.status === 200) { u.state = 'done'; u.loaded = u.size; } else {
            let m = '';
            try { m = JSON.parse(x.responseText).error; } catch { /* not JSON */ }
            u.state = 'error'; u.error = m || `上传失败（${x.status}）`;
          }
          resolve();
        };
        x.onerror = () => { u.state = 'error'; u.error = '上传中断了'; resolve(); };
        x.onabort = () => { u.state = 'cancelled'; resolve(); };
        const fd = new FormData();
        fd.append('file', u.file, u.name);
        x.send(fd);
      });
    }
    function cancelUpload(u) { if (u.state === 'wait') u.state = 'cancelled'; else if (u.state === 'up' && u.xhr) u.xhr.abort(); }
    const clearUploads = () => { uploads.value = uploads.value.filter(u => u.state === 'wait' || u.state === 'up'); };
    const upLeft = computed(() => uploads.value.filter(u => u.state === 'wait' || u.state === 'up').length);
    const upPct = u => u.size ? Math.min(100, Math.round(u.loaded / u.size * 100)) : 100;
    const upState = u => ({
      wait: '等待中', done: '已上传', cancelled: '已取消', error: u.error,
      up: u.loaded >= u.size ? '正在写入服务器……' : `${fmtBytes(u.loaded)} / ${fmtBytes(u.size)}`,
    }[u.state]);

    // ---- Editor ----
    const ed = reactive({ open: false, serverId: 0, path: '', name: '', content: '', orig: '', modTime: '', size: 0, crlf: false, binary: false,
      loading: false, saving: false, error: '', wrap: false, line: 1, col: 1 });
    const edEl = ref(null);
    let indent = '    ';
    async function openEditor(p) {
      Object.assign(ed, { open: true, serverId: sid.value, path: p, name: baseName(p), content: '', orig: '', modTime: '', size: 0, crlf: false,
        binary: false, loading: true, saving: false, error: '', line: 1, col: 1 });
      try {
        const f = await api('GET', url(`/text?path=${encodeURIComponent(p)}`));
        // The textarea turns CRLF into LF; saving puts it back.
        const crlf = f.content.includes('\r\n'), text = crlf ? f.content.replace(/\r\n/g, '\n') : f.content;
        Object.assign(ed, { content: text, orig: text, modTime: f.modTime, size: f.size, crlf, binary: f.binary });
        indent = indentOf(text);
        ed.loading = false;
        nextTick(() => { if (edEl.value) { edEl.value.focus(); edEl.value.setSelectionRange(0, 0); edEl.value.scrollTop = 0; } });
      } catch (e) { ed.error = e.message; } finally { ed.loading = false; }
    }
    const dirty = computed(() => ed.open && !ed.binary && !ed.loading && !ed.error && ed.content !== ed.orig);
    async function save(force = false) {
      if (!dirty.value && !force) return;
      if (ed.saving) return;
      ed.saving = true;
      const sent = ed.content;
      try {
        const f = await api('PUT', `/api/servers/${ed.serverId}/files/text`,
          { path: ed.path, content: ed.crlf ? sent.replace(/\n/g, '\r\n') : sent, expect: ed.modTime, force });
        Object.assign(ed, { orig: sent, modTime: f.modTime, size: f.size });
        notify('已保存 ' + ed.name);
        if (list.value && sid.value === ed.serverId && dirOf(ed.path) === list.value.path) refreshSoon();
      } catch (e) {
        if (e.code === 'conflict' && !force) {
          ed.saving = false;
          if (confirm(e.message + '\n\n仍然保存，用你的内容覆盖？')) await save(true);
          return;
        }
        notify(e.message, 'error');
      } finally { ed.saving = false; }
    }
    function closeEditor() {
      if (dirty.value && !confirm(`${ed.name} 的修改还没保存，确定关闭？`)) return;
      ed.open = false;
    }
    function caret() {
      const el = edEl.value;
      if (!el) return;
      const before = el.value.slice(0, el.selectionStart);
      const nl = before.lastIndexOf('\n');
      ed.line = (before.match(/\n/g) || []).length + 1; ed.col = before.length - nl;
    }
    function onEdKey(ev) {
      if (ev.isComposing || ev.keyCode === 229) return; // typing Chinese: the key belongs to the input method
      const k = ev.key.toLowerCase();
      if ((ev.ctrlKey || ev.metaKey) && k === 's') { ev.preventDefault(); save(); return; }
      if (k === 'escape') { ev.preventDefault(); closeEditor(); return; }
      if (k === 'tab' && !ev.shiftKey && !ev.ctrlKey && !ev.altKey && !ev.metaKey) {
        ev.preventDefault();
        // insertText keeps the browser's undo history.
        if (!document.execCommand || !document.execCommand('insertText', false, indent)) {
          const el = ev.target;
          el.setRangeText(indent, el.selectionStart, el.selectionEnd, 'end');
          ed.content = el.value;
        }
      }
    }

    // ---- Right-click menu ----
    const menu = reactive({ open: false, x: 0, y: 0, blank: false });
    const menuEl = ref(null);
    function onContext(ev, e) {
      if (!list.value) return;
      ev.preventDefault();
      if (e && !selSet.value.has(e.path)) { sel.value = [e.path]; anchor = e.path; }
      if (!e) sel.value = [];
      Object.assign(menu, { open: true, x: ev.clientX, y: ev.clientY, blank: !e });
      nextTick(() => {
        const el = menuEl.value;
        if (!el) return;
        const r = el.getBoundingClientRect();
        menu.x = Math.max(8, Math.min(menu.x, window.innerWidth - r.width - 8));
        menu.y = Math.max(8, Math.min(menu.y, window.innerHeight - r.height - 8));
      });
    }
    function act(fn, ...args) { menu.open = false; fn(...args); }

    // ---- Keyboard ----
    function onKey(ev) {
      if (!props.active || ed.open || dlg.kind || !list.value) return;
      if (ev.key === 'Escape' && menu.open) { menu.open = false; return; }
      const t = ev.target;
      if (t && (['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName) || t.isContentEditable)) return;
      if (document.querySelector('.sheet-mask')) return; // another window is open
      const k = ev.key, mod = ev.ctrlKey || ev.metaKey, lk = k.toLowerCase();
      const textPicked = () => String(window.getSelection() || '') !== '';
      if (mod && lk === 'a') { ev.preventDefault(); sel.value = shown.value.map(e => e.path); }
      else if (mod && lk === 'c' && selected.value.length && !textPicked()) toClip('copy');
      else if (mod && lk === 'x' && selected.value.length) toClip('move');
      else if (mod && lk === 'v' && clip.paths.length) { ev.preventDefault(); paste(); }
      else if (k === 'Delete' && selected.value.length) remove();
      else if (k === 'F2' && one.value) { ev.preventDefault(); askRename(); }
      else if (k === 'F5' || (mod && lk === 'r')) { ev.preventDefault(); refresh(); }
      else if (k === 'Enter' && one.value) openEntry(one.value);
      else if (k === 'Backspace' || (ev.altKey && k === 'ArrowUp')) { ev.preventDefault(); up(); }
      else if (k === 'Escape') sel.value = [];
      else if ((k === 'ArrowDown' || k === 'ArrowUp') && !mod && shown.value.length) {
        ev.preventDefault();
        const order = visible.value.map(e => e.path), cur = order.indexOf(anchor);
        const next = order[Math.max(0, Math.min(order.length - 1, cur < 0 ? 0 : cur + (k === 'ArrowDown' ? 1 : -1)))];
        sel.value = [next]; anchor = next;
        nextTick(() => { const row = document.querySelector('.fm-table tr.on'); if (row) row.scrollIntoView({ block: 'nearest' }); });
      }
    }
    const closeMenu = () => { menu.open = false; };
    const warnLeave = ev => { if (dirty.value || upLeft.value) { ev.preventDefault(); ev.returnValue = ''; } };
    onMounted(() => {
      window.addEventListener('keydown', onKey);
      window.addEventListener('click', closeMenu);
      window.addEventListener('blur', closeMenu);
      window.addEventListener('beforeunload', warnLeave);
      const r = props.request;
      if (r && usable.value.some(s => s.id === r.serverId)) pickServer(r.serverId, r.path);
      else pickServer(usable.value.some(s => s.id === saved.last) ? saved.last : (usable.value[0] || {}).id || 0);
    });
    onUnmounted(() => {
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('click', closeMenu);
      window.removeEventListener('blur', closeMenu);
      window.removeEventListener('beforeunload', warnLeave);
    });

    return {
      usable, sid, server, list, loading, error, addr, editingAddr, addrEl, pathEl, q, sort, sel, selSet, selected, one, selSize, allOn, limit,
      shown, visible, places, crumbs, clip, clipText, busy, dlg, DLG, dlgInput, uploads, fileInput, dragging, ed, edEl, dirty, menu, menuEl,
      PERM_WHO, PERM_BITS, FILE_SHOW, fmtBytes, baseName, archiveName, ariaSort, fileTime, iconOf, ownerText, isCut, isArchive, upPct, upState, upLeft,
      pickServer, load, refresh, up, sortBy, toggle, toggleAll, clickRow, editAddr, goAddr, goPlace, openEntry, toClip, clearClip, paste, remove,
      download, copyPath, openDlg, dlgClose, askNew, askRename, askCompress, askExtract, askPerm, permOn, flipPerm, dlgSubmit, uploadChoice,
      onDragEnter, onDragLeave, onDrop, onPicked, cancelUpload, clearUploads, save, closeEditor, caret, onEdKey, onContext, act,
    };
  },
  template: `
  <div class="fm" @dragenter="onDragEnter" @dragleave="onDragLeave" @dragover.prevent @drop.prevent="onDrop">
    <div class="term-empty" v-if="!usable.length">
      <ui-icon name="folder" class="lg"></ui-icon>
      <p v-if="servers.length">文件管理要用 SSH 连接。现在的服务器都是用腾讯云自动化助手连接的，可以改用 SSH（密码或密钥）重新添加。</p>
      <p v-else>先在左边添加服务器。</p>
    </div>
    <template v-else>
    <div class="fm-bar">
      <select v-if="usable.length > 1" class="fm-srv" :value="sid" @change="pickServer(+$event.target.value)" aria-label="服务器">
        <option v-for="s in usable" :key="s.id" :value="s.id">{{ s.name }}</option>
      </select>
      <span class="fm-server" v-else-if="server"><ui-icon name="server"></ui-icon>{{ server.name }}</span>
      <button class="plain icon-only fm-upbtn" title="上一级（Backspace）" aria-label="上一级" :disabled="!list || !list.parent" @click="up"><ui-icon name="up"></ui-icon></button>
      <div class="fm-path" ref="pathEl" :class="{editing: editingAddr}" @click.self="editAddr">
        <input v-if="editingAddr" ref="addrEl" v-model="addr" @keydown.enter="!$event.isComposing && goAddr()" @keydown.esc="!$event.isComposing && (editingAddr = false)" @blur="editingAddr = false"
          aria-label="路径" spellcheck="false" autocomplete="off" autocapitalize="off" placeholder="输入路径，比如 /etc/nginx">
        <template v-else>
          <template v-for="(c, i) in crumbs" :key="c.path"><span class="sep" v-if="i"><ui-icon name="chevron"></ui-icon></span><button class="crumb" :class="{last: i === crumbs.length - 1}" :title="c.path" @click="load(c.path)">{{ c.text }}</button></template>
        </template>
      </div>
      <button class="plain icon-only fm-editbtn" title="输入路径" aria-label="输入路径" @click="editAddr" :disabled="!list"><ui-icon name="pencil"></ui-icon></button>
      <select class="fm-places" @change="goPlace" aria-label="常用位置"><option value="-" selected disabled>常用位置</option><option v-for="p in places" :key="p.path" :value="p.path">{{ p.text }}</option></select>
      <input class="fm-search" type="search" v-model="q" placeholder="筛选当前文件夹" aria-label="筛选当前文件夹" autocomplete="off" spellcheck="false" @keydown.esc="q = ''">
      <button class="plain icon-only fm-refresh" title="刷新（F5）" aria-label="刷新" @click="refresh" :disabled="loading"><ui-icon name="refresh"></ui-icon></button>
      <span class="fm-break"></span>
    </div>
    <div class="fm-actions">
      <button @click="fileInput.click()" :disabled="!list" title="上传文件（也可以把文件拖进来）"><ui-icon name="upload"></ui-icon><span class="lbl">上传</span></button>
      <input type="file" multiple ref="fileInput" class="sr-only" tabindex="-1" aria-hidden="true" @change="onPicked">
      <button @click="askNew('mkdir')" :disabled="!list" title="新建文件夹"><ui-icon name="folder"></ui-icon><span class="lbl">新建文件夹</span></button>
      <button @click="askNew('touch')" :disabled="!list" title="新建文件"><ui-icon name="file"></ui-icon><span class="lbl">新建文件</span></button>
      <span class="fm-divider"></span>
      <!-- Always shown, only enabled or not, so selecting never moves the list. -->
      <button class="plain" @click="toClip('copy')" :disabled="!selected.length" title="复制（Ctrl+C）"><ui-icon name="copy"></ui-icon><span class="lbl">复制</span></button>
      <button class="plain" @click="toClip('move')" :disabled="!selected.length" title="剪切（Ctrl+X）"><ui-icon name="cut"></ui-icon><span class="lbl">剪切</span></button>
      <button class="plain" @click="paste()" :disabled="!clip.paths.length || busy || !list" :title="clip.paths.length ? clipText + '，粘贴到这里（Ctrl+V）' : '先复制或剪切文件'"><ui-icon name="paste"></ui-icon><span class="lbl">粘贴</span><span class="fm-count" v-if="clip.paths.length">{{ clip.paths.length }}</span></button>
      <button class="plain" @click="askRename" :disabled="!one" title="重命名（F2）"><ui-icon name="pencil"></ui-icon><span class="lbl">重命名</span></button>
      <button class="plain" @click="download(one)" :disabled="!one" :title="one && one.dir ? '文件夹会打包成 .tar.gz 下载' : '下载到电脑'"><ui-icon name="download"></ui-icon><span class="lbl">下载</span></button>
      <button class="plain" @click="askCompress" :disabled="!selected.length" title="压缩成 tar.gz 或 zip"><ui-icon name="archive"></ui-icon><span class="lbl">压缩</span></button>
      <button class="plain" @click="askExtract(one)" :disabled="!one || !isArchive(one)" title="解压压缩包"><ui-icon name="archive"></ui-icon><span class="lbl">解压</span></button>
      <button class="plain" @click="askPerm" :disabled="!selected.length" title="权限和所有者"><ui-icon name="lock"></ui-icon><span class="lbl">权限</span></button>
      <button class="plain destructive" @click="remove" :disabled="!selected.length || busy" title="删除（Delete）"><ui-icon name="trash"></ui-icon><span class="lbl">删除</span></button>
    </div>
    <div class="fm-listwrap">
      <div class="fm-list" :class="{loading}" @contextmenu.self="onContext($event, null)">
        <div class="fm-msg" v-if="!list && error">
          <ui-icon name="alert" class="lg st-crit"></ui-icon><p>{{ error }}</p><button @click="refresh"><ui-icon name="refresh"></ui-icon>重试</button>
        </div>
        <div class="fm-msg tertiary" v-else-if="!list">正在连接服务器……</div>
        <template v-else>
        <table class="fm-table" @contextmenu.self="onContext($event, null)">
          <thead><tr>
            <th class="ck"><input type="checkbox" :checked="allOn" :indeterminate="sel.length > 0 && !allOn" @change="toggleAll" aria-label="全选"></th>
            <th :aria-sort="ariaSort('name')"><button class="th-sort" :class="{on: sort.key === 'name', desc: sort.key === 'name' && sort.desc}" @click="sortBy('name')">名称<ui-icon name="arrow-up"></ui-icon></button></th>
            <th class="num" :aria-sort="ariaSort('size')"><button class="th-sort" :class="{on: sort.key === 'size', desc: sort.key === 'size' && sort.desc}" @click="sortBy('size')">大小<ui-icon name="arrow-up"></ui-icon></button></th>
            <th class="hide-sm" :aria-sort="ariaSort('modTime')"><button class="th-sort" :class="{on: sort.key === 'modTime', desc: sort.key === 'modTime' && sort.desc}" @click="sortBy('modTime')">修改时间<ui-icon name="arrow-up"></ui-icon></button></th>
            <th class="hide-sm">权限</th>
            <th class="hide-sm">所有者</th>
          </tr></thead>
          <tbody>
            <tr v-for="e in visible" :key="e.path" :class="{on: selSet.has(e.path), cut: isCut(e)}" :aria-selected="selSet.has(e.path)"
              @click="clickRow(e, $event)" @dblclick="openEntry(e)" @contextmenu="onContext($event, e)">
              <td class="ck" @click.stop @dblclick.stop><input type="checkbox" :checked="selSet.has(e.path)" @change="toggle(e)" :aria-label="'选择 ' + e.name"></td>
              <td class="name"><div class="fm-name"><ui-icon :name="iconOf(e)" :class="{dir: e.dir}"></ui-icon><span class="fname" :title="e.name">{{ e.name }}</span><span class="lnk" v-if="e.link" :title="e.link">→ {{ e.link }}</span></div></td>
              <td class="num">{{ e.dir ? '' : fmtBytes(e.size) }}</td>
              <td class="hide-sm">{{ fileTime(e.modTime) }}</td>
              <td class="hide-sm mono" :title="e.mode">{{ e.perm }}</td>
              <td class="hide-sm">{{ ownerText(e) }}</td>
            </tr>
          </tbody>
        </table>
        <div class="fm-more" v-if="shown.length > visible.length"><button @click="limit = shown.length">还有 {{ shown.length - visible.length }} 项，全部显示</button></div>
        <div class="fm-msg tertiary" v-if="!shown.length" @contextmenu="onContext($event, null)">
          <template v-if="q">没有名字里带「{{ q }}」的文件</template>
          <template v-else>这个文件夹是空的<br><span class="small">可以把电脑上的文件拖到这里上传</span></template>
        </div>
        </template>
      </div>
      <div class="fm-drop" v-if="dragging && list"><ui-icon name="upload" class="lg"></ui-icon>松开鼠标，上传到 {{ list.path }}</div>
    </div>
    <div class="fm-foot small tertiary">
      <span v-if="list">{{ list.entries.length }} 项<template v-if="selected.length"> · 已选 {{ selected.length }} 项{{ selSize }}</template></span>
      <span v-if="clip.paths.length" class="fm-clip">· 剪贴板：{{ clipText }}<button class="link" @click="clearClip">清空</button></span>
      <span class="grow"></span>
      <span v-if="list">以 {{ list.user }} 身份登录 · 改动会记在「记录」里</span>
    </div>

    <div class="fm-uploads" v-if="uploads.length" role="status">
      <div class="fm-up-head"><b>上传</b><span class="small tertiary">{{ upLeft ? '还剩 ' + upLeft + ' 个' : '已完成' }}</span><span class="grow"></span>
        <button class="plain" v-if="uploads.length > upLeft" @click="clearUploads">清除已完成</button></div>
      <div class="fm-up" v-for="u in uploads" :key="u.key">
        <div class="line"><ui-icon :name="u.state === 'done' ? 'check' : u.state === 'error' ? 'alert' : 'file'" :class="{'st-ok': u.state === 'done', 'st-crit': u.state === 'error'}"></ui-icon>
          <span class="grow ellipsis" :title="u.dir + '/' + u.name">{{ u.name }}</span>
          <button class="plain icon-only" v-if="u.state === 'wait' || u.state === 'up'" title="取消" aria-label="取消上传" @click="cancelUpload(u)"><ui-icon name="close"></ui-icon></button></div>
        <div class="bar" v-if="u.state === 'up' || u.state === 'wait'"><i :style="{width: upPct(u) + '%'}"></i></div>
        <div class="small" :class="u.state === 'error' ? 'st-crit' : 'tertiary'">{{ upState(u) }}</div>
      </div>
    </div>
    </template>

    <div class="fm-menu" v-if="menu.open" ref="menuEl" :style="{left: menu.x + 'px', top: menu.y + 'px'}" role="menu" @click.stop @contextmenu.prevent>
      <template v-if="menu.blank || !selected.length">
        <button role="menuitem" @click="act(askNew, 'mkdir')"><ui-icon name="folder"></ui-icon>新建文件夹</button>
        <button role="menuitem" @click="act(askNew, 'touch')"><ui-icon name="file"></ui-icon>新建文件</button>
        <button role="menuitem" @click="act(() => fileInput.click())"><ui-icon name="upload"></ui-icon>上传文件</button>
        <button role="menuitem" v-if="clip.paths.length" @click="act(paste)"><ui-icon name="paste"></ui-icon>粘贴 {{ clip.paths.length }} 项</button>
        <hr>
        <button role="menuitem" @click="act(refresh)"><ui-icon name="refresh"></ui-icon>刷新</button>
        <button role="menuitem" @click="act(copyPath, list.path)"><ui-icon name="copy"></ui-icon>复制当前路径</button>
      </template>
      <template v-else>
        <button role="menuitem" v-if="one" @click="act(openEntry, one)"><ui-icon :name="one.dir ? 'folder' : isArchive(one) ? 'archive' : 'pencil'"></ui-icon>{{ one.dir ? '打开' : isArchive(one) ? '解压' : '编辑' }}</button>
        <button role="menuitem" v-if="one" @click="act(download, one)"><ui-icon name="download"></ui-icon>下载</button>
        <hr v-if="one">
        <button role="menuitem" @click="act(toClip, 'copy')"><ui-icon name="copy"></ui-icon>复制</button>
        <button role="menuitem" @click="act(toClip, 'move')"><ui-icon name="cut"></ui-icon>剪切</button>
        <button role="menuitem" v-if="clip.paths.length" @click="act(paste)"><ui-icon name="paste"></ui-icon>粘贴到这里</button>
        <hr>
        <button role="menuitem" v-if="one" @click="act(askRename)"><ui-icon name="pencil"></ui-icon>重命名</button>
        <button role="menuitem" @click="act(askCompress)"><ui-icon name="archive"></ui-icon>压缩</button>
        <button role="menuitem" @click="act(askPerm)"><ui-icon name="lock"></ui-icon>权限和所有者</button>
        <button role="menuitem" v-if="one" @click="act(copyPath, one.path)"><ui-icon name="copy"></ui-icon>复制路径</button>
        <hr>
        <button role="menuitem" class="destructive" @click="act(remove)"><ui-icon name="trash"></ui-icon>删除</button>
      </template>
    </div>

    <div class="sheet-mask" v-if="dlg.kind" @click.self="dlgClose">
      <div class="sheet fm-dlg" role="dialog" :aria-label="DLG[dlg.kind].title">
        <h2>{{ DLG[dlg.kind].title }}</h2>
        <template v-if="dlg.kind === 'mkdir' || dlg.kind === 'touch' || dlg.kind === 'rename'">
          <p>{{ dlg.kind === 'rename' ? '位置：' : '新建在 ' }}{{ list.path }}</p>
          <div class="group"><div class="row form"><span class="k">名称</span><span class="v">
            <input ref="dlgInput" v-model="dlg.name" @keydown.enter="!$event.isComposing && dlgSubmit()" aria-label="名称" spellcheck="false" autocomplete="off" autocapitalize="off"></span></div></div>
        </template>
        <template v-else-if="dlg.kind === 'compress'">
          <p>{{ dlg.text }}</p>
          <div class="group">
            <div class="row form"><span class="k">压缩包名称</span><span class="v"><input ref="dlgInput" v-model="dlg.name" @keydown.enter="!$event.isComposing && dlgSubmit()" aria-label="压缩包名称" spellcheck="false" autocomplete="off"></span></div>
            <div class="row form"><span class="k">格式</span><span class="v"><span class="segmented">
              <button :class="{on: dlg.format === 'tar.gz'}" @click="dlg.format = 'tar.gz'">tar.gz</button>
              <button :class="{on: dlg.format === 'zip'}" @click="dlg.format = 'zip'">zip</button></span></span></div>
          </div>
          <div class="hint">会生成 {{ list.path === '/' ? '' : list.path }}/{{ archiveName(dlg.name.trim() || '…', dlg.format) }}。tar.gz 在 Linux 上最通用；zip 在 Windows 上双击就能打开，服务器上要装有 zip 命令。</div>
        </template>
        <template v-else-if="dlg.kind === 'extract'">
          <p>{{ dlg.text }}</p>
          <div class="group">
            <div class="row form"><span class="k">解压到</span><span class="v"><span class="segmented">
              <button :class="{on: dlg.dest === dlg.here}" @click="dlg.dest = dlg.here">当前文件夹</button>
              <button :class="{on: dlg.dest === dlg.sub}" @click="dlg.dest = dlg.sub">新文件夹「{{ baseName(dlg.sub) }}」</button></span></span></div>
            <div class="row form"><span class="k">位置</span><span class="v"><input ref="dlgInput" v-model="dlg.dest" @keydown.enter="!$event.isComposing && dlgSubmit()" aria-label="解压到" spellcheck="false" autocomplete="off"></span></div>
          </div>
          <div class="hint">里面的文件和这个位置已有的同名文件会被覆盖。</div>
        </template>
        <template v-else-if="dlg.kind === 'perm'">
          <p>{{ dlg.text }}</p>
          <div class="group">
            <div class="row"><label class="fm-check"><input type="checkbox" v-model="dlg.doPerm">修改权限</label><span class="grow"></span>
              <input class="fm-perm-input mono" v-model="dlg.perm" @input="dlg.doPerm = true" maxlength="4" inputmode="numeric" placeholder="755" aria-label="权限数字"></div>
            <div class="row"><div class="perm-grid" :class="{off: !dlg.doPerm}">
              <span></span><span v-for="b in PERM_BITS" :key="b.bit" class="small tertiary">{{ b.text }}</span>
              <template v-for="w in PERM_WHO" :key="w.shift"><span>{{ w.text }}</span>
                <input v-for="b in PERM_BITS" :key="b.bit" type="checkbox" :checked="permOn(w.shift, b.bit)" @change="flipPerm(w.shift, b.bit)" :aria-label="w.text + b.text"></template>
            </div></div>
            <div class="row"><label class="fm-check"><input type="checkbox" v-model="dlg.doOwner">修改所有者</label><span class="grow"></span>
              <input class="fm-owner-input" v-model="dlg.owner" @input="dlg.doOwner = true" placeholder="www:www" aria-label="所有者" spellcheck="false" autocomplete="off"></div>
            <div class="row" v-if="dlg.hasDir"><label class="fm-check"><input type="checkbox" v-model="dlg.recursive">包括文件夹里的所有文件和子文件夹</label></div>
          </div>
          <div class="hint">网站文件一般是 644、文件夹 755，配置里有密码的文件用 600。所有者写成「用户:用户组」，要和运行网站的用户一致（宝塔一般是 www，1Panel 一般是 1000）。</div>
        </template>
        <template v-else-if="dlg.kind === 'paste'">
          <p>{{ dlg.text }}。</p>
          <div class="hint">「覆盖」会先删掉目标位置的同名文件或文件夹；「都保留」会给新的一份改名，比如 index (2).html。</div>
        </template>
        <template v-else-if="dlg.kind === 'upload'">
          <p>{{ list.path }} 里已经有 {{ dlg.text }}。</p>
        </template>
        <div class="fm-dlg-err st-crit small" v-if="dlg.error" role="alert">{{ dlg.error }}</div>
        <div class="sheet-actions">
          <button @click="dlgClose" :disabled="dlg.busy">取消</button>
          <template v-if="dlg.kind === 'paste'">
            <button @click="paste('skip')" :disabled="dlg.busy">跳过同名的</button>
            <button @click="paste('rename')" :disabled="dlg.busy">都保留</button>
            <button class="primary danger" @click="paste('overwrite')" :disabled="dlg.busy">覆盖</button>
          </template>
          <template v-else-if="dlg.kind === 'upload'">
            <button @click="uploadChoice('skip')" v-if="dlg.files.length > dlg.clash.length">跳过同名的</button>
            <button class="primary danger" @click="uploadChoice('overwrite')">覆盖</button>
          </template>
          <button v-else class="primary" @click="dlgSubmit" :disabled="dlg.busy">{{ dlg.busy ? '正在处理……' : DLG[dlg.kind].ok }}</button>
        </div>
      </div>
    </div>

    <div class="sheet-mask fm-ed-mask" v-if="ed.open">
      <div class="fm-editor" role="dialog" :aria-label="'编辑 ' + ed.name">
        <div class="fm-ed-head">
          <ui-icon name="file" class="lg"></ui-icon>
          <div class="grow"><div class="fm-ed-title"><b>{{ ed.name }}</b><span class="tag dirty" v-if="dirty">未保存</span></div><div class="small tertiary ellipsis" :title="ed.path">{{ ed.path }}</div></div>
          <label class="fm-check small" v-if="!ed.binary && !ed.error"><input type="checkbox" v-model="ed.wrap">自动换行</label>
          <button class="plain" @click="download({ path: ed.path, name: ed.name, dir: false })" :disabled="ed.loading"><ui-icon name="download"></ui-icon>下载</button>
          <button class="primary" @click="save()" :disabled="!dirty || ed.saving" title="保存（Ctrl+S）">{{ ed.saving ? '正在保存……' : '保存' }}</button>
          <button class="plain icon-only" @click="closeEditor" title="关闭（Esc）" aria-label="关闭"><ui-icon name="close"></ui-icon></button>
        </div>
        <div class="fm-ed-body">
          <div class="fm-msg tertiary" v-if="ed.loading">正在打开……</div>
          <div class="fm-msg" v-else-if="ed.error"><ui-icon name="alert" class="lg st-crit"></ui-icon><p>{{ ed.error }}</p></div>
          <div class="fm-msg" v-else-if="ed.binary"><ui-icon name="info" class="lg"></ui-icon><p>这不是文本文件（可能是图片、压缩包或程序），不能在这里编辑，可以下载到电脑上打开。</p>
            <button @click="download({ path: ed.path, name: ed.name, dir: false })"><ui-icon name="download"></ui-icon>下载到电脑</button></div>
          <textarea v-else ref="edEl" v-model="ed.content" :wrap="ed.wrap ? 'soft' : 'off'" spellcheck="false" autocomplete="off" autocapitalize="off"
            @keydown="onEdKey" @keyup="caret" @click="caret" aria-label="文件内容"></textarea>
        </div>
        <div class="fm-ed-foot small tertiary" v-if="!ed.loading && !ed.error && !ed.binary">
          <span>第 {{ ed.line }} 行，第 {{ ed.col }} 列</span><span>{{ fmtBytes(ed.size) }}</span><span>UTF-8 · {{ ed.crlf ? 'CRLF（Windows 换行）' : 'LF' }}</span>
          <span class="grow"></span><span>Ctrl+S 保存 · Esc 关闭</span>
        </div>
      </div>
    </div>
  </div>`,
};

// ---- Web edition: logging in, and the account ----

// uaText names a browser from its UA string: 「Chrome · Windows」.
function uaText(ua) {
  const s = String(ua || '');
  const b = /Edg\//.test(s) ? 'Edge' : /Firefox\//.test(s) ? 'Firefox' : /Chrome\//.test(s) ? 'Chrome' : /Safari\//.test(s) ? 'Safari' : '浏览器';
  const o = /Windows/.test(s) ? 'Windows' : /iPhone|iPad/.test(s) ? 'iOS' : /Android/.test(s) ? 'Android' : /Mac OS X/.test(s) ? 'macOS' : /Linux/.test(s) ? 'Linux' : '';
  return o ? `${b} · ${o}` : b;
}

// LoginApp is the whole page until someone logs in to the web edition
// (or, the first time, makes the account with the setup code).
const LOGIN_WAYS = [{ id: 'password', text: '密码' }, { id: 'email', text: '邮箱验证码' }, { id: 'sms', text: '短信验证码' }];
const LoginApp = {
  props: { state: Object },
  setup(props) {
    const setup = ref(!!props.state.setup);
    const methods = props.state.methods || { password: true };
    // The ways turned on, the one used last time first.
    const ways = LOGIN_WAYS.filter(w => methods[w.id]);
    const last = pref('miao.loginWay', '');
    const way = ref(ways.some(w => w.id === last) ? last : (ways[0] || LOGIN_WAYS[0]).id);
    const f = reactive({ code: '', name: setup.value ? 'admin' : '', password: '', password2: '', totp: '', target: '', loginCode: '' });
    const needCode = ref(false), busy = ref(false), error = ref(''), note = ref('');
    const codeEl = ref(null), loginCodeEl = ref(null);
    const wait = ref(0), sending = ref(false);
    let timer = null;
    const local = ['localhost', '127.0.0.1', '[::1]'].includes(location.hostname);
    const insecure = !props.state.https && !local && location.protocol !== 'https:';
    watch(way, v => { setPref('miao.loginWay', v); needCode.value = false; error.value = ''; note.value = ''; f.totp = ''; });
    function countdown() {
      clearInterval(timer);
      wait.value = 60;
      timer = setInterval(() => { if (--wait.value <= 0) clearInterval(timer); }, 1000);
    }
    async function sendCode() {
      error.value = ''; note.value = '';
      if (!f.target.trim()) { error.value = way.value === 'email' ? '请填写邮箱' : '请填写手机号'; return; }
      sending.value = true;
      try {
        const r = await api('POST', '/api/auth/code', { channel: way.value, target: f.target });
        note.value = r.done;
        countdown();
        nextTick(() => loginCodeEl.value && loginCodeEl.value.focus());
      } catch (e) {
        error.value = e.message;
        const m = e.message.match(/(\d+) 秒后/);
        if (e.code === 'too_soon' && m) { countdown(); wait.value = Number(m[1]); }
      } finally { sending.value = false; }
    }
    async function submit() {
      error.value = '';
      if (setup.value && f.password !== f.password2) { error.value = '两次输入的密码不一样'; return; }
      busy.value = true;
      try {
        if (setup.value) await api('POST', '/api/auth/setup', { code: f.code, name: f.name, password: f.password });
        else if (way.value === 'password') await api('POST', '/api/auth/login', { name: f.name, password: f.password, code: f.totp });
        else await api('POST', '/api/auth/login', { channel: way.value, target: f.target, loginCode: f.loginCode, code: f.totp });
        location.reload();
        return;
      } catch (e) {
        if (e.code === 'need_code') {
          needCode.value = true;
          nextTick(() => codeEl.value && codeEl.value.focus());
        } else {
          error.value = e.message;
          if (e.code === 'setup_done') setup.value = false;
          if (e.code === 'bad_code') f.totp = '';
          if (e.code === 'method_off') location.reload();
        }
      }
      busy.value = false;
    }
    return { setup, ways, way, f, needCode, busy, error, note, codeEl, loginCodeEl, wait, sending, insecure, sendCode, submit };
  },
  template: `
  <div class="login-page">
    <form class="login-card" @submit.prevent="submit">
      <div class="login-brand"><span class="app-mark"><miao-logo></miao-logo></span>
        <div><div class="login-name">Miao Panel</div><div class="small tertiary">Web 版</div></div></div>
      <h1>{{ setup ? '创建管理员账号' : '登录' }}</h1>
      <p class="small secondary" v-if="setup">第一次使用，需要服务器上的初始化码：运行 <code>docker logs miaopanel</code> 或 <code>journalctl -u miaopanel</code> 就能看到，也保存在数据目录的 <code>setup-code</code> 文件里。</p>
      <div class="login-warn" v-if="insecure" role="alert"><ui-icon name="warn"></ui-icon><span>现在是 HTTP 连接，密码会明文传输。请改用 HTTPS 访问（部署说明里有设置方法）。</span></div>
      <div class="segmented login-ways" v-if="!setup && ways.length > 1" role="tablist" aria-label="登录方式">
        <button type="button" v-for="w in ways" :key="w.id" role="tab" :aria-selected="way === w.id" :class="{ on: way === w.id }" @click="way = w.id">{{ w.text }}</button>
      </div>
      <label class="field" v-if="setup"><span>初始化码</span><input v-model="f.code" autocomplete="off" autocapitalize="characters" spellcheck="false" placeholder="XXXX-XXXX-XXXX-XXXX" required></label>
      <template v-if="setup || way === 'password'">
        <label class="field"><span>{{ setup ? '用户名' : '账号' }}</span><input v-model="f.name" autocomplete="username" autocapitalize="off" spellcheck="false" required :autofocus="!setup"
          :placeholder="setup ? '' : '用户名，或者绑定的邮箱、手机号'"></label>
        <label class="field"><span>密码</span><input type="password" v-model="f.password" :autocomplete="setup ? 'new-password' : 'current-password'" required></label>
        <label class="field" v-if="setup"><span>再输一次密码</span><input type="password" v-model="f.password2" autocomplete="new-password" required></label>
        <p class="small tertiary" v-if="setup">至少 10 个字符。这个账号能管理你所有的服务器，请用一个别处没用过的密码，登录后建议开启两步验证。</p>
      </template>
      <template v-else>
        <label class="field"><span>{{ way === 'email' ? '邮箱' : '手机号' }}</span>
          <span class="login-send"><input v-model="f.target" :type="way === 'email' ? 'email' : 'tel'" :autocomplete="way === 'email' ? 'email' : 'tel'" autocapitalize="off" spellcheck="false" required
            :placeholder="way === 'email' ? '绑定的邮箱' : '绑定的手机号'" :aria-label="way === 'email' ? '邮箱' : '手机号'">
            <button type="button" @click="sendCode" :disabled="sending || wait > 0">{{ wait > 0 ? wait + ' 秒后重发' : sending ? '正在发送……' : '获取验证码' }}</button></span></label>
        <label class="field"><span>{{ way === 'email' ? '邮件' : '短信' }}里的验证码</span><input ref="loginCodeEl" v-model="f.loginCode" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="6 位数字" required></label>
        <p class="small secondary" v-if="note" role="status">{{ note }}</p>
      </template>
      <label class="field" v-if="needCode"><span>两步验证码</span><input ref="codeEl" v-model="f.totp" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="身份验证器 App 里的 6 位数字"></label>
      <div class="login-error" v-if="error" role="alert"><ui-icon name="alert"></ui-icon><span>{{ error }}</span></div>
      <button class="primary login-btn" type="submit" :disabled="busy">{{ busy ? '请稍候……' : setup ? '创建并登录' : '登录' }}</button>
    </form>
    <p class="small tertiary login-foot">Miao Panel {{ state.version }} · 开源项目（GPL-3.0）</p>
  </div>`,
};

// AccountPanel is 设置 → 账号与安全 in the web edition.
const AccountPanel = {
  setup() {
    const acct = ref(null);
    const pw = reactive({ open: false, old: '', new1: '', new2: '', busy: false, error: '' });
    const tf = reactive({ setup: null, code: '', password: '', off: false, busy: false, error: '' });
    async function load() {
      try { acct.value = await api('GET', '/api/account'); } catch (e) { notify(e.message, 'error'); }
    }
    onMounted(load);
    async function savePassword() {
      pw.error = '';
      if (pw.new1 !== pw.new2) { pw.error = '两次输入的新密码不一样'; return; }
      pw.busy = true;
      try {
        await api('PUT', '/api/account/password', { old: pw.old, new: pw.new1 });
        Object.assign(pw, { open: false, old: '', new1: '', new2: '' });
        notify('密码已修改，其他地方的登录都已退出');
        load();
      } catch (e) { pw.error = e.message; } finally { pw.busy = false; }
    }
    async function beginTOTP() {
      tf.error = ''; tf.busy = true;
      try { tf.setup = await api('POST', '/api/account/totp'); tf.code = ''; } catch (e) { notify(e.message, 'error'); } finally { tf.busy = false; }
    }
    async function enableTOTP() {
      tf.error = ''; tf.busy = true;
      try {
        await api('PUT', '/api/account/totp', { code: tf.code });
        tf.setup = null;
        notify('两步验证已开启，以后登录要输入 App 里的验证码');
        load();
      } catch (e) { tf.error = e.message; } finally { tf.busy = false; }
    }
    async function disableTOTP() {
      tf.error = ''; tf.busy = true;
      try {
        await api('POST', '/api/account/totp/off', { password: tf.password });
        Object.assign(tf, { off: false, password: '' });
        notify('两步验证已关闭');
        load();
      } catch (e) { tf.error = e.message; } finally { tf.busy = false; }
    }
    async function endSession(s) {
      try { await api('DELETE', `/api/account/sessions/${s.key}`); notify('已让这个设备退出登录'); load(); } catch (e) { notify(e.message, 'error'); }
    }
    async function logout() {
      try { await api('POST', '/api/auth/logout'); } catch { /* logged out anyway */ }
      location.reload();
    }
    const when = t => t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '';

    // ---- Ways of logging in ----
    const methodBusy = ref(false);
    async function setMethod(k, on) {
      if (acct.value.methods[k] === on) return;
      const m = { ...acct.value.methods, [k]: on };
      if (k === 'password' && !on && !confirm('关闭密码登录后，只能用验证码登录。\n收不到验证码时（比如邮箱或短信设置出了问题），可以在服务器上运行 miaopanel reset-password 重新开启密码登录。确定关闭吗？')) return;
      methodBusy.value = true;
      try {
        Object.assign(acct.value, await api('PUT', '/api/account/methods', m));
        notify(on ? '已开启' : '已关闭');
      } catch (e) { notify(e.message, 'error'); } finally { methodBusy.value = false; }
    }
    const methodNote = k => {
      const a = acct.value, name = { email: '邮箱', sms: '手机号' }[k], sender = { email: a.mail.configured, sms: a.sms.configured }[k];
      const bound = { email: a.email, sms: a.phone }[k];
      if (a.methods[k] && !a.active[k]) return `已开启，但${k === 'email' ? '发信邮箱' : '短信'}设置不完整，暂时不能用`;
      if (a.methods[k]) return `已开启：用绑定的${name}收验证码登录`;
      if (!sender && !bound) return `先在下面设置${k === 'email' ? '发信邮箱' : '短信'}，再绑定${name}`;
      if (!sender) return `先在下面设置${k === 'email' ? '发信邮箱' : '短信'}`;
      if (!bound) return `先在下面绑定${name}`;
      return '未开启';
    };
    const canOn = k => { const a = acct.value; return k === 'email' ? a.mail.configured && !!a.email : a.sms.configured && !!a.phone; };

    // Codes only to the administrator's email and phone: on at once, off
    // with the password.
    const scope = reactive({ asking: false, password: '', busy: false, error: '' });
    function scopeOff() { if (acct.value.adminOnly) Object.assign(scope, { asking: true, password: '', error: '' }); }
    async function setScope(on) {
      if (on && acct.value.adminOnly) return;
      scope.busy = true; scope.error = '';
      try {
        const r = await api('PUT', '/api/account/send-scope', { on, password: on ? '' : scope.password });
        acct.value.adminOnly = r.adminOnly;
        Object.assign(scope, { asking: false, password: '' });
        notify(on ? '已开启：验证码只发给管理员邮箱和手机号' : '已关闭发送限制');
      } catch (e) { scope.error = e.message; if (on) notify(e.message, 'error'); } finally { scope.busy = false; }
    }

    // Binding: the password, then a code sent to the new address.
    const bd = reactive({ open: false, channel: 'email', mode: 'bind', step: 1, target: '', password: '', code: '', busy: false, error: '' });
    function openBind(channel, mode) { Object.assign(bd, { open: true, channel, mode, step: 1, target: '', password: '', code: '', busy: false, error: '' }); }
    async function bindSend() {
      bd.busy = true; bd.error = '';
      try {
        await api('POST', '/api/account/bind', { channel: bd.channel, target: bd.target, password: bd.password });
        bd.step = 2; bd.password = '';
      } catch (e) { bd.error = e.message; } finally { bd.busy = false; }
    }
    async function bindConfirm() {
      bd.busy = true; bd.error = '';
      try {
        const r = await api('PUT', '/api/account/bind', { channel: bd.channel, code: bd.code });
        bd.open = false;
        notify('已绑定 ' + r.target);
        load();
      } catch (e) { bd.error = e.message; } finally { bd.busy = false; }
    }
    async function unbind() {
      bd.busy = true; bd.error = '';
      try {
        await api('POST', '/api/account/unbind', { channel: bd.channel, password: bd.password });
        bd.open = false;
        notify('已解绑');
        load();
      } catch (e) { bd.error = e.message; } finally { bd.busy = false; }
    }

    // Where codes are sent from.
    const ml = reactive({ open: false, preset: '', host: '', port: 465, security: 'ssl', username: '', smtpPassword: '', from: '', fromName: 'Miao Panel', to: '', current: '', busy: '', error: '', ok: '' });
    function openMail() {
      const m = acct.value.mail;
      Object.assign(ml, { open: true, preset: '', host: m.host || '', port: m.port || 465, security: m.security || 'ssl', username: m.username || '', smtpPassword: '',
        from: m.from || '', fromName: m.fromName || 'Miao Panel', to: acct.value.email || m.username || '', current: '', busy: '', error: '', ok: '' });
      const p = acct.value.presets.find(x => x.host === ml.host);
      ml.preset = p ? p.id : '';
    }
    const mailPreset = computed(() => acct.value ? acct.value.presets.find(x => x.id === ml.preset) : null);
    watch(() => ml.preset, id => {
      const p = acct.value && acct.value.presets.find(x => x.id === id);
      if (p) Object.assign(ml, { host: p.host, port: p.port, security: p.security });
    });
    const mailBody = () => ({ host: ml.host, port: Number(ml.port), security: ml.security, username: ml.username.trim(), smtpPassword: ml.smtpPassword,
      from: ml.from.trim(), fromName: ml.fromName.trim() });
    async function mailRun(kind) {
      ml.busy = kind; ml.error = ''; ml.ok = '';
      try {
        if (kind === 'test') {
          await api('POST', '/api/account/mail/test', { ...mailBody(), to: ml.to });
          ml.ok = '测试邮件已发出，去 ' + ml.to + ' 看看（可能在垃圾邮件里）';
        } else if (kind === 'save') {
          acct.value.mail = await api('PUT', '/api/account/mail', { ...mailBody(), current: ml.current });
          ml.open = false; notify('发信设置已保存'); load();
        } else {
          if (!confirm('清除发信设置？邮箱验证码登录会停用。')) { ml.busy = ''; return; }
          await api('POST', '/api/account/mail/clear', { current: ml.current });
          ml.open = false; notify('已清除'); load();
        }
      } catch (e) { ml.error = e.message; } finally { ml.busy = ''; }
    }
    const sm = reactive({ open: false, provider: 'tencent', appId: '', sign: '', template: '', params: 2, region: 'ap-guangzhou', accessKeyId: '', accessKeySecret: '',
      codeVar: 'code', minutesVar: '', dailyLimit: 30, phone: '', current: '', busy: '', error: '', ok: '' });
    // Each provider keeps what was typed for it while switching back and forth.
    function openSMS() {
      const v = acct.value.sms;
      Object.assign(sm, { open: true, provider: v.provider || 'tencent', appId: v.appId || '', sign: v.sign || '', template: v.template || '', params: v.params || 2,
        region: v.region || 'ap-guangzhou', accessKeyId: v.accessKeyId || '', accessKeySecret: '', codeVar: v.codeVar || 'code', minutesVar: v.minutesVar || '',
        dailyLimit: v.dailyLimit || 30, phone: acct.value.phone || '', current: '', busy: '', error: '', ok: '' });
    }
    // Template IDs differ between the two: switching shows the saved one
    // for that provider, or none.
    watch(() => sm.provider, (p, old) => {
      if (!sm.open || !old || p === old) return;
      const v = acct.value.sms;
      Object.assign(sm, { template: v.provider === p ? v.template || '' : '', error: '', ok: '' });
    });
    const smsBody = () => sm.provider === 'aliyun'
      ? { provider: 'aliyun', sign: sm.sign.trim(), template: sm.template.trim(), accessKeyId: sm.accessKeyId.trim(), accessKeySecret: sm.accessKeySecret,
        codeVar: sm.codeVar.trim(), minutesVar: sm.minutesVar.trim(), dailyLimit: Number(sm.dailyLimit) }
      : { provider: 'tencent', appId: sm.appId.trim(), sign: sm.sign.trim(), template: sm.template.trim(), params: Number(sm.params), region: sm.region, dailyLimit: Number(sm.dailyLimit) };
    const smsFilled = computed(() => sm.sign.trim() && sm.template.trim() && (sm.provider === 'aliyun'
      ? sm.accessKeyId.trim() && (sm.accessKeySecret || (acct.value && acct.value.sms.hasSecret && acct.value.sms.provider === 'aliyun'))
      : sm.appId.trim() && acct.value && acct.value.sms.keys));
    async function smsRun(kind) {
      sm.busy = kind; sm.error = ''; sm.ok = '';
      try {
        if (kind === 'test') {
          await api('POST', '/api/account/sms/test', { ...smsBody(), phone: sm.phone });
          sm.ok = '测试短信已发出（验证码 123456），看看手机收到没有';
        } else if (kind === 'save') {
          await api('PUT', '/api/account/sms', { ...smsBody(), current: sm.current });
          sm.open = false; notify('短信设置已保存'); load();
        } else {
          if (!confirm('清除短信设置？短信验证码登录会停用。')) { sm.busy = ''; return; }
          await api('POST', '/api/account/sms/clear', { current: sm.current });
          sm.open = false; notify('已清除'); load();
        }
      } catch (e) { sm.error = e.message; } finally { sm.busy = ''; }
    }
    const mailText = computed(() => {
      const m = acct.value && acct.value.mail;
      return m && m.configured ? `${m.from || m.username}（${m.host}:${m.port}）` : '还没有设置';
    });
    const smsText = computed(() => {
      const v = acct.value && acct.value.sms;
      if (!v || !v.configured) return '还没有设置';
      return `${v.provider === 'aliyun' ? '阿里云' : '腾讯云'}短信，签名【${v.sign}】，模板 ${v.template}，今天已发 ${v.sentToday} / ${v.dailyLimit} 条`;
    });
    const SMS_REGIONS = [['ap-guangzhou', '广州'], ['ap-beijing', '北京'], ['ap-nanjing', '南京']];

    return { acct, pw, tf, savePassword, beginTOTP, enableTOTP, disableTOTP, endSession, logout, when, uaText,
      methodBusy, setMethod, methodNote, canOn, bd, openBind, bindSend, bindConfirm, unbind, scope, scopeOff, setScope,
      ml, openMail, mailPreset, mailRun, sm, openSMS, smsRun, smsFilled, mailText, smsText, SMS_REGIONS };
  },
  template: `
  <div class="group" v-if="acct">
    <div class="row"><span class="k">用户名</span><span class="v">{{ acct.name }}</span></div>
    <div class="row"><span class="k">密码</span><span class="grow small tertiary">上次修改：{{ when(acct.changedAt) }}</span>
      <button class="small" @click="pw.open = !pw.open">{{ pw.open ? '取消' : '修改密码' }}</button></div>
    <template v-if="pw.open">
      <div class="row form"><span class="k">原密码</span><span class="v"><input type="password" v-model="pw.old" autocomplete="current-password" aria-label="原密码"></span></div>
      <div class="row form"><span class="k">新密码</span><span class="v"><input type="password" v-model="pw.new1" autocomplete="new-password" placeholder="至少 10 个字符" aria-label="新密码"></span></div>
      <div class="row form"><span class="k">再输一次</span><span class="v"><input type="password" v-model="pw.new2" autocomplete="new-password" aria-label="再输一次新密码"></span></div>
      <div class="row"><span class="grow small st-crit">{{ pw.error }}</span><button class="primary small" @click="savePassword" :disabled="pw.busy || !pw.old || !pw.new1">保存新密码</button></div>
    </template>
    <div class="row"><span class="k">两步验证</span>
      <span class="grow small" :class="acct.totp ? 'st-ok' : 'st-warn'">{{ acct.totp ? '已开启：登录时还要输入手机 App 里的验证码' : '未开启。建议开启：就算密码泄露，别人也登录不了' }}</span>
      <button class="small" v-if="!acct.totp && !tf.setup" @click="beginTOTP" :disabled="tf.busy">开启两步验证</button>
      <button class="small" v-if="acct.totp && !tf.off" @click="tf.off = true">关闭</button></div>
    <div class="row totp-setup" v-if="tf.setup">
      <img :src="tf.setup.qr" alt="两步验证二维码" width="168" height="168">
      <div class="grow">
        <p class="small">1. 用身份验证器 App（如 Google Authenticator、Microsoft Authenticator、腾讯身份验证器）扫描二维码；扫不了就手动输入密钥：</p>
        <p class="mono totp-secret">{{ tf.setup.secret }}</p>
        <p class="small">2. 输入 App 显示的 6 位验证码：</p>
        <div class="totp-confirm"><input v-model="tf.code" inputmode="numeric" maxlength="6" placeholder="123456" aria-label="验证码" @keydown.enter="enableTOTP">
          <button class="primary small" @click="enableTOTP" :disabled="tf.busy || tf.code.length !== 6">确认开启</button>
          <button class="plain small" @click="tf.setup = null">取消</button></div>
        <p class="small st-crit" v-if="tf.error">{{ tf.error }}</p>
      </div>
    </div>
    <div class="row" v-if="tf.off"><span class="k">输入密码关闭</span>
      <span class="v"><input type="password" v-model="tf.password" autocomplete="current-password" aria-label="密码" @keydown.enter="disableTOTP"></span>
      <button class="small destructive" @click="disableTOTP" :disabled="tf.busy || !tf.password">关闭两步验证</button>
      <button class="plain small" @click="tf.off = false; tf.error = ''">取消</button></div>
    <div class="row small st-crit" v-if="tf.off && tf.error">{{ tf.error }}</div>
  </div>
  <template v-if="acct">
  <div class="group-title">登录方式</div>
  <div class="group login-methods">
    <div class="row"><div class="grow"><div>密码</div><div class="small tertiary">{{ acct.methods.password ? '账号（用户名或绑定的邮箱、手机号）加密码' : '已关闭：只能用验证码登录' }}</div></div>
      <span class="segmented" role="radiogroup" aria-label="密码登录"><button role="radio" :aria-checked="!acct.methods.password" :class="{ on: !acct.methods.password }" @click="setMethod('password', false)" :disabled="methodBusy">关闭</button>
        <button role="radio" :aria-checked="acct.methods.password" :class="{ on: acct.methods.password }" @click="setMethod('password', true)" :disabled="methodBusy">开启</button></span></div>
    <div class="row" v-for="k in ['email', 'sms']" :key="k"><div class="grow"><div>{{ k === 'email' ? '邮箱验证码' : '短信验证码' }}</div>
        <div class="small" :class="acct.methods[k] && !acct.active[k] ? 'st-warn' : 'tertiary'">{{ methodNote(k) }}</div></div>
      <span class="segmented" role="radiogroup" :aria-label="k === 'email' ? '邮箱验证码登录' : '短信验证码登录'"><button role="radio" :aria-checked="!acct.methods[k]" :class="{ on: !acct.methods[k] }" @click="setMethod(k, false)" :disabled="methodBusy">关闭</button>
        <button role="radio" :aria-checked="acct.methods[k]" :class="{ on: acct.methods[k] }" @click="setMethod(k, true)" :disabled="methodBusy || (!acct.methods[k] && !canOn(k))">开启</button></span></div>
    <div class="row small tertiary">验证码 10 分钟内有效、只能用一次；开了两步验证的账号，用验证码登录后还要输入 App 里的验证码。</div>
  </div>
  <div class="group-title">管理员邮箱和手机号</div>
  <div class="group">
    <div class="row"><div class="grow"><div>只给管理员邮箱和手机号发送</div>
        <div class="small tertiary">{{ acct.adminOnly ? '已开启：登录验证码和测试邮件、短信只发到下面这两个，其他的一律拒发（记在操作记录里）；要更换邮箱或手机号，先关闭它' : '已关闭：可以更换绑定，测试邮件、短信可以发到别的地址。登录验证码仍然只发给绑定的邮箱和手机号' }}</div></div>
      <span class="segmented" role="radiogroup" aria-label="只给管理员邮箱和手机号发送"><button role="radio" :aria-checked="!acct.adminOnly" :class="{ on: !acct.adminOnly }" @click="scopeOff" :disabled="scope.busy">关闭</button>
        <button role="radio" :aria-checked="acct.adminOnly" :class="{ on: acct.adminOnly }" @click="setScope(true)" :disabled="scope.busy">开启</button></span></div>
    <div class="row" v-if="scope.asking"><span class="k">输入密码关闭</span>
      <span class="v"><input type="password" v-model="scope.password" autocomplete="current-password" aria-label="关闭发送限制的账号密码" @keydown.enter="setScope(false)"></span>
      <button class="small destructive" @click="setScope(false)" :disabled="scope.busy || !scope.password">关闭限制</button>
      <button class="plain small" @click="scope.asking = false; scope.error = ''">取消</button></div>
    <div class="row small st-crit" v-if="scope.asking && scope.error">{{ scope.error }}</div>
    <div class="row"><span class="k">邮箱</span><span class="grow" :class="{ tertiary: !acct.email }">{{ acct.email || '未绑定' }}</span>
      <button class="small" @click="openBind('email', 'bind')" :disabled="!acct.mail.configured || (acct.adminOnly && !!acct.email)"
        :title="!acct.mail.configured ? '先设置发信邮箱' : acct.adminOnly && acct.email ? '先关闭「只给管理员邮箱和手机号发送」' : ''">{{ acct.email ? '更换' : '绑定' }}</button>
      <button class="small plain" v-if="acct.email" @click="openBind('email', 'unbind')">解绑</button></div>
    <div class="row"><span class="k">手机号</span><span class="grow" :class="{ tertiary: !acct.phone }">{{ acct.phone || '未绑定' }}</span>
      <button class="small" @click="openBind('sms', 'bind')" :disabled="!acct.sms.configured || (acct.adminOnly && !!acct.phone)"
        :title="!acct.sms.configured ? '先设置短信' : acct.adminOnly && acct.phone ? '先关闭「只给管理员邮箱和手机号发送」' : ''">{{ acct.phone ? '更换' : '绑定' }}</button>
      <button class="small plain" v-if="acct.phone" @click="openBind('sms', 'unbind')">解绑</button></div>
  </div>
  <div class="group-title">验证码从哪里发出</div>
  <div class="group">
    <div class="row"><div class="grow"><div>发信邮箱（SMTP）</div><div class="small tertiary">{{ mailText }}</div></div><button class="small" @click="openMail">设置</button></div>
    <div class="row"><div class="grow"><div>短信（腾讯云或阿里云）</div><div class="small tertiary">{{ smsText }}</div></div><button class="small" @click="openSMS">设置</button></div>
  </div>
  </template>

  <!-- Bind or unbind -->
  <div class="sheet-mask" v-if="bd.open" @click.self="!bd.busy && (bd.open = false)">
    <div class="sheet" role="dialog" :aria-label="(bd.mode === 'bind' ? '绑定' : '解绑') + (bd.channel === 'email' ? '邮箱' : '手机号')">
      <h2>{{ bd.mode === 'bind' ? (bd.channel === 'email' ? (acct.email ? '更换邮箱' : '绑定邮箱') : (acct.phone ? '更换手机号' : '绑定手机号')) : (bd.channel === 'email' ? '解绑邮箱' : '解绑手机号') }}</h2>
      <template v-if="bd.mode === 'unbind'">
        <p>解绑后不能再用它收验证码登录{{ bd.channel === 'email' ? '，邮箱验证码登录会关闭' : '，短信验证码登录会关闭' }}。</p>
        <div class="group"><div class="row form"><span class="k">账号密码</span><span class="v"><input type="password" v-model="bd.password" autocomplete="current-password" aria-label="账号密码" @keydown.enter="unbind"></span></div></div>
      </template>
      <template v-else-if="bd.step === 1">
        <p>先输入账号密码，再往新的{{ bd.channel === 'email' ? '邮箱' : '手机号' }}发一个验证码，确认是你的。</p>
        <div class="group">
          <div class="row form"><span class="k">{{ bd.channel === 'email' ? '邮箱' : '手机号' }}</span><span class="v"><input v-model="bd.target" :type="bd.channel === 'email' ? 'email' : 'tel'" autocapitalize="off" spellcheck="false"
            :placeholder="bd.channel === 'email' ? 'name@example.com' : '中国大陆号码直接填 11 位'" :aria-label="bd.channel === 'email' ? '邮箱' : '手机号'"></span></div>
          <div class="row form"><span class="k">账号密码</span><span class="v"><input type="password" v-model="bd.password" autocomplete="current-password" aria-label="账号密码" @keydown.enter="bindSend"></span></div>
        </div>
      </template>
      <template v-else>
        <p>验证码已发到 {{ bd.target }}，10 分钟内有效。</p>
        <div class="group"><div class="row form"><span class="k">验证码</span><span class="v"><input v-model="bd.code" inputmode="numeric" maxlength="6" autocomplete="one-time-code" placeholder="6 位数字" aria-label="验证码" @keydown.enter="bindConfirm"></span></div></div>
      </template>
      <div class="notice" v-if="bd.error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ bd.error }}</div>
      <div class="sheet-actions"><button @click="bd.open = false" :disabled="bd.busy">取消</button>
        <button v-if="bd.mode === 'unbind'" class="primary danger" @click="unbind" :disabled="bd.busy || !bd.password">解绑</button>
        <button v-else-if="bd.step === 1" class="primary" @click="bindSend" :disabled="bd.busy || !bd.target.trim() || !bd.password">{{ bd.busy ? '正在发送……' : '发送验证码' }}</button>
        <button v-else class="primary" @click="bindConfirm" :disabled="bd.busy || bd.code.length !== 6">确认绑定</button></div>
    </div>
  </div>

  <!-- SMTP -->
  <div class="sheet-mask" v-if="ml.open" @click.self="!ml.busy && (ml.open = false)">
    <div class="sheet" role="dialog" aria-label="发信邮箱">
      <h2>发信邮箱（SMTP）</h2>
      <p>登录验证码从这个邮箱发出。建议用一个单独的邮箱，不要用收验证码的那个。</p>
      <div class="group">
        <div class="row form"><span class="k">邮箱服务商</span><span class="v"><select v-model="ml.preset" aria-label="邮箱服务商"><option value="">其他（自己填写）</option>
          <option v-for="p in acct.presets" :key="p.id" :value="p.id">{{ p.name }}</option></select>
          <span class="small secondary block" v-if="mailPreset">{{ mailPreset.note }}</span></span></div>
        <div class="row form"><span class="k">SMTP 服务器</span><span class="v"><input v-model="ml.host" placeholder="smtp.qq.com" aria-label="SMTP 服务器" autocapitalize="off" spellcheck="false"></span></div>
        <div class="row form"><span class="k">端口和加密</span><span class="v mail-port"><input type="number" v-model.number="ml.port" min="1" max="65535" aria-label="端口">
          <span class="segmented"><button :class="{ on: ml.security === 'ssl' }" @click="ml.security = 'ssl'">SSL</button><button :class="{ on: ml.security === 'starttls' }" @click="ml.security = 'starttls'">STARTTLS</button><button :class="{ on: ml.security === 'none' }" @click="ml.security = 'none'">不加密</button></span></span></div>
        <div class="row form"><span class="k">邮箱账号</span><span class="v"><input v-model="ml.username" placeholder="you@qq.com" autocomplete="off" aria-label="邮箱账号" autocapitalize="off" spellcheck="false"></span></div>
        <div class="row form"><span class="k">密码或授权码</span><span class="v"><input type="password" v-model="ml.smtpPassword" autocomplete="new-password" :placeholder="acct.mail.hasPassword ? '已保存，不修改就留空' : 'QQ、163 邮箱填授权码'" aria-label="密码或授权码"></span></div>
        <div class="row form"><span class="k">发件人名称</span><span class="v"><input v-model="ml.fromName" placeholder="Miao Panel" aria-label="发件人名称"></span></div>
        <div class="row form"><span class="k">发件地址</span><span class="v"><input v-model="ml.from" :placeholder="ml.username || '不填就用邮箱账号'" aria-label="发件地址" autocapitalize="off" spellcheck="false"></span></div>
      </div>
      <div class="group">
        <div class="row form"><span class="k">发一封测试邮件</span><span class="v login-send"><input v-model="ml.to" type="email" placeholder="收件邮箱" aria-label="测试收件邮箱" autocapitalize="off" spellcheck="false">
          <button @click="mailRun('test')" :disabled="!!ml.busy || !ml.host || !ml.username || !ml.to">{{ ml.busy === 'test' ? '正在发送……' : '发送' }}</button></span></div>
      </div>
      <div class="group"><div class="row form"><span class="k">账号密码</span><span class="v"><input type="password" v-model="ml.current" autocomplete="current-password" placeholder="保存或清除要输入 Miao Panel 的登录密码" aria-label="账号密码"></span></div></div>
      <div class="notice" v-if="ml.error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ ml.error }}</div>
      <div class="notice" v-if="ml.ok"><ui-icon name="check" class="st-ok"></ui-icon>{{ ml.ok }}</div>
      <div class="sheet-actions"><button class="plain destructive" v-if="acct.mail.configured" @click="mailRun('clear')" :disabled="!!ml.busy || !ml.current">清除设置</button><span class="grow"></span>
        <button @click="ml.open = false" :disabled="!!ml.busy">取消</button>
        <button class="primary" @click="mailRun('save')" :disabled="!!ml.busy || !ml.current || !ml.host || !ml.username">{{ ml.busy === 'save' ? '正在保存……' : '保存' }}</button></div>
    </div>
  </div>

  <!-- SMS: Tencent Cloud or Alibaba Cloud -->
  <div class="sheet-mask" v-if="sm.open" @click.self="!sm.busy && (sm.open = false)">
    <div class="sheet" role="dialog" aria-label="短信">
      <h2>短信</h2>
      <div class="segmented sms-provider" role="radiogroup" aria-label="短信服务商">
        <button role="radio" :aria-checked="sm.provider === 'tencent'" :class="{ on: sm.provider === 'tencent' }" @click="sm.provider = 'tencent'">腾讯云短信</button>
        <button role="radio" :aria-checked="sm.provider === 'aliyun'" :class="{ on: sm.provider === 'aliyun' }" @click="sm.provider = 'aliyun'">阿里云短信</button></div>
      <p v-if="sm.provider === 'tencent'">用「设置 → 腾讯云」里的密钥发送（子账号要有 QcloudSMSFullAccess 权限）。先在腾讯云「短信」控制台创建应用、申请签名和「验证码」类正文模板，审核通过后填到这里。</p>
      <p v-else>用一个阿里云 RAM 用户的 AccessKey 发送（只给它 AliyunDysmsFullAccess 权限，不要用主账号的）。先在阿里云「短信服务」控制台申请签名和「验证码」类模板，审核通过后填到这里。</p>
      <div class="notice" v-if="sm.provider === 'tencent' && !acct.sms.keys"><ui-icon name="warn" class="st-warn"></ui-icon>还没有填写腾讯云密钥：先在上面的「腾讯云」里填好，或者改用阿里云短信。</div>
      <div class="group" v-if="sm.provider === 'tencent'">
        <div class="row form"><span class="k">SDK AppID</span><span class="v"><input v-model="sm.appId" placeholder="1400000000" inputmode="numeric" aria-label="SDK AppID"><span class="small secondary block">「应用管理 → 应用列表」里</span></span></div>
        <div class="row form"><span class="k">签名</span><span class="v"><input v-model="sm.sign" placeholder="喵面板" aria-label="签名"><span class="small secondary block">审核通过的签名内容，不带【】</span></span></div>
        <div class="row form"><span class="k">模板 ID</span><span class="v"><input v-model="sm.template" placeholder="1234567" inputmode="numeric" aria-label="模板 ID"></span></div>
        <div class="row form"><span class="k">模板里的变量</span><span class="v"><span class="segmented"><button :class="{ on: sm.params === 1 }" @click="sm.params = 1">1 个：验证码</button><button :class="{ on: sm.params === 2 }" @click="sm.params = 2">2 个：验证码、分钟数</button></span>
          <span class="small secondary block">例如「您的验证码为：{1}，{2}分钟内有效，请勿泄露。」是 2 个</span></span></div>
        <div class="row form"><span class="k">地域</span><span class="v"><select v-model="sm.region" aria-label="地域"><option v-for="r in SMS_REGIONS" :key="r[0]" :value="r[0]">{{ r[1] }}（{{ r[0] }}）</option></select></span></div>
      </div>
      <div class="group" v-else>
        <div class="row form"><span class="k">AccessKey ID</span><span class="v"><input v-model="sm.accessKeyId" placeholder="LTAI…" autocomplete="off" aria-label="AccessKey ID" autocapitalize="off" spellcheck="false"></span></div>
        <div class="row form"><span class="k">AccessKey Secret</span><span class="v"><input type="password" v-model="sm.accessKeySecret" autocomplete="new-password"
          :placeholder="acct.sms.hasSecret && acct.sms.provider === 'aliyun' ? '已保存，不修改就留空' : '创建 AccessKey 时只显示一次'" aria-label="AccessKey Secret"></span></div>
        <div class="row form"><span class="k">签名</span><span class="v"><input v-model="sm.sign" placeholder="喵面板" aria-label="签名"><span class="small secondary block">审核通过的签名名称</span></span></div>
        <div class="row form"><span class="k">模板 CODE</span><span class="v"><input v-model="sm.template" placeholder="SMS_123456789" aria-label="模板 CODE" autocapitalize="off" spellcheck="false"></span></div>
        <div class="row form"><span class="k">验证码变量</span><span class="v"><input v-model="sm.codeVar" placeholder="code" aria-label="验证码变量名" autocapitalize="off" spellcheck="false">
          <span class="small secondary block">模板「您的验证码为：\${code}，请勿泄露」里是 code</span></span></div>
        <div class="row form"><span class="k">分钟数变量</span><span class="v"><input v-model="sm.minutesVar" placeholder="模板里没有就不填" aria-label="分钟数变量名" autocapitalize="off" spellcheck="false">
          <span class="small secondary block">模板里有「\${min} 分钟内有效」这样的变量才填（这里就填 min）。验证码 10 分钟内有效，模板里直接写了分钟数的，请写 10 分钟</span></span></div>
      </div>
      <div class="group">
        <div class="row form"><span class="k">每天最多发</span><span class="v"><input type="number" v-model.number="sm.dailyLimit" min="1" max="500" aria-label="每天最多发多少条">
          <span class="small secondary block">条。短信按条收费，防止被人刷</span></span></div>
        <div class="row form"><span class="k">发一条测试短信</span><span class="v login-send"><input v-model="sm.phone" type="tel" placeholder="手机号" aria-label="测试手机号">
          <button @click="smsRun('test')" :disabled="!!sm.busy || !smsFilled || !sm.phone">{{ sm.busy === 'test' ? '正在发送……' : '发送' }}</button></span></div>
      </div>
      <div class="group"><div class="row form"><span class="k">账号密码</span><span class="v"><input type="password" v-model="sm.current" autocomplete="current-password" placeholder="保存或清除要输入 Miao Panel 的登录密码" aria-label="账号密码"></span></div></div>
      <div class="notice" v-if="sm.error"><ui-icon name="alert" class="st-crit"></ui-icon>{{ sm.error }}</div>
      <div class="notice" v-if="sm.ok"><ui-icon name="check" class="st-ok"></ui-icon>{{ sm.ok }}</div>
      <div class="sheet-actions"><button class="plain destructive" v-if="acct.sms.configured" @click="smsRun('clear')" :disabled="!!sm.busy || !sm.current">清除设置</button><span class="grow"></span>
        <button @click="sm.open = false" :disabled="!!sm.busy">取消</button>
        <button class="primary" @click="smsRun('save')" :disabled="!!sm.busy || !sm.current || !smsFilled">{{ sm.busy === 'save' ? '正在保存……' : '保存' }}</button></div>
    </div>
  </div>

  <div class="group-title" v-if="acct">登录的设备</div>
  <div class="group" v-if="acct">
    <div class="row" v-for="s in acct.sessions" :key="s.key">
      <ui-icon name="server"></ui-icon>
      <div class="grow"><div>{{ uaText(s.ua) }} <span class="tag on" v-if="s.current">当前</span></div>
        <div class="small tertiary">{{ s.ip }} · 最近使用 {{ when(s.seenAt) }} · 登录于 {{ when(s.createdAt) }}</div></div>
      <button class="small" v-if="!s.current" @click="endSession(s)">退出</button>
    </div>
    <div class="row"><span class="grow small tertiary">3 天没有使用，或者登录满 30 天，会自动退出。</span><button class="small destructive" @click="logout">退出登录</button></div>
  </div>`,
};

const app = createApp({
  setup() {
    const tab = ref('home');
    // On a phone the sidebar is a drawer behind the menu button.
    const navOpen = ref(false);
    const navEl = ref(null), navBtn = ref(null);
    watch(navOpen, open => {
      document.body.classList.toggle('nav-open', open);
      nextTick(() => { if (open && navEl.value) navEl.value.focus({ preventScroll: true }); });
    });
    function closeNav() {
      if (!navOpen.value) return;
      navOpen.value = false;
      if (navBtn.value && navBtn.value.getClientRects().length) navBtn.value.focus(); // shown only on phones
    }
    window.addEventListener('keydown', e => { if (e.key === 'Escape' && navOpen.value) closeNav(); });
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
    // select opens a server's page; stay only loads it, for the first one at
    // startup when you have already gone to another page.
    async function select(id, stay) {
      navOpen.value = false;
      if (!stay) tab.value = 'servers';
      if (selectedId.value !== id) current.value = null;
      selectedId.value = id;
      cloud.value = null;
      if (tc.configured) {
        api('GET', `/api/servers/${id}/cloud`).then(r => { if (selectedId.value === id) cloud.value = r.instance || null; }).catch(() => {});
      }
      // Another server may be picked while these load; its page wins.
      try {
        const prof = await api('GET', `/api/servers/${id}/profile`);
        if (selectedId.value !== id) return;
        current.value = prof;
        if (prof.server.adapter === '1panel') {
          const o = await api('GET', `/api/servers/${id}/onepanel`);
          if (selectedId.value === id) Object.assign(op, o, { apiKey: '', info: '' });
        }
      } catch (e) { if (selectedId.value === id) notify(e.message, 'error'); }
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
    const termRequest = ref(null);
    // The web edition's logged-in account.
    const me = window.MIAO_USER;
    async function logout() {
      try { await api('POST', '/api/auth/logout'); } catch { /* logged out anyway */ }
      location.reload();
    }
    function openTerminal(serverId) { termRequest.value = { serverId, at: Date.now() }; go('terminal'); }
    const filesRequest = ref(null);
    function openFiles(serverId, path) { filesRequest.value = { serverId, path, at: Date.now() }; go('files'); }
    const sitesRequest = ref(null);
    function openSite(serverId, siteId, section) { sitesRequest.value = { serverId, siteId, section, at: Date.now() }; go('sites'); }
    function newSite() { sitesRequest.value = { create: true, at: Date.now() }; go('sites'); }
    // 网站统计 shows the access logs or EdgeOne; remembered per viewer.
    const statsView = ref((() => { try { return localStorage.getItem('miao.statsView') || 'logs'; } catch { return 'logs'; } })());
    const statsSeen = reactive({ [statsView.value]: true });
    watch(statsView, v => { statsSeen[v] = true; try { localStorage.setItem('miao.statsView', v); } catch { /* not kept */ } });
    // The AI side panel: the same conversation, opened over any page with
    // ⌘J / Ctrl+J; a question asked there says which page it came from.
    const aiPanel = ref(false);
    const siteContext = ref('');
    const PAGE_NAMES = { home: '总览', chat: 'AI 助手', inbox: '待处理', certs: '证书', dns: '解析', storage: '存储', terminal: '终端', files: '文件', logs: '记录', settings: '设置', sites: '网站管理', cloud: '腾讯云 · 云服务器' };
    const pageContext = computed(() => {
      const t = tab.value;
      if (t === 'servers') {
        if (!current.value) return '服务器';
        if (serverTab.value === 'sites' && siteContext.value) return siteContext.value;
        const part = serverTab.value === 'overview' ? '' : (SERVER_TABS.find(x => x.id === serverTab.value) || { text: '' }).text;
        return '服务器 ' + current.value.server.name + (part ? ' · ' + part : '');
      }
      if (t === 'sites') return siteContext.value || '网站管理';
      if (t === 'stats') return statsView.value === 'eo' ? 'EdgeOne 实时统计' : (visitSection.value === 'security' ? '访问统计 · 安全' : '访问统计');
      return PAGE_NAMES[t] || '';
    });
    function toggleAI(open) {
      if (tab.value === 'chat') return;
      aiPanel.value = open == null ? !aiPanel.value : open;
      if (aiPanel.value) nextTick(() => { const el = document.querySelector('.ai-host.panel textarea'); if (el) el.focus(); });
    }
    const palette = ref(false);
    const modKey = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? '⌘' : 'Ctrl ';
    window.addEventListener('keydown', e => {
      if ((e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && (e.key === 'j' || e.key === 'J')) { e.preventDefault(); toggleAI(); }
      if ((e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && (e.key === 'k' || e.key === 'K')) { e.preventDefault(); palette.value = !palette.value; }
      if (e.key === 'Escape' && aiPanel.value && !document.querySelector('.sheet-mask')) aiPanel.value = false;
    });
    watch(tab, t => { if (t === 'chat') aiPanel.value = false; });

    // 建议 and 通知 are parts of 待处理 now.
    const inboxFocus = ref(null);
    function openInbox(view) { inboxFocus.value = { view, at: Date.now() }; go('inbox'); }
    function go(id) {
      if (id === 'plans' || id === 'notices') { inboxFocus.value = { view: id, at: Date.now() }; id = 'inbox'; }
      navOpen.value = false;
      tab.value = id;
      seen[id] = true;
      if (id === 'home') loadOverview();
      if (id === 'plans') api('GET', '/api/plans').then(v => { plans.value = v; }).catch(e => notify(e.message, 'error'));
      if (id === 'logs') loadAudit();
    }
    function loadAudit() { api('GET', '/api/audit').then(v => { audit.value = v; }).catch(e => notify(e.message, 'error')); }
    function openLog(id) { logView.value = 'exec'; logFocus.value = id; tab.value = 'logs'; }
    provide('openLog', openLog);
    provide('loadSpend', () => loadSpend().catch(() => {}));

    function openAdd() {
      navOpen.value = false;
      Object.assign(addForm, { name: '', host: '', port: 22, username: 'root', authKind: 'password', password: '', keyPath: '', keyText: '',
        keySource: info.value.mode === 'server' ? 'text' : 'path', keyPassphrase: '', instanceId: '', region: '' });
      showAdd.value = true;
      cloudPick.value = '';
      if (tc.configured) api('GET', '/api/tencent/servers').then(r => { cloudList.value = (r.servers || []).filter(s => s.publicIPs && s.publicIPs.length); }).catch(() => {});
    }
    // 添加 on the 云服务器 page: the add dialog, filled in from the instance.
    function addFromCloud(inst) {
      openAdd();
      if (!cloudList.value.some(x => x.id === inst.id)) cloudList.value = [...cloudList.value, inst];
      cloudPick.value = inst.id;
      pickCloud();
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
    // While an answer is still coming, the question waits in the box.
    function askAI(text) {
      draft.value = text; tab.value = 'chat';
      if (chatBusy.value) notify('AI 还在回答上一个问题，这个问题先放在输入框里，答完再发送');
      else newChat();
    }
    const daysTo = t => t ? Math.floor((new Date(t) - Date.now()) / 86400000) : null;
    async function addServer() {
      await guarded('正在添加并测试连接……', async () => {
        const body = { ...addForm };
        if (body.keySource === 'text' || info.value.mode === 'server') body.keyPath = ''; else body.keyText = '';
        const sv = await api('POST', '/api/servers', body);
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
        const id = selectedId.value;
        const prof = await api('POST', `/api/servers/${id}/discover`);
        if (selectedId.value === id) current.value = prof;
        await loadServers();
        notify('识别完成');
      });
    }
    async function removeServer() {
      // The name and the id come from the same place, the selected server.
      const id = selectedId.value, sv = servers.value.find(x => x.id === id);
      if (!sv || !confirm(`确定要从 Miao Panel 里删除「${sv.name}」吗？\n只是不再管理，不会影响服务器本身。`)) return;
      await guarded('正在删除……', async () => {
        await api('DELETE', `/api/servers/${id}`);
        current.value = null; selectedId.value = null;
        await loadServers();
        if (servers.value.length) await select(servers.value[0].id, tab.value !== 'servers');
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
          body: JSON.stringify({ conversationId: conversationId.value, message: text, page: aiPanel.value && tab.value !== 'chat' ? pageContext.value : '' }),
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
      certificates: '查看 HTTPS 证书', tencent_eo_security: '查看 EdgeOne 安全防护', panel_websites: '查看 1Panel 网站',
      site_visits: '统计网站访问日志' }[t] || t);
    // What a lookup is about, in a few words: the server, domain, checks.
    function toolDetail(tool, args) {
      let a;
      try { a = JSON.parse(args || '{}'); } catch { return ''; }
      if (!a || typeof a !== 'object') return '';
      const parts = [];
      if (a.server_id != null) parts.push(serverName(a.server_id));
      for (const k of ['domain', 'site', 'zone', 'instance', 'title']) if (a[k]) parts.push(String(a[k]));
      if (a.days) parts.push(a.days === 1 ? '今天' : `${a.days} 天`);
      if (Array.isArray(a.checks) && a.checks.length) parts.push(a.checks.join('、'));
      return parts.join(' · ');
    }
    const actorName = a => ({ user: '你', ai: 'AI', system: '系统', auto: '自动' }[a] || a);
    const actionName = a => ({ 'server.add': '添加服务器', 'server.delete': '删除服务器', 'server.test': '测试连接', 'server.discover': '识别环境',
      'server.hostkey.recorded': '记录服务器指纹', 'settings.ai': '修改 AI 设置', 'ai.chat': 'AI 对话', 'plan.propose': 'AI 生成清单',
      'plan.execute': '执行清单', 'plan.step': '执行步骤', 'plan.undo': '撤销步骤', 'exec.rollback': '回滚', 'onepanel.settings': '修改 1Panel 接口设置', 'settings.tencent': '修改腾讯云密钥',
      'terminal.open': '打开终端', 'terminal.close': '关闭终端', 'settings.autoblock': '修改自动封禁', 'settings.notices': '修改通知设置',
      'settings.webhook': '修改推送地址', 'visits.judge': 'AI 研判 IP',
      'auth.setup': '创建管理员账号', 'auth.login': '登录', 'auth.fail': '登录失败', 'auth.password': '修改密码', 'auth.reset': '命令行重设密码', 'auth.totp': '两步验证',
      'auth.methods': '修改登录方式', 'auth.scope': '修改验证码发送范围', 'auth.bind': '绑定邮箱或手机号', 'auth.code': '发送登录验证码', 'settings.mail': '修改发信邮箱', 'settings.sms': '修改短信设置',
      'cos.upload': '上传到存储桶', 'cos.mkdir': '存储桶新建文件夹', 'cos.delete': '删除存储桶文件', 'cos.rename': '存储桶文件改名', 'cos.link': '生成存储桶文件链接' }[a] || a);
    // Unread notices, for the sidebar; checked every minute.
    const unread = ref(0);
    const loadUnread = () => api('GET', '/api/notices/unread').then(v => { unread.value = v.unread; }).catch(() => {});
    setInterval(() => { if (document.visibilityState === 'visible' && tab.value !== 'notices' && tab.value !== 'inbox') loadUnread(); }, 60000);
    // The overview: how each server is doing, and the counts in the sidebar.
    const overview = ref(null);
    const loadOverview = () => api('GET', '/api/overview').then(v => { overview.value = v; unread.value = v.unread; }).catch(() => {});
    setInterval(() => { if (document.visibilityState === 'visible') loadOverview(); }, 120000);
    const inboxCount = computed(() => (overview.value ? overview.value.pending : 0) + unread.value);
    const serverState = id => overview.value && overview.value.servers.find(x => x.id === id);
    const serverDot = id => ({ ok: 'good', warn: 'warn', crit: 'crit' }[(serverState(id) || {}).level] || 'off');
    function serverMeta(s) {
      const o = serverState(s.id);
      const kind = adapterName(s.adapter);
      if (!o) return kind + ' · ' + s.host;
      if (o.level === 'warn' || o.level === 'crit') return kind + ' · ' + o.note;
      if (o.memPct != null) return kind + ' · 内存 ' + o.memPct + '%';
      return kind + ' · ' + (o.note || s.host);
    }
    // The server workbench's tab; the 网站 tab stays open once shown.
    const serverTab = ref('overview');
    const seenServerSites = ref(false);
    watch(serverTab, t => { if (t === 'sites') seenServerSites.value = true; });
    const serverSitesRequest = ref(null);
    function openServerSite(siteId) {
      serverTab.value = 'sites';
      serverSitesRequest.value = { serverId: selectedId.value, siteId };
    }
    function serverStateText(id) {
      const o = serverState(id);
      if (!o || o.level === 'unknown') return '还没有识别';
      return { ok: '运行正常', warn: '需要注意', crit: '有严重问题' }[o.level] || '';
    }
    // The line of facts under the server's name.
    const serverFacts = computed(() => {
      const c = current.value;
      if (!c) return [];
      const x = c.profile, k = cloud.value, out = [];
      if (x && x.os) out.push({ k: '系统', v: x.os });
      if (x) out.push({ k: '面板', v: adapterName(x.adapter) + (x.panel && x.panel.version ? ' ' + x.panel.version : '') });
      const spec = [];
      if (k) spec.push(k.cpu + ' 核', k.memoryGB + ' GB 内存', k.diskGB + ' GB 硬盘');
      else if (x) {
        if (x.cpuCores) spec.push(x.cpuCores + ' 核');
        if (x.memory && x.memory.totalMB) spec.push(gbText(x.memory.totalMB) + ' 内存');
        const d = (x.disks || []).find(d => d.mount === '/');
        if (d) spec.push(d.size + ' 硬盘');
      }
      if (spec.length) out.push({ k: '配置', v: spec.join(' · ') });
      if (k) out.push({ k: '位置', v: (k.kind === 'lighthouse' ? '腾讯云轻量' : '腾讯云 CVM') + ' · ' + k.regionName });
      out.push({ k: 'IP', v: c.server.host });
      if (k && k.expiredTime) {
        const d = daysTo(k.expiredTime);
        out.push({ k: '到期', v: new Date(k.expiredTime).toLocaleDateString('zh-CN') + (d < 30 ? (d < 0 ? '（已过期）' : `（还剩 ${d} 天）`) : ''), warn: d < 15 });
      }
      return out;
    });
    // 腾讯云 › 云服务器, opened on one instance.
    const cloudRequest = ref(null);
    function openCloud(c) { cloudRequest.value = { region: c.region, id: c.id }; go('cloud'); }
    // 网站 › 访问统计 and 安全, 腾讯云 › EdgeOne: parts of the statistics page.
    const visitSection = ref(pref('miao.visitSection', 'overview'));
    const statsRequest = ref(null);
    function openStats(view, section) {
      statsView.value = view;
      if (section) { visitSection.value = section; statsRequest.value = { section, at: Date.now() }; }
      go('stats');
    }

    onMounted(async () => {
      try {
        info.value = await api('GET', '/api/info');
        presets.value = await api('GET', '/api/ai/presets');
        await Promise.all([loadServers(), loadAI(), loadSpend(), loadConvs(), loadTencent(), loadFree(), loadUnread(), loadOverview()]);
        if (convs.value.length) await openConv(convs.value[0].id);
        if (servers.value.length) await select(servers.value[0].id, tab.value !== 'servers');
      } catch (e) { notify(e.message, 'error'); }
    });

    return {
      tab, go, navOpen, navEl, navBtn, closeNav, servers, selectedId, current, p, busy, busyText, toast, info, ai, presets, aiForm, presetNote,
      spendText, plans, audit, logView, logFocus, loadAudit, openLog, showAdd, addForm, messages, draft, chatBusy, msgBox, suggestions,
      select, openAdd, addServer, testConn, discover, removeServer, askAbout, send, onEnter, newChat, applyPreset, saveAI, testAI,
      convs, showConvs, conversationId, openConv, deleteConv, relTime,
      op, saveOnePanel, testOnePanel, tc, saveTencent, testTencent, clearTencent, freeCmd, setFree, seen, statsView, statsSeen, termRequest, openTerminal, filesRequest, openFiles, sitesRequest, openSite, newSite, unread, me, logout,
      overview, loadOverview, inboxCount, inboxFocus, openInbox, aiPanel, toggleAI, pageContext, siteContext, palette, modKey, serverDot, serverMeta, visitSection, statsRequest, openStats,
      cloud, cloudList, cloudPick, pickCloud, askAI, daysTo, fmtBytes,
      SERVER_TABS, serverTab, seenServerSites, serverSitesRequest, openServerSite, serverStateText, serverFacts, cloudRequest, openCloud, addFromCloud,
      memPct, rootDisk, envSub, dockerText, money, mb, meterClass, levelClass, levelIcon, levelName, riskName, adapterName,
      fmtTime, serverName, parseSteps, toolName, toolDetail, actorName, actionName, md, live, liveStatus, thinkTail, stopAnswer,
    };
  },
});

app.component('plan-card', PlanCard);
app.component('exec-log', ExecLog);
app.component('line-chart', LineChart);
app.component('eo-stats', EoStats);
app.component('notice-page', NoticePage);
app.component('visit-stats', VisitStats);
app.component('rank-list', RankList);
app.component('terminal-page', TerminalPage);
app.component('cert-page', CertPage);
app.component('dns-page', DnsPage);
app.component('site-page', SitePage);
app.component('home-page', HomePage);
app.component('server-ring', ServerRing);
app.component('server-overview', ServerOverview);
app.component('server-apps', ServerApps);
app.component('cloud-page', CloudPage);
app.component('command-palette', CommandPalette);
app.component('inbox-page', InboxPage);
app.component('eo-cache-form', EoCacheForm);
app.component('storage-page', StoragePage);
app.component('file-page', FilePage);
const UiIcon = {
  props: { name: { type: String, required: true } },
  setup(props) { return { d: computed(() => ICONS[props.name] || '') }; },
  template: '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path :d="d"></path></svg>',
};
app.component('ui-icon', UiIcon);
app.component('miao-logo', MiaoLogo);
app.component('account-panel', AccountPanel);

// The web edition shows the login page until someone is logged in; the
// desktop goes straight in.
(async () => {
  let state = { mode: 'desktop' };
  try { state = await api('GET', '/api/auth/state'); } catch { /* an older desktop build */ }
  window.MIAO_MODE = state.mode;
  window.MIAO_USER = state.user || null;
  if (state.mode === 'server' && !state.user) {
    const el = document.getElementById('app');
    el.className = '';
    const login = createApp(LoginApp, { state });
    login.component('ui-icon', UiIcon);
    login.component('miao-logo', MiaoLogo);
    login.mount(el);
    return;
  }
  app.mount('#app');
})();

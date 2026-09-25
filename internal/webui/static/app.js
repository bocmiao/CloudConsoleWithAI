/* Miao Panel web UI. Plain Vue 3 (global build), no build step. */
const { createApp, ref, reactive, computed, onMounted, nextTick } = Vue;

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

createApp({
  setup() {
    const tabs = [
      { id: 'servers', name: '服务器' }, { id: 'chat', name: 'AI 助手' },
      { id: 'plans', name: '建议' }, { id: 'audit', name: '操作记录' }, { id: 'settings', name: '设置' },
    ];
    const tab = ref('servers');
    const servers = ref([]);
    const selectedId = ref(null);
    const current = ref(null);
    const busy = ref(false);
    const busyText = ref('');
    const toast = reactive({ text: '', kind: 'ok' });
    const info = ref({});
    const ai = ref({ hasKey: false });
    const presets = ref([]);
    const aiForm = reactive({});
    const spend = ref({});
    const plans = ref([]);
    const audit = ref([]);
    const showAdd = ref(false);
    const addForm = reactive({});
    const messages = ref([]);
    const conversationId = ref('');
    const draft = ref('');
    const chatBusy = ref(false);
    const msgBox = ref(null);
    const suggestions = [
      '帮我看看服务器现在的整体状况',
      '服务器内存占用是不是太高了？',
      '帮我做一次安全体检',
      '网站打不开，帮我排查一下',
    ];

    let toastTimer = null;
    function notify(text, kind = 'ok') {
      toast.text = text; toast.kind = kind;
      clearTimeout(toastTimer);
      toastTimer = setTimeout(() => { toast.text = ''; }, kind === 'error' ? 8000 : 3500);
    }
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
      selectedId.value = id;
      current.value = null;
      try { current.value = await api('GET', `/api/servers/${id}/profile`); } catch (e) { notify(e.message, 'error'); }
    }
    function go(id) {
      tab.value = id;
      if (id === 'plans') api('GET', '/api/plans').then(v => { plans.value = v; }).catch(e => notify(e.message, 'error'));
      if (id === 'audit') api('GET', '/api/audit').then(v => { audit.value = v; }).catch(e => notify(e.message, 'error'));
    }

    function openAdd() {
      Object.assign(addForm, { name: '', host: '', port: 22, username: 'root', authKind: 'password', password: '', keyPath: '', keyPassphrase: '' });
      showAdd.value = true; tab.value = 'servers';
    }
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
      if (!confirm(`确定要从 Miao Panel 里删除「${current.value.server.name}」吗？（只是不再管理，不会影响服务器本身）`)) return;
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

    async function send() {
      const text = draft.value.trim();
      if (!text || chatBusy.value) return;
      messages.value.push({ role: 'user', text });
      draft.value = '';
      chatBusy.value = true;
      scrollChat();
      try {
        const r = await api('POST', '/api/chat', { conversationId: conversationId.value, message: text });
        conversationId.value = r.conversationId;
        messages.value.push({ role: 'assistant', text: r.reply.text, steps: r.reply.steps || [], usage: r.reply.usage, cost: r.cost, currency: r.currency });
        loadSpend().catch(() => {});
      } catch (e) {
        messages.value.push({ role: 'error', text: e.message });
      } finally {
        chatBusy.value = false;
        scrollChat();
      }
    }
    function newChat() { messages.value = []; conversationId.value = ''; }
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
    const dockerText = computed(() => {
      const d = p.value && p.value.docker;
      if (!d || !d.status) return '未知';
      if (d.status === 'no') return '没有安装';
      if (d.status === 'unreachable') return '已安装，但服务没有运行';
      return `版本 ${d.status}，${(d.containers || []).length} 个容器`;
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
    const mb = v => v >= 1024 ? (v / 1024).toFixed(1) + 'G' : (v || 0) + 'M';
    const pctClass = v => v >= 90 ? 'danger' : v >= 80 ? 'warn' : 'good';
    const levelName = l => ({ danger: '严重', warn: '注意', info: '提示' }[l] || l);
    const riskClass = r => ({ R0: 'info', R1: 'good', R2: 'warn', R3: 'danger' }[r] || 'info');
    const adapterName = a => ({ '1panel': '1Panel', bt: '宝塔', linux: '纯 Linux' }[a] || '未识别');
    const fmtTime = t => t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '';
    const serverName = id => (servers.value.find(s => s.id === id) || { name: `服务器 ${id}` }).name;
    const parseSteps = s => { try { return JSON.parse(s); } catch { return []; } };
    const toolName = t => ({ list_servers: '查看服务器列表', get_server_profile: '读取服务器画像', refresh_server_profile: '重新识别服务器',
      run_check: '执行只读检查', propose_plan: '保存修改建议' }[t] || t);
    const actorName = a => ({ user: '你', ai: 'AI', system: '系统' }[a] || a);
    const actionName = a => ({ 'server.add': '添加服务器', 'server.delete': '删除服务器', 'server.test': '测试连接', 'server.discover': '识别环境',
      'server.hostkey.recorded': '记录服务器指纹', 'settings.ai': '修改 AI 设置', 'ai.chat': 'AI 对话', 'plan.propose': 'AI 提出建议' }[a] || a);

    onMounted(async () => {
      try {
        info.value = await api('GET', '/api/info');
        presets.value = await api('GET', '/api/ai/presets');
        await Promise.all([loadServers(), loadAI(), loadSpend()]);
        if (servers.value.length) await select(servers.value[0].id);
      } catch (e) { notify(e.message, 'error'); }
    });

    return {
      tabs, tab, go, servers, selectedId, current, p, busy, busyText, toast, info, ai, presets, aiForm, presetNote,
      spendText, plans, audit, showAdd, addForm, messages, draft, chatBusy, msgBox, suggestions,
      select, openAdd, addServer, testConn, discover, removeServer, askAbout, send, newChat, applyPreset, saveAI, testAI,
      memPct, dockerText, money, mb, pctClass, levelName, riskClass, adapterName, fmtTime, serverName, parseSteps,
      toolName, actorName, actionName, md,
    };
  },
}).mount('#app');

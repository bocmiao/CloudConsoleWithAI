// Local UI geometry regression checks. No credentials or live services are used.
// npm install --no-save playwright; node scripts/layout_smoke.mjs [output-directory]
import { createServer } from 'node:http';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { resolve, extname } from 'node:path';
import { pathToFileURL } from 'node:url';
import assert from 'node:assert/strict';
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ? pathToFileURL(process.env.PLAYWRIGHT_MODULE).href : 'playwright');
const root = resolve('internal/webui/static');
const output = resolve(process.argv[2] || 'layout-review');
await mkdir(output, { recursive: true });
const serverInfo = { id: 'layout-server', name: '生产环境-web-01.example.com', host: '192.0.2.1', adapter: 'linux', authKind: 'key', port: 22, username: 'admin' };
const overview = {
  servers: [{ ...serverInfo, level: 'warn', note: 'SSH 允许密码登录', cpuPct: 27, memPct: 53, diskPct: 42 }],
  visits: { pv: 1306, pvBefore: 6000, sites: 16, title: '测试日志', hours: [30, 15, 20, 10, 12, 14, 10, 12, 20, 40, 150, 145, 55, 25, 18, 20, 25, 22, 20, 30, 20, 0, 0, 0] },
  certs: { total: 2, attention: 0 }, security: { known: true, high: 0, unblocked: 0 }, unread: 1,
  todo: Array.from({ length: 5 }, (_, i) => ({ id: String(i), kind: 'plan', level: 'plan', title: i === 2 ? '清单等你确认：给 Halo 和 MySQL 设内存上限（需先固定 JVM 堆，否则 Halo 会出现问题）' : '清单等你确认：只读查看服务器的配置与运行状态', meta: '11-04 12:00', action: '查看清单' })),
  changes: Array.from({ length: 5 }, (_, i) => ({ id: String(i), title: '添加 very-long-subdomain.example.com 的 TXT 解析记录', serverName: '腾讯云', status: 'done', startedAt: '2026-11-04T12:00:00Z' }))
};
const fixtures = {
  '/api/auth/state': { mode: 'desktop' }, '/api/info': {}, '/api/ai/presets': [],
  '/api/servers': [serverInfo], '/api/settings/ai': { hasKey: true }, '/api/usage': {},
  '/api/conversations': [], '/api/settings/tencent': {}, '/api/settings/aliyun': {},
  '/api/settings/free-command': {}, '/api/notices/unread': { unread: 1 }, '/api/overview': overview,
  '/api/servers/layout-server/cloud': {}, '/api/servers/layout-server/profile': { server: serverInfo, profile: null },
  '/api/plans': [], '/api/exec': [], '/api/audit': [], '/api/terminals': [],
  '/api/websites': { servers: [], errors: [] }, '/api/certificates': { certificates: [], errors: [] },
  '/api/visits/sources': [], '/api/visits/blocked': [], '/api/baize/settings': {},
  '/api/monitor': { sites: [], servers: [], settings: {} }, '/api/statuspage': {},
  '/api/cloud/alarms': { alarms: [] }, '/api/notices': { notices: [], settings: {} },
  '/api/update': {}, '/api/preload': {}, '/api/eo/sites': { sites: [] },
};
const missing = new Set();
const server = createServer(async (req, res) => {
  const pathname = new URL(req.url, 'http://localhost').pathname;
  if (pathname.startsWith('/api/')) {
    res.setHeader('Content-Type', 'application/json');
    if (req.method !== 'GET') { res.writeHead(405); res.end('{}'); return; }
    if (!(pathname in fixtures)) { missing.add(pathname); res.writeHead(503); res.end('{"error":"本地布局测试未连接服务"}'); return; }
    res.end(JSON.stringify(fixtures[pathname])); return;
  }
  try {
    const file = resolve(root, '.' + (pathname === '/' ? '/index.html' : pathname));
    if (!file.startsWith(root)) throw new Error('path');
    res.setHeader('Content-Type', ({ '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' })[extname(file)] || 'application/octet-stream');
    res.end(await readFile(file));
  } catch { res.writeHead(404); res.end(); }
});
await new Promise(r => server.listen(0, '127.0.0.1', r));
const browser = await chromium.launch({ headless: true });
const results = [], errors = [];
const inspect = page => page.evaluate(() => {
  const rect = el => { const b = el.getBoundingClientRect(); return { x: b.x, y: b.y, w: b.width, h: b.height, right: b.right, bottom: b.bottom }; };
  const main = document.querySelector('.main');
  const visible = el => el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden';
  const page = [...document.querySelectorAll('.page')].find(visible);
  const heading = [...document.querySelectorAll('.toolbar h1')].find(visible);
  return {
    documentOverflow: document.documentElement.scrollWidth > innerWidth + 1,
    mainOverflow: main.scrollWidth > main.clientWidth + 1,
    page: page && rect(page),
    contentLeft: page && page.getBoundingClientRect().x + parseFloat(getComputedStyle(page).paddingLeft),
    heading: heading && rect(heading),
    formWidths: [...document.querySelectorAll('.row.form .v input')].filter(visible).map(el => el.getBoundingClientRect().width),
    home: document.querySelector('.home') && rect(document.querySelector('.home')),
    tiles: [...document.querySelectorAll('.home-card')].map(rect),
    rows: [...document.querySelectorAll('.home-row')].map(el => [...el.children].map(rect)),
    serverColumns: [...document.querySelectorAll('.srv-grid')].map(el => [...el.children].map(rect)),
    overflowing: [...document.querySelectorAll('.main *')].filter(el => visible(el) && !el.closest('.table-wrap, .fm-list, .subtabs, .segmented, pre') && el.getBoundingClientRect().right > main.getBoundingClientRect().right + 1).slice(0, 8).map(el => el.className),
  };
});
try {
  for (const variant of ['classic', 'glass-compact-dark']) for (const width of [1920, 1440, 1280, 1100, 1024, 768, 720, 390, 320]) {
    const page = await browser.newPage({ viewport: { width, height: 1080 }, reducedMotion: 'reduce', colorScheme: variant === 'classic' ? 'light' : 'dark' });
    page.on('pageerror', e => { errors.push({ width, error: e.message }); console.error(e.stack); });
    page.on('console', msg => { if (msg.type() === 'error' && !msg.text().includes('Failed to load resource')) errors.push({ width, variant, error: msg.text() }); });
    await page.addInitScript(variant => { localStorage.setItem('miao.skin', variant === 'classic' ? 'classic' : 'glass'); if (variant !== 'classic') localStorage.setItem('miao.density', 'compact'); }, variant);
    await page.goto(`http://127.0.0.1:${server.address().port}`);
    await page.waitForSelector('.home-card');
    // Delayed tile entrances still have a delay under reduced motion.
    await page.waitForTimeout(250);
    const home = await inspect(page);
    results.push({ width, variant, tab: 'home', ...home });
    if ([1920, 1024, 390].includes(width)) await page.screenshot({ path: resolve(output, `home-${variant}-${width}.png`), fullPage: true });
    if (width <= 720) {
      await page.getByRole('button', { name: /菜单/ }).click();
      assert.ok(await page.locator('.sidebar.open').isVisible());
      await page.locator('.nav-scrim').click({ position: { x: width - 5, y: 200 } });
      assert.equal(await page.locator('.sidebar.open').count(), 0);
    }
    for (const tab of ['servers', 'sites', 'stats', 'monitor', 'inbox', 'certs', 'cloud', 'cdn', 'dns', 'storage', 'terminal', 'files', 'logs', 'settings', 'chat']) {
      await page.evaluate(tab => app._container._vnode.component.proxy.go(tab), tab);
      await page.waitForTimeout(80);
      const geometry = await inspect(page);
      assert.ok(geometry.heading, `Missing page: ${tab}`);
      results.push({ width, variant, tab, ...geometry });
      if (tab === 'settings' && [1920, 390].includes(width)) await page.screenshot({ path: resolve(output, `settings-${variant}-${width}.png`), fullPage: true });
    }
    await page.evaluate(() => { const vm = app._container._vnode.component.proxy; vm.go('home'); vm.toggleAI(true); });
    await page.waitForTimeout(100);
    results.push({ width, variant, tab: 'home-ai', ...await inspect(page) });
    await page.getByRole('button', { name: '关闭 AI 侧栏', exact: true }).click();
    assert.equal(await page.locator('.ai-host.panel').count(), 0);
    await page.close();
  }
  await writeFile(resolve(output, 'results.json'), JSON.stringify({ results, errors, missing: [...missing] }, null, 2));
  console.log(JSON.stringify({ cases: results.length, errors, overflow: results.filter(r => r.documentOverflow || r.mainOverflow).map(r => ({ width: r.width, tab: r.tab })), missing: [...missing] }));
  if (!process.env.LAYOUT_BASELINE) {
    assert.equal(errors.length, 0, 'Browser runtime errors');
    assert.equal(results.filter(r => r.documentOverflow || r.mainOverflow).length, 0, 'Page overflow');
    for (const r of results.filter(r => r.tab === 'home')) {
      assert.ok(r.page.y < 100, `Sidebar displaced page at ${r.width}`);
      if (r.width >= 1400) {
        assert.ok(Math.abs(r.rows[0][1].x - r.tiles[2].x) < 1, 'Top cards share middle grid line');
        assert.ok(Math.abs(r.rows[1][1].x - r.tiles[2].x) < 1, 'Bottom cards share middle grid line');
      }
      for (const row of r.rows) if (row.every(b => Math.abs(b.y - row[0].y) < 1)) assert.ok(row.every(b => Math.abs(b.bottom - row[0].bottom) < 1), `Unequal cards at ${r.width}`);
      if (r.serverColumns.length > 1) for (let i = 0; i < 4; i++) assert.ok(Math.abs(r.serverColumns[0][i].x - r.serverColumns[1][i].x) < 1, `Server column ${i} at ${r.width}`);
    }
    for (const r of results.filter(r => r.page && r.width > 720 && r.tab !== 'home-ai')) assert.ok(Math.abs(r.heading.x - r.contentLeft) < 1, `Header gutter: ${r.width}/${r.tab}`);
    for (const r of results.filter(r => r.tab === 'settings')) assert.ok(r.formWidths.every(w => w >= 180), `Narrow settings fields: ${r.width}`);
  }
} finally { await browser.close(); server.close(); }

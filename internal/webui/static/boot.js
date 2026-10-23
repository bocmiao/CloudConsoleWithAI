// 启动前应用外观偏好（内联脚本会被 CSP 拦，所以放在这里）
try {
  if (localStorage.getItem('miao.density') === 'compact') document.documentElement.classList.add('compact');
} catch (e) { /* 隐私模式下没有 storage，用默认 */ }
try {
  // 默认玻璃拟态；只有显式选过「经典」才不加
  if (localStorage.getItem('miao.skin') !== 'classic') document.documentElement.classList.add('glass');
} catch (e) { /* 同上 */ }

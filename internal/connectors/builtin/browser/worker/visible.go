// SPDX-License-Identifier: AGPL-3.0-only

package worker

// visibleTextJS — текст, который видит человек: innerText отдаёт и то, что
// сайт спрятал стилями (полный номер полиса в подсказке с opacity: 0,
// обрезанный или нулевого размера элемент, прозрачный цвет) — такой текст
// агенту не показывается. Узлы обходятся сверху вниз, скрытое поддерево
// пропускается целиком; блоки — с новой строки, ячейки — через табуляцию.
const visibleTextJS = `(function g4visible(root) {
  if (!root) return '';
  const hidden = el => {
    const st = getComputedStyle(el);
    if (st.display === 'none' || st.visibility === 'hidden' || st.visibility === 'collapse') return true;
    if (parseFloat(st.opacity) === 0 || parseFloat(st.fontSize) === 0) return true;
    if (st.color === 'transparent' || /^rgba\(.*,\s*0\)$/.test(st.color)) return true;
    if (/^rect\(0(px)?,\s*0(px)?,\s*0(px)?,\s*0(px)?\)$/.test(st.clip || '') || /inset\(50%|circle\(0/.test(st.clipPath || '')) return true;
    if (/hidden|clip/.test(st.overflow + st.overflowX + st.overflowY)) {
      const r = el.getBoundingClientRect();
      if (r.width < 1 || r.height < 1) return true;
    }
    return false;
  };
  const skip = new Set(['script', 'style', 'noscript', 'template', 'head']);
  let out = '';
  const walk = n => {
    if (n.nodeType === 3) {
      const t = n.nodeValue;
      if (!t.trim()) { out += ' '; return; }
      const r = document.createRange(); r.selectNodeContents(n);
      const b = r.getBoundingClientRect();
      if (b.width < 1 || b.height < 1 || b.right + scrollX <= 0 || b.bottom + scrollY <= 0) return;
      out += t;
      return;
    }
    if (n.nodeType !== 1 || skip.has(n.localName) || hidden(n)) return;
    if (n.localName === 'br') { out += '\n'; return; }
    const d = getComputedStyle(n).display;
    const blk = /^(block|flex|grid|list-item|table|table-row|flow-root)$/.test(d);
    if (blk) out += '\n';
    if (d === 'table-cell') out += '\t';
    for (const c of n.childNodes) walk(c);
    if (blk) out += '\n';
  };
  walk(root);
  return out.split('\n').map(l => l.replace(/[  ]+/g, ' ').trim()).filter(Boolean).join('\n');
})`

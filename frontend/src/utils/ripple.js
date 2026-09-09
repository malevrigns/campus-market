// 按钮点击涟漪效果：在可点元素上绑定 onClick={ripple}（元素需带 .ripple 类）
export function ripple(e) {
  const el = e.currentTarget
  if (!el) return
  const rect = el.getBoundingClientRect()
  const d = Math.max(rect.width, rect.height)
  const span = document.createElement('span')
  span.className = 'ripple-ink'
  span.style.width = span.style.height = `${d}px`
  span.style.left = `${(e.clientX || rect.left + rect.width / 2) - rect.left - d / 2}px`
  span.style.top = `${(e.clientY || rect.top + rect.height / 2) - rect.top - d / 2}px`
  el.appendChild(span)
  setTimeout(() => span.remove(), 650)
}
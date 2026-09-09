import { useEffect, useLayoutEffect, useState } from 'react'
import { MoonOutlined, SunOutlined } from '@ant-design/icons'

export function useTheme() {
  const [mode, setMode] = useState(() => {
    try {
      return localStorage.getItem('campus-theme') === 'light' ? 'light' : 'dark'
    } catch {
      return 'dark'
    }
  })
  useLayoutEffect(() => {
    document.documentElement.dataset.theme = mode
    try {
      localStorage.setItem('campus-theme', mode)
    } catch {
      // Theme switching still works when browser storage is unavailable.
    }
  }, [mode])
  return [mode, () => setMode((value) => (value === 'dark' ? 'light' : 'dark'))]
}

export function ThemeButton({ mode, onToggle }) {
  const label = mode === 'dark' ? '切换到浅色主题' : '切换到深色主题'
  return (
    <button className="icon-button theme-toggle" aria-label={label} title={label} onClick={onToggle}>
      {mode === 'dark' ? <SunOutlined /> : <MoonOutlined />}
    </button>
  )
}

export function useReducedMotion() {
  const [reduced, setReduced] = useState(() => window.matchMedia('(prefers-reduced-motion: reduce)').matches)
  useEffect(() => {
    const preference = window.matchMedia('(prefers-reduced-motion: reduce)')
    const update = () => setReduced(preference.matches)
    preference.addEventListener('change', update)
    return () => preference.removeEventListener('change', update)
  }, [])
  return reduced
}

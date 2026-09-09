import { lazy, Suspense, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { CloseOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { Loading } from './ui'

const Agent = lazy(() => import('./Agent'))
const SIZE = 56
const GAP = 12
const POSITION_KEY = 'campus-assistant-position'

function viewport() {
  const visual = window.visualViewport
  const width = visual?.width || window.innerWidth
  const height = visual?.height || window.innerHeight
  const nav = document.querySelector('.mobile-nav')
  const bottom = nav && getComputedStyle(nav).display !== 'none' ? nav.getBoundingClientRect().height : 0
  return { width, height, bottom, left: visual?.offsetLeft || 0, top: visual?.offsetTop || 0 }
}

function clampPosition(position, avoidComposer = false) {
  const { width, height, bottom: navigationBottom, left, top } = viewport()
  const bottom = navigationBottom + (avoidComposer ? 160 : 0)
  return {
    x: Math.max(left + GAP, Math.min(position?.x ?? width - SIZE - 24, left + width - SIZE - GAP)),
    y: Math.max(
      top + GAP,
      Math.min(position?.y ?? height - bottom - SIZE - 24, top + height - bottom - SIZE - GAP),
    ),
  }
}

function readPosition(avoidComposer) {
  try {
    const value = JSON.parse(localStorage.getItem(POSITION_KEY))
    if (Number.isFinite(value?.x) && Number.isFinite(value?.y)) return clampPosition(value, avoidComposer)
  } catch {
    // Invalid or unavailable storage falls back to the lower right corner.
  }
  return clampPosition(undefined, avoidComposer)
}

export default function FloatingAssistant({ user, open, onOpen, onClose, avoidComposer = false }) {
  const [position, setPosition] = useState(() => readPosition(avoidComposer))
  const [bounds, setBounds] = useState(viewport)
  const [visitedUser, setVisitedUser] = useState(null)
  const [dragging, setDragging] = useState(false)
  const drag = useRef(null)
  const suppressClick = useRef(false)
  const launcher = useRef(null)
  const closeButton = useRef(null)
  const closeAction = useRef(onClose)
  closeAction.current = onClose

  useLayoutEffect(() => {
    if (open) {
      setVisitedUser(user?.id)
      closeButton.current?.focus({ preventScroll: true })
    }
  }, [open, user?.id])
  useEffect(() => {
    try {
      localStorage.setItem(POSITION_KEY, JSON.stringify(position))
    } catch {
      // Dragging remains available without local storage.
    }
  }, [position])
  useLayoutEffect(() => {
    const resize = () => {
      setBounds(viewport())
      setPosition((current) => clampPosition(current, avoidComposer))
    }
    resize()
    window.addEventListener('resize', resize)
    window.visualViewport?.addEventListener('resize', resize)
    window.visualViewport?.addEventListener('scroll', resize)
    return () => {
      window.removeEventListener('resize', resize)
      window.visualViewport?.removeEventListener('resize', resize)
      window.visualViewport?.removeEventListener('scroll', resize)
    }
  }, [avoidComposer])
  useEffect(() => {
    if (!open) return
    const escape = (event) => {
      if (
        event.key === 'Escape' &&
        !document.querySelector('.ant-modal-wrap:not([style*="display: none"])')
      ) {
        closeAction.current()
        launcher.current?.focus({ preventScroll: true })
      }
    }
    window.addEventListener('keydown', escape)
    return () => window.removeEventListener('keydown', escape)
  }, [open])

  const finishDrag = (event) => {
    if (!drag.current || drag.current.id !== event.pointerId) return
    suppressClick.current = drag.current.moved
    drag.current = null
    setDragging(false)
    if (event.currentTarget.hasPointerCapture(event.pointerId))
      event.currentTarget.releasePointerCapture(event.pointerId)
  }
  const panelWidth = Math.min(440, bounds.width - GAP * 2)
  const panelHeight = Math.min(640, bounds.height - bounds.bottom - GAP * 2)
  const panelLeft = Math.max(
    bounds.left + GAP,
    Math.min(position.x + SIZE - panelWidth, bounds.left + bounds.width - panelWidth - GAP),
  )
  const panelTop = Math.max(
    bounds.top + GAP,
    Math.min(position.y - panelHeight - GAP, bounds.top + bounds.height - bounds.bottom - panelHeight - GAP),
  )
  const close = () => {
    onClose()
    launcher.current?.focus({ preventScroll: true })
  }

  return (
    <>
      <button
        ref={launcher}
        className={`assistant-launcher ${dragging ? 'is-dragging' : ''}`}
        style={{ left: position.x, top: position.y }}
        aria-label={open ? '收起 AI 助手悬浮窗' : '打开 AI 助手'}
        aria-expanded={open}
        aria-controls="campus-assistant"
        title="AI 助手 · 拖动可移动位置"
        onPointerDown={(event) => {
          if (!event.isPrimary || event.button !== 0) return
          suppressClick.current = false
          drag.current = {
            id: event.pointerId,
            x: event.clientX,
            y: event.clientY,
            origin: position,
            moved: false,
          }
          event.currentTarget.setPointerCapture(event.pointerId)
        }}
        onPointerMove={(event) => {
          const current = drag.current
          if (!current || current.id !== event.pointerId) return
          const dx = event.clientX - current.x,
            dy = event.clientY - current.y
          if (Math.hypot(dx, dy) > 6) current.moved = true
          if (current.moved) {
            setDragging(true)
            setPosition(clampPosition({ x: current.origin.x + dx, y: current.origin.y + dy }, avoidComposer))
          }
        }}
        onPointerUp={finishDrag}
        onPointerCancel={finishDrag}
        onLostPointerCapture={finishDrag}
        onClick={(event) => {
          if (suppressClick.current && event.detail !== 0) {
            suppressClick.current = false
            return
          }
          suppressClick.current = false
          open ? close() : onOpen()
        }}
      >
        <ThunderboltOutlined />
        <span>AI</span>
      </button>
      <section
        id="campus-assistant"
        role="dialog"
        aria-label="AI 助手"
        aria-modal="false"
        className="assistant-panel"
        hidden={!open}
        style={{ left: panelLeft, top: panelTop, width: panelWidth, height: panelHeight }}
      >
        <header className="assistant-panel-title">
          <div>
            <ThunderboltOutlined />
            <strong>AI 助手</strong>
            <span>随时聊，不打断逛集市</span>
          </div>
          <button ref={closeButton} className="icon-button" aria-label="收起 AI 助手" onClick={close}>
            <CloseOutlined />
          </button>
        </header>
        {user && (open || visitedUser === user.id) && (
          <Suspense fallback={<Loading />}>
            <Agent key={user.id} user={user} compact visible={open} />
          </Suspense>
        )}
      </section>
    </>
  )
}

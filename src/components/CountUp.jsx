import { useEffect, useRef, useState } from 'react'

// 数字滚动动画：进入视口后从 0 缓动递增到目标值
export default function CountUp({ to, duration = 1400, suffix = '' }) {
  const ref = useRef(null)
  const started = useRef(false)
  const [val, setVal] = useState(0)

  useEffect(() => {
    const ob = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting && !started.current) {
          started.current = true
          const t0 = performance.now()
          const tick = (t) => {
            const p = Math.min(1, (t - t0) / duration)
            setVal(Math.round(to * (1 - Math.pow(1 - p, 3))))
            if (p < 1) requestAnimationFrame(tick)
          }
          requestAnimationFrame(tick)
          ob.disconnect()
        }
      },
      { threshold: 0.4 },
    )
    if (ref.current) ob.observe(ref.current)
    return () => ob.disconnect()
  }, [to, duration])

  return (
    <span ref={ref}>
      {val.toLocaleString()}
      {suffix}
    </span>
  )
}
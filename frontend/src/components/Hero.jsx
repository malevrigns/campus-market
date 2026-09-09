import { useEffect, useState } from 'react'
import { DownOutlined, ArrowRightOutlined } from '@ant-design/icons'
import CountUp from './CountUp'
import { ripple } from '../utils/ripple'

const STATS = [
  { label: '在售好物', value: 1286, suffix: '+' },
  { label: '注册同学', value: 5438, suffix: '' },
  { label: '成功交易', value: 3214, suffix: '' },
  { label: '平均响应', value: 3, suffix: ' min' },
]

export default function Hero({ total }) {
  const [scrollY, setScrollY] = useState(0)

  useEffect(() => {
    const fn = () => setScrollY(window.scrollY)
    window.addEventListener('scroll', fn, { passive: true })
    return () => window.removeEventListener('scroll', fn)
  }, [])

  const scrollToMarket = () => document.getElementById('market')?.scrollIntoView({ behavior: 'smooth' })

  return (
    <section className="relative min-h-screen flex flex-col items-center justify-center text-center px-6 overflow-hidden">
      {/* 顶部微弱电光蓝光晕（motionsites 空间纵深，无 blob 漂移） */}
      <div className="absolute inset-0 pointer-events-none" aria-hidden>
        <div
          className="absolute left-1/2 top-[2%] w-[52rem] h-[52rem] -translate-x-1/2 rounded-full"
          style={{ transform: `translate(-50%, ${scrollY * 0.04}px)`, background: 'radial-gradient(circle, rgba(26,64,255,.10) 0%, rgba(26,64,255,.03) 45%, transparent 70%)' }}
        />
      </div>

      {/* 顶部状态徽章 */}
      <div className="fade-up relative z-10" style={{ animationDelay: '100ms' }}>
        <div className="inline-flex items-center gap-2.5 glass rounded-full px-4 py-1.5 text-sm text-zinc-400 font-mono tracking-tight">
          <span className="w-1.5 h-1.5 rounded-full bg-[#1a40ff] pulse-dot" />
          LIVE · 本学期已上架
          <b className="text-white tabular-nums">{total}</b>
          件好物
        </div>
      </div>

      {/* 超大标题 —— 克制、留白 */}
      <h1
        className="fade-up relative z-10 mt-9 font-bold tracking-[-0.03em] leading-[1.04] text-white"
        style={{ fontSize: 'clamp(3rem, 8vw, 6.75rem)', animationDelay: '250ms' }}
      >
        校园好物
        <br />
        <span className="text-gradient">即刻流转</span>
      </h1>

      <p
        className="fade-up relative z-10 mt-7 max-w-lg text-zinc-400 text-base sm:text-lg leading-relaxed font-light"
        style={{ animationDelay: '420ms' }}
      >
        一个为校园而生的二手流转平台。发布、浏览、沟通、面交，
        在指尖完成每一次高效交易。
      </p>

      {/* CTA */}
      <div
        className="fade-up relative z-10 mt-10 flex flex-wrap items-center justify-center gap-4"
        style={{ animationDelay: '570ms' }}
      >
        <button className="btn-primary ripple" onClick={(e) => { ripple(e); scrollToMarket() }}>
          进入集市 <ArrowRightOutlined />
        </button>
        <button className="btn-ghost ripple" onClick={(e) => { ripple(e); scrollToMarket() }}>
          发布闲置
        </button>
      </div>

      {/* 数据统计 —— 等宽数字，专业感 */}
      <div
        className="fade-up relative z-10 mt-16 grid grid-cols-2 sm:grid-cols-4 gap-x-12 gap-y-6"
        style={{ animationDelay: '720ms' }}
      >
        {STATS.map((s) => (
          <div key={s.label}>
            <div className="font-mono text-2xl sm:text-3xl font-semibold text-white tabular-nums">
              <CountUp to={s.value} suffix={s.suffix} />
            </div>
            <div className="mt-1.5 text-[11px] text-zinc-500 tracking-[0.2em] uppercase">{s.label}</div>
          </div>
        ))}
      </div>

      {/* 滚动提示 */}
      <button
        onClick={scrollToMarket}
        className="scroll-hint absolute bottom-8 z-10 text-zinc-500 hover:text-white transition-colors duration-300"
        aria-label="向下滚动"
      >
        <DownOutlined style={{ fontSize: 20 }} />
      </button>
    </section>
  )
}

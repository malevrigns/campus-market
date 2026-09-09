// motionsites.ai 风格背景：极淡网格底纹 + 顶部微弱电光蓝光晕（克制、无 blob）
export default function Aurora() {
  return (
    <div className="fixed inset-0 z-0 pointer-events-none overflow-hidden">
      {/* 顶部电光蓝光晕（极弱，营造空间纵深）*/}
      <div
        className="absolute left-1/2 -top-1/3 w-[120vw] h-[80vh] -translate-x-1/2"
        style={{
          background: 'radial-gradient(ellipse at center top, rgba(26,64,255,.14) 0%, rgba(26,64,255,.05) 35%, transparent 70%)',
        }}
      />
      {/* 底部更弱一层 */}
      <div
        className="absolute left-1/2 bottom-0 w-[100vw] h-[40vh] -translate-x-1/2"
        style={{
          background: 'radial-gradient(ellipse at center bottom, rgba(26,64,255,.06) 0%, transparent 60%)',
        }}
      />
      {/* 极淡网格底纹（科技感）*/}
      <div
        className="absolute inset-0 opacity-[0.18]"
        style={{
          backgroundImage:
            'linear-gradient(to right, rgba(255,255,255,.04) 1px, transparent 1px), linear-gradient(to bottom, rgba(255,255,255,.04) 1px, transparent 1px)',
          backgroundSize: '64px 64px',
          maskImage: 'radial-gradient(ellipse at center, black 30%, transparent 80%)',
          WebkitMaskImage: 'radial-gradient(ellipse at center, black 30%, transparent 80%)',
        }}
      />
    </div>
  )
}

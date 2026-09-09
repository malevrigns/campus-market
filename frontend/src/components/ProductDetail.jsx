import { Drawer } from 'antd'
import {
  EnvironmentOutlined,
  HeartFilled,
  HeartOutlined,
  MessageOutlined,
  ArrowRightOutlined,
} from '@ant-design/icons'
import { money, dateTime } from '../lib/api'
import { Avatar, Button, ProductImage } from './ui'
export default function ProductDetail({
  product: p,
  user,
  onClose,
  liked,
  onLike,
  onChat,
  onBuy,
  onWithdraw,
}) {
  return (
    <Drawer
      open={!!p}
      onClose={onClose}
      title="好物详情"
      width={520}
      className="detail-drawer"
      destroyOnClose
    >
      {p && (
        <>
          <div className="detail-photo">
            <ProductImage src={p.image} alt={p.title} />
            <span className="condition-tag">{p.condition}</span>
          </div>
          <div className="detail-content">
            <div className="detail-price">
              <span className="price">{money(p.price_ct)}</span>
              <span>{p.category}</span>
            </div>
            <h2>{p.title}</h2>
            <div className="detail-tags">
              <span>
                <EnvironmentOutlined />
                {p.campus}
              </span>
              <span>{p.condition}</span>
              <span>{p.demo ? '展示商品' : dateTime(p.created_at)}</span>
            </div>
            <p className="description">{p.description || '卖家还没有添加详细描述，聊聊了解更多。'}</p>
            <div className="seller-box">
              <Avatar name={p.seller_name} />
              <div>
                <strong>{p.seller_name}</strong>
                <span>{p.demo ? '灵感展示 · 不可交易' : '校园集市成员'}</span>
              </div>
              <MessageOutlined />
            </div>
            <div className="trade-note">
              <strong>把细节聊清楚，再约个时间。</strong>
              <p>建议在校园公共场所见面，核对实物、配件与成色。确认收货前，请认真验货。</p>
            </div>
            {p.demo && (
              <div className="inline-notice">
                这是用于展示界面的示例商品，图片仅作风格参考。真实同学发布的商品可以下单和沟通。
              </div>
            )}
          </div>
          <div className="detail-actions">
            <button
              className={`icon-button ${liked ? 'lime' : ''}`}
              aria-label={liked ? '取消收藏' : '收藏商品'}
              aria-pressed={liked}
              onClick={() => onLike(p.id)}
            >
              {liked ? <HeartFilled /> : <HeartOutlined />}
            </button>
            {p.seller_id === user?.id ? (
              <Button variant="secondary" onClick={() => onWithdraw(p)} disabled={p.status !== 'available'}>
                下架商品
              </Button>
            ) : (
              <>
                <Button variant="secondary" onClick={() => onChat(p)} disabled={p.demo}>
                  <MessageOutlined />
                  聊一聊
                </Button>
                <Button onClick={() => onBuy(p)} disabled={p.demo || p.status !== 'available'}>
                  {p.status === 'reserved' ? '已被预订' : p.status === 'available' ? '我想要' : '暂不可购买'}
                  <ArrowRightOutlined />
                </Button>
              </>
            )}
          </div>
        </>
      )}
    </Drawer>
  )
}

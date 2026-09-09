import { ArrowRightOutlined, ReloadOutlined, ShoppingOutlined } from '@ant-design/icons'
import { Children } from 'react'

export function Button({ children, variant = 'primary', busy, className = '', ...props }) {
  const label = Children.toArray(children)
    .filter((child) => typeof child === 'string' || typeof child === 'number')
    .join(' ')
    .trim()
  return (
    <button
      aria-label={label || undefined}
      className={`button button-${variant} ${className}`}
      {...props}
      disabled={busy || props.disabled}
    >
      {busy && <span className="spinner" aria-hidden="true" />}
      {children}
    </button>
  )
}
export function Avatar({ name = '', size = '' }) {
  return (
    <span className={`avatar ${size}`} aria-hidden="true">
      {name.slice(0, 1) || '同'}
    </span>
  )
}
export function EmptyState({ title, description, action, onAction, icon }) {
  return (
    <div className="empty-state">
      <span className="empty-icon">{icon || <ShoppingOutlined />}</span>
      <h3>{title}</h3>
      <p>{description}</p>
      {action && (
        <Button variant="secondary" onClick={onAction}>
          {action}
          <ArrowRightOutlined />
        </Button>
      )}
    </div>
  )
}
export function ErrorState({ error, retry }) {
  return (
    <div className="error-state" role="alert">
      <span>{error?.message || String(error)}</span>
      {retry && (
        <button onClick={retry}>
          <ReloadOutlined /> 重新加载
        </button>
      )}
    </div>
  )
}
export function Loading({ label = '正在加载' }) {
  return (
    <div className="loading-state" role="status">
      <span className="spinner" />
      {label}
    </div>
  )
}
export function ProductImage({ src, alt, ...props }) {
  return (
    <img
      src={src || '/product-fallback.svg'}
      alt={alt}
      onError={(event) => {
        event.currentTarget.onerror = null
        event.currentTarget.src = '/product-fallback.svg'
      }}
      {...props}
    />
  )
}
export function PageHeading({ eyebrow, title, description, children }) {
  return (
    <div className="page-heading">
      <div>
        <span className="eyebrow">{eyebrow}</span>
        <h1>
          {title}
          <span className="lime">.</span>
        </h1>
        {description && <p>{description}</p>}
      </div>
      {children}
    </div>
  )
}

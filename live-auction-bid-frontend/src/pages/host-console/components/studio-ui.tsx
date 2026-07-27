import { type ButtonHTMLAttributes, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { X } from 'lucide-react';
import type { StudioToastItem } from './studio-toast';

export type StudioTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'purple';
type StudioButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'soft';
type StudioSize = 'sm' | 'md' | 'lg';

type StudioButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: StudioButtonVariant;
  size?: StudioSize;
  loading?: boolean;
  icon?: ReactNode;
};

export function StudioButton({ variant = 'secondary', size = 'md', loading = false, icon, children, className = '', disabled, ...props }: StudioButtonProps) {
  return <button className={`studioButton studioButton-${variant} studioButton-${size} ${className}`.trim()} disabled={disabled || loading} {...props}>{loading ? <span className="studioButtonSpinner" /> : icon}{children ? <span>{children}</span> : null}</button>;
}

type StudioCardProps = {
  title?: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  padding?: 'sm' | 'md' | 'lg';
  className?: string;
};

export function StudioCard({ title, subtitle, actions, children, padding = 'md', className = '' }: StudioCardProps) {
  return <article className={`studioCard studioCard-${padding} ${className}`.trim()}>{title || subtitle || actions ? <header className="studioCardHeader"><div>{subtitle ? <p>{subtitle}</p> : null}{title ? <h2>{title}</h2> : null}</div>{actions ? <div className="studioCardActions">{actions}</div> : null}</header> : null}{children}</article>;
}

export function StudioBadge({ tone = 'neutral', children, className = '' }: { tone?: StudioTone; children: ReactNode; className?: string }) {
  return <span className={`studioBadge studioBadge-${tone} ${className}`.trim()}><i />{children}</span>;
}

type StudioFieldProps = {
  label: ReactNode;
  help?: ReactNode;
  error?: ReactNode;
  children: ReactNode;
  className?: string;
};

export function StudioField({ label, help, error, children, className = '' }: StudioFieldProps) {
  return <label className={`studioField ${className}`.trim()}><span>{label}</span>{children}{help ? <small>{help}</small> : null}{error ? <em>{error}</em> : null}</label>;
}

export function StudioPageHeader({ eyebrow, title, description, actions, className = '' }: { eyebrow?: ReactNode; title: ReactNode; description?: ReactNode; actions?: ReactNode; className?: string }) {
  return <header className={`studioSectionHeader ${className}`.trim()}><div>{eyebrow ? <p>{eyebrow}</p> : null}<h2>{title}</h2>{description ? <span>{description}</span> : null}</div>{actions ? <div className="studioSectionActions">{actions}</div> : null}</header>;
}

export function StudioMetricCard({ icon, label, value, trend, tone = 'info' }: { icon?: ReactNode; label: ReactNode; value: ReactNode; trend?: ReactNode; tone?: StudioTone }) {
  return <article className={`studioMetricCard studioMetric-${tone}`}><span>{icon}</span><div><p>{label}</p><strong>{value}</strong>{trend ? <small>{trend}</small> : null}</div></article>;
}

type StudioOverlayProps = {
  eyebrow?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  onClose: () => void;
  className?: string;
};

function studioOverlayLabel(title: ReactNode) {
  return typeof title === 'string' || typeof title === 'number' ? String(title) : 'Studio dialog';
}

function studioPortal(node: ReactNode) {
  if (typeof document === 'undefined') return node;
  return createPortal(node, document.querySelector('.laAdminShell') || document.body);
}

export function StudioDrawer({ eyebrow, title, description, children, footer, onClose, className = '' }: StudioOverlayProps) {
  return studioPortal(<div className={`studioOverlay studioDrawerOverlay ${className}`.trim()}>
    <button className="studioOverlayBackdrop" type="button" aria-label="关闭" onClick={onClose} />
    <aside className="studioDrawerPanel" role="dialog" aria-modal="true" aria-label={studioOverlayLabel(title)}>
      <header className="studioOverlayHeader">
        <div>{eyebrow ? <p>{eyebrow}</p> : null}<h2>{title}</h2>{description ? <span>{description}</span> : null}</div>
        <button className="studioOverlayClose" type="button" aria-label="关闭" onClick={onClose}><X size={17} /></button>
      </header>
      <div className="studioOverlayBody">{children}</div>
      {footer ? <footer className="studioOverlayFooter">{footer}</footer> : null}
    </aside>
  </div>);
}

export function StudioDialog({ eyebrow, title, description, children, footer, onClose, className = '' }: StudioOverlayProps) {
  return studioPortal(<div className={`studioOverlay studioDialogOverlay ${className}`.trim()}>
    <button className="studioOverlayBackdrop" type="button" aria-label="关闭" onClick={onClose} />
    <section className="studioDialogPanel" role="dialog" aria-modal="true" aria-label={studioOverlayLabel(title)}>
      <header className="studioOverlayHeader">
        <div>{eyebrow ? <p>{eyebrow}</p> : null}<h2>{title}</h2>{description ? <span>{description}</span> : null}</div>
        <button className="studioOverlayClose" type="button" aria-label="关闭" onClick={onClose}><X size={17} /></button>
      </header>
      <div className="studioOverlayBody">{children}</div>
      {footer ? <footer className="studioOverlayFooter">{footer}</footer> : null}
    </section>
  </div>);
}

type StudioTableProps<T> = {
  columns: { label: ReactNode; render: (row: T, index: number) => ReactNode }[];
  rows: T[];
  rowKey: (row: T, index: number) => string;
  header?: ReactNode;
  filters?: ReactNode;
  empty?: ReactNode;
  className?: string;
  rowClassName?: (row: T, index: number) => string;
};

function studioColumnLabel(label: ReactNode) {
  return typeof label === 'string' || typeof label === 'number' ? String(label) : '字段';
}

export function StudioTable<T>({ columns, rows, rowKey, header, filters, empty, className = '', rowClassName }: StudioTableProps<T>) {
  return <div className={`studioTableWrap ${className}`.trim()}>{header || filters ? <div className="studioTableTools"><div>{filters}</div><span>{header}</span></div> : null}{rows.length ? <table className="studioTable"><thead><tr>{columns.map((column, index) => <th key={`${column.label}-${index}`}>{column.label}</th>)}</tr></thead><tbody>{rows.map((row, rowIndex) => <tr key={rowKey(row, rowIndex)} className={rowClassName?.(row, rowIndex)}>{columns.map((column, columnIndex) => <td data-label={studioColumnLabel(column.label)} key={`${column.label}-${columnIndex}`}>{column.render(row, rowIndex)}</td>)}</tr>)}</tbody></table> : <div className="studioTableEmpty">{empty || <StudioEmptyState title="暂无数据" />}</div>}</div>;
}

type StudioStateProps = {
  icon?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  tone?: StudioTone;
  compact?: boolean;
  className?: string;
};

export function StudioEmptyState({ icon, title, description, action, tone = 'neutral', compact = false, className = '' }: StudioStateProps) {
  return <div className={`studioState studioState-empty studioState-${tone} ${compact ? 'studioState-compact' : ''} ${className}`.trim()}>{icon ? <span className="studioStateIcon">{icon}</span> : null}<h3>{title}</h3>{description ? <p>{description}</p> : null}{action ? <div className="studioStateAction">{action}</div> : null}</div>;
}

export function StudioErrorState({ icon, title, description, action, tone = 'danger', compact = false, className = '' }: StudioStateProps) {
  return <div className={`studioState studioState-error studioState-${tone} ${compact ? 'studioState-compact' : ''} ${className}`.trim()}>{icon ? <span className="studioStateIcon">{icon}</span> : null}<h3>{title}</h3>{description ? <p>{description}</p> : null}{action ? <div className="studioStateAction">{action}</div> : null}</div>;
}

function StudioToast({ tone = 'info', title, description, action, className = '' }: Omit<StudioStateProps, 'icon' | 'compact'>) {
  return <div className={`studioToast studioToast-${tone} ${className}`.trim()} role={tone === 'danger' ? 'alert' : 'status'}><i /> <div><strong>{title}</strong>{description ? <span>{description}</span> : null}</div>{action ? <div className="studioToastAction">{action}</div> : null}</div>;
}

export function StudioToastViewport({ toasts, className = '' }: { toasts: StudioToastItem[]; className?: string }) {
  if (!toasts.length) return null;
  const viewport = <div className={`studioToastViewport ${className}`.trim()}>{toasts.map((toast) => <StudioToast key={toast.id} tone={toast.tone || 'info'} title={toast.title} description={toast.description} action={toast.action} />)}</div>;
  return typeof document === 'undefined' ? viewport : createPortal(viewport, document.body);
}

export function StudioTableSkeleton({ rows = 5, columns = 6, className = '' }: { rows?: number; columns?: number; className?: string }) {
  return <div className={`studioTableSkeleton ${className}`.trim()} aria-busy="true" aria-label="正在加载"><div className="studioSkeletonTools"><span /><b /></div>{Array.from({ length: rows }).map((_, row) => <div className="studioSkeletonRow" key={row}>{Array.from({ length: columns }).map((__, col) => <span key={col} />)}</div>)}</div>;
}

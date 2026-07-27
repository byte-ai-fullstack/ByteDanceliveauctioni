import { useCallback, useEffect, useMemo, useState } from 'react';
import type { PointerEvent as ReactPointerEvent } from 'react';
import {
  Archive,
  ChevronLeft,
  Filter,
  MapPin,
  MessageCircle,
  Search,
  Settings,
  ShoppingBag,
  Trash2,
  X,
} from 'lucide-react';
import { formatShopMoney, listMyFrequentStores, listUserOrders, mockPayUserOrder } from '../features/shop/api/shopApi';
import type { FrequentStore, ShopOrderStatus, UserOrder, UserOrderItem } from '../features/shop/api/shopApi';
import { isAuthRequiredError } from '../shared/api/errors';
import { BuyerAuthSheet } from '../shared/auth/BuyerAuthSheet';
import { useAuthSession } from '../shared/auth/useAuthSession';
import { createIdempotencyKey } from '../shared/lib/idempotency';
import { navigateTo } from '../shared/navigation';
import './shop-replica.css';

const ORDER_TABS: Array<{ label: string; value: ShopOrderStatus | '' }> = [
  { label: '全部', value: '' },
  { label: '待支付', value: 'pending_payment' },
  { label: '待发货', value: 'paid' },
  { label: '待收货/使用', value: 'shipped' },
  { label: '评价', value: 'completed' },
];

function statusText(status: ShopOrderStatus): string {
  switch (status) {
    case 'pending_payment':
      return '待支付';
    case 'paid':
      return '待发货';
    case 'shipped':
      return '待收货';
    case 'completed':
      return '交易完成';
    case 'expired':
      return '超时取消';
    case 'cancelled':
      return '已取消';
    default:
      return String(status || '全部');
  }
}

function fallbackOrderImage(order: UserOrder): string {
  return order.items[0]?.imageUrl || '';
}

function fallbackOrderItems(order: UserOrder): UserOrderItem[] {
  return [{
    id: `${order.id}-fallback`,
    orderId: order.id,
    source: order.source,
    title: order.title || order.shopName || '订单商品',
    imageUrl: fallbackOrderImage(order),
    skuName: order.source === 'auction' ? '直播拍得' : '',
    quantity: 1,
    unitAmount: order.totalAmount,
    totalAmount: order.totalAmount,
    currency: order.currency,
  }];
}

function displayShopName(order: UserOrder): string {
  if (order.shopName) return order.shopName;
  return order.source === 'auction' ? '直播竞拍' : '商城订单';
}

function PackageEntryIcon() {
  return (
    <svg width="50" height="34" viewBox="0 0 150 100" fill="none" aria-hidden="true">
      <g stroke="currentColor" strokeWidth="6" strokeLinecap="round" strokeLinejoin="round">
        <rect x="6" y="34" width="52" height="41" fill="#fff" />
        <path d="M6 34L29 6" />
        <path d="M58 34L76 6" />
        <path d="M28 6H76" />
        <path d="M76 37V7" />
        <path d="M18 20H66" />
      </g>
      <text
        x="65"
        y="66"
        fill="currentColor"
        fontFamily="PingFang SC, Microsoft YaHei, SimHei, Arial, sans-serif"
        fontSize="32"
        fontWeight="900"
        letterSpacing="-2"
      >
        包裹
      </text>
    </svg>
  );
}

function OrderToolsIcon() {
  return (
    <svg width="32" height="36" viewBox="0 0 80 100" fill="none" aria-hidden="true">
      <g stroke="currentColor" strokeWidth="5" strokeLinecap="round" strokeLinejoin="round">
        <rect x="12" y="16" width="18" height="18" rx="4" />
        <rect x="42" y="16" width="18" height="18" rx="4" />
        <rect x="12" y="46" width="18" height="18" rx="4" />
        <rect x="42" y="46" width="18" height="18" rx="4" />
      </g>
    </svg>
  );
}

type ShopOrdersContentProps = {
  embedded?: boolean;
  onBack?: () => void;
  initialFrom?: string;
  onSheetDragStart?: (event: ReactPointerEvent<HTMLElement>) => void;
};

export function ShopOrdersContent({ embedded = false, onBack, initialFrom, onSheetDragStart }: ShopOrdersContentProps) {
  const { user } = useAuthSession();
  const params = new URLSearchParams(window.location.search);
  const initialStatus = params.get('status') || '';
  const from = initialFrom ?? params.get('from') ?? '';
  const [activeStatus, setActiveStatus] = useState<ShopOrderStatus | ''>(initialStatus);
  const [query, setQuery] = useState('');
  const [submittedQuery, setSubmittedQuery] = useState('');
  const [orders, setOrders] = useState<UserOrder[]>([]);
  const [frequentStores, setFrequentStores] = useState<FrequentStore[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [payingId, setPayingId] = useState('');
  const [toolsOpen, setToolsOpen] = useState(false);
  const requiresAuth = !user;
  const userID = user?.id ?? '';

  const filteredOrders = useMemo(() => (
    activeStatus ? orders.filter((order) => order.status === activeStatus) : orders
  ), [activeStatus, orders]);

  const loadOrders = useCallback(() => {
    if (!userID) {
      setOrders([]);
      setError('');
      setLoading(false);
      return;
    }
    setLoading(true);
    listUserOrders({ status: activeStatus || undefined, q: submittedQuery || undefined, page: 1, pageSize: 50 })
      .then((reply) => {
        setOrders(reply.orders);
        setError('');
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : '订单加载失败');
        setOrders([]);
      })
      .finally(() => setLoading(false));
  }, [activeStatus, submittedQuery, userID]);

  useEffect(() => {
    const timer = window.setTimeout(loadOrders, 0);
    return () => window.clearTimeout(timer);
  }, [loadOrders]);

  const loadFrequentStores = useCallback(() => {
    if (!userID) {
      setFrequentStores([]);
      return;
    }
    listMyFrequentStores({ limit: 10 })
      .then((reply) => setFrequentStores(reply.stores))
      .catch(() => setFrequentStores([]));
  }, [userID]);

  useEffect(() => {
    const timer = window.setTimeout(loadFrequentStores, 0);
    return () => window.clearTimeout(timer);
  }, [loadFrequentStores]);

  const handlePay = async (order: UserOrder) => {
    setPayingId(order.id);
    try {
      const result = await mockPayUserOrder(
        order.id,
        createIdempotencyKey('h5-order-pay', order.id),
        order.totalAmount,
        order.currency,
      );
      setOrders((current) => current.map((item) => (item.id === order.id ? result.order : item)));
      loadFrequentStores();
      setError('');
    } catch (err) {
      if (isAuthRequiredError(err)) {
        setError('');
        return;
      }
      setError(err instanceof Error ? err.message : '支付失败');
    } finally {
      setPayingId('');
    }
  };

  const handleBack = () => {
    if (onBack) {
      onBack();
      return;
    }
    if (['room', 'live-more', 'result'].includes(from) && window.history.length > 1) {
      window.history.back();
      return;
    }
    if (from === 'money-notice') {
      navigateTo('/message');
      return;
    }
    if (from === 'look-history') {
      navigateTo('/me/right-menu/look-history');
      return;
    }
    navigateTo('/shop');
  };

  const handleEmbeddedDragStart = (event: ReactPointerEvent<HTMLElement>) => {
    if (!embedded || !onSheetDragStart) return;
    const target = event.target as HTMLElement;
    if (target.closest('button, a, input, textarea, select')) return;
    onSheetDragStart(event);
  };

  return (
    <main className={`mobileShell dyMallOrdersShell${embedded ? ' dyMallOrdersEmbedded' : ''}`}>
      <header className="dyMallOrdersTop" onPointerDown={handleEmbeddedDragStart}>
        <button type="button" aria-label="返回" onClick={handleBack}><ChevronLeft size={28} /></button>
        <form onSubmit={(event) => {
          event.preventDefault();
          setSubmittedQuery(query.trim());
        }}>
          <Search size={18} />
          <input aria-label="搜索订单" placeholder="搜索商品名 / 订单号 / 快递单号" value={query} onChange={(event) => setQuery(event.target.value)} />
        </form>
        <a href="/shop?panel=package" aria-label="包裹"><PackageEntryIcon /></a>
        <button type="button" aria-label="订单工具" aria-expanded={toolsOpen} onClick={() => setToolsOpen(true)}><OrderToolsIcon /></button>
      </header>

      <nav className="dyMallOrderTabs" aria-label="订单状态" onPointerDown={handleEmbeddedDragStart}>
        {ORDER_TABS.map((tab) => (
          <button
            className={tab.value === activeStatus ? 'isActive' : ''}
            type="button"
            key={tab.label}
            onClick={() => setActiveStatus(tab.value)}
          >
            {tab.label}
          </button>
        ))}
        <button type="button" aria-label="筛选"><Filter size={22} /></button>
      </nav>

      {!requiresAuth && frequentStores.length > 0 ? (
        <section className="dyMallFrequentStores" aria-label="常买的店">
          <header><h2>常买的店</h2><a href="/shop">查看更多 ›</a></header>
          <div>
            {frequentStores.map((store) => (
              <a href={store.targetUrl || '/shop'} key={store.storeKey}>
                {store.imageUrl ? (
                  <img src={store.imageUrl} alt={store.storeName} />
                ) : (
                  <i aria-hidden="true">{store.storeName.slice(0, 1) || '店'}</i>
                )}
                <span>{store.storeName}</span>
              </a>
            ))}
          </div>
        </section>
      ) : null}

      {!requiresAuth && error ? <section className="dyMallOrdersNotice">{error}</section> : null}
      {!requiresAuth && loading ? <section className="dyMallOrdersNotice">正在同步订单...</section> : null}

      {requiresAuth ? (
        <section className="dyMallOrderEmpty dyMallOrderEmptyAuth">
          <ShoppingBag size={58} />
          <h1>暂无订单</h1>
          <p>登录后会同步你的订单、地址和支付状态。</p>
        </section>
      ) : null}

      {!requiresAuth && filteredOrders.length === 0 ? (
        <section className="dyMallOrderEmpty">
          <ShoppingBag size={58} />
          <h1>暂无相关订单</h1>
          <p>试试查看全部或回商城继续逛逛</p>
        </section>
      ) : null}

      {!requiresAuth ? <section className="dyMallOrderList" aria-label="我的订单">
        {filteredOrders.map((order) => (
          <article className="dyMallOrderCard" key={order.id}>
            <header>
              <b>{displayShopName(order)}</b>
              <span>{statusText(order.status)}</span>
            </header>
            {(order.items.length ? order.items : fallbackOrderItems(order)).map((item) => (
              <a href={item.productId ? `/shop/detail?id=${item.productId}` : '#'} className="dyMallOrderItem" key={item.id}>
                <img src={item.imageUrl || fallbackOrderImage(order)} alt={item.title || order.title} />
                <section>
                  <h2>{item.title || order.title}</h2>
                  <p>{order.source === 'auction' ? order.orderNo || '拍卖订单' : item.skuName}</p>
                  <em>{order.source === 'auction' ? '直播拍得' : '7天无理由退货'}</em>
                </section>
                <aside>
                  <b>￥{formatShopMoney(item.unitAmount || item.totalAmount || order.totalAmount)}</b>
                  <span>x{item.quantity || 1}</span>
                </aside>
              </a>
            ))}
            <div className="dyMallOrderCoupon">{order.source === 'auction' ? '拍卖订单支付后进入待发货' : '本单限时可报 最高得 85 折券'}</div>
            <footer>
              <span>含运费险服务 实付款 <b>￥{formatShopMoney(order.totalAmount)}</b></span>
              <div>
                {order.status === 'pending_payment' ? (
                  <button type="button" className="isPrimary" disabled={payingId === order.id} onClick={() => handlePay(order)}>
                    {payingId === order.id ? '支付中' : '去支付'}
                  </button>
                ) : (
                  <>
                    <button type="button"><Trash2 size={15} />删除订单</button>
                    <button type="button" className="isPrimary">再买一单</button>
                  </>
                )}
              </div>
            </footer>
          </article>
        ))}
      </section> : null}
      {requiresAuth ? (
        <BuyerAuthSheet
          title="登录后查看我的订单"
          description="订单、地址和支付状态会按当前买家账号隔离。"
          actionLabel="查看订单"
          onClose={handleBack}
        />
      ) : null}
      {toolsOpen ? (
        <div className="dyMallOrderToolsMask" role="presentation">
          <section className="dyMallOrderToolsSheet" aria-modal="true" role="dialog" aria-label="订单工具">
            <header>
              <h2>订单工具</h2>
              <button type="button" aria-label="关闭订单工具" onClick={() => setToolsOpen(false)}><X size={29} /></button>
            </header>
            <nav aria-label="订单工具">
              <button type="button" onClick={() => navigateTo('/shop/addresses')}>
                <span><MapPin size={27} /></span>
                我的地址
              </button>
              <button type="button">
                <span><Archive size={27} /></span>
                订单回收站
              </button>
              <button type="button">
                <span><MessageCircle size={27} /></span>
                购物消息
              </button>
              <button type="button">
                <span><Settings size={27} /></span>
                授权设置
              </button>
            </nav>
          </section>
        </div>
      ) : null}
    </main>
  );
}

export function ShopOrdersPage() {
  return <ShopOrdersContent />;
}

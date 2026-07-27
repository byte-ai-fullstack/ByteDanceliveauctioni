import { useEffect, useMemo, useState } from 'react';
import { ChevronLeft, Heart, Home, MessageCircle, Search, Share2, ShoppingCart, Store } from 'lucide-react';
import { createShopOrder, FALLBACK_PRODUCTS, formatShopMoney, getShopProduct } from '../features/shop/api/shopApi';
import type { ShopProduct, ShopSKU } from '../features/shop/api/shopApi';
import { isAuthRequiredError } from '../shared/api/errors';
import { getDefaultDeliveryAddress, listDeliveryAddresses } from '../shared/address/addressBook';
import { BuyerAuthSheet } from '../shared/auth/BuyerAuthSheet';
import { useAuthSession } from '../shared/auth/useAuthSession';
import { navigateTo } from '../shared/navigation';
import './shop-replica.css';

function productFromURL(): ShopProduct {
  const id = new URLSearchParams(window.location.search).get('id');
  return FALLBACK_PRODUCTS.find((item) => item.id === id) ?? FALLBACK_PRODUCTS[0];
}

function backToShop() {
  if (window.history.length > 1) {
    window.history.back();
    return;
  }
  navigateTo('/shop', { replace: true });
}

export function ShopDetailPage() {
  const { user } = useAuthSession();
  const fallback = useMemo(() => productFromURL(), []);
  const [product, setProduct] = useState<ShopProduct>(fallback);
  const [activeImage, setActiveImage] = useState(0);
  const [selectedSkuId, setSelectedSkuId] = useState(fallback.skus[0]?.id ?? '');
  const [quantity, setQuantity] = useState(1);
  const [toast, setToast] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [authOpen, setAuthOpen] = useState(false);

  useEffect(() => {
    let cancelled = false;
    getShopProduct(fallback.id)
      .then((reply) => {
        if (cancelled) return;
        setProduct(reply);
        setSelectedSkuId(reply.skus[0]?.id ?? '');
      })
      .catch(() => {
        if (!cancelled) setProduct(fallback);
      });
    return () => {
      cancelled = true;
    };
  }, [fallback]);

  useEffect(() => {
    if (!toast) return undefined;
    const timer = window.setTimeout(() => setToast(''), 1800);
    return () => window.clearTimeout(timer);
  }, [toast]);

  const images = product.detailImageUrls.length ? product.detailImageUrls : [product.mainImageUrl];
  const selectedSku: ShopSKU | undefined = product.skus.find((item) => item.id === selectedSkuId) ?? product.skus[0];
  const price = selectedSku?.priceAmount ?? product.priceAmount;

  const handleCreateOrder = async () => {
    if (!selectedSku) {
      setToast('请选择规格');
      return;
    }
    if (!user) {
      setToast('');
      setAuthOpen(true);
      return;
    }
    setSubmitting(true);
    try {
      const addresses = await listDeliveryAddresses();
      const address = getDefaultDeliveryAddress(addresses);
      if (!address) {
        setToast('请先新增收货地址');
        window.setTimeout(() => navigateTo('/shop/addresses/new'), 500);
        return;
      }
      const order = await createShopOrder({
        skuId: selectedSku.id,
        quantity,
        addressId: address.id,
        idempotencyKey: `h5-shop-${Date.now()}-${selectedSku.id}`,
      });
      setToast('订单已创建');
      window.setTimeout(() => navigateTo(`/shop/orders?status=pending_payment&orderId=${encodeURIComponent(order.id)}`), 350);
    } catch (error) {
      if (isAuthRequiredError(error)) {
        setToast('');
        setAuthOpen(true);
        return;
      }
      setToast(error instanceof Error ? error.message : '下单失败，请稍后重试');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <main className="mobileShell dyMallDetailShell">
      <section className="dyMallDetailScroll">
        <header className="dyMallDetailTop">
          <button type="button" aria-label="返回" onClick={backToShop}><ChevronLeft size={26} /></button>
          <label>
            <Search size={17} />
            <span>{product.category}</span>
          </label>
          <button type="button" aria-label="收藏"><Heart size={20} /></button>
          <button type="button" aria-label="分享"><Share2 size={20} /></button>
        </header>

        <section className="dyMallDetailGallery">
          <div className="dyMallDetailSlides" style={{ transform: `translateX(-${activeImage * 100}%)` }}>
            {images.map((image) => <img src={image} alt={product.title} key={image} />)}
          </div>
          <span>{activeImage + 1}/{images.length}</span>
        </section>

        {images.length > 1 ? (
          <nav className="dyMallDetailThumbs" aria-label="商品图片">
            {images.map((image, index) => (
              <button className={index === activeImage ? 'isActive' : ''} type="button" key={image} onClick={() => setActiveImage(index)}>
                <img src={image} alt="" />
              </button>
            ))}
          </nav>
        ) : null}

        <section className="dyMallDetailInfo">
          <p className="dyMallDetailPrice">
            <b>￥{formatShopMoney(price)}</b>
            {product.originalPriceAmount ? <del>￥{formatShopMoney(product.originalPriceAmount)}</del> : null}
          </p>
          <h1>{product.title}</h1>
          <p className="dyMallDetailSub">{product.subtitle || product.description}</p>
          <div className="dyMallDetailBadges">
            {product.tags.slice(0, 4).map((tag) => <span key={tag}>{tag}</span>)}
          </div>
        </section>

        <section className="dyMallDetailCard">
          <article>
            <b>保障</b>
            <p>假一赔四 · 运费险 · 极速退款 · 7天无理由</p>
          </article>
          <article>
            <b>物流</b>
            <p>48小时内发货，预计明后天送达</p>
          </article>
          <article className="dyMallDetailSpecs">
            <b>规格</b>
            <div>
              {product.skus.map((sku) => (
                <button className={sku.id === selectedSkuId ? 'isActive' : ''} type="button" key={sku.id} onClick={() => setSelectedSkuId(sku.id)}>
                  {sku.name}
                </button>
              ))}
            </div>
          </article>
          <article className="dyMallDetailQuantity">
            <b>数量</b>
            <div>
              <button type="button" onClick={() => setQuantity((current) => Math.max(1, current - 1))}>-</button>
              <span>{quantity}</span>
              <button type="button" onClick={() => setQuantity((current) => Math.min(99, current + 1))}>+</button>
            </div>
          </article>
        </section>

        <section className="dyMallDetailCard dyMallStoreCard">
          <header>
            <span><Store size={22} /></span>
            <div>
              <h2>{product.shopName}</h2>
              <p>店铺销量 {product.soldLabel} · 口碑 4.9</p>
            </div>
            <a href="/shop">进店</a>
          </header>
        </section>

        <section className="dyMallDetailCard dyMallDetailDesc">
          <h2>商品详情</h2>
          <p>{product.description || '直播精选好物，支持进入直播间看实物细节，实际成交以订单页为准。'}</p>
          {images.map((image) => <img src={image} alt={product.title} key={`detail-${image}`} />)}
        </section>
      </section>

      <footer className="dyMallBuyBar">
        <div>
          <a href="/shop"><Home size={20} /><span>首页</span></a>
          <button type="button" onClick={() => setToast('客服已收到消息')}><MessageCircle size={20} /><span>客服</span></button>
          <button type="button" onClick={() => setToast('已加入购物车')}><ShoppingCart size={20} /><span>购物车</span></button>
        </div>
        <button type="button" onClick={() => setToast('已加入购物车')}>加入购物车</button>
        <button type="button" disabled={submitting} onClick={handleCreateOrder}>{submitting ? '提交中' : '立即购买'}</button>
      </footer>

      {toast ? <div className="dyMallToast" role="status">{toast}</div> : null}
      {authOpen && !user ? (
        <BuyerAuthSheet
          title="登录后继续购买"
          description="订单、地址和支付状态会按当前买家账号隔离。"
          actionLabel="继续购买"
          onAuthenticated={() => setAuthOpen(false)}
          onClose={() => setAuthOpen(false)}
        />
      ) : null}
    </main>
  );
}

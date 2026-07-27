import { useEffect, useMemo, useState } from 'react';
import { Camera, Menu, Search, ShoppingCart } from 'lucide-react';
import { listShopProducts, FALLBACK_PRODUCTS, formatShopMoney } from '../features/shop/api/shopApi';
import type { ShopProduct } from '../features/shop/api/shopApi';
import { navigateTo } from '../shared/navigation';
import { DouyinTabBar } from '../shared/ui/DouyinTabBar';
import './shop-replica.css';

const NAV_ITEMS = ['热点', '长视频', '关注', '直播', '商城', '推荐'];
const CATEGORY_TABS = ['推荐', '直播精选', '珠宝玉石', '项链吊坠', '手链手串', '收纳配饰'];
const SEARCH_HINTS = ['冰阳绿翡翠手镯', '送礼项链', '低价开拍', '直播同款'];
const SHOP_ICON_BASE = '/shop-icon-drafts/';

const SHORTCUTS = [
  { label: '我的订单', href: '/shop/orders', icon: 'order.svg', badge: '' },
  { label: '订单报销', href: '/shop/orders?status=paid', icon: 'reimbursement.svg', badge: '待领券' },
  { label: '天天抽券', href: '/shop?entry=coupon', icon: 'daily-lottery.svg', badge: '' },
  { label: '充值中心', href: '/shop?entry=recharge', icon: 'recharge.svg', badge: '抢4元' },
  { label: '券红包', href: '/shop?entry=gift', icon: 'coupon-wallet.svg', badge: '10张券' },
];

const NAV_HREF: Record<string, string> = {
  热点: '/home',
  长视频: '/home',
  关注: '/home',
  直播: '/home/live',
  商城: '/shop',
  推荐: '/home',
};

function productMatches(product: ShopProduct, keyword: string, category: string): boolean {
  const matchCategory = category === '推荐' || product.category === category || product.tags.includes(category);
  const text = [product.title, product.subtitle, product.category, product.shopName, ...product.tags].join(' ');
  return matchCategory && (!keyword || text.includes(keyword));
}

export function ShopPage() {
  const [activeCategory, setActiveCategory] = useState('推荐');
  const [query, setQuery] = useState(SEARCH_HINTS[0]);
  const [submittedQuery, setSubmittedQuery] = useState('');
  const [products, setProducts] = useState<ShopProduct[]>(FALLBACK_PRODUCTS);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    const timer = window.setTimeout(() => {
      setLoading(true);
      listShopProducts({ category: activeCategory === '推荐' ? undefined : activeCategory, q: submittedQuery || undefined, page: 1, pageSize: 30 })
        .then((reply) => {
          if (cancelled) return;
          setProducts(reply.products.length ? reply.products : FALLBACK_PRODUCTS);
          setError('');
        })
        .catch((err: unknown) => {
          if (cancelled) return;
          setProducts(FALLBACK_PRODUCTS);
          setError(err instanceof Error ? err.message : '商品加载失败');
        })
        .finally(() => {
          if (!cancelled) setLoading(false);
        });
    }, 0);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [activeCategory, submittedQuery]);

  const visibleProducts = useMemo(() => {
    const keyword = submittedQuery.trim();
    return products.filter((item) => productMatches(item, keyword, activeCategory));
  }, [activeCategory, products, submittedQuery]);

  const hotProducts = useMemo(() => products.slice(0, 4), [products]);

  const handleSearch = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmittedQuery(query.trim());
    setActiveCategory('推荐');
  };

  return (
    <main className="mobileShell dyMallShell">
      <section className="dyMallHero">
        <header className="dyMallTopNav">
          <button type="button" aria-label="菜单"><Menu size={24} /></button>
          <nav aria-label="频道">
            {NAV_ITEMS.map((item) => (
              <button
                className={item === '商城' ? 'isActive' : ''}
                type="button"
                key={item}
                onClick={() => {
                  if (item !== '商城') navigateTo(NAV_HREF[item] || '/home');
                }}
              >
                {item}
              </button>
            ))}
          </nav>
          <a href="/home/search" aria-label="搜索"><Search size={25} /></a>
        </header>

        <form className="dyMallSearch" onSubmit={handleSearch}>
          <span className="dyMallSearchSide" aria-hidden="true">
            <img src={`${SHOP_ICON_BASE}message-dots.svg`} alt="" />
          </span>
          <label>
            <Search size={20} />
            <input aria-label="搜索商品" value={query} onChange={(event) => setQuery(event.target.value)} />
            <button type="button" aria-label="拍照搜索"><Camera size={22} /></button>
            <button type="submit">搜索</button>
          </label>
          <a href="/shop?panel=cart" aria-label="购物车"><ShoppingCart size={24} /></a>
        </form>

        <section className="dyMallShortcutPanel" aria-label="商城快捷入口">
          {SHORTCUTS.map(({ label, href, icon, badge }) => (
            <a href={href} key={label}>
              <span className="dyMallShortcutIcon">
                <img src={`${SHOP_ICON_BASE}${icon}`} alt="" aria-hidden="true" />
                {badge ? <em>{badge}</em> : null}
              </span>
              <b>{label}</b>
            </a>
          ))}
        </section>

        <section className="dyMallPromoGrid" aria-label="优惠活动">
          <article>
            <strong>限时疯抢</strong>
            <span>百秒抢购 低至1元</span>
            <div>
              {hotProducts.slice(0, 2).map((product) => (
                <a href={`/shop/detail?id=${product.id}`} key={product.id}>
                  <img src={product.mainImageUrl} alt={product.title} />
                  <b>￥{formatShopMoney(product.priceAmount)}</b>
                </a>
              ))}
            </div>
          </article>
          <article>
            <strong>大牌试用</strong>
            <span>直播好物 小样尝鲜</span>
            <div>
              {hotProducts.slice(2, 4).map((product) => (
                <a href={`/shop/detail?id=${product.id}`} key={product.id}>
                  <img src={product.mainImageUrl} alt={product.title} />
                  <b>￥{formatShopMoney(product.priceAmount)}</b>
                </a>
              ))}
            </div>
          </article>
        </section>
      </section>

      <nav className="dyMallCategoryTabs" aria-label="商城分类">
        {CATEGORY_TABS.map((category) => (
          <button
            className={category === activeCategory ? 'isActive' : ''}
            type="button"
            key={category}
            onClick={() => {
              setActiveCategory(category);
              setSubmittedQuery('');
            }}
          >
            {category}
          </button>
        ))}
      </nav>

      {error ? <section className="dyMallInlineNotice">已展示本地商品，接口返回：{error}</section> : null}
      {loading ? <section className="dyMallInlineNotice">正在同步商城商品...</section> : null}

      <section className="dyMallWaterfall" aria-label="商品推荐">
        {visibleProducts.map((product) => (
          <a href={`/shop/detail?id=${product.id}`} className="dyMallGoodsCard" key={product.id}>
            <figure>
              {product.live ? <span>直播中</span> : null}
              <img src={product.mainImageUrl} alt={product.title} loading="lazy" />
            </figure>
            <section>
              <h2>{product.title}</h2>
              <p className="dyMallGoodsTags">
                {product.tags.slice(0, 2).map((tag) => <em key={tag}>{tag}</em>)}
              </p>
              <p className="dyMallGoodsPrice">
                <b>￥{formatShopMoney(product.priceAmount)}</b>
                <span>店铺销量 {product.soldLabel}</span>
              </p>
              <small>{product.shopName}</small>
            </section>
          </a>
        ))}
      </section>

      {visibleProducts.length === 0 ? <section className="dyMallEmpty">暂无相关商品，换个关键词试试</section> : null}

      <DouyinTabBar active="shop" />
    </main>
  );
}

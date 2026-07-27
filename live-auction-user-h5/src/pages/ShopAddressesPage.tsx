import { useCallback, useEffect, useState } from 'react';
import { ChevronLeft, CircleHelp, MapPin } from 'lucide-react';
import type { DeliveryAddress, DeliveryAddressInput } from '../shared/address/addressBook';
import {
  createDeliveryAddress,
  deleteDeliveryAddress,
  listDeliveryAddresses,
  setDefaultDeliveryAddress,
  updateDeliveryAddress,
} from '../shared/address/addressBook';
import { DeliveryAddressCard, DeliveryAddressForm, DeliveryAddressFormSheet } from '../features/address/components/AddressPanels';
import { isAuthRequiredError } from '../shared/api/errors';
import { BuyerAuthSheet } from '../shared/auth/BuyerAuthSheet';
import { useAuthSession } from '../shared/auth/useAuthSession';
import { navigateTo } from '../shared/navigation';
import './shop-replica.css';

function AddressAuthSheet({ title, description }: { title: string; description: string }) {
  return (
    <BuyerAuthSheet
      title={title}
      description={description}
      actionLabel={title.includes('新增') ? '新增地址' : '查看地址'}
      onClose={() => navigateTo('/shop/orders')}
    />
  );
}

export function ShopAddressesPage() {
  const { user } = useAuthSession();
  const [addresses, setAddresses] = useState<DeliveryAddress[]>([]);
  const [editingAddress, setEditingAddress] = useState<DeliveryAddress | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const requiresAuth = !user;
  const userID = user?.id ?? '';

  const loadAddresses = useCallback(async () => {
    if (!userID) {
      setAddresses([]);
      setLoading(false);
      setError('');
      return;
    }
    setLoading(true);
    setError('');
    try {
      setAddresses(await listDeliveryAddresses());
    } catch (err) {
      if (isAuthRequiredError(err)) {
        setError('');
        setAddresses([]);
        return;
      }
      setError(err instanceof Error ? err.message : '地址加载失败');
      setAddresses([]);
    } finally {
      setLoading(false);
    }
  }, [userID]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void loadAddresses();
    }, 0);
    return () => window.clearTimeout(timer);
  }, [loadAddresses]);

  const handleSetDefault = async (id: string) => {
    try {
      setAddresses(await setDefaultDeliveryAddress(id));
      setError('');
    } catch (err) {
      if (isAuthRequiredError(err)) {
        setError('');
        return;
      }
      setError(err instanceof Error ? err.message : '设置默认地址失败');
    }
  };

  const handleDelete = async (id: string) => {
    try {
      await deleteDeliveryAddress(id);
      await loadAddresses();
    } catch (err) {
      if (isAuthRequiredError(err)) {
        setError('');
        return;
      }
      setError(err instanceof Error ? err.message : '删除地址失败');
    }
  };

  const handleUpdate = async (input: DeliveryAddressInput) => {
    if (!editingAddress) return;
    try {
      await updateDeliveryAddress(editingAddress.id, input);
      await loadAddresses();
      setEditingAddress(null);
    } catch (err) {
      if (isAuthRequiredError(err)) {
        setError('');
        return;
      }
      setError(err instanceof Error ? err.message : '地址保存失败');
    }
  };

  return (
    <main className="mobileShell shopAddressPage">
      <header className="shopAddressTop">
        <button type="button" aria-label="返回订单" onClick={() => navigateTo('/shop/orders')}><ChevronLeft size={30} /></button>
        <h1>收货地址 <CircleHelp size={17} /></h1>
        <span />
      </header>
      <section className="shopAddressList" aria-label="收货地址列表">
        {!requiresAuth && error ? <p className="shopAddressError">{error}</p> : null}
        {!requiresAuth && loading ? (
          <section className="shopAddressEmpty">
            <MapPin size={40} />
            <h2>正在加载地址</h2>
          </section>
        ) : !requiresAuth && addresses.length > 0 ? addresses.map((address) => (
          <DeliveryAddressCard
            key={address.id}
            address={address}
            onEdit={setEditingAddress}
            onSetDefault={handleSetDefault}
            onDelete={handleDelete}
          />
        )) : !requiresAuth ? (
          <section className="shopAddressEmpty">
            <MapPin size={40} />
            <h2>还没有收货地址</h2>
            <p>新增地址后，支付保证金和订单收货都会默认使用它。</p>
          </section>
        ) : null}
      </section>
      <div className="shopAddressBottomAction">
        <button type="button" onClick={() => navigateTo('/shop/addresses/new')}>+ 新建地址</button>
      </div>
      {requiresAuth ? (
        <AddressAuthSheet title="登录后查看收货地址" description="地址会按当前买家账号隔离，支付保证金和订单收货只读取你的地址。" />
      ) : null}
      {editingAddress ? (
        <DeliveryAddressFormSheet
          title="修改地址"
          initialAddress={editingAddress}
          onClose={() => setEditingAddress(null)}
          onSave={handleUpdate}
        />
      ) : null}
    </main>
  );
}

export function ShopAddressEditPage() {
  const { user } = useAuthSession();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const requiresAuth = !user;

  const handleSave = async (input: DeliveryAddressInput) => {
    if (!user) return;
    setSaving(true);
    setError('');
    try {
      await createDeliveryAddress(input);
    } catch (err) {
      if (isAuthRequiredError(err)) {
        setError('');
        setSaving(false);
        return;
      }
      setError(err instanceof Error ? err.message : '地址保存失败');
      setSaving(false);
      return;
    }
    navigateTo('/shop/addresses', { replace: true });
  };

  return (
    <main className="mobileShell shopAddressPage isEdit">
      <header className="shopAddressTop">
        <button type="button" aria-label="返回地址列表" onClick={() => navigateTo('/shop/addresses')}><ChevronLeft size={30} /></button>
        <h1>新建地址</h1>
        <span />
      </header>
      {!requiresAuth && error ? <p className="shopAddressError">{error}</p> : null}
      {!requiresAuth ? <DeliveryAddressForm embedded submitLabel="保存" onSave={handleSave} /> : null}
      {!requiresAuth && saving ? <p className="shopAddressSaving">保存中...</p> : null}
      {requiresAuth ? (
        <AddressAuthSheet title="登录后新增收货地址" description="新增地址会保存到当前买家账号下，不会写入其他用户的地址列表。" />
      ) : null}
    </main>
  );
}

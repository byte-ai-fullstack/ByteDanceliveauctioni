import { useCallback, useEffect, useMemo, useState } from 'react';
import { ChevronRight } from 'lucide-react';
import type { Lot } from '../../../shared/api/types';
import { isAuthRequiredError } from '../../../shared/api/errors';
import { formatLotDeposit } from '../../../entities/auction/model/deposit';
import type { DeliveryAddress, DeliveryAddressInput } from '../../../shared/address/addressBook';
import {
  createDeliveryAddress,
  deleteDeliveryAddress,
  formatAddressLine,
  formatAddressSummary,
  getDefaultDeliveryAddress,
  listDeliveryAddresses,
  setDefaultDeliveryAddress,
  updateDeliveryAddress,
} from '../../../shared/address/addressBook';
import { DeliveryAddressFormSheet, DeliveryAddressListSheet } from '../../address/components/AddressPanels';

export function DepositPayModal({
  lot,
  onConfirm,
  onAuthRequired,
  onClose,
}: {
  lot: Lot;
  onConfirm: (address: DeliveryAddress) => void | Promise<void>;
  onAuthRequired?: (reason: unknown) => void;
  onClose: () => void;
}) {
  const [addresses, setAddresses] = useState<DeliveryAddress[]>([]);
  const [selectedAddressId, setSelectedAddressId] = useState('');
  const [addressSheetOpen, setAddressSheetOpen] = useState(false);
  const [addressFormOpen, setAddressFormOpen] = useState(false);
  const [editingAddress, setEditingAddress] = useState<DeliveryAddress | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const selectedAddress = useMemo(() => (
    addresses.find((address) => address.id === selectedAddressId) ?? getDefaultDeliveryAddress(addresses)
  ), [addresses, selectedAddressId]);

  const handleAuthError = useCallback((reason: unknown): boolean => {
    if (!isAuthRequiredError(reason)) return false;
    setError('');
    onAuthRequired?.(reason);
    return true;
  }, [onAuthRequired]);

  useEffect(() => {
    let cancelled = false;
    listDeliveryAddresses()
      .then((next) => {
        if (cancelled) return;
        setAddresses(next);
        setSelectedAddressId(getDefaultDeliveryAddress(next)?.id ?? '');
      })
      .catch((err) => {
        if (cancelled) return;
        if (handleAuthError(err)) return;
        setError(err instanceof Error ? err.message : '地址加载失败');
      });
    return () => {
      cancelled = true;
    };
  }, [handleAuthError]);

  const refreshAddresses = (next: DeliveryAddress[]) => {
    setAddresses(next);
    const current = next.find((address) => address.id === selectedAddressId);
    if (!current) setSelectedAddressId(getDefaultDeliveryAddress(next)?.id ?? '');
  };

  const handleAddressRowClick = () => {
    if (addresses.length > 0) setAddressSheetOpen(true);
    else {
      setEditingAddress(null);
      setAddressFormOpen(true);
    }
  };

  const handleSelectAddress = (address: DeliveryAddress) => {
    setSelectedAddressId(address.id);
    setAddressSheetOpen(false);
  };

  const handleSetDefaultAddress = async (id: string) => {
    try {
      const next = await setDefaultDeliveryAddress(id);
      setAddresses(next);
      setSelectedAddressId(id);
      setError('');
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(err instanceof Error ? err.message : '设置默认地址失败');
    }
  };

  const handleDeleteAddress = async (id: string) => {
    try {
      await deleteDeliveryAddress(id);
      refreshAddresses(await listDeliveryAddresses());
      setError('');
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(err instanceof Error ? err.message : '删除地址失败');
    }
  };

  const handleOpenCreateAddress = () => {
    setEditingAddress(null);
    setAddressFormOpen(true);
  };

  const handleOpenEditAddress = (address: DeliveryAddress) => {
    setEditingAddress(address);
    setAddressFormOpen(true);
  };

  const handleSaveAddress = async (input: DeliveryAddressInput) => {
    try {
      const savedAddress = editingAddress ? await updateDeliveryAddress(editingAddress.id, input) : await createDeliveryAddress(input);
      const next = await listDeliveryAddresses();
      setAddresses(next);
      setSelectedAddressId(savedAddress.id);
      setAddressFormOpen(false);
      setEditingAddress(null);
      setError('');
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(err instanceof Error ? err.message : '地址保存失败');
    }
  };

  const handleConfirm = async () => {
    if (!selectedAddress) {
      setError('请先新增收货地址');
      setEditingAddress(null);
      setAddressFormOpen(true);
      return;
    }
    setBusy(true);
    setError('');
    try {
      await onConfirm(selectedAddress);
    } catch (err) {
      if (handleAuthError(err)) return;
      setError(err instanceof Error ? err.message : '保证金支付失败');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modalMask paySheetMask">
      <section className="payModal depositModal" aria-modal="true" role="dialog">
        <button className="modalClose" onClick={onClose} aria-label="关闭保证金弹窗">
          ×
        </button>
        <h2>出价前需先缴纳保证金</h2>
        <section className="depositAmount" aria-label="保证金金额">
          <b className="scrollAmount" title={formatLotDeposit(lot)}>{formatLotDeposit(lot)}</b>
          <span>若竞拍不成功，保证金将原路返回</span>
        </section>
        <button className="depositOptionRow" type="button">
          <span>支付方式</span>
          <b>不分期</b>
          <ChevronRight size={18} />
        </button>
        <button className="depositOptionRow isAddress" type="button" onClick={handleAddressRowClick}>
          <span>收货地址</span>
          {selectedAddress ? (
            <b>
              {formatAddressSummary(selectedAddress)}
              <small>{formatAddressLine(selectedAddress)}</small>
            </b>
          ) : (
            <b>新增收货地址</b>
          )}
          <ChevronRight size={18} />
        </button>
        {error ? <p className="depositInlineError">{error}</p> : null}
        <button className="resultPrimaryButton" type="button" onClick={handleConfirm} disabled={busy}>
          {busy ? '支付中...' : '支付保证金'}
        </button>
        <p className="depositNote">阅读并同意《拍卖服务用户协议》</p>
      </section>
      {addressSheetOpen ? (
        <DeliveryAddressListSheet
          addresses={addresses}
          selectedAddressId={selectedAddress?.id}
          onSelect={handleSelectAddress}
          onAdd={handleOpenCreateAddress}
          onEdit={handleOpenEditAddress}
          onClose={() => setAddressSheetOpen(false)}
          onSetDefault={handleSetDefaultAddress}
          onDelete={handleDeleteAddress}
        />
      ) : null}
      {addressFormOpen ? (
        <DeliveryAddressFormSheet
          title={editingAddress ? '修改地址' : '新建地址'}
          initialAddress={editingAddress ?? undefined}
          onClose={() => setAddressFormOpen(false)}
          onSave={handleSaveAddress}
        />
      ) : null}
    </div>
  );
}

import { RefreshCw, WalletCards } from 'lucide-react';
import { apiClient } from '../../api/client';
import { DataContractError, parseAccountQuota, parseRuntimeStatus } from '../../api/contracts';
import { useAccountResource } from './useAccountResource';

export function formatAccountUSD(amount: number) {
  return new Intl.NumberFormat('en-US', {
    style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 6,
  }).format(amount);
}

async function loadBalance(signal: AbortSignal) {
  const [user, runtime] = await Promise.all([
    apiClient.get<unknown>('/user/self', { signal }),
    apiClient.get<unknown>('/status', { signal }),
  ]);
  const amount = parseAccountQuota(user) / parseRuntimeStatus(runtime).quota_per_unit;
  if (!Number.isFinite(amount)) throw new DataContractError();
  return amount;
}

export function AccountBalance({ refreshKey = '' }: { refreshKey?: string }) {
  const balance = useAccountResource(loadBalance, refreshKey);
  return (
    <section className="account-balance" aria-label="账户余额">
      <WalletCards aria-hidden="true" size={24} />
      <div className="account-balance__value" aria-live="polite" aria-busy={balance.status === 'loading'}>
        <h2>可用余额 <span>USD</span></h2>
        {balance.status === 'ready' && <strong>{formatAccountUSD(balance.data)}</strong>}
        {balance.status === 'loading' && <p>正在读取余额...</p>}
        {balance.status === 'error' && <p role="status">余额加载失败，请刷新重试。</p>}
      </div>
      <button className="console-icon-action" aria-label="刷新余额" title="刷新余额"
        type="button" onClick={balance.refresh} disabled={balance.status === 'loading'}>
        <RefreshCw aria-hidden="true" size={18} />
      </button>
    </section>
  );
}

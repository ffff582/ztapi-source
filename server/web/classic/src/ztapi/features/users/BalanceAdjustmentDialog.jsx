/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import React, { useRef, useState } from 'react';
import { X } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';
import { formatU, uToQuota, useQuotaPerUnit } from '../../quota.jsx';

function createIdempotencyKey() {
  if (typeof crypto?.randomUUID === 'function') return crypto.randomUUID();
  return `ztapi-${Date.now()}-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`;
}

export default function BalanceAdjustmentDialog({ user, onClose, onSuccess }) {
  const idempotencyKey = useRef(createIdempotencyKey());
  const quotaPerUnit = useQuotaPerUnit();
  const [delta, setDelta] = useState('');
  const [reason, setReason] = useState('');
  const [status, setStatus] = useState('idle');
  const [error, setError] = useState('');

  // Operators think in U; the ledger stores internal quota units.
  const parsedUnits = uToQuota(delta.trim() === '' ? Number.NaN : delta, quotaPerUnit);

  const submit = async (event) => {
    event.preventDefault();
    const parsedDelta = parsedUnits;
    if (!Number.isInteger(parsedDelta) || parsedDelta === 0) {
      setError('调整金额必须是非零的 U 数量。');
      return;
    }
    if (!reason.trim()) {
      setError('必须填写调整原因。');
      return;
    }
    setStatus('saving');
    setError('');
    try {
      await adminRequest({
        method: 'POST',
        url: `/api/admin/users/${user.id}/balance-adjustments`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          delta: parsedDelta,
          reason: reason.trim(),
          idempotency_key: idempotencyKey.current,
        }),
      });
      await onSuccess();
      onClose();
    } catch {
      setError('余额调整失败。再次提交会沿用同一个幂等键，不会重复入账。');
    } finally {
      setStatus('idle');
    }
  };

  return (
    <div className='ztapi-user-modal-layer'>
      <button
        type='button'
        className='ztapi-user-modal-backdrop'
        aria-label='关闭余额调整'
        disabled={status === 'saving'}
        onClick={onClose}
      />
      <section
        className='ztapi-user-modal'
        role='dialog'
        aria-modal='true'
        aria-label='调整余额'
      >
        <header>
          <div>
            <h3>调整余额</h3>
            <p>
              {user.username} · 当前余额 {formatU(user.quota, quotaPerUnit)}
            </p>
          </div>
          <button
            type='button'
            className='ztapi-user-icon-button'
            aria-label='关闭余额调整'
            disabled={status === 'saving'}
            onClick={onClose}
          >
            <X aria-hidden='true' size={18} />
          </button>
        </header>
        <form onSubmit={submit}>
          <label>
            <span>调整金额（U）</span>
            <input
              autoFocus
              required
              step='0.01'
              type='number'
              value={delta}
              onChange={(event) => setDelta(event.target.value)}
              placeholder='正数充值，负数扣减，单位 U'
            />
          </label>
          <p className='ztapi-user-form-note'>
            {Number.isInteger(parsedUnits) && parsedUnits !== 0
              ? `本次写入 ${parsedUnits > 0 ? '+' : ''}${parsedUnits.toLocaleString('en-US')} 额度（${formatU(parsedUnits, quotaPerUnit)}），调整后约 ${formatU(user.quota + parsedUnits, quotaPerUnit)}`
              : '请输入非零的 U 数量，例如 5 或 -2.5。'}
          </p>
          <label>
            <span>调整原因</span>
            <textarea
              required
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </label>
          <p className='ztapi-user-form-note'>
            本次操作将写入不可修改的余额账本。
          </p>
          {error ? (
            <p role='alert' className='ztapi-user-error'>
              {error}
            </p>
          ) : null}
          <footer>
            <button
              type='button'
              className='ztapi-user-secondary-button'
              disabled={status === 'saving'}
              onClick={onClose}
            >
              取消
            </button>
            <button
              type='submit'
              className='ztapi-user-primary-button'
              disabled={status === 'saving'}
            >
              {status === 'saving' ? '正在提交...' : '确认调整'}
            </button>
          </footer>
        </form>
      </section>
    </div>
  );
}

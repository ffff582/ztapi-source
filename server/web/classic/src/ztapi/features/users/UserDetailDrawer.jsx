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
import React, { useCallback, useEffect, useState } from 'react';
import { WalletCards, X } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';
import BalanceAdjustmentDialog from './BalanceAdjustmentDialog.jsx';

const roleLabels = {
  1: '普通用户',
  2: '客服',
  3: '财务',
  10: '管理员',
  100: '超级管理员',
};

function formatTime(value) {
  if (!value) return '暂无';
  const date =
    typeof value === 'number' ? new Date(value * 1000) : new Date(value);
  return Number.isNaN(date.getTime()) ? '暂无' : date.toLocaleString('zh-CN');
}

export default function UserDetailDrawer({
  canAdjustBalance = false,
  canChangeStaffStatus = false,
  canChangeStatus = false,
  canViewLedger = false,
  onChanged,
  onClose,
  userId,
}) {
  const [user, setUser] = useState(null);
  const [ledger, setLedger] = useState([]);
  const [status, setStatus] = useState('loading');
  const [error, setError] = useState('');
  const [balanceOpen, setBalanceOpen] = useState(false);
  const [statusFormOpen, setStatusFormOpen] = useState(false);
  const [statusReason, setStatusReason] = useState('');
  const [savingStatus, setSavingStatus] = useState(false);

  const load = useCallback(async () => {
    setStatus('loading');
    setError('');
    try {
      const [detail, ledgerPage] = await Promise.all([
        adminRequest({ url: `/api/admin/users/${userId}` }),
        canViewLedger
          ? adminRequest({
              url: `/api/admin/users/${userId}/balance-ledger?p=1&page_size=20`,
            })
          : Promise.resolve({ items: [] }),
      ]);
      setUser(detail);
      setLedger(ledgerPage?.items || []);
      setStatus('ready');
    } catch {
      setError('无法加载用户详情。');
      setStatus('error');
    }
  }, [canViewLedger, userId]);

  useEffect(() => {
    load();
  }, [load]);

  const changeStatus = async (event) => {
    event.preventDefault();
    if (!statusReason.trim()) return;
    const nextStatus = user.status === 1 ? 2 : 1;
    setSavingStatus(true);
    setError('');
    try {
      const updated = await adminRequest({
        method: 'PATCH',
        url: `/api/admin/users/${user.id}/status`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          status: nextStatus,
          expected_status: user.status,
          reason: statusReason.trim(),
        }),
      });
      setUser(updated);
      setStatusFormOpen(false);
      setStatusReason('');
      await onChanged?.();
    } catch {
      setError('状态修改失败，用户状态可能已发生变化，请关闭后重试。');
    } finally {
      setSavingStatus(false);
    }
  };

  const reloadAfterBalance = async () => {
    await load();
    await onChanged?.();
  };

  return (
    <div className='ztapi-user-drawer-layer'>
      <button
        type='button'
        className='ztapi-user-modal-backdrop'
        aria-label='关闭用户详情'
        onClick={onClose}
      />
      <aside
        className='ztapi-user-drawer'
        role='dialog'
        aria-modal='true'
        aria-label='用户详情'
      >
        <header>
          <div>
            <h2>用户详情</h2>
            <p>{user?.username || `用户 #${userId}`}</p>
          </div>
          <button
            type='button'
            className='ztapi-user-icon-button'
            aria-label='关闭用户详情'
            onClick={onClose}
          >
            <X aria-hidden='true' size={18} />
          </button>
        </header>

        {status === 'loading' ? (
          <p role='status' className='ztapi-user-drawer-state'>
            正在加载...
          </p>
        ) : null}
        {error ? (
          <p role='alert' className='ztapi-user-error ztapi-user-drawer-state'>
            {error}
          </p>
        ) : null}
        {user ? (
          <div className='ztapi-user-drawer-body'>
            <dl className='ztapi-user-detail-grid'>
              <div>
                <dt>账号</dt>
                <dd>{user.username}</dd>
              </div>
              <div>
                <dt>显示名称</dt>
                <dd>{user.display_name || '未设置'}</dd>
              </div>
              <div>
                <dt>邮箱</dt>
                <dd>{user.email || '未设置'}</dd>
              </div>
              <div>
                <dt>角色</dt>
                <dd>{roleLabels[user.role] || '未知'}</dd>
              </div>
              <div>
                <dt>状态</dt>
                <dd>{user.status === 1 ? '启用' : '停用'}</dd>
              </div>
              <div>
                <dt>用户组</dt>
                <dd>{user.group}</dd>
              </div>
              <div>
                <dt>余额</dt>
                <dd>{user.quota}</dd>
              </div>
              <div>
                <dt>已使用</dt>
                <dd>{user.used_quota}</dd>
              </div>
              <div>
                <dt>请求数</dt>
                <dd>{user.request_count}</dd>
              </div>
              <div>
                <dt>最后登录</dt>
                <dd>{formatTime(user.last_login_at)}</dd>
              </div>
            </dl>
            {user.remark ? (
              <p className='ztapi-user-remark'>{user.remark}</p>
            ) : null}

            <div className='ztapi-user-actions'>
              {canAdjustBalance ? (
                <button
                  type='button'
                  className='ztapi-user-primary-button'
                  onClick={() => setBalanceOpen(true)}
                >
                  <WalletCards aria-hidden='true' size={16} />
                  调整余额
                </button>
              ) : null}
              {canChangeStatus && (user.role === 1 || canChangeStaffStatus) ? (
                <button
                  type='button'
                  className='ztapi-user-secondary-button'
                  onClick={() => setStatusFormOpen(true)}
                >
                  {user.status === 1 ? '停用用户' : '启用用户'}
                </button>
              ) : null}
            </div>

            {statusFormOpen ? (
              <form className='ztapi-user-inline-form' onSubmit={changeStatus}>
                <label>
                  <span>状态变更原因</span>
                  <textarea
                    autoFocus
                    required
                    value={statusReason}
                    onChange={(event) => setStatusReason(event.target.value)}
                  />
                </label>
                <div>
                  <button
                    type='button'
                    className='ztapi-user-secondary-button'
                    onClick={() => setStatusFormOpen(false)}
                  >
                    取消
                  </button>
                  <button
                    type='submit'
                    className='ztapi-user-danger-button'
                    disabled={savingStatus}
                  >
                    {user.status === 1 ? '确认停用' : '确认启用'}
                  </button>
                </div>
              </form>
            ) : null}

            {canViewLedger ? (
              <section
                className='ztapi-user-ledger'
                aria-labelledby='ztapi-ledger-title'
              >
                <h3 id='ztapi-ledger-title'>余额账本</h3>
                {ledger.length ? (
                  <div className='ztapi-user-ledger-list'>
                    {ledger.map((entry) => (
                      <article key={entry.id}>
                        <strong>
                          {entry.delta > 0 ? '+' : ''}
                          {entry.delta}
                        </strong>
                        <span>
                          {entry.balance_before} → {entry.balance_after}
                        </span>
                        <p>{entry.reason}</p>
                        <time>{formatTime(entry.created_at)}</time>
                      </article>
                    ))}
                  </div>
                ) : (
                  <p>暂无余额变更记录。</p>
                )}
              </section>
            ) : null}
          </div>
        ) : null}
      </aside>
      {balanceOpen && user ? (
        <BalanceAdjustmentDialog
          user={user}
          onClose={() => setBalanceOpen(false)}
          onSuccess={reloadAfterBalance}
        />
      ) : null}
    </div>
  );
}

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
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Eye, Search, UsersRound } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';
import UserDetailDrawer from './UserDetailDrawer.jsx';

const roleLabels = {
  1: '普通用户',
  2: '客服',
  3: '财务',
  10: '管理员',
  100: '超级管理员',
};

export default function AdminUsersPage({
  canAdjustBalance = false,
  canChangeStaffStatus = false,
  canChangeStatus = false,
  canViewLedger = false,
}) {
  const [users, setUsers] = useState([]);
  const [query, setQuery] = useState('');
  const [activeQuery, setActiveQuery] = useState('');
  const [selectedUserId, setSelectedUserId] = useState(null);
  const [status, setStatus] = useState('loading');
  const [error, setError] = useState('');
  const loadGeneration = useRef(0);

  const load = useCallback(async () => {
    const generation = ++loadGeneration.current;
    setStatus('loading');
    setError('');
    const params = new URLSearchParams({ p: '1', page_size: '20' });
    if (activeQuery) params.set('keyword', activeQuery);
    try {
      const page = await adminRequest({ url: `/api/admin/users?${params}` });
      if (generation !== loadGeneration.current) return;
      setUsers(page?.items || []);
      setStatus('ready');
    } catch {
      if (generation !== loadGeneration.current) return;
      setError('用户列表加载失败。');
      setStatus('error');
    }
  }, [activeQuery]);

  useEffect(() => {
    load();
  }, [load]);

  const search = (event) => {
    event.preventDefault();
    setActiveQuery(query.trim());
  };

  return (
    <section
      className='ztapi-admin-page ztapi-users-page'
      aria-labelledby='ztapi-users-title'
    >
      <div className='ztapi-user-page-heading'>
        <div>
          <span className='ztapi-user-eyebrow'>
            <UsersRound aria-hidden='true' size={15} />
            用户运营
          </span>
          <h1 id='ztapi-users-title'>用户管理</h1>
          <p>查询账号、处理启停状态，并通过不可变账本调整余额。</p>
        </div>
      </div>

      <form className='ztapi-user-search' role='search' onSubmit={search}>
        <Search aria-hidden='true' size={17} />
        <input
          aria-label='搜索用户'
          type='search'
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder='账号、邮箱、显示名称或用户 ID'
        />
        <button type='submit'>搜索</button>
      </form>

      {status === 'loading' ? (
        <p role='status' className='ztapi-user-state'>
          正在加载用户...
        </p>
      ) : null}
      {error ? (
        <p role='alert' className='ztapi-user-error ztapi-user-state'>
          {error}
        </p>
      ) : null}
      {status === 'ready' ? (
        <div className='ztapi-user-table' role='table' aria-label='用户列表'>
          <div className='ztapi-user-row ztapi-user-table-head' role='row'>
            <span role='columnheader'>用户</span>
            <span role='columnheader'>角色</span>
            <span role='columnheader'>状态</span>
            <span role='columnheader'>用户组</span>
            <span role='columnheader'>余额 / 已用</span>
            <span role='columnheader'>操作</span>
          </div>
          {users.map((item) => (
            <div className='ztapi-user-row' role='row' key={item.id}>
              <div className='ztapi-user-identity' role='cell'>
                <strong>{item.username}</strong>
                <small>{item.email || `用户 #${item.id}`}</small>
              </div>
              <span role='cell'>{roleLabels[item.role] || '未知'}</span>
              <span
                role='cell'
                className={item.status === 1 ? 'is-enabled' : 'is-disabled'}
              >
                {item.status === 1 ? '启用' : '停用'}
              </span>
              <span role='cell'>{item.group}</span>
              <span role='cell'>
                {item.quota} / {item.used_quota}
              </span>
              <span role='cell'>
                <button
                  type='button'
                  className='ztapi-user-icon-button'
                  aria-label={`查看 ${item.username}`}
                  title='查看详情'
                  onClick={() => setSelectedUserId(item.id)}
                >
                  <Eye aria-hidden='true' size={17} />
                </button>
              </span>
            </div>
          ))}
          {!users.length ? (
            <p className='ztapi-user-empty'>没有符合条件的用户。</p>
          ) : null}
        </div>
      ) : null}

      {selectedUserId ? (
        <UserDetailDrawer
          userId={selectedUserId}
          canAdjustBalance={canAdjustBalance}
          canChangeStaffStatus={canChangeStaffStatus}
          canChangeStatus={canChangeStatus}
          canViewLedger={canViewLedger}
          onChanged={load}
          onClose={() => setSelectedUserId(null)}
        />
      ) : null}
    </section>
  );
}

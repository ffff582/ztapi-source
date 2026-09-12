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
import { Search, ShieldCheck, UserRoundCog, X } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';

const roles = [
  { value: 1, label: '普通用户', impact: '将移除全部后台管理权限。' },
  {
    value: 2,
    label: '客服',
    impact: '将获得用户读取、用户状态管理和日志读取权限。',
  },
  { value: 3, label: '财务', impact: '将获得订单与账本读取、财务处理权限。' },
  {
    value: 10,
    label: '管理员',
    impact: '将获得渠道、模型、用户、余额、财务和审计管理权限。',
  },
];

function roleLabel(role) {
  if (role === 100) return '超级管理员';
  return roles.find((item) => item.value === Number(role))?.label || '未知';
}

function RoleDialog({ staff, onClose, onSaved }) {
  const [role, setRole] = useState(staff.role === 100 ? 10 : staff.role);
  const [reason, setReason] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const selected = roles.find((item) => item.value === Number(role));

  const submit = async (event) => {
    event.preventDefault();
    if (!reason.trim()) return;
    setSaving(true);
    setError('');
    try {
      await adminRequest({
        method: 'PATCH',
        url: `/api/admin/staff/${staff.id}/role`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          role: Number(role),
          expected_role: staff.role,
          reason: reason.trim(),
        }),
      });
      await onSaved();
      onClose();
    } catch {
      setError('角色变更失败。员工角色可能已被其他管理员修改，请刷新后重试。');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className='ztapi-user-modal-layer'>
      <button
        type='button'
        className='ztapi-user-modal-backdrop'
        aria-label='关闭角色变更'
        onClick={onClose}
      />
      <section
        className='ztapi-user-modal'
        role='dialog'
        aria-modal='true'
        aria-label='角色变更确认'
      >
        <header>
          <div>
            <h3>修改员工角色</h3>
            <p>
              {staff.username} · 当前为 {roleLabel(staff.role)}
            </p>
          </div>
          <button
            type='button'
            className='ztapi-user-icon-button'
            aria-label='关闭角色变更'
            onClick={onClose}
          >
            <X aria-hidden='true' size={18} />
          </button>
        </header>
        <form onSubmit={submit}>
          <label>
            <span>新角色</span>
            <select
              aria-label='新角色'
              value={role}
              onChange={(event) => setRole(Number(event.target.value))}
            >
              {roles.map((item) => (
                <option value={item.value} key={item.value}>
                  {item.label}
                </option>
              ))}
            </select>
          </label>
          <div className='ztapi-role-impact'>
            <ShieldCheck aria-hidden='true' size={18} />
            <p>{selected?.impact}</p>
          </div>
          <label>
            <span>变更原因</span>
            <textarea
              required
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </label>
          <p className='ztapi-user-form-note'>
            确认后将立即刷新该员工的权限缓存，并写入审计日志。
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
              onClick={onClose}
            >
              取消
            </button>
            <button
              type='submit'
              className='ztapi-user-primary-button'
              disabled={saving || Number(role) === staff.role}
            >
              {saving ? '正在保存...' : '确认角色变更'}
            </button>
          </footer>
        </form>
      </section>
    </div>
  );
}

export default function StaffRolesPage() {
  const [staff, setStaff] = useState([]);
  const [query, setQuery] = useState('');
  const [activeQuery, setActiveQuery] = useState('');
  const [editing, setEditing] = useState(null);
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
      const page = await adminRequest({ url: `/api/admin/staff?${params}` });
      if (generation !== loadGeneration.current) return;
      setStaff(page?.items || []);
      setStatus('ready');
    } catch {
      if (generation !== loadGeneration.current) return;
      setError('员工列表加载失败。');
      setStatus('error');
    }
  }, [activeQuery]);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <section
      className='ztapi-admin-page ztapi-users-page'
      aria-labelledby='ztapi-staff-title'
    >
      <div className='ztapi-user-page-heading'>
        <div>
          <span className='ztapi-user-eyebrow'>
            <UserRoundCog aria-hidden='true' size={15} />
            权限治理
          </span>
          <h1 id='ztapi-staff-title'>员工管理</h1>
          <p>员工角色变更仅限超级管理员，并记录完整审计原因。</p>
        </div>
      </div>
      <form
        className='ztapi-user-search'
        role='search'
        onSubmit={(event) => {
          event.preventDefault();
          setActiveQuery(query.trim());
        }}
      >
        <Search aria-hidden='true' size={17} />
        <input
          aria-label='搜索员工'
          type='search'
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder='账号、邮箱或员工 ID'
        />
        <button type='submit'>搜索</button>
      </form>
      {status === 'loading' ? (
        <p role='status' className='ztapi-user-state'>
          正在加载员工...
        </p>
      ) : null}
      {error ? (
        <p role='alert' className='ztapi-user-error ztapi-user-state'>
          {error}
        </p>
      ) : null}
      {status === 'ready' ? (
        <div className='ztapi-staff-table' role='table' aria-label='员工列表'>
          <div className='ztapi-staff-row ztapi-user-table-head' role='row'>
            <span role='columnheader'>员工</span>
            <span role='columnheader'>角色</span>
            <span role='columnheader'>状态</span>
            <span role='columnheader'>操作</span>
          </div>
          {staff.map((item) => (
            <div className='ztapi-staff-row' role='row' key={item.id}>
              <div className='ztapi-user-identity' role='cell'>
                <strong>{item.username}</strong>
                <small>{item.email || `员工 #${item.id}`}</small>
              </div>
              <span role='cell'>{roleLabel(item.role)}</span>
              <span
                role='cell'
                className={item.status === 1 ? 'is-enabled' : 'is-disabled'}
              >
                {item.status === 1 ? '启用' : '停用'}
              </span>
              <span role='cell'>
                <button
                  type='button'
                  className='ztapi-user-icon-button'
                  aria-label={`修改 ${item.username} 的角色`}
                  title='修改角色'
                  onClick={() => setEditing(item)}
                >
                  <UserRoundCog aria-hidden='true' size={17} />
                </button>
              </span>
            </div>
          ))}
        </div>
      ) : null}
      {editing ? (
        <RoleDialog
          staff={editing}
          onClose={() => setEditing(null)}
          onSaved={load}
        />
      ) : null}
    </section>
  );
}

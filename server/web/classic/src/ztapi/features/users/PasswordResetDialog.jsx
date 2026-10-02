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
import React, { useState } from 'react';
import { X } from 'lucide-react';
import { adminRequest } from '../../auth/admin-session.js';

const minimumPasswordLength = 10;

function generatePassword() {
  const alphabet = 'abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789';
  const values = new Uint32Array(14);
  if (typeof crypto?.getRandomValues === 'function') {
    crypto.getRandomValues(values);
  } else {
    for (let index = 0; index < values.length; index += 1) {
      values[index] = Math.floor(Math.random() * alphabet.length);
    }
  }
  return Array.from(values, (value) => alphabet[value % alphabet.length]).join(
    '',
  );
}

export default function PasswordResetDialog({ user, onClose, onSuccess }) {
  const [password, setPassword] = useState('');
  const [reason, setReason] = useState('');
  const [status, setStatus] = useState('idle');
  const [error, setError] = useState('');

  const submit = async (event) => {
    event.preventDefault();
    if (password.length < minimumPasswordLength) {
      setError(`新密码至少 ${minimumPasswordLength} 位。`);
      return;
    }
    if (!reason.trim()) {
      setError('必须填写修改原因。');
      return;
    }
    setStatus('saving');
    setError('');
    try {
      await adminRequest({
        method: 'POST',
        url: `/api/admin/users/${user.id}/password`,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password, reason: reason.trim() }),
      });
      await onSuccess(password);
      onClose();
    } catch {
      setError('密码修改失败，用户的密码没有变化。');
    } finally {
      setStatus('idle');
    }
  };

  return (
    <div className='ztapi-user-modal-layer'>
      <button
        type='button'
        className='ztapi-user-modal-backdrop'
        aria-label='关闭修改密码'
        disabled={status === 'saving'}
        onClick={onClose}
      />
      <section
        className='ztapi-user-modal'
        role='dialog'
        aria-modal='true'
        aria-label='修改密码'
      >
        <header>
          <div>
            <h3>修改密码</h3>
            <p>{user.username}</p>
          </div>
          <button
            type='button'
            className='ztapi-user-icon-button'
            aria-label='关闭修改密码'
            disabled={status === 'saving'}
            onClick={onClose}
          >
            <X aria-hidden='true' size={18} />
          </button>
        </header>
        <form onSubmit={submit}>
          <label>
            <span>新密码</span>
            <input
              autoFocus
              required
              type='text'
              autoComplete='off'
              minLength={minimumPasswordLength}
              value={password}
              onChange={(event) => {
                setPassword(event.target.value);
                setError('');
              }}
              placeholder={`至少 ${minimumPasswordLength} 位`}
            />
          </label>
          <button
            type='button'
            className='ztapi-user-secondary-button'
            onClick={() => {
              setPassword(generatePassword());
              setError('');
            }}
          >
            随机生成
          </button>
          <label>
            <span>修改原因</span>
            <textarea
              required
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </label>
          <p className='ztapi-user-form-note'>
            保存后该用户的所有登录会话会立即失效，需要用新密码重新登录；API
            令牌不受影响。本次操作会记入审计日志，密码本身不会被记录，请自行转告用户。
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
              {status === 'saving' ? '保存中...' : '保存新密码'}
            </button>
          </footer>
        </form>
      </section>
    </div>
  );
}

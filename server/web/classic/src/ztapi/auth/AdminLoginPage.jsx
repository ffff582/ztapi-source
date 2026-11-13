import React, { useState } from 'react';
import { useAdminAuth } from './AdminAuthProvider';

export default function AdminLoginPage(props) {
  const auth = useAdminAuth();
  const [credentials, setCredentials] = useState({
    username: '',
    password: '',
  });
  const [pending, setPending] = useState(false);
  const [error, setError] = useState('');

  const updateField = (field) => (event) => {
    setCredentials((current) => ({ ...current, [field]: event.target.value }));
  };

  const submit = async (event) => {
    event.preventDefault();
    setPending(true);
    setError('');
    try {
      const session = await auth.login(credentials);
      props.onSuccess?.(session);
    } catch {
      setError('管理员登录失败。');
    } finally {
      setPending(false);
    }
  };

  return (
    <main className='ztapi-admin-login'>
      <h1>管理员登录</h1>
      <form onSubmit={submit}>
        <label htmlFor='admin-username'>用户名</label>
        <input
          id='admin-username'
          name='username'
          autoComplete='username'
          value={credentials.username}
          onChange={updateField('username')}
          required
        />
        <label htmlFor='admin-password'>密码</label>
        <input
          id='admin-password'
          name='password'
          type='password'
          autoComplete='current-password'
          value={credentials.password}
          onChange={updateField('password')}
          required
        />
        <button type='submit' disabled={pending}>
          {pending ? '登录中...' : '登录'}
        </button>
        {error ? <p role='alert'>{error}</p> : null}
      </form>
    </main>
  );
}

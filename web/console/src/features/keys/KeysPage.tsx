import {
  Ban,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  KeyRound,
  Plus,
  Trash2,
} from 'lucide-react';
import {
  useEffect,
  useMemo,
  useState,
  type FormEvent,
} from 'react';
import { useBlocker } from 'react-router-dom';
import { apiClient } from '../../api/client';
import {
  getPublicModelFamily,
  parseCreatedToken,
  parsePricingEnvelope,
  parseRuntimeStatus,
  parseUserTokenResponse,
  type PricingModel,
  type UserToken,
} from '../../api/contracts';
import { useOneTimeSecret } from './OneTimeSecretProvider';
import { useTokenPageCoordinator } from './useTokenPageCoordinator';

const TOKEN_STATUS_ENABLED = 1;
const TOKEN_STATUS_DISABLED = 2;

interface KeyForm {
  name: string;
  expirationMode: 'never' | 'custom';
  expiresAt: string;
  restrictModels: boolean;
  selectedModels: string[];
  unlimitedQuota: boolean;
  spendingLimit: string;
  allowIPs: string;
}

type FormErrors = Partial<
  Record<'name' | 'expiration' | 'models' | 'spending' | 'ips', string>
>;

const initialForm: KeyForm = {
  name: '',
  expirationMode: 'never',
  expiresAt: '',
  restrictModels: false,
  selectedModels: [],
  unlimitedQuota: true,
  spendingLimit: '',
  allowIPs: '',
};

function parseIPv4(address: string) {
  const parts = address.split('.');
  if (
    parts.length !== 4 ||
    parts.some(
      (part) =>
        !/^(0|[1-9]\d{0,2})$/.test(part) ||
        Number(part) < 0 ||
        Number(part) > 255,
    )
  ) {
    return null;
  }
  return parts.reduce((value, part) => (value << 8) + Number(part), 0) >>> 0;
}

function formatIPv4(value: number) {
  return [24, 16, 8, 0].map((shift) => (value >>> shift) & 255).join('.');
}

function parseIPv6(address: string) {
  if (address.includes('%') || address.includes('.')) {
    return null;
  }
  const halves = address.toLowerCase().split('::');
  if (halves.length > 2) {
    return null;
  }
  const left = halves[0] === '' ? [] : halves[0].split(':');
  const right =
    halves.length === 1 || halves[1] === '' ? [] : halves[1].split(':');
  if (
    [...left, ...right].some((part) => !/^[0-9a-f]{1,4}$/.test(part)) ||
    (halves.length === 1 && left.length !== 8) ||
    (halves.length === 2 && left.length + right.length >= 8)
  ) {
    return null;
  }
  const zeros = Array.from(
    { length: 8 - left.length - right.length },
    () => '0',
  );
  const groups = [...left, ...zeros, ...right].map((part) => Number.parseInt(part, 16));
  return groups.reduce(
    (value, group) => (value << 16n) | BigInt(group),
    0n,
  );
}

function formatIPv6(value: bigint) {
  const groups = Array.from({ length: 8 }, (_, index) =>
    Number((value >> BigInt((7 - index) * 16)) & 0xffffn).toString(16),
  );
  let bestStart = -1;
  let bestLength = 0;
  let currentStart = -1;

  for (let index = 0; index <= groups.length; index += 1) {
    if (index < groups.length && groups[index] === '0') {
      if (currentStart === -1) {
        currentStart = index;
      }
    } else if (currentStart !== -1) {
      const length = index - currentStart;
      if (length > bestLength && length >= 2) {
        bestStart = currentStart;
        bestLength = length;
      }
      currentStart = -1;
    }
  }

  if (bestStart === -1) {
    return groups.join(':');
  }
  const before = groups.slice(0, bestStart).join(':');
  const after = groups.slice(bestStart + bestLength).join(':');
  return `${before}::${after}`;
}

function normalizeIPEntry(entry: string) {
  const segments = entry.split('/');
  if (segments.length > 2 || segments[0].trim() === '') {
    return null;
  }
  const address = segments[0].trim();
  const ipv4 = parseIPv4(address);
  if (ipv4 !== null) {
    const prefix = segments.length === 1 ? 32 : Number(segments[1]);
    if (!Number.isInteger(prefix) || prefix < 0 || prefix > 32) {
      return null;
    }
    const mask = prefix === 0 ? 0 : (0xffffffff << (32 - prefix)) >>> 0;
    return `${formatIPv4(ipv4 & mask)}/${prefix}`;
  }

  const ipv6 = parseIPv6(address);
  if (ipv6 === null) {
    return null;
  }
  const prefix = segments.length === 1 ? 128 : Number(segments[1]);
  if (!Number.isInteger(prefix) || prefix < 0 || prefix > 128) {
    return null;
  }
  const mask =
    prefix === 0
      ? 0n
      : ((1n << BigInt(prefix)) - 1n) << BigInt(128 - prefix);
  return `${formatIPv6(ipv6 & mask)}/${prefix}`;
}

function normalizeAllowIPs(raw: string) {
  const entries = raw
    .split(/[,\r\n]/)
    .map((entry) => entry.trim())
    .filter(Boolean);
  const normalized = entries.map(normalizeIPEntry);
  if (normalized.some((entry) => entry === null)) {
    return null;
  }
  return [...new Set(normalized as string[])].sort().join('\n');
}

function buildCreatePayload(form: KeyForm, quotaPerUnit: number) {
  const errors: FormErrors = {};
  const name = form.name.trim();
  if (name === '') {
    errors.name = '请输入密钥名称';
  } else if (new TextEncoder().encode(name).length > 50) {
    errors.name = '密钥名称不能超过 50 个 UTF-8 字节';
  }

  let expiredTime = -1;
  if (form.expirationMode === 'custom') {
    const parsed = Date.parse(form.expiresAt);
    if (!Number.isFinite(parsed) || parsed <= Date.now()) {
      errors.expiration = '请选择未来的过期时间';
    } else {
      expiredTime = Math.floor(parsed / 1000);
    }
  }

  const selectedModels = [...new Set(form.selectedModels)].sort();
  if (form.restrictModels && selectedModels.length === 0) {
    errors.models = '至少选择一个可用模型';
  }

  let remainQuota = 0;
  if (!form.unlimitedQuota) {
    const spendingLimit = Number(form.spendingLimit);
    if (
      !Number.isFinite(spendingLimit) ||
      spendingLimit <= 0 ||
      spendingLimit > 1_000_000_000
    ) {
      errors.spending = '请输入大于 0 的额度上限';
    } else {
      const convertedQuota = spendingLimit * quotaPerUnit;
      if (!Number.isSafeInteger(convertedQuota) || convertedQuota < 1) {
        errors.spending = '额度必须转换为至少 1 个安全整数内部单位';
      } else {
        remainQuota = convertedQuota;
      }
    }
  }

  const allowIPs = normalizeAllowIPs(form.allowIPs);
  if (allowIPs === null) {
    errors.ips = 'IP 白名单包含无效的 IP 或 CIDR';
  }

  if (Object.keys(errors).length > 0) {
    return { errors, payload: null };
  }
  return {
    errors,
    payload: {
      name,
      status: TOKEN_STATUS_ENABLED,
      expired_time: expiredTime,
      unlimited_quota: form.unlimitedQuota,
      remain_quota: remainQuota,
      model_limits_enabled: form.restrictModels,
      model_limits: form.restrictModels ? selectedModels.join(',') : '',
      allow_ips: allowIPs ?? '',
    },
  };
}

function tokenStatus(token: UserToken) {
  if (token.status === TOKEN_STATUS_ENABLED) {
    return '已启用';
  }
  if (token.status === TOKEN_STATUS_DISABLED) {
    return '已禁用';
  }
  if (token.status === 3) {
    return '已过期';
  }
  return '额度已用尽';
}

function formatExpiration(timestamp: number) {
  if (timestamp === -1) {
    return '永不过期';
  }
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(new Date(timestamp * 1000));
}

export function KeysPage() {
  const [form, setForm] = useState<KeyForm>(initialForm);
  const [formErrors, setFormErrors] = useState<FormErrors>({});
  const {
    pageNumber: tokenPageNumber,
    page: tokenPage,
    loadStatus: tokenLoadStatus,
    navigate: navigateTokenPage,
    revalidate: revalidateTokenPage,
    commitMutation: commitTokenMutation,
  } = useTokenPageCoordinator();
  const [pricing, setPricing] = useState<PricingModel[]>([]);
  const [quotaPerUnit, setQuotaPerUnit] = useState<number | null>(null);
  const [creationStatus, setCreationStatus] = useState<
    'loading' | 'ready' | 'error'
  >('loading');
  const [operationError, setOperationError] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [busyTokenID, setBusyTokenID] = useState<number | null>(null);
  const [confirmRevokeID, setConfirmRevokeID] = useState<number | null>(null);
  const { secretPending, showOneTimeSecret } = useOneTimeSecret();
  const blocker = useBlocker(secretPending);

  const publicModels = useMemo(
    () =>
      pricing
        .filter(
          (model) =>
            getPublicModelFamily(model.model_name, model.owner_by) !== null,
        )
        .sort((a, b) => a.model_name.localeCompare(b.model_name)),
    [pricing],
  );

  useEffect(() => {
    let active = true;
    setCreationStatus('loading');
    void Promise.all([
      apiClient.getResponse<unknown>('/pricing').then(parsePricingEnvelope),
      apiClient.get<unknown>('/status').then(parseRuntimeStatus),
    ])
      .then(([pricingValue, statusValue]) => {
        if (!active) {
          return;
        }
        setPricing(pricingValue.models);
        setQuotaPerUnit(statusValue.quota_per_unit);
        setCreationStatus('ready');
      })
      .catch(() => {
        if (active) {
          setCreationStatus('error');
        }
      });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    if (!secretPending && blocker.state === 'blocked') {
      blocker.reset();
    }
  }, [blocker, secretPending]);

  async function createToken(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (quotaPerUnit === null || creationStatus !== 'ready') {
      return;
    }
    const createdPage = tokenPageNumber;
    const result = buildCreatePayload(form, quotaPerUnit);
    setFormErrors(result.errors);
    setOperationError(false);
    if (result.payload === null) {
      return;
    }

    setSubmitting(true);
    try {
      const value = await apiClient.post<unknown>('/token/', result.payload);
      const created = parseCreatedToken(value);
      showOneTimeSecret(created.key);
      setForm(initialForm);
      revalidateTokenPage(createdPage);
    } catch {
      setOperationError(true);
    } finally {
      setSubmitting(false);
    }
  }

  async function updateStatus(token: UserToken) {
    const updatedPage = tokenPageNumber;
    const nextStatus =
      token.status === TOKEN_STATUS_ENABLED
        ? TOKEN_STATUS_DISABLED
        : TOKEN_STATUS_ENABLED;
    setBusyTokenID(token.id);
    setOperationError(false);
    try {
      const value = await apiClient.put<unknown, { id: number; status: number }>(
        '/token/?status_only=true',
        { id: token.id, status: nextStatus },
      );
      const updated = parseUserTokenResponse(value);
      commitTokenMutation(updatedPage, (current) => {
        if (!current.items.some((item) => item.id === updated.id)) {
          return null;
        }
        return {
          ...current,
          items: current.items.map((item) =>
            item.id === updated.id ? updated : item,
          ),
        };
      });
      revalidateTokenPage(updatedPage);
    } catch {
      setOperationError(true);
    } finally {
      setBusyTokenID(null);
    }
  }

  async function revokeToken(tokenID: number) {
    const revokedPage = tokenPageNumber;
    setBusyTokenID(tokenID);
    setOperationError(false);
    try {
      await apiClient.delete<unknown>(`/token/${tokenID}`);
      const updatedPage = commitTokenMutation(revokedPage, (current) => {
        if (!current.items.some((token) => token.id === tokenID)) {
          return null;
        }
        return {
          ...current,
          total: Math.max(0, current.total - 1),
          items: current.items.filter((token) => token.id !== tokenID),
        };
      });
      revalidateTokenPage(revokedPage);
      if (
        updatedPage !== null &&
        updatedPage.items.length === 0 &&
        revokedPage > 1
      ) {
        navigateTokenPage(revokedPage - 1);
      }
      setConfirmRevokeID(null);
    } catch {
      setOperationError(true);
    } finally {
      setBusyTokenID(null);
    }
  }

  const tokens = tokenPage?.items ?? [];
  const tokenPageCount =
    tokenPage === null
      ? 1
      : Math.max(1, Math.ceil(tokenPage.total / tokenPage.page_size));

  return (
    <div className="console-page">
      <header className="console-page__header">
        <div>
          <p className="console-eyebrow">访问凭证</p>
          <h1>API 密钥</h1>
        </div>
        <p>创建受模型、额度、来源 IP 和有效期约束的访问凭证。</p>
      </header>

      <section className="console-panel" aria-labelledby="create-key-heading">
        <div className="console-panel__heading">
          <Plus aria-hidden="true" size={19} />
          <h2 id="create-key-heading">创建密钥</h2>
        </div>
        {creationStatus === 'loading' && (
          <div className="console-state" aria-live="polite" aria-busy="true">
            正在加载创建选项...
          </div>
        )}
        {creationStatus === 'error' && (
          <div className="console-state console-state--error" role="alert">
            创建选项加载失败，请刷新后重试。
          </div>
        )}
        {creationStatus === 'ready' && (
          <form className="key-form" onSubmit={createToken} noValidate>
          <div className="console-field key-form__wide">
            <label htmlFor="key-name">密钥名称</label>
            <input
              aria-invalid={formErrors.name !== undefined}
              id="key-name"
              maxLength={50}
              value={form.name}
              onChange={(event) =>
                setForm((current) => ({ ...current, name: event.target.value }))
              }
            />
            {formErrors.name && (
              <p className="console-field__error">{formErrors.name}</p>
            )}
          </div>

          <div className="console-field">
            <label htmlFor="key-expiration-mode">有效期</label>
            <select
              id="key-expiration-mode"
              value={form.expirationMode}
              onChange={(event) =>
                setForm((current) => ({
                  ...current,
                  expirationMode: event.target.value as KeyForm['expirationMode'],
                }))
              }
            >
              <option value="never">永不过期</option>
              <option value="custom">指定时间</option>
            </select>
          </div>
          {form.expirationMode === 'custom' && (
            <div className="console-field">
              <label htmlFor="key-expiration">过期时间</label>
              <input
                aria-invalid={formErrors.expiration !== undefined}
                id="key-expiration"
                type="datetime-local"
                value={form.expiresAt}
                onChange={(event) =>
                  setForm((current) => ({
                    ...current,
                    expiresAt: event.target.value,
                  }))
                }
              />
              {formErrors.expiration && (
                <p className="console-field__error">{formErrors.expiration}</p>
              )}
            </div>
          )}

          <fieldset className="key-form__fieldset key-form__wide">
            <legend>模型权限</legend>
            <label className="console-check">
              <input
                checked={form.restrictModels}
                type="checkbox"
                onChange={(event) =>
                  setForm((current) => ({
                    ...current,
                    restrictModels: event.target.checked,
                  }))
                }
              />
              限制可用模型
            </label>
            {form.restrictModels && (
              <div className="key-model-options">
                {publicModels.map((model) => (
                  <label className="console-check" key={model.model_name}>
                    <input
                      checked={form.selectedModels.includes(model.model_name)}
                      type="checkbox"
                      onChange={(event) =>
                        setForm((current) => ({
                          ...current,
                          selectedModels: event.target.checked
                            ? [...current.selectedModels, model.model_name]
                            : current.selectedModels.filter(
                                (name) => name !== model.model_name,
                              ),
                        }))
                      }
                    />
                    {model.model_name}
                  </label>
                ))}
                {publicModels.length === 0 && (
                  <p className="console-empty-inline">暂无可限制的公开模型</p>
                )}
              </div>
            )}
            {formErrors.models && (
              <p className="console-field__error">{formErrors.models}</p>
            )}
          </fieldset>

          <fieldset className="key-form__fieldset">
            <legend>额度限制</legend>
            <label className="console-check">
              <input
                checked={form.unlimitedQuota}
                type="checkbox"
                onChange={(event) =>
                  setForm((current) => ({
                    ...current,
                    unlimitedQuota: event.target.checked,
                  }))
                }
              />
              不限制密钥额度
            </label>
            {!form.unlimitedQuota && (
              <div className="console-field console-field--nested">
                <label htmlFor="key-spending-limit">额度上限</label>
                <input
                  aria-invalid={formErrors.spending !== undefined}
                  id="key-spending-limit"
                  inputMode="decimal"
                  min="0"
                  step="0.01"
                  type="number"
                  value={form.spendingLimit}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      spendingLimit: event.target.value,
                    }))
                  }
                />
                {formErrors.spending && (
                  <p className="console-field__error">{formErrors.spending}</p>
                )}
              </div>
            )}
          </fieldset>

          <div className="console-field key-form__wide">
            <label htmlFor="key-allow-ips">IP 白名单</label>
            <textarea
              aria-invalid={formErrors.ips !== undefined}
              id="key-allow-ips"
              placeholder="192.0.2.10&#10;2001:db8::/48"
              rows={3}
              value={form.allowIPs}
              onChange={(event) =>
                setForm((current) => ({
                  ...current,
                  allowIPs: event.target.value,
                }))
              }
            />
            <p className="console-field__help">每行一个 IP 或 CIDR，留空允许任意来源。</p>
            {formErrors.ips && (
              <p className="console-field__error">{formErrors.ips}</p>
            )}
          </div>

          {operationError && (
            <p className="console-alert key-form__wide" role="alert">
              操作失败，请稍后重试。
            </p>
          )}
            <button
              className="console-button console-button--primary key-form__submit"
              disabled={submitting || creationStatus !== 'ready'}
              type="submit"
            >
              <KeyRound aria-hidden="true" size={17} />
              {submitting ? '正在创建...' : '创建 API Key'}
            </button>
          </form>
        )}
      </section>

      <section className="console-section" aria-labelledby="key-list-heading">
        <div className="console-section__heading">
          <div>
            <p className="console-eyebrow">现有凭证</p>
            <h2 id="key-list-heading">密钥列表</h2>
          </div>
          {tokenLoadStatus === 'ready' && tokenPage !== null && (
            <span>{tokenPage.total} 个</span>
          )}
        </div>
        {tokenLoadStatus === 'loading' && (
          <div className="console-state" aria-live="polite" aria-busy="true">
            正在加载密钥...
          </div>
        )}
        {tokenLoadStatus === 'error' && (
          <div className="console-state console-state--error" role="alert">
            密钥数据加载失败，请刷新后重试。
          </div>
        )}
        {tokenLoadStatus === 'ready' && tokens.length === 0 && (
          <div className="console-state">尚未创建 API 密钥。</div>
        )}
        {tokenLoadStatus === 'ready' && tokens.length > 0 && (
          <>
            <div className="console-table-wrap">
              <table className="console-table">
                <thead>
                  <tr>
                    <th scope="col">名称</th>
                    <th scope="col">Key</th>
                    <th scope="col">状态</th>
                    <th scope="col">有效期</th>
                    <th scope="col">限制</th>
                    <th scope="col">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {tokens.map((token) => (
                    <tr key={token.id}>
                      <td>
                        <strong>{token.name}</strong>
                      </td>
                      <td>
                        <code>{token.key_prefix}</code>
                      </td>
                      <td>
                        <span
                          className={`console-status console-status--${
                            token.status === TOKEN_STATUS_ENABLED
                              ? 'success'
                              : 'muted'
                          }`}
                        >
                          {tokenStatus(token)}
                        </span>
                      </td>
                      <td>{formatExpiration(token.expired_time)}</td>
                      <td className="key-limits">
                        <span>
                          {token.model_limits_enabled
                            ? token.model_limits
                            : '全部模型'}
                        </span>
                        <span>{token.allow_ips || '任意来源 IP'}</span>
                      </td>
                      <td>
                        <div className="console-actions">
                          <button
                            className="console-icon-action"
                            disabled={busyTokenID === token.id}
                            title={
                              token.status === TOKEN_STATUS_ENABLED
                                ? '禁用密钥'
                                : '启用密钥'
                            }
                            type="button"
                            onClick={() => void updateStatus(token)}
                          >
                            {token.status === TOKEN_STATUS_ENABLED ? (
                              <Ban aria-hidden="true" size={16} />
                            ) : (
                              <CheckCircle2 aria-hidden="true" size={16} />
                            )}
                            {token.status === TOKEN_STATUS_ENABLED
                              ? '禁用'
                              : '启用'}
                          </button>
                          {confirmRevokeID === token.id ? (
                            <>
                              <button
                                className="console-icon-action console-icon-action--danger"
                                disabled={busyTokenID === token.id}
                                type="button"
                                onClick={() => void revokeToken(token.id)}
                              >
                                <Trash2 aria-hidden="true" size={16} />
                                确认撤销
                              </button>
                              <button
                                className="console-icon-action"
                                type="button"
                                onClick={() => setConfirmRevokeID(null)}
                              >
                                取消
                              </button>
                            </>
                          ) : (
                            <button
                              className="console-icon-action console-icon-action--danger"
                              disabled={busyTokenID === token.id}
                              type="button"
                              onClick={() => setConfirmRevokeID(token.id)}
                            >
                              <Trash2 aria-hidden="true" size={16} />
                              撤销
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <nav className="console-pagination" aria-label="密钥分页">
              <button
                aria-label="上一页"
                className="console-icon-action"
                disabled={tokenPageNumber <= 1}
                title="上一页"
                type="button"
                onClick={() => navigateTokenPage(tokenPageNumber - 1)}
              >
                <ChevronLeft aria-hidden="true" size={17} />
              </button>
              <span>第 {tokenPageNumber} / {tokenPageCount} 页</span>
              <button
                aria-label="下一页"
                className="console-icon-action"
                disabled={tokenPageNumber >= tokenPageCount}
                title="下一页"
                type="button"
                onClick={() => navigateTokenPage(tokenPageNumber + 1)}
              >
                <ChevronRight aria-hidden="true" size={17} />
              </button>
            </nav>
          </>
        )}
      </section>
    </div>
  );
}

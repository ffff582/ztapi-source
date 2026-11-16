import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Check,
  ChevronLeft,
  ChevronRight,
  RefreshCw,
  Send,
} from 'lucide-react';
import { useSearchParams } from 'react-router-dom';
import { supportAdminApi, supportAdminErrorMessage } from './support-api';
import './support.css';
import SupportAttachment from './SupportAttachment';

const statuses = { open: '待处理', in_progress: '处理中', resolved: '已解决' };
const roles = {
  user: '用户',
  assistant: 'AI 回复',
  human: '人工回复',
  system: '系统通知',
};
const date = (value) =>
  new Date(value).toLocaleString('zh-CN', { hour12: false });

export default function SupportTicketsPage() {
  const [params, setParams] = useSearchParams();
  const [filter, setFilter] = useState('open');
  const [cursor, setCursor] = useState(0);
  const [previous, setPrevious] = useState([]);
  const [list, setList] = useState({ items: [], next_after_id: 0 });
  const [listLoading, setListLoading] = useState(true);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detail, setDetail] = useState(null);
  const [reply, setReply] = useState('');
  const [target, setTarget] = useState('open');
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState('');
  const [error, setError] = useState('');
  const [statusStale, setStatusStale] = useState(false);
  const listGeneration = useRef(0);
  const detailGeneration = useRef(0);
  const lock = useRef(false);
  const operation = useRef(null);
  const alive = useRef(true);
  const selectedId = useRef(null);

  const loadList = useCallback(async (status, after) => {
    const current = ++listGeneration.current;
    setListLoading(true);
    try {
      const result = await supportAdminApi.list(status, after);
      if (alive.current && current === listGeneration.current) setList(result);
    } catch (cause) {
      if (alive.current && current === listGeneration.current)
        setError(supportAdminErrorMessage(cause));
    } finally {
      if (alive.current && current === listGeneration.current)
        setListLoading(false);
    }
  }, []);

  const loadDetail = useCallback(async (id) => {
    const current = ++detailGeneration.current;
    setDetailLoading(true);
    try {
      const result = await supportAdminApi.detail(id);
      if (
        !alive.current ||
        current !== detailGeneration.current ||
        selectedId.current !== id
      )
        return false;
      setDetail(result);
      setTarget(result.ticket.status);
      setStatusStale(false);
      return true;
    } catch (cause) {
      if (alive.current && current === detailGeneration.current) {
        setStatusStale(true);
        setError(supportAdminErrorMessage(cause));
      }
      return false;
    } finally {
      if (alive.current && current === detailGeneration.current)
        setDetailLoading(false);
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    void loadList('open', 0);
    const deepLink = Number(params.get('ticket'));
    if (Number.isSafeInteger(deepLink) && deepLink > 0) {
      selectedId.current = deepLink;
      void loadDetail(deepLink);
    }
    return () => {
      alive.current = false;
      listGeneration.current++;
      detailGeneration.current++;
    };
    // Explicit selections update the URL without resetting edits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loadList, loadDetail]);

  function choose(id) {
    if (busy || failed) return;
    selectedId.current = id;
    setDetail(null);
    setReply('');
    setError('');
    setStatusStale(false);
    setParams({ ticket: String(id) }, { replace: true });
    void loadDetail(id);
  }

  function updateTicket(ticket, message) {
    setDetail((value) =>
      value
        ? {
            ...value,
            ticket,
            messages: message
              ? [
                  ...value.messages.filter((item) => item.id !== message.id),
                  message,
                ]
              : value.messages,
          }
        : value,
    );
    setList((value) => ({
      ...value,
      items: value.items.map((item) => (item.id === ticket.id ? ticket : item)),
    }));
    setTarget(ticket.status);
  }

  async function mutate(kind, event) {
    event?.preventDefault();
    if (
      lock.current ||
      !detail ||
      detailLoading ||
      statusStale ||
      (failed && failed !== kind)
    )
      return;
    if (!operation.current) {
      if (kind === 'reply' && !reply.trim()) return;
      if (kind === 'status' && target === detail.ticket.status) return;
      operation.current = {
        kind,
        id: detail.ticket.id,
        input:
          kind === 'reply'
            ? { content: reply.trim(), idempotency_key: crypto.randomUUID() }
            : {
                status: target,
                version: detail.ticket.version,
                idempotency_key: crypto.randomUUID(),
              },
      };
    }
    const mutation = operation.current;
    lock.current = true;
    setBusy(true);
    setError('');
    try {
      const result = await supportAdminApi[mutation.kind](
        mutation.id,
        mutation.input,
      );
      if (!alive.current) return;
      if (mutation.kind === 'reply') {
        updateTicket(result.ticket, result.message);
        setReply('');
      } else updateTicket(result);
      operation.current = null;
      setFailed('');
    } catch (cause) {
      if (!alive.current) return;
      if (mutation.kind === 'status' && cause?.status === 409) {
        operation.current = null;
        setFailed('');
        setStatusStale(true);
        await loadDetail(mutation.id);
        if (alive.current)
          setError(
            '状态已被其他员工修改，已尝试读取最新版本；请核对后重新选择并应用，不会自动重试。',
          );
      } else {
        setFailed(mutation.kind);
        setError(supportAdminErrorMessage(cause));
      }
    } finally {
      lock.current = false;
      if (alive.current) setBusy(false);
    }
  }

  const frozen = busy || !!failed;
  const d = detail?.diagnosis;
  return (
    <section className='ztapi-ops-page ztapi-support-page'>
      <header className='ztapi-ops-heading'>
        <h1>客服工单</h1>
        <button
          type='button'
          title='刷新工单列表'
          aria-label='刷新工单列表'
          disabled={busy || listLoading}
          onClick={() => void loadList(filter, cursor)}
        >
          <RefreshCw size={17} />
        </button>
      </header>
      {error && <p role='alert'>{error}</p>}
      <div className='ztapi-support-layout'>
        <aside className='ztapi-support-inbox'>
          <label>
            工单状态筛选
            <select
              aria-label='工单状态筛选'
              value={filter}
              disabled={frozen}
              onChange={(event) => {
                const value = event.target.value;
                setFilter(value);
                setCursor(0);
                setPrevious([]);
                void loadList(value, 0);
              }}
            >
              <option value='all'>全部</option>
              {Object.entries(statuses).map(([value, label]) => (
                <option value={value} key={value}>
                  {label}
                </option>
              ))}
            </select>
          </label>
          {listLoading && <p role='status'>正在加载工单...</p>}
          <nav aria-label='工单列表'>
            {list.items.map((item) => (
              <button
                type='button'
                key={item.id}
                disabled={frozen || listLoading}
                aria-current={
                  detail?.ticket.id === item.id ? 'page' : undefined
                }
                onClick={() => choose(item.id)}
              >
                <strong>
                  #{item.id} {item.subject}
                </strong>
                <span>
                  {statuses[item.status] || '状态未知'} ·{' '}
                  {date(item.updated_at)}
                </span>
                {item.request_id && <span>{item.request_id}</span>}
              </button>
            ))}
          </nav>
          {!listLoading && !list.items.length && <p>暂无工单</p>}
          <nav className='ztapi-support-pagination' aria-label='工单分页'>
            <button
              type='button'
              title='上一页'
              aria-label='上一页'
              disabled={frozen || listLoading || !previous.length}
              onClick={() => {
                const after = previous[previous.length - 1];
                setPrevious(previous.slice(0, -1));
                setCursor(after);
                void loadList(filter, after);
              }}
            >
              <ChevronLeft size={18} />
            </button>
            <button
              type='button'
              title='下一页'
              aria-label='下一页'
              disabled={frozen || listLoading || !list.next_after_id}
              onClick={() => {
                const after = list.next_after_id;
                setPrevious([...previous, cursor]);
                setCursor(after);
                void loadList(filter, after);
              }}
            >
              <ChevronRight size={18} />
            </button>
          </nav>
        </aside>
        <div className='ztapi-support-detail'>
          {detailLoading && <p role='status'>正在读取工单详情...</p>}
          {!detail && !detailLoading && <p>请选择工单</p>}
          {detail && (
            <>
              <header className='ztapi-support-detail-heading'>
                <h2>
                  #{detail.ticket.id} {detail.ticket.subject}
                </h2>
                <button
                  type='button'
                  title='刷新工单详情'
                  aria-label='刷新工单详情'
                  disabled={busy || detailLoading}
                  onClick={() => void loadDetail(detail.ticket.id)}
                >
                  <RefreshCw size={17} />
                </button>
              </header>
              <dl className='ztapi-support-evidence'>
                {[
                  ['状态', statuses[detail.ticket.status] || '状态未知'],
                  ['版本', String(detail.ticket.version)],
                  ['会话 ID', detail.conversation.id],
                  ['请求 ID', detail.ticket.request_id || '-'],
                  [
                    '用户 ID',
                    String(detail.ticket.user_id ?? detail.user_id ?? '-'),
                  ],
                  ['创建时间', date(detail.ticket.created_at)],
                  ['更新时间', date(detail.ticket.updated_at)],
                  ['联系方式', detail.ticket.contact || '-'],
                ].map(([label, value]) => (
                  <div key={label}>
                    <dt>{label}</dt>
                    <dd>{value}</dd>
                  </div>
                ))}
              </dl>
              <p className='ztapi-support-text'>{detail.ticket.content}</p>
              {detail.ticket.request_id && !d && (
                <p role='status'>诊断证据暂不可用，不能据此认定无扣款。</p>
              )}
              {d && (
                <section aria-label='请求诊断证据'>
                  <h3>诊断证据</h3>
                  <p className='ztapi-support-text'>{d.summary}</p>
                  <dl className='ztapi-support-evidence'>
                    {[
                      ['请求 ID', d.request_id],
                      ['模型', d.model],
                      ['时间', date(d.created_at)],
                      ['结果', d.outcome],
                      ['诊断代码', d.code],
                      ['HTTP 状态', String(d.http_status)],
                      ['流式请求', d.stream ? '是' : '否'],
                      ['已派发上游', d.dispatched ? '是' : '否'],
                      [
                        '用量凭证',
                        d.usage_received ? '已收到' : '未收到，费用尚未确认',
                      ],
                      ['已扣金额', `${d.charged_u} U`],
                      ['预留金额', `${d.reserved_u} U`],
                      ['需要人工', d.needs_human ? '是' : '否'],
                    ].map(([label, value]) => (
                      <div key={label}>
                        <dt>{label}</dt>
                        <dd>{value || '-'}</dd>
                      </div>
                    ))}
                  </dl>
                </section>
              )}
              <section aria-label='完整会话'>
                <h3>{detail.conversation.title}</h3>
                <div
                  className='ztapi-support-messages'
                  role='log'
                  aria-label='聊天记录'
                >
                  {detail.messages.map((item) => (
                    <article key={item.id}>
                      <header>
                        <strong>{roles[item.role] || '系统通知'}</strong>
                        <time dateTime={item.created_at}>
                          {date(item.created_at)}
                        </time>
                      </header>
                      <p className='ztapi-support-text'>{item.content}</p>
                      {item.attachment_id &&
                      Number.isSafeInteger(item.attachment_id) &&
                      item.attachment_id > 0 ? (
                        <SupportAttachment
                          key={item.attachment_id}
                          attachmentId={item.attachment_id}
                        />
                      ) : (
                        item.has_image && <small>截图暂不可用</small>
                      )}
                    </article>
                  ))}
                </div>
              </section>
              <form
                className='ztapi-support-reply'
                onSubmit={(event) => void mutate('reply', event)}
              >
                <label>
                  人工回复内容
                  <textarea
                    rows={4}
                    maxLength={8000}
                    value={reply}
                    disabled={frozen || detailLoading || statusStale}
                    onChange={(event) => setReply(event.target.value)}
                  />
                </label>
                <button
                  type='submit'
                  disabled={
                    busy ||
                    detailLoading ||
                    statusStale ||
                    (failed && failed !== 'reply') ||
                    !reply.trim()
                  }
                >
                  <Send size={16} />
                  {failed === 'reply' ? '重试原回复' : '发送人工回复'}
                </button>
              </form>
              <form
                className='ztapi-support-status'
                onSubmit={(event) => void mutate('status', event)}
              >
                <label>
                  目标工单状态
                  <select
                    value={target}
                    disabled={frozen || detailLoading || statusStale}
                    onChange={(event) => setTarget(event.target.value)}
                  >
                    {Object.entries(statuses).map(([value, label]) => (
                      <option key={value} value={value}>
                        {label}
                      </option>
                    ))}
                  </select>
                </label>
                <button
                  type='submit'
                  disabled={
                    busy ||
                    detailLoading ||
                    statusStale ||
                    (failed && failed !== 'status') ||
                    (target === detail.ticket.status && failed !== 'status')
                  }
                >
                  <Check size={16} />
                  {failed === 'status' ? '重试原状态' : '应用状态'}
                </button>
              </form>
            </>
          )}
        </div>
      </div>
    </section>
  );
}

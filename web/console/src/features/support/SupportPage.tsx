import { ImagePlus, Plus, RefreshCw, Send, TicketPlus, Users, X } from 'lucide-react';
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
} from 'react';
import { useSearchParams } from 'react-router-dom';
import { useAuth } from '../../auth/session';
import { useLocale } from '../../i18n/locale';
import {
  supportApi,
  supportErrorMessage,
  SupportError,
  type Conversation,
  type ConversationDetail,
  type Diagnosis,
  type SendMessage,
  type SupportConfig,
  type SupportImage,
  type TicketInput,
} from '../../api/support';
import { readSupportImage } from './image';
import { SupportAttachment } from './SupportAttachment';
import { TelegramBinding } from './TelegramBinding';
import './support.css';

type Pending = {
  conversationId: string;
  key: string;
  after: number;
  blockedByOther?: boolean;
  human?: boolean;
};
type Attempt = {
  createKey: string;
  conversationId?: string;
  human: boolean;
  input: SendMessage;
};
const statusLabels: Record<string, string> = {
  open: '待处理',
  in_progress: '处理中',
  resolved: '已解决',
};
const roleLabels = {
  user: '你',
  assistant: 'AI 回复',
  human: '人工回复',
  system: '系统通知',
};
function date(value: string) {
  return new Date(value).toLocaleString('zh-CN', { hour12: false });
}
function restoredPending(key: string): Pending | null {
  try {
    const value = JSON.parse(sessionStorage.getItem(key) || 'null');
    return value &&
      typeof value.conversationId === 'string' &&
      typeof value.key === 'string' &&
      Number.isInteger(value.after)
      ? value
      : null;
  } catch {
    return null;
  }
}

function DiagnosisEvidence({ diagnosis: d }: { diagnosis: Diagnosis }) {
  const { t } = useLocale();
  const yes = (value: boolean) => t(value ? '是' : '否');
  const amount = (value: string) =>
    value.trim() === '' ? t('未确认') : `${value.trim()} U`;
  return (
    <section className="support-evidence" aria-label={t('请求诊断证据')}>
      <h3>{t('诊断证据')}</h3>
      <p>{d.summary}</p>
      <dl>
        {[
          ['请求 ID', d.request_id],
          ['模型', d.model],
          ['时间', date(d.created_at)],
          ['结果', d.outcome],
          ['诊断代码', d.code],
          ['HTTP 状态', String(d.http_status)],
          ['流式请求', yes(d.stream)],
          ['已派发上游', yes(d.dispatched)],
          ['用量凭证', t(d.usage_received ? '已收到' : '未收到，费用尚未确认')],
          ['已扣金额', amount(d.charged_u)],
          ['预留金额', amount(d.reserved_u)],
          ['需要人工', yes(d.needs_human)],
        ].map(([label, value]) => (
          <div key={label}>
            <dt>{t(label)}</dt>
            <dd>{value || '-'}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

export function SupportPage() {
  const { user } = useAuth();
  return <SupportWorkspace key={user?.id} userId={user?.id ?? 0} />;
}

function SupportWorkspace({ userId }: { userId: number }) {
  const { t } = useLocale();
  const [params, setParams] = useSearchParams();
  const initialConversation = useRef(params.get('conversation')).current;
  const storageKey = `ztapi.support.pending.${userId}`;
  const [pending, setPending] = useState<Pending | null>(() =>
    restoredPending(storageKey),
  );
  const [config, setConfig] = useState<SupportConfig | null>(null);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [requests, setRequests] = useState<Diagnosis[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<ConversationDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [content, setContent] = useState('');
  const [image, setImage] = useState<SupportImage | undefined>();
  const [imageBusy, setImageBusy] = useState(false);
  const [requestId, setRequestId] = useState('');
  const [diagnosis, setDiagnosis] = useState<Diagnosis | null>(null);
  const [diagnosing, setDiagnosing] = useState(false);
  const [ticketOpen, setTicketOpen] = useState(false);
  const [subject, setSubject] = useState('');
  const [ticketContent, setTicketContent] = useState('');
  const [contact, setContact] = useState('');
  const [failedMessage, setFailedMessage] = useState(false);
  const [failedTicket, setFailedTicket] = useState(false);
  const [imageRejected, setImageRejected] = useState(false);
  const [pollStopped, setPollStopped] = useState(false);
  const attempt = useRef<Attempt | null>(null);
  const ticketAttempt = useRef<{
    createKey: string;
    input: Omit<TicketInput, 'conversation_id'>;
    conversationId?: string;
  } | null>(null);
  const actionLock = useRef(false);
  const generation = useRef(0);
  const diagnosisGeneration = useRef(0);
  const alive = useRef(true);
  const pendingRef = useRef(pending);
  const activeId = useRef<string | null>(null);
  const imageGeneration = useRef(0);
  const messageLog = useRef<HTMLDivElement>(null);
  const followLatest = useRef(true);
  const scrolledConversation = useRef<string | null>(null);
  const scrollToLatest = useCallback(() => {
    followLatest.current = true;
    const log = messageLog.current;
    if (log) log.scrollTop = Math.max(0, log.scrollHeight - log.clientHeight);
  }, []);
  const visibleConversation = detail?.conversation.id;
  const latestMessageId = detail?.messages.at(-1)?.id;
  useLayoutEffect(() => {
    if (!selected || visibleConversation !== selected) return;
    if (scrolledConversation.current !== selected || followLatest.current)
      scrollToLatest();
    scrolledConversation.current = selected;
  }, [selected, visibleConversation, latestMessageId, scrollToLatest]);
  const invalidateReads = useCallback(() => {
    generation.current++;
    diagnosisGeneration.current++;
    imageGeneration.current++;
  }, []);

  const savePending = useCallback(
    (value: Pending | null) => {
      pendingRef.current = value;
      setPending(value);
      try {
        if (value) sessionStorage.setItem(storageKey, JSON.stringify(value));
        else sessionStorage.removeItem(storageKey);
      } catch {
        /* Server history remains authoritative without browser storage. */
      }
    },
    [storageKey],
  );
  const releaseAIWait = useCallback(() => {
    if (pendingRef.current?.human) return;
    savePending(null);
    attempt.current = null;
    setFailedMessage(false);
  }, [savePending]);

  const loadConversation = useCallback(
    async (id: string) => {
      const current = ++generation.current;
      const result = await supportApi.conversation(id);
      if (
        !alive.current ||
        current !== generation.current ||
        activeId.current !== id
      )
        return;
      setDetail(result);
      setConversations((items) =>
        [result.conversation, ...items.filter((item) => item.id !== id)].slice(
          0,
          30,
        ),
      );
      const waiting = pendingRef.current;
      if (
        waiting?.conversationId === id &&
        !waiting.human &&
        (result.ticket?.status === 'open' ||
          result.ticket?.status === 'in_progress')
      ) {
        releaseAIWait();
        return;
      }
      if (
        waiting?.conversationId === id &&
        result.messages.some(
          (item) =>
            item.id > waiting.after &&
            (item.role === 'system' ||
              (!waiting.human && item.role === 'assistant')) &&
            (waiting.blockedByOther ||
              result.messages.some(
                (userMessage) =>
                  userMessage.role === 'user' &&
                  userMessage.id > waiting.after &&
                  userMessage.id < item.id,
              )),
        )
      ) {
        savePending(null);
        if (waiting.blockedByOther) {
          setFailedMessage(attempt.current !== null);
          setError(
            '会话中的其他请求已完成，原消息未自动重发；请核对记录后显式重试原消息。',
          );
          return;
        }
        attempt.current = null;
        setFailedMessage(false);
        setContent('');
        setImage(undefined);
        setError('');
      }
    },
    [savePending, releaseAIWait],
  );

  useEffect(() => {
    alive.current = true;
    let active = true;
    void (async () => {
      try {
        const [settings, history] = await Promise.all([
          supportApi.config(),
          supportApi.conversations(),
        ]);
        if (!active) return;
        setConfig(settings);
        setConversations(history.items ?? []);
        const id =
          pendingRef.current?.conversationId ||
          initialConversation ||
          history.items?.[0]?.id;
        if (id) {
          activeId.current = id;
          setSelected(id);
          await loadConversation(id);
        }
      } catch (cause) {
        if (active) setError(supportErrorMessage(cause));
      } finally {
        if (active) setLoading(false);
      }
    })();
    void supportApi
      .requests()
      .then((value) => {
        if (active) setRequests(value.items ?? []);
      })
      .catch(() => {
        if (active) setError('本人请求列表加载失败，请刷新重试。');
      });
    return () => {
      alive.current = false;
      active = false;
      invalidateReads();
    };
  }, [loadConversation, initialConversation, invalidateReads]);

  const ticketId = detail?.ticket?.id;
  const humanOnly =
    detail?.ticket?.status === 'open' ||
    detail?.ticket?.status === 'in_progress';
  useEffect(() => {
    if (!selected || (!pending && !ticketId) || pollStopped) return;
    let reads = 0;
    let inFlight = false;
    const timer = window.setInterval(() => {
      if (inFlight || actionLock.current) return;
      if (++reads > 24) {
        window.clearInterval(timer);
        setPollStopped(true);
        return;
      }
      inFlight = true;
      void loadConversation(selected)
        .catch(() => {
          if (alive.current) setError('会话刷新失败，请手动刷新确认。');
        })
        .finally(() => {
          inFlight = false;
        });
    }, 5000);
    return () => window.clearInterval(timer);
  }, [selected, pending, ticketId, pollStopped, loadConversation]);

  const blocked = busy || !!pending || failedMessage || failedTicket;
  const usable =
    (config?.enabled === true || humanOnly) &&
    !loading &&
    (!selected || detail !== null);
  const messageModeChanged =
    failedMessage &&
    attempt.current !== null &&
    attempt.current.human !== humanOnly;

  async function choose(id: string | null) {
    if (blocked) return;
    invalidateReads();
    activeId.current = id;
    setSelected(id);
    setDetail(null);
    setError('');
    setContent('');
    setImage(undefined);
    setImageBusy(false);
    setRequestId('');
    setDiagnosis(null);
    setDiagnosing(false);
    setTicketOpen(false);
    setPollStopped(false);
    setSubject('');
    setTicketContent('');
    setContact('');
    setParams(id ? { conversation: id } : {}, { replace: true });
    if (!id) return;
    setLoading(true);
    try {
      await loadConversation(id);
    } catch (cause) {
      if (alive.current) setError(supportErrorMessage(cause));
    } finally {
      if (alive.current) setLoading(false);
    }
  }

  async function ensureConversation(operation: {
    createKey: string;
    conversationId?: string;
  }) {
    if (operation.conversationId) return operation.conversationId;
    const created = await supportApi.createConversation(operation.createKey);
    operation.conversationId = created.id;
    if (!alive.current) return created.id;
    activeId.current = created.id;
    setSelected(created.id);
    setParams({ conversation: created.id }, { replace: true });
    setDetail({ conversation: created, messages: [] });
    setConversations((items) =>
      [created, ...items.filter((item) => item.id !== created.id)].slice(0, 30),
    );
    return created.id;
  }

  async function send(event?: FormEvent) {
    event?.preventDefault();
    if (
      actionLock.current ||
      (pending && !failedMessage) ||
      !usable ||
      imageBusy ||
      failedTicket ||
      messageModeChanged
    )
      return;
    if (!attempt.current) {
      if (
        (!content.trim() && !image) ||
        [...content].length > config!.max_message_chars
      ) {
        setError(t('请输入有效消息或选择截图，且不要超过字数限制。'));
        return;
      }
      attempt.current = {
        createKey: crypto.randomUUID(),
        conversationId: selected ?? undefined,
        human: humanOnly,
        input: {
          content: content.trim(),
          ...(requestId ? { request_id: requestId } : {}),
          ...(config!.vision_enabled && image ? { image } : {}),
          idempotency_key: crypto.randomUUID(),
        },
      };
    }
    const operation = attempt.current;
    scrollToLatest();
    actionLock.current = true;
    generation.current++;
    setBusy(true);
    setError('');
    setFailedMessage(false);
    setImageRejected(false);
    try {
      const id = await ensureConversation(operation);
      if (!alive.current) return;
      const marker: Pending = {
        conversationId: id,
        key: operation.input.idempotency_key,
        after:
          pendingRef.current?.key === operation.input.idempotency_key &&
          pendingRef.current.conversationId === id
            ? pendingRef.current.after
            : Math.max(0, ...(detail?.messages.map((item) => item.id) ?? [])),
        human: humanOnly,
      };
      savePending(marker);
      const result = await supportApi.sendMessage(id, operation.input);
      if (!alive.current) return;
      if ('pending' in result) {
        savePending({ ...marker, blockedByOther: !result.call_id });
        setPollStopped(false);
        return;
      }
      savePending(null);
      attempt.current = null;
      setContent('');
      setImage(undefined);
      setDetail((previous) =>
        previous
          ? {
              ...previous,
              messages: [
                ...previous.messages.filter(
                  (item) =>
                    item.id !== result.message.id &&
                    item.id !== result.reply.id,
                ),
                result.message,
                result.reply,
              ],
            }
          : previous,
      );
      await loadConversation(id);
    } catch (cause) {
      if (alive.current) {
        setFailedMessage(attempt.current !== null);
        if (
          operation.input.image &&
          cause instanceof SupportError &&
          [413, 507].includes(cause.status)
        ) {
          setImageRejected(true);
          setError(
            cause.status === 507 || cause.code === 'image_storage_full'
              ? '私有截图存储空间不足（每用户 50MB、全站 512MB）；可移除截图后发送文本，或申请人工工单。'
              : '截图被拒绝，可移除截图后发送文本，或申请人工工单。',
          );
          return;
        }
        setError(supportErrorMessage(cause));
      }
    } finally {
      actionLock.current = false;
      if (alive.current) setBusy(false);
    }
  }

  async function submitTicket(event: FormEvent) {
    event.preventDefault();
    if (actionLock.current || !usable || (failedMessage && !selected)) return;
    if (!ticketAttempt.current) {
      if (!subject.trim() || !ticketContent.trim()) return;
      ticketAttempt.current = {
        createKey: crypto.randomUUID(),
        conversationId: selected ?? undefined,
        input: {
          subject: subject.trim(),
          content: ticketContent.trim(),
          ...(contact.trim() ? { contact: contact.trim() } : {}),
          ...(requestId ? { request_id: requestId } : {}),
          idempotency_key: crypto.randomUUID(),
        },
      };
    }
    actionLock.current = true;
    generation.current++;
    setBusy(true);
    setError('');
    try {
      const operation = ticketAttempt.current;
      const id = await ensureConversation(operation);
      if (!alive.current) return;
      const ticket = await supportApi.createTicket({
        ...operation.input,
        conversation_id: id,
      });
      if (!alive.current) return;
      setDetail((value) => (value ? { ...value, ticket } : value));
      if (ticket.status === 'open' || ticket.status === 'in_progress')
        releaseAIWait();
      ticketAttempt.current = null;
      setFailedTicket(false);
      setTicketOpen(false);
      setPollStopped(false);
    } catch (cause) {
      if (alive.current) {
        setFailedTicket(true);
        setError(supportErrorMessage(cause));
      }
    } finally {
      actionLock.current = false;
      if (alive.current) setBusy(false);
    }
  }

  async function inspect(id: string) {
    const current = ++diagnosisGeneration.current;
    setRequestId(id);
    setDiagnosis(null);
    setDiagnosing(true);
    try {
      const value = await supportApi.diagnosis(id);
      if (alive.current && current === diagnosisGeneration.current)
        setDiagnosis(value);
    } catch (cause) {
      if (alive.current && current === diagnosisGeneration.current)
        setError(supportErrorMessage(cause));
    } finally {
      if (alive.current && current === diagnosisGeneration.current)
        setDiagnosing(false);
    }
  }

  async function upload(files: FileList | null) {
    if (!files?.length || !config?.vision_enabled || blocked) return;
    const current = ++imageGeneration.current;
    setImage(undefined);
    setImageBusy(true);
    setError('');
    try {
      const value = await readSupportImage(files, config.max_image_bytes);
      if (alive.current && current === imageGeneration.current) setImage(value);
    } catch (cause) {
      if (alive.current && current === imageGeneration.current)
        setError(cause instanceof Error ? cause.message : t('截图读取失败'));
    } finally {
      if (alive.current && current === imageGeneration.current)
        setImageBusy(false);
    }
  }

  return (
    <div className="console-page support-page">
      <header className="console-page__header">
        <div>
          <p className="console-eyebrow">ZTAPI · 小 Z</p>
          <h1>{t('平台客服')}</h1>
        </div>
        <div className="console-actions">
          <span>{config?.model_label}</span>
          <a className="console-button console-button--secondary" href="https://t.me/ztapivip" target="_blank" rel="noopener noreferrer">
            <Users size={16} aria-hidden="true" />
            {t('用户交流群')}
          </a>
        </div>
      </header>
      <p className="support-privacy">{t('配置问题无需绑定 Telegram。不要发送密码、API Key 或支付凭证中的敏感信息；客服不会代建密钥或操作资金。')}</p>
      {config?.telegram_binding_enabled === true && <TelegramBinding />}
      {error && (
        <p className="console-alert" role="alert">
          {t(error)}
        </p>
      )}
      {loading && <p role="status">{t('正在加载客服记录...')}</p>}
      {config?.enabled === false && !humanOnly && (
        <p role="status">{t('平台客服暂未开放')}</p>
      )}
      <div className="support-layout">
        <aside className="support-history">
          <div className="support-section-heading">
            <h2>{t('会话记录')}</h2>
            <button
              type="button"
              title={t('新建会话')}
              aria-label={t('新建会话')}
              disabled={blocked || loading}
              onClick={() => void choose(null)}
            >
              <Plus size={17} />
            </button>
          </div>
          <nav aria-label={t('客服会话')}>
            {conversations.map((item) => (
              <button
                type="button"
                aria-current={selected === item.id ? 'page' : undefined}
                key={item.id}
                disabled={blocked || loading}
                onClick={() => void choose(item.id)}
              >
                {item.title || t('新对话')}
              </button>
            ))}
          </nav>
          {!conversations.length && !loading && <p>{t('暂无会话')}</p>}
        </aside>
        <section className="support-chat" aria-label={t('客服聊天')}>
          <div className="support-section-heading">
            <h2>{detail?.conversation.title || t('新对话')}</h2>
            <button
              type="button"
              title={t('刷新会话')}
              aria-label={t('刷新会话')}
              disabled={!selected || busy || loading}
              onClick={() => {
                if (selected)
                  void loadConversation(selected).catch((cause) =>
                    setError(supportErrorMessage(cause)),
                  );
              }}
            >
              <RefreshCw size={17} />
            </button>
          </div>
          {detail?.ticket && (
            <p className="support-ticket-status" role="status">
              {t(`工单 #${detail.ticket.id}`)} ·{' '}
              {t(statusLabels[detail.ticket.status] || '状态未知')} ·{' '}
              {t('工单已记录，人工回复将在此会话显示。')}
            </p>
          )}
          {humanOnly && (
            <p className="support-ticket-status" role="status">
              {t('人工工单处理中，后续消息交由人工，不调用 AI。')}
            </p>
          )}
          <div
            className="support-messages"
            ref={messageLog}
            onScroll={(event) => {
              const log = event.currentTarget;
              followLatest.current =
                log.scrollHeight - log.clientHeight - log.scrollTop <= 80;
            }}
            role="log"
            aria-label={t('聊天记录')}
            aria-live="polite"
          >
            {detail?.messages.map((item) => (
              <article
                className={`support-message support-message--${item.role}`}
                key={item.id}
              >
                <header>
                  <strong>{t(roleLabels[item.role] || '系统通知')}</strong>
                  <time dateTime={item.created_at}>
                    {date(item.created_at)}
                  </time>
                </header>
                <p>{item.content}</p>
                {item.attachment_id &&
                Number.isSafeInteger(item.attachment_id) &&
                item.attachment_id > 0 ? (
                  <SupportAttachment
                    key={item.attachment_id}
                    attachmentId={item.attachment_id}
                  />
                ) : (
                  item.has_image && <span>{t('截图暂不可用')}</span>
                )}
              </article>
            ))}
            {!detail?.messages.length && !loading && (
              <p className="support-empty">{t('暂无消息')}</p>
            )}
          </div>
          {pending && !failedMessage && (
            <p role="status">
              {t('回复尚未确认，正在读取会话结果。请勿重复发送。')}
            </p>
          )}
          {pollStopped && (
            <p role="status">{t('自动刷新已暂停，请手动刷新会话。')}</p>
          )}
          <form
            className="support-composer"
            onSubmit={(event) => void send(event)}
          >
            <label htmlFor="support-message">{t('消息内容')}</label>
            <textarea
              id="support-message"
              rows={4}
              value={content}
              maxLength={config?.max_message_chars}
              disabled={!usable || blocked}
              onChange={(event) => setContent(event.target.value)}
            />
            {config?.vision_enabled && (
              <p className="support-upload-warning">
                {t(
                  '上传前请遮住 API Key、密码等敏感信息。截图将私有保存，存储满时仍可发送文本或申请人工。',
                )}
              </p>
            )}
            <div className="support-composer-actions">
              <span>
                {[...content].length} / {config?.max_message_chars ?? '-'}
              </span>
              {config?.vision_enabled && (
                <label className="support-upload" title={t('上传截图')}>
                  <ImagePlus size={17} />
                  <span>{t('截图')}</span>
                  <input
                    aria-label={t('上传截图')}
                    type="file"
                    accept="image/png,image/jpeg,image/webp"
                    disabled={!usable || blocked || imageBusy}
                    onChange={(event) => {
                      void upload(event.target.files);
                      event.target.value = '';
                    }}
                  />
                </label>
              )}
              {image && (
                <button
                  type="button"
                  title={t('移除截图')}
                  aria-label={t('移除截图')}
                  disabled={blocked}
                  onClick={() => setImage(undefined)}
                >
                  <X size={16} />
                  {t('已选截图')}
                </button>
              )}
              <button
                type="submit"
                className="console-button console-button--primary"
                aria-label={t('发送消息')}
                disabled={
                  !usable || blocked || imageBusy || (!content.trim() && !image)
                }
              >
                <Send size={16} />
                {t('发送')}
              </button>
            </div>
          </form>
          {failedMessage && (
            <button
              className="console-button"
              type="button"
              disabled={busy || messageModeChanged}
              onClick={() => void send()}
            >
              <RefreshCw size={16} />
              {t('重试原消息')}
            </button>
          )}
          {messageModeChanged && (
            <p role="status">
              {t(
                '工单状态已变化，原消息尚未确认；请核对会话或重新申请人工，不能跨人工与 AI 模式重试。',
              )}
            </p>
          )}
          {imageRejected && (
            <button
              className="console-button console-button--secondary"
              type="button"
              disabled={busy}
              onClick={() => {
                attempt.current = null;
                savePending(null);
                setFailedMessage(false);
                setImageRejected(false);
                setImage(undefined);
                setError('');
              }}
            >
              <X size={16} />
              {t('移除截图，改发文本')}
            </button>
          )}
          <button
            className="console-button console-button--secondary support-handoff"
            type="button"
            disabled={
              !usable ||
              busy ||
              failedTicket ||
              (failedMessage && !selected) ||
              humanOnly
            }
            onClick={() => setTicketOpen(!ticketOpen)}
          >
            <TicketPlus size={16} />
            {t('申请人工工单')}
          </button>
          {ticketOpen && (
            <form
              className="support-ticket-form"
              onSubmit={(event) => void submitTicket(event)}
            >
              <label>
                {t('工单主题')}
                <input
                  value={subject}
                  maxLength={160}
                  required
                  disabled={busy || failedTicket}
                  onChange={(event) => setSubject(event.target.value)}
                />
              </label>
              <label>
                {t('问题详情')}
                <textarea
                  value={ticketContent}
                  rows={4}
                  maxLength={config?.max_message_chars}
                  required
                  disabled={busy || failedTicket}
                  onChange={(event) => setTicketContent(event.target.value)}
                />
              </label>
              <label>
                {t('联系方式（选填）')}
                <input
                  value={contact}
                  maxLength={240}
                  disabled={busy || failedTicket}
                  onChange={(event) => setContact(event.target.value)}
                />
              </label>
              <p>{t('工单会持久保存，不代表人工即时在线。')}</p>
              <button
                className="console-button"
                type="submit"
                disabled={busy || !subject.trim() || !ticketContent.trim()}
              >
                <TicketPlus size={16} />
                {t(failedTicket ? '重试原工单' : '提交工单')}
              </button>
            </form>
          )}
        </section>
        <aside className="support-diagnostics">
          <h2>{t('本人请求')}</h2>
          <p>{t('最近 30 天，最多 20 条')}</p>
          <div className="support-request-list">
            {requests.map((item) => (
              <button
                type="button"
                key={item.request_id}
                aria-pressed={requestId === item.request_id}
                disabled={blocked}
                onClick={() => void inspect(item.request_id)}
              >
                <strong>{item.request_id}</strong>
                <span>
                  {item.model} · HTTP {item.http_status}
                </span>
              </button>
            ))}
          </div>
          {!requests.length && <p>{t('暂无请求')}</p>}
          {requestId && (
            <button
              type="button"
              disabled={blocked}
              onClick={() => {
                diagnosisGeneration.current++;
                setRequestId('');
                setDiagnosis(null);
                setDiagnosing(false);
              }}
            >
              <X size={15} />
              {t('取消关联请求')}
            </button>
          )}
          {diagnosing && <p role="status">{t('正在读取诊断证据...')}</p>}
          {diagnosis && <DiagnosisEvidence diagnosis={diagnosis} />}
        </aside>
      </div>
    </div>
  );
}

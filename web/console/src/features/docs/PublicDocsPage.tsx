import {
  BookOpen,
  Cable,
  CircleHelp,
  Code2,
  KeyRound,
  Send,
  UserRound,
} from 'lucide-react';
import { Link, useParams } from 'react-router-dom';
import type { ReactNode } from 'react';
import { PublicHeader } from '../../components/layout/PublicHeader';
import { useLocale } from '../../i18n/locale';
import './docs.css';

type DocSection = 'api' | 'integration' | 'user-guide' | 'faq';

const BASE_URL = 'https://ztapi.vip/v1';

const sections: ReadonlyArray<{
  id: DocSection;
  label: string;
  path: string;
  icon: typeof BookOpen;
}> = [
  { id: 'api', label: 'API 手册', path: '/docs/api', icon: Code2 },
  { id: 'integration', label: '集成指南', path: '/docs/integration', icon: Cable },
  { id: 'user-guide', label: '用户指南', path: '/docs/user-guide', icon: UserRound },
  { id: 'faq', label: '常见问题', path: '/docs/faq', icon: CircleHelp },
];

const endpointRows = [
  ['GET', '/v1/models', '获取当前可用模型'],
  ['POST', '/v1/chat/completions', 'OpenAI 兼容对话'],
  ['POST', '/v1/responses', 'Responses 模型调用'],
  ['POST', '/v1/embeddings', '生成文本向量'],
  ['POST', '/v1/images/generations', '生成图片'],
  ['POST', '/v1/video/generations', '创建视频任务'],
  ['GET', '/v1/video/generations/{task_id}', '查询视频任务'],
] as const;

const curlExample = `curl ${BASE_URL}/chat/completions \\
  -H "Authorization: Bearer \${ZTAPI_API_KEY}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "YOUR_MODEL_ID",
    "messages": [{"role": "user", "content": "你好"}]
  }'`;

const pythonExample = `from openai import OpenAI

client = OpenAI(
    api_key="YOUR_ZTAPI_API_KEY",
    base_url="${BASE_URL}",
)

response = client.chat.completions.create(
    model="YOUR_MODEL_ID",
    messages=[{"role": "user", "content": "你好"}],
)
print(response.choices[0].message.content)`;

function ApiReference() {
  const { t } = useLocale();
  return (
    <>
      <DocsHeading
        eyebrow={t('开发者文档')}
        title={t('API 手册')}
        summary={t('查看统一鉴权方式、公开端点和请求规范。')}
      />
      <DocsSection title={t('基础信息')}>
        <dl className="docs-facts">
          <div><dt>Base URL</dt><dd><code>{BASE_URL}</code></dd></div>
          <div><dt>{t('鉴权方式')}</dt><dd><code>Authorization: Bearer YOUR_ZTAPI_API_KEY</code></dd></div>
          <div><dt>Content-Type</dt><dd><code>application/json</code></dd></div>
        </dl>
      </DocsSection>
      <DocsSection title={t('公开端点')}>
        <div className="docs-table-wrap">
          <table className="docs-table">
            <thead><tr><th>{t('方法')}</th><th>{t('路径')}</th><th>{t('用途')}</th></tr></thead>
            <tbody>
              {endpointRows.map(([method, path, purpose]) => (
                <tr key={`${method}-${path}`}>
                  <td><span className={`docs-method docs-method--${method.toLowerCase()}`}>{method}</span></td>
                  <td><code>{path}</code></td>
                  <td>{t(purpose)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </DocsSection>
      <DocsSection title={t('最小请求示例')}>
        <CodeSample label="cURL" code={curlExample} />
      </DocsSection>
    </>
  );
}

function IntegrationGuide() {
  const { t } = useLocale();
  return (
    <>
      <DocsHeading
        eyebrow={t('快速接入')}
        title={t('集成指南')}
        summary={t('保留现有 OpenAI SDK，只替换 Base URL、API Key 和模型 ID。')}
      />
      <DocsSection title={t('三步完成接入')}>
        <ol className="docs-steps">
          <li><span>01</span><div><strong>{t('创建 API Key')}</strong><p>{t('登录控制台创建密钥，密钥只完整展示一次。')}</p></div></li>
          <li><span>02</span><div><strong>{t('选择模型 ID')}</strong><p>{t('在模型支持页复制准确的 ZTAPI 模型名称。')}</p></div></li>
          <li><span>03</span><div><strong>{t('替换连接信息')}</strong><p>{t('将 SDK 的 Base URL 改为 ZTAPI，再发送第一条请求。')}</p></div></li>
        </ol>
      </DocsSection>
      <DocsSection title={t('Python 示例')}>
        <CodeSample label="Python" code={pythonExample} />
      </DocsSection>
      <DocsSection title={t('选择正确的接口')}>
        <div className="docs-callout">
          <strong>{t('按模型支持页标注的调用地址接入')}</strong>
          <p>{t('Responses-only 模型使用 /v1/responses；图片和视频模型使用各自的专用端点。')}</p>
        </div>
      </DocsSection>
    </>
  );
}

function UserGuide() {
  const { t } = useLocale();
  const items = [
    ['1', '注册与登录', '注册只需账号和密码，邮箱可稍后补充并用于找回密码。'],
    ['2', '充值余额', '创建 USDT 订单后按页面金额和地址付款，到账后自动入账并额外赠送 5% 使用额度。'],
    ['3', '创建 API Key', '进入 API 密钥页面创建密钥并立即保存，后续无法再次查看完整密钥。'],
    ['4', '选择模型', '在模型支持页查看当前可用模型、调用端点与实时售价。'],
    ['5', '查看消费', '使用日志会记录模型、输入输出用量、费用、请求状态与时间。'],
  ] as const;
  return (
    <>
      <DocsHeading
        eyebrow={t('账户使用')}
        title={t('用户指南')}
        summary={t('从注册、充值到查看调用费用，按顺序完成首次使用。')}
      />
      <DocsSection title={t('首次使用流程')}>
        <ol className="docs-user-flow">
          {items.map(([number, title, description]) => (
            <li key={number}><span>{number}</span><div><strong>{t(title)}</strong><p>{t(description)}</p></div></li>
          ))}
        </ol>
      </DocsSection>
      <DocsSection title={t('安全提醒')}>
        <div className="docs-callout docs-callout--warning">
          <KeyRound aria-hidden="true" size={20} />
          <div>
            <strong>{t('不要公开 API Key')}</strong>
            <p>{t('请把密钥放在服务端环境变量中，不要写入网页、安装包、聊天记录或公开仓库。')}</p>
          </div>
        </div>
      </DocsSection>
    </>
  );
}

function FaqPage() {
  const { t } = useLocale();
  const entries = [
    ['请求返回 401 怎么办？', '检查 Authorization 是否为 Bearer 加空格再加 API Key，并确认密钥未被删除或禁用。'],
    ['提示模型不存在或不可用怎么办？', '不要手写模型名称，请从模型支持页复制模型 ID，并确认使用了该模型标注的接口。'],
    ['返回 429 是什么意思？', '请求过于频繁或上游线路限流。降低并发后重试；持续出现时联系 Telegram 客服并提供请求编号。'],
    ['返回 5xx 或空内容怎么办？', '先用相同参数重试一次。若仍失败，请提供模型名、发生时间和请求编号，切勿发送 API Key。'],
    ['充值后为什么没有立刻到账？', '订单有效期内按唯一金额付款。链上确认后系统自动入账；超时仍未到账请提供订单号和交易哈希。'],
    ['费用是怎样计算的？', '以模型支持页的实时售价和上游返回的实际用量为准，使用日志中可查看每次调用费用。'],
    ['API Key 忘记了怎么办？', '完整密钥只展示一次。无法找回时请删除旧密钥并创建新密钥。'],
  ] as const;
  return (
    <>
      <DocsHeading
        eyebrow={t('问题排查')}
        title={t('常见问题')}
        summary={t('先按错误信息自查；仍无法解决时带上请求编号联系客服。')}
      />
      <DocsSection title={t('常见问题与处理方式')}>
        <div className="docs-faq">
          {entries.map(([question, answer]) => (
            <details key={question}>
              <summary>{t(question)}</summary>
              <p>{t(answer)}</p>
            </details>
          ))}
        </div>
      </DocsSection>
    </>
  );
}

function DocsHeading({ eyebrow, title, summary }: { eyebrow: string; title: string; summary: string }) {
  return (
    <header className="docs-heading">
      <p>{eyebrow}</p>
      <h1>{title}</h1>
      <span>{summary}</span>
      <code>{BASE_URL}</code>
    </header>
  );
}

function DocsSection({ title, children }: { title: string; children: ReactNode }) {
  return <section className="docs-section"><h2>{title}</h2>{children}</section>;
}

function CodeSample({ label, code }: { label: string; code: string }) {
  return (
    <div className="docs-code">
      <div>{label}</div>
      <pre><code>{code}</code></pre>
    </div>
  );
}

export function PublicDocsPage() {
  const { t } = useLocale();
  const params = useParams<{ section?: string }>();
  const active = sections.some((section) => section.id === params.section)
    ? params.section as DocSection
    : 'integration';

  return (
    <div className="public-docs-page">
      <PublicHeader />
      <main className="public-docs public-shell">
        <aside className="docs-sidebar">
          <div className="docs-sidebar__title"><BookOpen aria-hidden="true" size={18} />{t('文档中心')}</div>
          <nav aria-label={t('文档分类')}>
            {sections.map((section) => {
              const Icon = section.icon;
              return (
                <Link key={section.id} to={section.path} aria-current={active === section.id ? 'page' : undefined}>
                  <Icon aria-hidden="true" size={17} />
                  {t(section.label)}
                </Link>
              );
            })}
          </nav>
          <a className="docs-support" href="https://t.me/gan66" target="_blank" rel="noreferrer">
            <Send aria-hidden="true" size={17} />
            <span><strong>{t('需要帮助？')}</strong><small>@gan66</small></span>
          </a>
        </aside>
        <article className="docs-article">
          {active === 'api' && <ApiReference />}
          {active === 'integration' && <IntegrationGuide />}
          {active === 'user-guide' && <UserGuide />}
          {active === 'faq' && <FaqPage />}
        </article>
      </main>
    </div>
  );
}

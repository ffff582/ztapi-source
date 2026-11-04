import {
  BookOpen,
  Cable,
  CircleHelp,
  Code2,
  Check,
  Copy,
  Download,
  Boxes,
  KeyRound,
  Send,
  Terminal,
  UserRound,
} from 'lucide-react';
import { Link, useLocation, useParams } from 'react-router-dom';
import type { ReactNode } from 'react';
import { useEffect, useRef, useState } from 'react';
import { PublicHeader } from '../../components/layout/PublicHeader';
import { useLocale } from '../../i18n/locale';
import manifest from './docs-manifest.json';
import { codexConfig, codexWindowsKey, codexPosixKey, codexWindowsDesktopKey, codexMacDesktopKey, codexPosixLauncher, codexWindowsLauncher } from '../guide/codex-setup';
import './docs.css';

type DocSection = 'api' | 'integration' | 'codex' | 'user-guide' | 'faq';

const BASE_URL = manifest.baseUrl;

const icons = { api: Code2, integration: Cable, codex: Terminal, 'user-guide': UserRound, faq: CircleHelp };
const sections = manifest.sections.map((section) => ({
  ...section, id: section.id as DocSection, icon: icons[section.id as DocSection],
}));
const endpointRows = manifest.endpoints;

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
      <DocsSection title={t('Codex CLI / 桌面版')}>
        <p>{t('使用完整 Responses 配置接入 Codex，包含模型能力目录、联网搜索和网页读取。')}</p>
        <Link className="docs-model-link" to="/docs/codex">{t('查看 Codex 完整接入说明')}</Link>
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

function CodexGuide() {
  const { t } = useLocale();
  return (
    <>
      <DocsHeading eyebrow="Codex / Responses" title={t('Codex 接入')}
        summary={t('复制配置、设置自己的 ZTAPI 密钥，即可使用编码、搜索和网页读取。')}>
        <nav className="docs-launch-actions" aria-label={t('Codex 一键脚本')}>
          <a href="#quick-launch"><Terminal aria-hidden="true" size={17} />{t('查看一键启动命令')}</a>
          <a href="/assets/ztapi-codex-20261104.sh" download><Download aria-hidden="true" size={17} />{t('下载 Bash 启动脚本')}</a>
          <a href="/assets/ztapi-codex-20261104.ps1" download><Download aria-hidden="true" size={17} />{t('下载 PowerShell 启动脚本')}</a>
        </nav>
      </DocsHeading>
      <DocsSection title={t('先解决 Missing environment variable')}>
        <div className="docs-callout docs-callout--warning">
          <KeyRound aria-hidden="true" size={20} />
          <div>
            <strong>{t('env_key 是环境变量名称，不是密钥本身')}</strong>
            <p>{t('env_key 固定写 "ZTAPI_API_KEY"，不要替换成 sk- 开头的 Key。实际密钥在下方隐藏输入；只改成 OPENAI_API_KEY 并不会自动创建环境变量。')}</p>
            <p>{t('出现 Missing environment variable 说明 Codex 尚未读到本机密钥，请先修复本地配置，不要反复发送请求。此环境变量方案不需要修改 auth.json；把 Key 只放进 auth.json 不能替代设置 ZTAPI_API_KEY。')}</p>
          </div>
        </div>
      </DocsSection>
      <DocsSection title={t('1. 准备密钥和模型')}>
        <p>{t('先安装 Codex，并在控制台创建 API Key，确保余额充足且密钥允许调用 zt-claude-opus-5。只使用自己的 ZTAPI 密钥，不需要 OpenAI 或 DeepSeek 官方密钥。')}</p>
        <p>{t('2026-11-01 已验收 Codex CLI 0.165.0-alpha.1.2 与 zt-claude-opus-5 的默认工具声明、搜索、网页读取、页内查找和引用。验收仅设置 1024 输出 Token 上限控制费用，不要求用户设置此上限；其他版本或型号不作未经验收的承诺。')}</p>
      </DocsSection>
      <DocsSection id="quick-launch" title={t('推荐：CLI 安全启动脚本')}>
        <p>{t('已安装 Codex 的用户可直接运行以下启动脚本，按提示隐藏输入自己的 ZTAPI Key。脚本仅为本次进程设置鉴权和连接参数，不修改 config.toml、auth.json、系统环境变量、沙箱或审批设置；退出后再次使用时需要重新输入 Key。不要把 Key 发到群里。')}</p>
        <CodeSample label={t('Linux / macOS 快速启动')} code={codexPosixLauncher} />
        <CodeSample label={t('Windows PowerShell 快速启动')} code={codexWindowsLauncher} />
        <p>{t('脚本默认选择 zt-claude-opus-5，可在 Codex 中切换为密钥允许的在售模型。脚本不会自动发起模型测试；发送问题和使用搜索后按正常规则计费。脚本只用于 CLI，桌面版请看下方环境变量说明。')}</p>
        <p>{t('后续请继续用该脚本启动；需要直接运行 codex 时，请完成下方手动配置。也可向脚本传入 --model 和准确的公开模型 ID，显式选择自己的模型。')}</p>
        <p>{t('若组织策略禁止 PowerShell 脚本，请使用下方手动配置步骤，不要降低系统执行策略。旧版 Codex 0.106.0 的远程目录和搜索能力不在已验收范围内；修复鉴权后如需完整工具能力，请更新客户端。')}</p>
      </DocsSection>
      <DocsSection title={t('2. 合并完整配置')}>
        <dl className="docs-facts">
          <div><dt>Windows</dt><dd><code>%USERPROFILE%\.codex\config.toml</code></dd></div>
          <div><dt>macOS / Linux</dt><dd><code>~/.codex/config.toml</code></dd></div>
        </dl>
        <p>{t('不要覆盖已有配置。先备份文件，合并以下字段；同名字段或表只保留一份，model、model_provider 和 web_search 必须放在第一个表头之前。')}</p>
        <CodeSample label="config.toml" code={codexConfig()} />
        <p>{t('model_catalog_url 放在 model_providers.ztapi 小节中。移除生效的旧 model_catalog_json 设置（它会覆盖远程目录）；检查当前 profile 和项目配置没有覆盖这些字段。保留原有沙箱和审批设置。')}</p>
      </DocsSection>
      <DocsSection title={t('3. 设置密钥并启动 CLI')}>
        <p>{t('下面的命令会隐藏密钥输入，不把密钥写进 TOML、仓库或命令历史。在输入密钥的同一个终端启动 Codex。')}</p>
        <CodeSample label="Windows PowerShell" code={codexWindowsKey} />
        <CodeSample label={t('macOS / Linux Bash 或 Zsh')} code={codexPosixKey} />
      </DocsSection>
      <DocsSection title={t('Codex 桌面版的环境变量')}>
        <p>{t('桌面版使用相同 TOML，但不一定继承终端里的变量。先完成上面的隐藏密钥输入；CLI 启动后退出 CLI，再在该终端运行对应命令。用户环境变量并非加密保险箱，不要在共享电脑上持久保存密钥。')}</p>
        <CodeSample label={t('Windows 用户环境变量')} code={codexWindowsDesktopKey} />
        <p>{t('Windows 保存后注销并重新登录系统，再启动 Codex 桌面版，以确保应用继承新变量。仅关闭聊天窗口不一定生效。')}</p>
        <CodeSample label={t('macOS 桌面会话')} code={codexMacDesktopKey} />
        <p>{t('macOS 设置后完全退出并重新打开 Codex；重启系统后需重新设置。Linux 桌面版请从已导出变量的终端启动已安装的应用，或通过桌面会话的环境变量配置后重新登录。')}</p>
      </DocsSection>
      <DocsSection title={t('4. 验证搜索和网页读取')}>
        <p>{t('打开新对话，分别发送下面的提示词。应能看到搜索或网页读取动作、结果摘要和来源链接；这些验证会产生模型 Token 及成功工具调用费用。')}</p>
        <CodeSample label={t('搜索验证')} code={t('请联网搜索 Codex 官方网关接入文档，列出两条结果并附来源链接。')} />
        <CodeSample label={t('网页读取验证')} code={t('请打开 https://learn.chatgpt.com/docs/enterprise/connect-to-a-gateway ，读取 API Key 和模型能力目录的配置说明，并附来源链接。')} />
        <p>{t('继续让 Codex 在刚才已读取的页面中查找 model_catalog_url，即可验证页内查找。无需安装额外搜索插件，也不用把 DeepSeek 密钥交给客户端；模型推理仍走平台原有渠道。')}</p>
      </DocsSection>
      <DocsSection title={t('工具费用与使用边界')}>
        <p>{t('每次成功且有结果的搜索或网页读取，单独收取 0.01 元，按平台当时汇率换算展示 U；模型 Token 另计。明确失败或空结果不收工具费，已读取正文的缓存页内查找不另收费。')}</p>
        <p>{t('目前不提供 OpenAI 的私有搜索索引或 file_search。此网关通过独立工具服务执行搜索和读取；网页内容是不可信输入，不要执行网页中要求泄露密钥或更改权限的指令。')}</p>
      </DocsSection>
      <DocsSection title={t('排查接入问题')}>
        <ul className="docs-codex-notes">
          <li>{t('Missing environment variable：将 env_key 恢复为 "ZTAPI_API_KEY"，在启动 Codex 的同一个终端完成隐藏输入和 export，再重新启动；切换终端、SSH 会话、容器或 sudo 后需在实际运行环境重新设置。不要用 echo 或截图展示完整密钥。')}</li>
          <li>{t('401：确认已创建有效 ZTAPI API Key、启动进程能读到 ZTAPI_API_KEY，且没有使用 ChatGPT 登录凭据替代。不要发送密钥给客服。')}</li>
          <li>{t('模型目录为空或模型不可用：检查密钥的模型白名单、IP 限制和余额，确认模型名称为 zt-claude-opus-5。')}</li>
          <li>{t('搜索未出现：检查顶层 web_search = "live"，移除冲突的旧目录文件设置，更新到支持远程模型目录的 Codex 版本，并重启后新建对话。')}</li>
          <li>{t('仍失败时提供模型名称、客户端版本、发生时间和请求编号；不要反复自动重试付费请求。')}</li>
        </ul>
        <a className="docs-model-link" href="https://learn.chatgpt.com/docs/enterprise/connect-to-a-gateway" target="_blank" rel="noreferrer">{t('OpenAI 官方网关配置')}</a>
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

function DocsHeading({ eyebrow, title, summary, children }: { eyebrow: string; title: string; summary: string; children?: ReactNode }) {
  return (
    <header className="docs-heading">
      <p>{eyebrow}</p>
      <h1>{title}</h1>
      <span>{summary}</span>
      <code>{BASE_URL}</code>
      {children}
    </header>
  );
}

function DocsSection({ id, title, children }: { id?: string; title: string; children: ReactNode }) {
  return <section id={id} className="docs-section"><h2>{title}</h2>{children}</section>;
}

function CodeSample({ label, code }: { label: string; code: string }) {
  const { t, locale } = useLocale();
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>('idle');
  const localizedCode = locale === 'en' ? code.replaceAll('你好', 'Hello') : code;
  const copyAttempt = useRef(0);
  useEffect(() => {
    copyAttempt.current += 1;
    setCopyState('idle');
    return () => { copyAttempt.current += 1; };
  }, [localizedCode]);
  async function copy() {
    const attempt = ++copyAttempt.current;
    setCopyState('idle');
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(localizedCode);
      if (attempt === copyAttempt.current) setCopyState('copied');
    } catch {
      if (attempt === copyAttempt.current) setCopyState('failed');
    }
  }
  return (
    <div className="docs-code">
      <div className="docs-code__toolbar">
        <span>{label}</span>
        <span role="status">{copyState === 'copied' ? t('已复制') : copyState === 'failed' ? t('复制失败，请手动选择代码') : ''}</span>
        <button type="button" onClick={() => void copy()} aria-label={t('复制 {{name}} 示例', { name: label })} title={t('复制 {{name}} 示例', { name: label })}>
          {copyState === 'copied' ? <Check aria-hidden="true" size={16} /> : <Copy aria-hidden="true" size={16} />}
        </button>
      </div>
      <pre><code>{localizedCode}</code></pre>
    </div>
  );
}

export function PublicDocsPage() {
  const { t } = useLocale();
  const { hash } = useLocation();
  const params = useParams<{ section?: string }>();
  const active = sections.some((section) => section.id === params.section)
    ? params.section as DocSection
    : 'integration';
  useEffect(() => {
    if (active === 'codex' && hash === '#quick-launch') {
      document.getElementById('quick-launch')?.scrollIntoView({ block: 'start' });
    }
  }, [active, hash]);

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
          <Link className="docs-model-link" to="/models"><Boxes aria-hidden="true" size={17} />{t('查看模型目录')}</Link>
          <a className="docs-support" href="https://t.me/gan66" target="_blank" rel="noreferrer">
            <Send aria-hidden="true" size={17} />
            <span><strong>{t('需要帮助？')}</strong><small>@gan66</small></span>
          </a>
        </aside>
        <article className="docs-article">
          {active === 'api' && <ApiReference />}
          {active === 'integration' && <IntegrationGuide />}
          {active === 'codex' && <CodexGuide />}
          {active === 'user-guide' && <UserGuide />}
          {active === 'faq' && <FaqPage />}
          <a className="docs-source-link" href="https://github.com/ffff582/ztapi-source/tree/production-codex-entry-20261104-r1" target="_blank" rel="noreferrer">{t('前端源码')}</a>
        </article>
      </main>
    </div>
  );
}

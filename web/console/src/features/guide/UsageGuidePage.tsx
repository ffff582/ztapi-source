import { ArrowRight, Boxes, Copy, KeyRound, ShieldCheck } from 'lucide-react';
import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { Link } from 'react-router-dom';
import { UsageBillingNote } from '../../components/UsageBillingNote';
import { useLocale } from '../../i18n/locale';

type GuideLanguage = 'curl' | 'python' | 'node';
type GuideEndpoint = 'chat' | 'responses' | 'embeddings' | 'images' | 'video-tasks';
type CopyStatus = 'idle' | 'success' | 'error';

const guideTabs: ReadonlyArray<{ id: GuideLanguage; label: string }> = [
  { id: 'curl', label: 'cURL' },
  { id: 'python', label: 'Python' },
  { id: 'node', label: 'Node.js' },
];

const chatExamples: Record<GuideLanguage, string> = {
  curl: [
    'curl https://ztapi.vip/v1/chat/completions \\',
    '  -H "Authorization: Bearer ${ZTAPI_API_KEY}" \\',
    '  -H "Content-Type: application/json" \\',
    "  -d '{",
    '    "model": "YOUR_MODEL_ID",',
    '    "messages": [{"role": "user", "content": "Hello"}]',
    "  }'",
  ].join('\n'),
  python: [
    'import os',
    'from openai import OpenAI',
    '',
    'client = OpenAI(',
    '    api_key=os.environ["ZTAPI_API_KEY"],',
    '    base_url="https://ztapi.vip/v1",',
    ')',
    '',
    'response = client.chat.completions.create(',
    '    model="YOUR_MODEL_ID",',
    '    messages=[{"role": "user", "content": "Hello"}],',
    ')',
    'print(response.choices[0].message.content)',
  ].join('\n'),
  node: [
    'import OpenAI from "openai";',
    '',
    'const client = new OpenAI({',
    '  apiKey: process.env.ZTAPI_API_KEY,',
    '  baseURL: "https://ztapi.vip/v1",',
    '});',
    '',
    'const response = await client.chat.completions.create({',
    '  model: "YOUR_MODEL_ID",',
    '  messages: [{ role: "user", content: "Hello" }],',
    '});',
    'console.log(response.choices[0].message.content);',
  ].join('\n'),
};

const responsesExamples: Record<GuideLanguage, string> = {
  curl: [
    'curl https://ztapi.vip/v1/responses \\',
    '  -H "Authorization: Bearer ${ZTAPI_API_KEY}" \\',
    '  -H "Content-Type: application/json" \\',
    "  -d '{",
    '    "model": "YOUR_MODEL_ID",',
    '    "input": "Hello",',
    '    "max_output_tokens": 1024',
    "  }'",
  ].join('\n'),
  python: [
    'import os',
    'from openai import OpenAI',
    '',
    'client = OpenAI(',
    '    api_key=os.environ["ZTAPI_API_KEY"],',
    '    base_url="https://ztapi.vip/v1",',
    ')',
    '',
    'response = client.responses.create(',
    '    model="YOUR_MODEL_ID",',
    '    input="Hello",',
    '    max_output_tokens=1024,',
    ')',
    'print(response.output_text)',
  ].join('\n'),
  node: [
    'import OpenAI from "openai";',
    '',
    'const client = new OpenAI({',
    '  apiKey: process.env.ZTAPI_API_KEY,',
    '  baseURL: "https://ztapi.vip/v1",',
    '});',
    '',
    'const response = await client.responses.create({',
    '  model: "YOUR_MODEL_ID",',
    '  input: "Hello",',
    '  max_output_tokens: 1024,',
    '});',
    'console.log(response.output_text);',
  ].join('\n'),
};

const embeddingsExamples: Record<GuideLanguage, string> = {
  curl: [
    'curl https://ztapi.vip/v1/embeddings \\',
    '  -H "Authorization: Bearer ${ZTAPI_API_KEY}" \\',
    '  -H "Content-Type: application/json" \\',
    "  -d '{",
    '    "model": "YOUR_MODEL_ID",',
    '    "input": "Hello"',
    "  }'",
  ].join('\n'),
  python: [
    'import os',
    'from openai import OpenAI',
    '',
    'client = OpenAI(',
    '    api_key=os.environ["ZTAPI_API_KEY"],',
    '    base_url="https://ztapi.vip/v1",',
    ')',
    '',
    'response = client.embeddings.create(',
    '    model="YOUR_MODEL_ID",',
    '    input="Hello",',
    ')',
    'print(response.data[0].embedding)',
  ].join('\n'),
  node: [
    'import OpenAI from "openai";',
    '',
    'const client = new OpenAI({',
    '  apiKey: process.env.ZTAPI_API_KEY,',
    '  baseURL: "https://ztapi.vip/v1",',
    '});',
    '',
    'const response = await client.embeddings.create({',
    '  model: "YOUR_MODEL_ID",',
    '  input: "Hello",',
    '});',
    'console.log(response.data[0].embedding);',
  ].join('\n'),
};

const imageExamples: Record<GuideLanguage, string> = {
  curl: [
    'curl https://ztapi.vip/v1/images/generations \\',
    '  -H "Authorization: Bearer ${ZTAPI_API_KEY}" \\',
    '  -H "Content-Type: application/json" \\',
    "  -d '{",
    '    "model": "YOUR_IMAGE_MODEL_ID",',
    '    "prompt": "A clean product photograph",',
    '    "size": "YOUR_SUPPORTED_SIZE",',
    '    "quality": "YOUR_SUPPORTED_QUALITY",',
    '    "response_format": "url"',
    "  }'",
  ].join('\n'),
  python: [
    'import os',
    'import requests',
    '',
    'response = requests.post(',
    '    "https://ztapi.vip/v1/images/generations",',
    '    headers={"Authorization": f\'Bearer {os.environ["ZTAPI_API_KEY"]}\'},',
    '    json={',
    '        "model": "YOUR_IMAGE_MODEL_ID",',
    '        "prompt": "A clean product photograph",',
    '        "size": "YOUR_SUPPORTED_SIZE",',
    '        "quality": "YOUR_SUPPORTED_QUALITY",',
    '        "response_format": "url",',
    '    },',
    ')',
    'response.raise_for_status()',
    'print(response.json()["data"][0]["url"])',
  ].join('\n'),
  node: [
    'const response = await fetch("https://ztapi.vip/v1/images/generations", {',
    '  method: "POST",',
    '  headers: {',
    '    Authorization: `Bearer ${process.env.ZTAPI_API_KEY}`,',
    '    "Content-Type": "application/json",',
    '  },',
    '  body: JSON.stringify({',
    '    model: "YOUR_IMAGE_MODEL_ID",',
    '    prompt: "A clean product photograph",',
    '    size: "YOUR_SUPPORTED_SIZE",',
    '    quality: "YOUR_SUPPORTED_QUALITY",',
    '    response_format: "url",',
    '  }),',
    '});',
    'if (!response.ok) throw new Error(`HTTP ${response.status}`);',
    'console.log((await response.json()).data[0].url);',
  ].join('\n'),
};

const videoExamples: Record<GuideLanguage, string> = {
  curl: [
    'TASK_ID=$(curl https://ztapi.vip/v1/video/generations \\',
    '  -H "Authorization: Bearer ${ZTAPI_API_KEY}" \\',
    '  -H "Content-Type: application/json" \\',
    "  -d '{",
    '    "model": "YOUR_VIDEO_MODEL_ID",',
    '    "prompt": "A slow camera move across a city skyline",',
    '    "size": "YOUR_SUPPORTED_RESOLUTION",',
    '    "duration": 5',
    "  }' | jq -r '.task_id')",
    '',
    'curl "https://ztapi.vip/v1/video/generations/${TASK_ID}" \\',
    '  -H "Authorization: Bearer ${ZTAPI_API_KEY}"',
  ].join('\n'),
  python: [
    'import os',
    'import time',
    'import requests',
    '',
    'headers = {"Authorization": f\'Bearer {os.environ["ZTAPI_API_KEY"]}\'}',
    'created = requests.post(',
    '    "https://ztapi.vip/v1/video/generations",',
    '    headers=headers,',
    '    json={"model": "YOUR_VIDEO_MODEL_ID", "prompt": "A slow camera move across a city skyline", "size": "YOUR_SUPPORTED_RESOLUTION", "duration": 5},',
    ')',
    'created.raise_for_status()',
    'task_id = created.json()["task_id"]',
    'while True:',
    '    task = requests.get(f"https://ztapi.vip/v1/video/generations/{task_id}", headers=headers)',
    '    task.raise_for_status()',
    '    result = task.json()',
    '    if result["status"] in {"succeeded", "failed"}: break',
    '    time.sleep(3)',
    'print(result)',
  ].join('\n'),
  node: [
    'const headers = { Authorization: `Bearer ${process.env.ZTAPI_API_KEY}`, "Content-Type": "application/json" };',
    'const created = await fetch("https://ztapi.vip/v1/video/generations", {',
    '  method: "POST", headers,',
    '  body: JSON.stringify({ model: "YOUR_VIDEO_MODEL_ID", prompt: "A slow camera move across a city skyline", size: "YOUR_SUPPORTED_RESOLUTION", duration: 5 }),',
    '});',
    'if (!created.ok) throw new Error(`HTTP ${created.status}`);',
    'const { task_id } = await created.json();',
    'let result;',
    'do {',
    '  await new Promise((resolve) => setTimeout(resolve, 3000));',
    '  const response = await fetch(`https://ztapi.vip/v1/video/generations/${task_id}`, { headers });',
    '  if (!response.ok) throw new Error(`HTTP ${response.status}`);',
    '  result = await response.json();',
    '} while (!["succeeded", "failed"].includes(result.status));',
    'console.log(result);',
  ].join('\n'),
};

const endpointDetails: Record<GuideEndpoint, { path: string; examples: Record<GuideLanguage, string> }> = {
  chat: { path: '/v1/chat/completions', examples: chatExamples },
  responses: { path: '/v1/responses', examples: responsesExamples },
  embeddings: { path: '/v1/embeddings', examples: embeddingsExamples },
  images: { path: '/v1/images/generations', examples: imageExamples },
  'video-tasks': { path: '/v1/video/generations', examples: videoExamples },
};

export function UsageGuidePage() {
  const { t } = useLocale();
  const [language, setLanguage] = useState<GuideLanguage>('curl');
  const [endpoint, setEndpoint] = useState<GuideEndpoint>('chat');
  const [copyStatus, setCopyStatus] = useState<CopyStatus>('idle');
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const copyAttempt = useRef(0);
  const { examples, path } = endpointDetails[endpoint];

  useEffect(() => {
    copyAttempt.current += 1;
    setCopyStatus('idle');
  }, [language, endpoint]);

  function handleTabKeyDown(
    event: KeyboardEvent<HTMLButtonElement>,
    currentIndex: number,
  ) {
    let nextIndex: number;
    if (event.key === 'ArrowRight') {
      nextIndex = (currentIndex + 1) % guideTabs.length;
    } else if (event.key === 'ArrowLeft') {
      nextIndex = (currentIndex - 1 + guideTabs.length) % guideTabs.length;
    } else if (event.key === 'Home') {
      nextIndex = 0;
    } else if (event.key === 'End') {
      nextIndex = guideTabs.length - 1;
    } else {
      return;
    }

    event.preventDefault();
    setLanguage(guideTabs[nextIndex].id);
    tabRefs.current[nextIndex]?.focus();
  }

  async function copyExample() {
    const attempt = ++copyAttempt.current;
    setCopyStatus('idle');
    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable');
      }
      await navigator.clipboard.writeText(examples[language]);
      if (attempt === copyAttempt.current) {
        setCopyStatus('success');
      }
    } catch {
      if (attempt === copyAttempt.current) {
        setCopyStatus('error');
      }
    }
  }

  return (
    <div className="console-page">
      <header className="console-page__header">
        <div>
          <p className="console-eyebrow">{t('接入文档')}</p>
          <h1>{t('使用说明')}</h1>
        </div>
        <p role="note" aria-label={t('端点选择')}>
          {t('请按模型支持页的调用地址选择示例。Responses-only 模型必须使用')}
          <code> /v1/responses</code>{t('，不适用 Chat Completions 示例。')}
        </p>
      </header>

      <section className="guide-start" aria-labelledby="guide-start-heading">
        <div className="console-section__heading">
          <div>
            <p className="console-eyebrow">{t('开始调用')}</p>
            <h2 id="guide-start-heading">{t('三步完成接入')}</h2>
          </div>
        </div>
        <ol className="guide-steps">
          <li>
            <span>01</span>
            <div>
              <strong>{t('创建 API Key')}</strong>
              <p>{t('密钥只在创建时完整显示一次，请立即妥善保存。')}</p>
            </div>
          </li>
          <li>
            <span>02</span>
            <div>
              <strong>{t('选择模型 ID')}</strong>
              <p>{t('在模型支持页查看当前账户实际可调用的模型。')}</p>
            </div>
          </li>
          <li>
            <span>03</span>
            <div>
              <strong>{t('发送请求')}</strong>
              <p>{t('在服务端设置 ZTAPI_API_KEY，再替换示例中的模型 ID。')}</p>
            </div>
          </li>
        </ol>
        <div className="guide-actions">
          <Link className="console-button console-button--primary" to="/console/keys">
            <KeyRound aria-hidden="true" size={17} />
            {t('创建 API 密钥')}
          </Link>
          <Link className="console-button console-button--secondary" to="/console/models">
            <Boxes aria-hidden="true" size={17} />
            {t('查看可用模型')}
            <ArrowRight aria-hidden="true" size={16} />
          </Link>
        </div>
      </section>

      <section
        className="console-section guide-reference"
        aria-labelledby="guide-reference-heading"
      >
        <div className="console-section__heading">
          <div>
            <p className="console-eyebrow">{t('OpenAI 兼容入口')}</p>
            <h2 id="guide-reference-heading">{t('接口信息')}</h2>
          </div>
        </div>
        <dl className="guide-reference__grid">
          <div>
            <dt>Base URL</dt>
            <dd><code>https://ztapi.vip/v1</code></dd>
          </div>
          <div>
            <dt>{t('请求地址')}</dt>
            <dd><code>{`POST ${path}`}</code></dd>
          </div>
          <div>
            <dt>{t('鉴权方式')}</dt>
            <dd><code>Authorization: Bearer YOUR_ZTAPI_API_KEY</code></dd>
          </div>
        </dl>
      </section>

      <UsageBillingNote />

      <section className="console-section" aria-labelledby="guide-code-heading">
        <div className="console-section__heading guide-code-heading">
          <div>
            <p className="console-eyebrow">{t('代码示例')}</p>
            <h2 id="guide-code-heading">{t('发送第一条请求')}</h2>
          </div>
          <label className="model-support-provider">
            <span>{t('调用接口')}</span>
            <select
              value={endpoint}
              onChange={(event) => setEndpoint(event.target.value as GuideEndpoint)}
            >
              <option value="chat">Chat Completions</option>
              <option value="responses">Responses</option>
              <option value="embeddings">{t('文本向量 Embeddings')}</option>
              <option value="images">{t('图片生成')}</option>
              <option value="video-tasks">{t('异步视频任务')}</option>
            </select>
          </label>
        </div>
        <div className="guide-code-workbench">
          <div className="guide-tabs" role="tablist" aria-label={t('代码示例')}>
            {guideTabs.map((tab, index) => (
              <button
                ref={(node) => {
                  tabRefs.current[index] = node;
                }}
                aria-controls="guide-code-panel"
                aria-selected={language === tab.id}
                id={`guide-tab-${tab.id}`}
                key={tab.id}
                role="tab"
                tabIndex={language === tab.id ? 0 : -1}
                type="button"
                onClick={() => setLanguage(tab.id)}
                onKeyDown={(event) => handleTabKeyDown(event, index)}
              >
                {tab.label}
              </button>
            ))}
          </div>
          <div
            aria-labelledby={`guide-tab-${language}`}
            className="guide-code-panel"
            id="guide-code-panel"
            role="tabpanel"
          >
            <div className="guide-code-toolbar">
              <span>{guideTabs.find((tab) => tab.id === language)?.label}</span>
              <button type="button" onClick={() => void copyExample()}>
                <Copy aria-hidden="true" size={15} />
                {t('复制代码')}
              </button>
            </div>
            <pre data-testid="guide-code"><code>{examples[language]}</code></pre>
            <span className="guide-copy-status" role="status" aria-live="polite">
              {copyStatus === 'success' && t('已复制')}
              {copyStatus === 'error' && t('复制失败，请手动复制')}
            </span>
          </div>
        </div>
      </section>

      <aside className="guide-security">
        <ShieldCheck aria-hidden="true" size={19} />
        <div>
          <strong>{t('保护你的 API Key')}</strong>
          <p>{t('不要在浏览器前端、移动端安装包或公开仓库中暴露 API Key。')}</p>
        </div>
      </aside>
    </div>
  );
}

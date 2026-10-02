import { readFile, writeFile } from 'node:fs/promises';
import { URL } from 'node:url';

const manifest = JSON.parse(await readFile(new URL('../src/features/docs/docs-manifest.json', import.meta.url), 'utf8'));
const text = [
  '# ZTAPI',
  '',
  '> Public documentation for the ZTAPI unified model API.',
  '',
  `Base URL: ${manifest.baseUrl}`,
  'Authentication: Authorization: Bearer YOUR_ZTAPI_API_KEY',
  '',
  '## Documentation',
  ...manifest.sections.map((section) => `- [${section.english}](${manifest.origin}${section.path})`),
  `- [Live models and pricing](${manifest.origin}/models)`,
  '',
  '## Public endpoints',
  ...manifest.endpoints.map(([method, path]) => `- ${method} ${path}`),
  '',
  '## Model selection',
  'Copy the exact public model ID and use its supported endpoint from the live catalog.',
  'Not every model supports Chat Completions. Responses-only models require /v1/responses.',
  'Image and video models use their dedicated endpoints. Poll video tasks using the task ID.',
  'Never silently substitute a different model. Do not publish or share API keys.',
  'Prices, availability and capabilities come from the live catalog, not this index.',
  'For support, provide the request ID, model and time without credentials: https://t.me/gan66',
  '',
].join('\n');
await writeFile(new URL('../public/llms.txt', import.meta.url), text, 'utf8');

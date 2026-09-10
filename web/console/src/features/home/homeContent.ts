export type ModelFamily = 'openai' | 'claude' | 'gemini';

export type ModelFamilyDefinition = {
  id: ModelFamily;
  label: string;
  description: string;
};

export const MODEL_FAMILIES: readonly ModelFamilyDefinition[] = [
  { id: 'openai', label: 'OpenAI', description: '通用与推理模型' },
  { id: 'claude', label: 'Claude', description: '长文本与复杂任务' },
  { id: 'gemini', label: 'Gemini', description: '多模态模型能力' },
] as const;

export const GATEWAY_CAPABILITIES = [
  'OpenAI 兼容接口',
  '流式响应',
  '按量计费',
  '可配置路由',
] as const;

export function getCodeExample(family: ModelFamily) {
  const definition = MODEL_FAMILIES.find((item) => item.id === family) ?? MODEL_FAMILIES[0];

  return [
    `// ${definition.label}: ${definition.description}`,
    'import OpenAI from "openai";',
    '',
    'const client = new OpenAI({',
    '  baseURL: "https://ztapi.vip/v1",',
    '  apiKey: process.env.ZTAPI_API_KEY,',
    '});',
    '',
    'const response = await client.chat.completions.create({',
    '  model: process.env.ZTAPI_MODEL_ID,',
    '  messages,',
    '});',
  ].join('\n');
}

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

export const modelFamilyOptions = Object.freeze([
  { value: 'openai', label: 'OpenAI' },
  { value: 'claude', label: 'Claude' },
  { value: 'gemini', label: 'Gemini' },
]);

export const modelProviderOptions = Object.freeze([
  { value: 'openai', label: 'OpenAI' },
  { value: 'anthropic', label: 'Claude' },
  { value: 'google', label: 'Gemini' },
  { value: 'deepseek', label: 'DeepSeek' },
  { value: 'doubao', label: 'Doubao' },
  { value: 'qwen', label: 'Qwen' },
  { value: 'moonshot', label: 'Kimi' },
  { value: 'glm', label: 'GLM' },
  { value: 'minimax', label: 'MiniMax' },
  { value: 'other', label: 'Other' },
]);

export const modelProtocolOptions = Object.freeze([
  { value: 'openai_compatible', label: 'OpenAI Compatible' },
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'gemini', label: 'Gemini' },
]);

export const modelWorkflowOptions = Object.freeze([
  { value: 'all', label: '全部' },
  { value: 'discovered', label: '已发现' },
  { value: 'mapped', label: '已映射' },
  { value: 'priced', label: '已定价' },
  { value: 'verified', label: '已验证' },
  { value: 'published', label: '已发布' },
]);

const identityBlockers = new Set(['identity_missing', 'identity_invalid']);
const priceBlockers = new Set([
  'price_source_missing',
  'price_dimension_incomplete',
]);
const verificationBlockers = new Set([
  'discovery_missing_or_stale',
  'route_unavailable',
  'verification_non_streaming_failed',
  'verification_streaming_missing',
  'verification_usage_unreconciled',
  'verification_error_classification_unsafe',
]);

export const publicationBlockerLabels = Object.freeze({
  discovery_missing_or_stale: '缺少 24 小时内的模型发现快照',
  identity_missing: '尚未确认公开名称、协议和供应商',
  identity_invalid: '身份映射与当前模型配置不一致',
  route_unavailable: '至少一个用户组没有已启用且已发现的上游通道',
  verification_non_streaming_failed: '非流式请求尚未验证通过',
  verification_streaming_missing: '流式请求尚未验证通过',
  verification_usage_unreconciled: '上游 Token 用量无法核对',
  verification_error_classification_unsafe: '上游错误分类验证不完整',
  price_source_missing: '尚未导入供应商报价证据',
  price_dimension_incomplete: '报价维度或 40% 毛利售价不完整',
  alias_conflict: '公开名称与其他模型冲突',
  groups_missing: '尚未选择明确的用户组',
  publication_evidence_unavailable: '发布证据暂时不可读取',
});

export function providerLabel(provider) {
  return (
    modelProviderOptions.find((option) => option.value === provider)?.label ||
    '未映射'
  );
}

export function protocolLabel(protocol) {
  return (
    modelProtocolOptions.find((option) => option.value === protocol)?.label ||
    '未映射'
  );
}

export function workflowState(model) {
  if (model?.published) return 'published';
  const blockers = new Set(
    Array.isArray(model?.publication_blockers)
      ? model.publication_blockers
      : [],
  );
  if ([...identityBlockers].some((blocker) => blockers.has(blocker))) {
    return 'discovered';
  }
  if ([...priceBlockers].some((blocker) => blockers.has(blocker))) {
    return 'mapped';
  }
  if ([...verificationBlockers].some((blocker) => blockers.has(blocker))) {
    return 'priced';
  }
  return 'verified';
}

export function workflowLabel(model) {
  const state = workflowState(model);
  return (
    modelWorkflowOptions.find((option) => option.value === state)?.label ||
    '已发现'
  );
}

export function familyLabel(family) {
  return (
    modelFamilyOptions.find((option) => option.value === family)?.label ||
    '不支持'
  );
}

function marginPercent(cost, price) {
  const numericCost = Number(cost);
  const numericPrice = Number(price);
  if (
    !Number.isFinite(numericCost) ||
    !Number.isFinite(numericPrice) ||
    numericPrice <= 0
  ) {
    return null;
  }
  return ((numericPrice - numericCost) / numericPrice) * 100;
}

export function minimumMarginPercent(model) {
  const margins = [
    marginPercent(
      model?.input_cost_per_million,
      model?.input_price_per_million,
    ),
    marginPercent(
      model?.output_cost_per_million,
      model?.output_price_per_million,
    ),
  ];
  if (margins.some((margin) => margin === null)) return null;
  return Math.min(...margins);
}

export function isBelowCost(model) {
  const inputCost = Number(model?.input_cost_per_million);
  const outputCost = Number(model?.output_cost_per_million);
  const inputPrice = Number(model?.input_price_per_million);
  const outputPrice = Number(model?.output_price_per_million);
  return (
    (Number.isFinite(inputCost) &&
      Number.isFinite(inputPrice) &&
      inputPrice < inputCost) ||
    (Number.isFinite(outputCost) &&
      Number.isFinite(outputPrice) &&
      outputPrice < outputCost)
  );
}

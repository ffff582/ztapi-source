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

import {
  familyLabel,
  minimumMarginPercent,
  providerLabel,
  protocolLabel,
  workflowState,
} from './model-family.js';

it('maps only the supported public model families', () => {
  expect(familyLabel('openai')).toBe('OpenAI');
  expect(familyLabel('claude')).toBe('Claude');
  expect(familyLabel('gemini')).toBe('Gemini');
  expect(familyLabel('other')).toBe('不支持');
});

it('labels explicit providers and protocols without guessing from model names', () => {
  expect(providerLabel('deepseek')).toBe('DeepSeek');
  expect(providerLabel('moonshot')).toBe('Kimi');
  expect(providerLabel('')).toBe('未映射');
  expect(protocolLabel('openai_compatible')).toBe('OpenAI Compatible');
});

it('derives the highest contiguous evidence-backed workflow state', () => {
  expect(workflowState({ publication_blockers: ['identity_missing'] })).toBe(
    'discovered',
  );
  expect(
    workflowState({ publication_blockers: ['price_source_missing'] }),
  ).toBe('mapped');
  expect(
    workflowState({ publication_blockers: ['verification_streaming_missing'] }),
  ).toBe('priced');
  expect(workflowState({ publication_blockers: [] })).toBe('verified');
  expect(workflowState({ published: true, publication_blockers: [] })).toBe(
    'published',
  );
});

it('calculates the lower input or output gross margin', () => {
  expect(
    minimumMarginPercent({
      input_cost_per_million: 0.4,
      output_cost_per_million: 1.6,
      input_price_per_million: 0.8,
      output_price_per_million: 4,
    }),
  ).toBe(50);
  expect(
    minimumMarginPercent({
      input_cost_per_million: 1,
      output_cost_per_million: 1,
      input_price_per_million: 0,
      output_price_per_million: 2,
    }),
  ).toBeNull();
});

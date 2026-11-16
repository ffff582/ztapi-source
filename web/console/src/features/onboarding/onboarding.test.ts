import { describe, expect, it } from 'vitest';
import {
  onboardingProgress,
  markLogsVisited,
  markModelSelected,
} from './onboarding';

describe('onboarding progress', () => {
  it('combines real account facts with browser navigation milestones', () => {
    expect(onboardingProgress(7, { tokenCount: 0, requestCount: 0 })).toEqual({
      hasKey: false,
      selectedModel: false,
      sentRequest: false,
      reviewedLogs: false,
      completed: 0,
    });

    markModelSelected(7);
    markLogsVisited(7);

    expect(onboardingProgress(7, { tokenCount: 1, requestCount: 3 })).toEqual({
      hasKey: true,
      selectedModel: true,
      sentRequest: true,
      reviewedLogs: true,
      completed: 4,
    });
  });

  it('keeps local milestones separate between accounts', () => {
    markModelSelected(7);
    expect(onboardingProgress(8, { tokenCount: 1, requestCount: 0 }).selectedModel).toBe(false);
  });
});

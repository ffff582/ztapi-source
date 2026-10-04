interface AccountFacts {
  tokenCount: number;
  requestCount: number;
}

function storageKey(userID: number, milestone: 'model-selected' | 'logs-visited') {
  return `ztapi.onboarding.${userID}.${milestone}`;
}

function writeMilestone(userID: number, milestone: 'model-selected' | 'logs-visited') {
  try {
    localStorage.setItem(storageKey(userID, milestone), 'true');
  } catch {
    // Browser storage can be unavailable; real server-side facts still work.
  }
}

function readMilestone(userID: number, milestone: 'model-selected' | 'logs-visited') {
  try {
    return localStorage.getItem(storageKey(userID, milestone)) === 'true';
  } catch {
    return false;
  }
}

export function markModelSelected(userID: number) {
  writeMilestone(userID, 'model-selected');
}

export function markLogsVisited(userID: number) {
  writeMilestone(userID, 'logs-visited');
}

export function onboardingProgress(userID: number, facts: AccountFacts) {
  const progress = {
    hasKey: facts.tokenCount > 0,
    selectedModel: readMilestone(userID, 'model-selected'),
    sentRequest: facts.requestCount > 0,
    reviewedLogs: readMilestone(userID, 'logs-visited'),
  };
  return {
    ...progress,
    completed: Object.values(progress).filter(Boolean).length,
  };
}

import type { DecisionMatch } from '../types';

export function decisionRouteLabel(match: DecisionMatch): string {
  if (match.equals !== undefined) return `${match.question} = ${match.equals}`;
  const bounds = [];
  if (match.min !== undefined) bounds.push(`≥ ${match.min}`);
  if (match.max !== undefined) bounds.push(`≤ ${match.max}`);
  return `${match.question} ${bounds.join(' and ')}`;
}

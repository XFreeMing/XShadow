/**
 * Per-container "left startup view this visit" guard. Memory only — cleared
 * when the issues surface unmounts so the next navigation can reopen startup.
 */
const dismissed = new Set<string>();

export function dismissIssueViewVisit(containerKey: string): void {
  dismissed.add(containerKey);
}

export function isIssueViewVisitDismissed(containerKey: string): boolean {
  return dismissed.has(containerKey);
}

export function clearIssueViewVisitDismiss(containerKey: string): void {
  dismissed.delete(containerKey);
}

/** Test helper */
export function __resetIssueViewVisitDismissForTests(): void {
  dismissed.clear();
}
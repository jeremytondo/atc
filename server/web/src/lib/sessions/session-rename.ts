import type { SessionDetail, SessionListItem } from '$lib/api';

export function sessionRenameDraft(session: SessionListItem): string {
  return session.name?.trim() ?? '';
}

export function canRenameSession(session: SessionListItem, draft: string): boolean {
  return draft.trim() !== (session.name?.trim() ?? '');
}

export function replaceRenamedSession(
  sessions: SessionListItem[],
  renamed: SessionDetail
): SessionListItem[] {
  return sessions.map((session) => (session.id === renamed.id ? renamed : session));
}

export async function submitSessionRename(
  session: SessionListItem,
  draft: string,
  rename: (id: string, name: string | null) => Promise<SessionDetail>
): Promise<SessionDetail> {
  const name = draft.trim();
  return rename(session.id, name || null);
}

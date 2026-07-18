import type { SessionDetail, SessionListItem } from '$lib/api';

export function sessionRenameDraft(session: SessionListItem): string {
  return session.name?.trim() || session.id;
}

export function canRenameSession(draft: string): boolean {
  return draft.trim() !== '';
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
  rename: (id: string, name: string) => Promise<SessionDetail>
): Promise<SessionDetail> {
  const name = draft.trim();
  if (!name) throw new Error('Name is required');
  return rename(session.id, name);
}

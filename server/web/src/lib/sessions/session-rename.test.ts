import { describe, expect, it, vi } from 'vitest';

import type { SessionDetail, SessionListItem } from '$lib/api';
import {
  canRenameSession,
  replaceRenamedSession,
  sessionRenameDraft,
  submitSessionRename
} from './session-rename';

const session: SessionListItem = {
  id: 'ses_123',
  name: 'Before',
  actionId: 'act_123',
  actionName: 'Codex',
  isAgent: true,
  workingDir: '/repo',
  status: 'ended',
  createdAt: '2026-07-18T10:00:00Z',
  updatedAt: '2026-07-18T10:00:00Z'
};

const renamed: SessionDetail = {
  ...session,
  name: 'After',
  updatedAt: '2026-07-18T11:00:00Z'
};

describe('shared session rename interaction', () => {
  it('prefills the displayed name and falls back to the displayed id', () => {
    expect(sessionRenameDraft(session)).toBe('Before');
    expect(sessionRenameDraft({ ...session, name: '   ' })).toBe('ses_123');
  });

  it('disables blank drafts and trims before submitting', async () => {
    expect(canRenameSession('   ')).toBe(false);
    const rename = vi.fn(async () => renamed);
    await expect(submitSessionRename(session, '  After  ', rename)).resolves.toBe(renamed);
    expect(rename).toHaveBeenCalledWith('ses_123', 'After');
    await expect(submitSessionRename(session, '   ', rename)).rejects.toThrow('Name is required');
  });

  it('replaces the authoritative list item returned by rename', () => {
    const other = { ...session, id: 'ses_other' };
    const result = replaceRenamedSession([session, other], renamed);
    expect(result[0]).toBe(renamed);
    expect(result[1]).toBe(other);
  });
});

// Binds the TypeScript API types and client to the shared cross-client
// fixtures in packages/contracts/fixtures — the same files the Go server
// round-trips and ATCKit decodes. The `satisfies` assertions fail
// `pnpm check` when a fixture and a type drift apart; the runtime tests
// exercise the client's envelope unwrapping against fixture bodies.
import { describe, expect, it, vi, afterEach } from 'vitest';

import actionCreate from '../../../../packages/contracts/fixtures/action-create.json';
import actionDelete from '../../../../packages/contracts/fixtures/action-delete.json';
import actionDetail from '../../../../packages/contracts/fixtures/action-detail.json';
import actionUpdate from '../../../../packages/contracts/fixtures/action-update.json';
import actionsList from '../../../../packages/contracts/fixtures/actions-list.json';
import errorFixture from '../../../../packages/contracts/fixtures/error.json';
import projectCreate from '../../../../packages/contracts/fixtures/project-create.json';
import projectsList from '../../../../packages/contracts/fixtures/projects-list.json';
import sessionDetail from '../../../../packages/contracts/fixtures/session-detail.json';
import sessionStart from '../../../../packages/contracts/fixtures/session-start.json';
import sessionRename from '../../../../packages/contracts/fixtures/session-rename.json';
import sessionsList from '../../../../packages/contracts/fixtures/sessions-list.json';
import workspaceCreate from '../../../../packages/contracts/fixtures/workspace-create.json';
import workspaceSessions from '../../../../packages/contracts/fixtures/workspace-sessions.json';
import workspacesList from '../../../../packages/contracts/fixtures/workspaces-list.json';

import {
  listActions,
  getAction,
  createAction,
  updateAction,
  deleteAction,
  listProjects,
  createProject,
  listSessions,
  getSession,
  startSession,
  renameSession,
  listWorkspaces,
  createWorkspace,
  listWorkspaceSessions,
  type Action,
  type ActionCreate,
  type ActionPatch,
  type ErrorResponse,
  type Project,
  type SessionDetail,
  type SessionListItem,
  type StartSessionRequest,
  type Workspace,
  type ApiError
} from './api';

// Compile-time contract checks: a fixture field the types can't represent
// (or a required type field the fixtures lack) fails svelte-check.

// Narrows a fixture's status string against the closed contract vocabulary,
// so an invalid fixture value fails the test run instead of being cast
// through.
const sessionStatuses = ['live', 'ended'] as const satisfies readonly SessionListItem['status'][];
function sessionStatus(value: string): SessionListItem['status'] {
  const status = sessionStatuses.find((candidate) => candidate === value);
  if (!status) {
    throw new Error(`fixture session status ${JSON.stringify(value)} is not in the contract`);
  }
  return status;
}

const sessionsContract = {
  sessions: sessionsList.response.sessions.map((session) => ({
    ...session,
    status: sessionStatus(session.status)
  }))
} satisfies { sessions: SessionListItem[] };
const sessionStartContract = {
  ...sessionStart.response,
  status: sessionStatus(sessionStart.response.status)
} satisfies SessionDetail;
const sessionDetailContract = {
  ...sessionDetail.response,
  status: sessionStatus(sessionDetail.response.status)
} satisfies SessionDetail;
const sessionRenameContract = {
  ...sessionRename.response,
  status: sessionStatus(sessionRename.response.status)
} satisfies SessionDetail;
sessionRename.request satisfies { name: string };
sessionStart.request satisfies StartSessionRequest;
projectsList.response satisfies { projects: Project[] };
projectCreate.response satisfies Project;
workspacesList.response satisfies { workspaces: Workspace[] };
workspaceCreate.response satisfies Workspace;
const workspaceSessionsContract = {
  sessions: workspaceSessions.response.sessions.map((session) => ({
    ...session,
    status: sessionStatus(session.status)
  }))
} satisfies { sessions: SessionListItem[] };
actionsList.response satisfies { actions: Action[] };
actionDetail.response satisfies Action;
actionCreate.request satisfies ActionCreate;
actionCreate.response satisfies Action;
actionUpdate.request satisfies ActionPatch;
actionUpdate.response satisfies Action;
actionDelete.response satisfies Record<string, never>;
errorFixture.response satisfies ErrorResponse;

function mockFetch(body: unknown, status = 200) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify(body), { status }))
  );
  // Node exposes a non-functional localStorage global; give the client's
  // token lookup a working, empty one.
  vi.stubGlobal('localStorage', {
    getItem: () => null,
    setItem: () => {},
    removeItem: () => {}
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('api client unwraps fixture responses', () => {
  it('listSessions returns the sessions array', async () => {
	mockFetch(sessionsContract);
    const sessions = await listSessions();
    expect(sessions.map((s) => s.id)).toEqual(['ses_fixture01']);
    expect(sessions[0].project?.id).toBe('prj_fixture01');
    expect(sessions[0].workspace?.id).toBe('wsp_fixture01');
  });

  it('startSession returns the session detail', async () => {
	mockFetch(sessionStartContract);
    const detail = await startSession(sessionStart.request);
	expect(detail.status).toBe('live');
    expect(detail.actionName).toBe('Claude');
    expect(detail.isAgent).toBe(true);
    expect(detail.workspace?.id).toBe('wsp_fixture01');
  });

  it('getSession returns an Interactive Shell session without action identity', async () => {
    mockFetch(sessionDetailContract);
    const detail = await getSession('ses/fixture 02');
    expect(detail.actionId).toBeUndefined();
    expect(detail.actionName).toBeUndefined();
    expect(detail.isAgent).toBe(false);
    expect(fetch).toHaveBeenCalledWith('/api/sessions/ses%2Ffixture%2002', expect.any(Object));
  });

  it('renameSession patches the encoded session path and returns updated detail', async () => {
    mockFetch(sessionRenameContract);
    const detail = await renameSession('ses/fixture 01', sessionRename.request.name);
    expect(detail.name).toBe('Review login fix');
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/ses%2Ffixture%2001',
      expect.objectContaining({
        method: 'PATCH',
        body: JSON.stringify(sessionRename.request)
      })
    );
  });

  it('listWorkspaces returns the workspaces array', async () => {
    mockFetch(workspacesList.response);
    const workspaces = await listWorkspaces();
    expect(workspaces).toHaveLength(1);
  });

  it('createWorkspace returns the workspace', async () => {
    mockFetch(workspaceCreate.response, 201);
    const workspace = await createWorkspace('prj_fixture01', 'Login bug');
    expect(workspace.id).toBe('wsp_fixture01');
    expect(workspace.projectId).toBe('prj_fixture01');
  });

  it('listWorkspaceSessions returns the sessions array', async () => {
	mockFetch(workspaceSessionsContract);
    const sessions = await listWorkspaceSessions('wsp_fixture01');
    expect(sessions).toHaveLength(1);
    expect(sessions[0].workspace?.id).toBe('wsp_fixture01');
  });

  it('listProjects returns the projects array', async () => {
    mockFetch(projectsList.response);
    const projects = await listProjects();
    expect(projects).toHaveLength(1);
  });

  it('createProject returns the project', async () => {
    mockFetch(projectCreate.response, 201);
    const project = await createProject('Atelier', '/home/dev/projects/atelier');
    expect(project.id).toBe('prj_fixture01');
  });

  it('listActions returns the actions array', async () => {
    mockFetch(actionsList.response);
    const actions = await listActions();
    expect(actions[0].id).toBe('act_vpj2tlg9viqd8ms52ptuvao5c4');
    expect(actions[1].args).toEqual(['run', 'dev']);
  });

  it('getAction returns the same complete shape as listActions', async () => {
    mockFetch(actionDetail.response);
    const detail = await getAction('act_vpj2tlg9viqd8ms52ptuvao5c4');
    expect(detail.command).toBe('claude');
    expect(detail.args).toEqual(['--verbose']);
    expect(fetch).toHaveBeenCalledWith(
      '/api/actions/act_vpj2tlg9viqd8ms52ptuvao5c4',
      expect.any(Object)
    );
  });

  it('runs the action administration lifecycle with ID-addressed requests', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(actionCreate.response), { status: 201 })
      )
      .mockResolvedValueOnce(new Response(JSON.stringify(actionUpdate.response), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(actionDelete.response), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    vi.stubGlobal('localStorage', {
      getItem: () => null,
      setItem: () => {},
      removeItem: () => {}
    });

    const created = await createAction(actionCreate.request);
    const updated = await updateAction(created.id, actionUpdate.request);
    await deleteAction(updated.id);

    expect(created.enabled).toBe(true);
    expect(updated.isAgent).toBe(true);
    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/actions',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify(actionCreate.request)
      })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      `/api/actions/${created.id}`,
      expect.objectContaining({
        method: 'PATCH',
        body: JSON.stringify(actionUpdate.request)
      })
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      `/api/actions/${updated.id}`,
      expect.objectContaining({ method: 'DELETE' })
    );
  });

  it('non-2xx responses surface the error envelope', async () => {
    mockFetch(errorFixture.response, 404);
    const failure = listSessions();
    await expect(failure).rejects.toMatchObject({
      status: 404,
      code: 'session_not_found'
    } satisfies Partial<ApiError>);
  });
});

// Binds the TypeScript API types and client to the shared cross-client
// fixtures in packages/contracts/fixtures — the same files the Go server
// round-trips and ATCKit decodes. The `satisfies` assertions fail
// `pnpm check` when a fixture and a type drift apart; the runtime tests
// exercise the client's envelope unwrapping against fixture bodies.
import { describe, expect, it, vi, afterEach } from 'vitest';

import actionDetail from '../../../../packages/contracts/fixtures/action-detail.json';
import actionsList from '../../../../packages/contracts/fixtures/actions-list.json';
import environments from '../../../../packages/contracts/fixtures/environments.json';
import errorFixture from '../../../../packages/contracts/fixtures/error.json';
import projectCreate from '../../../../packages/contracts/fixtures/project-create.json';
import projectsList from '../../../../packages/contracts/fixtures/projects-list.json';
import sessionStart from '../../../../packages/contracts/fixtures/session-start.json';
import sessionRename from '../../../../packages/contracts/fixtures/session-rename.json';
import sessionsList from '../../../../packages/contracts/fixtures/sessions-list.json';
import workspaceCreate from '../../../../packages/contracts/fixtures/workspace-create.json';
import workspaceSessions from '../../../../packages/contracts/fixtures/workspace-sessions.json';
import workspacesList from '../../../../packages/contracts/fixtures/workspaces-list.json';

import {
  listActions,
  getActionDetail,
  listEnvironments,
  listProjects,
  createProject,
  listSessions,
  startSession,
  renameSession,
  listWorkspaces,
  createWorkspace,
  listWorkspaceSessions,
  type ActionDetail,
  type Environment,
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
// actions-list is checked at runtime only: TS normalizes the two actions'
// differing `params` literals into phantom `key?: undefined` members that a
// Record index signature rejects.
actionDetail.response satisfies ActionDetail;
environments.response satisfies { environments: Environment[] };
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
    expect(detail.params).toEqual({ model: 'opus' });
    expect(detail.workspace?.id).toBe('wsp_fixture01');
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
    expect(actions[0].params.model.values).toEqual(['opus', 'sonnet']);
  });

  it('getActionDetail returns command and args', async () => {
    mockFetch(actionDetail.response);
    const detail = await getActionDetail('claude');
    expect(detail.command).toBe('claude');
    expect(detail.args).toEqual(['--verbose']);
  });

  it('listEnvironments returns the environments array', async () => {
    mockFetch(environments.response);
    const envs = await listEnvironments();
    expect(envs[0].default).toBe(true);
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

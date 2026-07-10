// Binds the TypeScript API types and client to the shared cross-client
// fixtures in packages/contracts/fixtures — the same files the Go server
// round-trips and AtelierCodeKit decodes. The `satisfies` assertions fail
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
import sessionsList from '../../../../packages/contracts/fixtures/sessions-list.json';

import {
  listActions,
  getActionDetail,
  listEnvironments,
  listProjects,
  createProject,
  listSessions,
  startSession,
  type ActionDetail,
  type Environment,
  type ErrorResponse,
  type Project,
  type SessionDetail,
  type SessionListItem,
  type StartSessionRequest,
  type ApiError
} from './api';

// Compile-time contract checks: a fixture field the types can't represent
// (or a required type field the fixtures lack) fails svelte-check.
sessionsList.response satisfies { sessions: SessionListItem[] };
sessionStart.response satisfies SessionDetail;
sessionStart.request satisfies StartSessionRequest;
projectsList.response satisfies { projects: Project[] };
projectCreate.response satisfies Project;
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
    mockFetch(sessionsList.response);
    const sessions = await listSessions();
    expect(sessions.map((s) => s.id)).toEqual(['ses_fixture01', 'ses_fixture02']);
    expect(sessions[0].project?.id).toBe('prj_fixture01');
  });

  it('startSession returns the session detail', async () => {
    mockFetch(sessionStart.response);
    const detail = await startSession(sessionStart.request);
    expect(detail.status).toBe('running');
    expect(detail.params).toEqual({ model: 'opus' });
  });

  it('listProjects returns the projects array', async () => {
    mockFetch(projectsList.response);
    const projects = await listProjects();
    expect(projects).toHaveLength(2);
    expect(projects[1].archivedAt).toBeDefined();
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

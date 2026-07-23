// Shared atc API client. All frontend network access goes through here so
// the bearer token (persisted in localStorage by the sidebar token field) is
// applied consistently. Mirrors the fetch pattern the old session dashboard
// used, extracted so the console shell and every page can reuse it.

export const tokenStorageKey = 'atc.apiToken';

export type Action = {
  id: string;
  name: string;
  description?: string;
  enabled: boolean;
  command: string;
  args: string[];
  isAgent: boolean;
};

export type ActionCreate = {
  name: string;
  description?: string;
  command: string;
  args?: string[];
  enabled?: boolean;
  isAgent?: boolean;
};

export type ActionPatch = {
  name?: string;
  description?: string | null;
  command?: string;
  args?: string[];
  enabled?: boolean;
  isAgent?: boolean;
};

export type Project = {
  id: string;
  name: string;
  workingDir: string;
  createdAt: string;
  updatedAt: string;
};

// Workspace is a named unit of work inside a project that groups sessions.
export type Workspace = {
  id: string;
  projectId: string;
  name: string;
  createdAt: string;
  updatedAt: string;
};

// SessionWorkspace is the workspace object nested on sessions.
export type SessionWorkspace = {
  id: string;
  name: string;
};

// SessionProject is the derived project object nested on sessions, kept so
// clients that group by project keep working.
export type SessionProject = {
  id: string;
  name: string;
};

export type SessionListItem = {
  id: string;
  name?: string;
  // Absent actionId/actionName means the session is an Interactive Shell.
  actionId?: string;
  actionName?: string;
  isAgent: boolean;
  workingDir: string;
  status: 'live' | 'ended';
  createdAt: string;
  updatedAt: string;
  workspace?: SessionWorkspace;
  project?: SessionProject;
};

export type SessionDetail = SessionListItem;

export type StartSessionRequest = {
  workspaceId: string;
  // Omitted actionId launches the Interactive Shell.
  actionId?: string;
  name?: string;
};

export type ErrorResponse = {
  error?: string;
  message?: string;
  sessionId?: string;
};

export type ApiError = Error & { status?: number; code?: string };

export function getToken(): string {
  if (typeof localStorage === 'undefined') return '';
  return localStorage.getItem(tokenStorageKey) ?? '';
}

export function setToken(token: string): void {
  if (typeof localStorage === 'undefined') return;
  const trimmed = token.trim();
  if (trimmed) localStorage.setItem(tokenStorageKey, trimmed);
  else localStorage.removeItem(tokenStorageKey);
}

// authHeaders merges the stored bearer token into request headers. Exposed so
// callers that want the raw Response (e.g. the Try it panel, which renders 4xx
// bodies rather than throwing) can build their own fetch.
export function authHeaders(init?: HeadersInit): Headers {
  const headers = new Headers(init);
  const token = getToken().trim();
  if (token) headers.set('Authorization', `Bearer ${token}`);
  return headers;
}

// apiFetch throws on non-2xx, surfacing the JSON {error,message} shape as an
// Error. Use it for list/mutation calls where a failure is an error state.
export async function apiFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const res = await fetch(path, { ...init, headers: authHeaders(init.headers) });
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as ErrorResponse;
    const err = new Error(
      body.message || body.error || `request failed (${res.status})`
    ) as ApiError;
    err.status = res.status;
    err.code = body.error;
    throw err;
  }
  return res;
}

export function messageFromError(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

export async function listActions(): Promise<Action[]> {
  const res = await apiFetch('/api/actions');
  const body = (await res.json()) as { actions?: Action[] };
  return body.actions ?? [];
}

export async function getAction(id: string): Promise<Action> {
  const res = await apiFetch(`/api/actions/${encodeURIComponent(id)}`);
  return (await res.json()) as Action;
}

function jsonInit(method: string, body: unknown): RequestInit {
  return {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  };
}

export async function createAction(body: ActionCreate): Promise<Action> {
  const res = await apiFetch('/api/actions', jsonInit('POST', body));
  return (await res.json()) as Action;
}

export async function updateAction(id: string, body: ActionPatch): Promise<Action> {
  const res = await apiFetch(`/api/actions/${encodeURIComponent(id)}`, jsonInit('PATCH', body));
  return (await res.json()) as Action;
}

export async function deleteAction(id: string): Promise<void> {
  await apiFetch(`/api/actions/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function listSessions(
  opts: { status?: 'live' | 'ended' } = {}
): Promise<SessionListItem[]> {
  const qs = opts.status ? `?status=${opts.status}` : '';
  const res = await apiFetch(`/api/sessions${qs}`);
  const body = (await res.json()) as { sessions?: SessionListItem[] };
  return body.sessions ?? [];
}

export async function getSession(id: string): Promise<SessionDetail> {
  const res = await apiFetch(`/api/sessions/${encodeURIComponent(id)}`);
  return (await res.json()) as SessionDetail;
}

export async function startSession(body: StartSessionRequest): Promise<SessionDetail> {
  const res = await apiFetch('/api/sessions/start', jsonInit('POST', body));
  return (await res.json()) as SessionDetail;
}

export async function renameSession(id: string, name: string): Promise<SessionDetail> {
  const res = await apiFetch(
    `/api/sessions/${encodeURIComponent(id)}`,
    jsonInit('PATCH', { name })
  );
  return (await res.json()) as SessionDetail;
}

// deleteSession ends a Live process before removing its record; an Ended
// Session only has its record removed.
// Files on disk are never touched.
export async function deleteSession(id: string): Promise<void> {
  await apiFetch(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function listProjects(): Promise<Project[]> {
  const res = await apiFetch('/api/projects');
  const body = (await res.json()) as { projects?: Project[] };
  return body.projects ?? [];
}

export async function getProject(id: string): Promise<Project> {
  const res = await apiFetch(`/api/projects/${encodeURIComponent(id)}`);
  return (await res.json()) as Project;
}

export async function createProject(name: string, workingDir: string): Promise<Project> {
  const res = await apiFetch('/api/projects', jsonInit('POST', { name, workingDir }));
  return (await res.json()) as Project;
}

export async function renameProject(id: string, name: string): Promise<Project> {
  const res = await apiFetch(
    `/api/projects/${encodeURIComponent(id)}`,
    jsonInit('PATCH', { name })
  );
  return (await res.json()) as Project;
}

// deleteProject removes a project with zero workspaces. Files on disk are
// never touched.
export async function deleteProject(id: string): Promise<void> {
  await apiFetch(`/api/projects/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function listProjectSessions(
	id: string,
	opts: { status?: 'live' | 'ended' } = {}
): Promise<SessionListItem[]> {
	const qs = opts.status ? `?status=${opts.status}` : '';
  const res = await apiFetch(`/api/projects/${encodeURIComponent(id)}/sessions${qs}`);
  const body = (await res.json()) as { sessions?: SessionListItem[] };
  return body.sessions ?? [];
}

export async function listWorkspaces(
  opts: { projectId?: string } = {}
): Promise<Workspace[]> {
  const query = new URLSearchParams();
  if (opts.projectId) query.set('projectId', opts.projectId);
  const qs = query.size > 0 ? `?${query.toString()}` : '';
  const res = await apiFetch(`/api/workspaces${qs}`);
  const body = (await res.json()) as { workspaces?: Workspace[] };
  return body.workspaces ?? [];
}

export async function getWorkspace(id: string): Promise<Workspace> {
  const res = await apiFetch(`/api/workspaces/${encodeURIComponent(id)}`);
  return (await res.json()) as Workspace;
}

export async function createWorkspace(projectId: string, name: string): Promise<Workspace> {
  const res = await apiFetch('/api/workspaces', jsonInit('POST', { projectId, name }));
  return (await res.json()) as Workspace;
}

export async function renameWorkspace(id: string, name: string): Promise<Workspace> {
  const res = await apiFetch(
    `/api/workspaces/${encodeURIComponent(id)}`,
    jsonInit('PATCH', { name })
  );
  return (await res.json()) as Workspace;
}

// deleteWorkspace stops the workspace's active sessions, then removes the
// workspace and its session metadata. Files on disk are never touched.
export async function deleteWorkspace(id: string): Promise<void> {
  await apiFetch(`/api/workspaces/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function listWorkspaceSessions(
  id: string,
  opts: { status?: 'live' | 'ended' } = {}
): Promise<SessionListItem[]> {
  const qs = opts.status ? `?status=${opts.status}` : '';
  const res = await apiFetch(`/api/workspaces/${encodeURIComponent(id)}/sessions${qs}`);
  const body = (await res.json()) as { sessions?: SessionListItem[] };
  return body.sessions ?? [];
}

// sessionActionLabel is the copied launch-time action name; an absent name is
// the Interactive Shell.
export function sessionActionLabel(session: { actionName?: string }): string {
  return session.actionName ?? 'interactive shell';
}

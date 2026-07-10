// Shared Atelier Code API client. All frontend network access goes through here so
// the bearer token (persisted in localStorage by the sidebar token field) is
// applied consistently. Mirrors the fetch pattern the old session dashboard
// used, extracted so the console shell and every page can reuse it.

export const tokenStorageKey = 'atc.apiToken';

export type ParamSpec = {
  type: 'enum' | 'bool' | string;
  values?: string[];
  default?: unknown;
  flag?: string;
  label?: string;
  description?: string;
};

export type ActionOrigin = 'builtin' | 'modified' | 'custom' | string;

export type Action = {
  name: string;
  origin?: ActionOrigin;
  enabled?: boolean;
  label?: string;
  description?: string;
  prompt?: { flag?: string };
  params: Record<string, ParamSpec>;
};

export type ActionDetail = {
  name: string;
  origin?: ActionOrigin;
  enabled?: boolean;
  label?: string;
  description?: string;
  command: string;
  args: string[];
  prompt?: { flag?: string };
  params: Record<string, ParamSpec>;
};

// ActionWrite is the create/update body the backend accepts. Prompt is null when
// the action takes no initial prompt; enabled is optional (omitted = enabled).
export type ActionWrite = {
  name?: string;
  label?: string;
  description?: string;
  command: string;
  args?: string[];
  prompt?: { flag?: string } | null;
  params?: Record<string, ParamSpec>;
  enabled?: boolean;
};

export type Environment = {
  name: string;
  kind: string;
  label?: string;
  description?: string;
  default?: boolean;
};

export type Project = {
  id: string;
  name: string;
  workingDir: string;
  createdAt: string;
  updatedAt: string;
  archivedAt?: string;
};

// SessionProject is the project object nested on project-scoped sessions.
export type SessionProject = {
  id: string;
  name: string;
  workingDir: string;
  archivedAt?: string;
};

export type SessionListItem = {
  id: string;
  name?: string;
  action: string;
  environment: string;
  workingDir: string;
  status: string;
  attachable: boolean;
  failureReason?: string;
  failureCode?: string;
  createdAt: string;
  updatedAt: string;
  terminatedAt?: string;
  archivedAt?: string;
  project?: SessionProject;
};

export type SessionDetail = SessionListItem & {
  params: Record<string, unknown>;
  prompt?: string;
};

export type StartSessionRequest = {
  action: string;
  environment?: string;
  params?: Record<string, string>;
  prompt?: string;
  name?: string;
  projectId?: string;
  workingDir?: string;
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

export async function getActionDetail(name: string): Promise<ActionDetail> {
  const res = await apiFetch(`/api/actions/${encodeURIComponent(name)}`);
  return (await res.json()) as ActionDetail;
}

function jsonInit(method: string, body: unknown): RequestInit {
  return {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  };
}

export async function createAction(body: ActionWrite): Promise<ActionDetail> {
  const res = await apiFetch('/api/actions', jsonInit('POST', body));
  return (await res.json()) as ActionDetail;
}

export async function updateAction(name: string, body: ActionWrite): Promise<ActionDetail> {
  const res = await apiFetch(`/api/actions/${encodeURIComponent(name)}`, jsonInit('PUT', body));
  return (await res.json()) as ActionDetail;
}

export async function deleteAction(name: string): Promise<void> {
  await apiFetch(`/api/actions/${encodeURIComponent(name)}`, { method: 'DELETE' });
}

export async function setActionEnabled(name: string, enabled: boolean): Promise<ActionDetail> {
  const res = await apiFetch(
    `/api/actions/${encodeURIComponent(name)}/enabled`,
    jsonInit('PUT', { enabled })
  );
  return (await res.json()) as ActionDetail;
}

export async function listEnvironments(): Promise<Environment[]> {
  const res = await apiFetch('/api/environments');
  const body = (await res.json()) as { environments?: Environment[] };
  return body.environments ?? [];
}

export async function listSessions(
  opts: { includeArchived?: boolean } = {}
): Promise<SessionListItem[]> {
  const qs = opts.includeArchived ? '?includeArchived=true' : '';
  const res = await apiFetch(`/api/sessions${qs}`);
  const body = (await res.json()) as { sessions?: SessionListItem[] };
  return body.sessions ?? [];
}

export async function startSession(body: StartSessionRequest): Promise<SessionDetail> {
  const res = await apiFetch('/api/sessions/start', jsonInit('POST', body));
  return (await res.json()) as SessionDetail;
}

export async function terminateSession(id: string): Promise<void> {
  await apiFetch(`/api/sessions/${encodeURIComponent(id)}/terminate`, { method: 'POST' });
}

export async function archiveSession(id: string): Promise<void> {
  await apiFetch(`/api/sessions/${encodeURIComponent(id)}/archive`, { method: 'POST' });
}

export async function listProjects(opts: { includeArchived?: boolean } = {}): Promise<Project[]> {
  const qs = opts.includeArchived ? '?includeArchived=true' : '';
  const res = await apiFetch(`/api/projects${qs}`);
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

export async function archiveProject(id: string): Promise<Project> {
  const res = await apiFetch(`/api/projects/${encodeURIComponent(id)}/archive`, { method: 'POST' });
  return (await res.json()) as Project;
}

export async function unarchiveProject(id: string): Promise<Project> {
  const res = await apiFetch(`/api/projects/${encodeURIComponent(id)}/unarchive`, {
    method: 'POST'
  });
  return (await res.json()) as Project;
}

export async function listProjectSessions(
  id: string,
  opts: { includeArchived?: boolean } = {}
): Promise<SessionListItem[]> {
  const qs = opts.includeArchived ? '?includeArchived=true' : '';
  const res = await apiFetch(`/api/projects/${encodeURIComponent(id)}/sessions${qs}`);
  const body = (await res.json()) as { sessions?: SessionListItem[] };
  return body.sessions ?? [];
}

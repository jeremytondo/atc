// API reference content, ported from "atc Docs Console - Linear.dc.html".
// This is authored documentation (descriptions, example payloads, CLI
// equivalents) that drives the Reference pages. The Try it panel builds real
// requests from each endpoint's `fields`; the backend is the source of truth
// for actual responses.

export type EndpointParam = {
  name: string;
  type: string;
  required?: boolean;
  desc: string;
};

export type FieldKind = 'select' | 'text' | 'textarea';

export type EndpointField = {
  key: string;
  label: string;
  kind: FieldKind;
  options?: string[];
  placeholder?: string;
  required?: boolean;
};

export type EndpointCli = {
  cmd: string;
  example: string;
};

export type Endpoint = {
  key: string;
  group: string;
  method: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
  path: string;
  title: string;
  desc: string;
  params: EndpointParam[];
  fields: EndpointField[];
  returns: string;
  cli?: EndpointCli;
};

export const ENDPOINTS: Endpoint[] = [
  {
    key: 'start',
    group: 'Sessions',
    method: 'POST',
    path: '/api/sessions/start',
    title: 'Start session',
	desc: 'Launch a persistent terminal in a workspace. The session runs in the workspace project’s working directory. Omit action to launch the Interactive Shell (the host user’s shell); params and prompt require an action. The launch is synchronous: the response is the new Live Session.',
    params: [
      {
        name: 'workspaceId',
        type: 'string',
        required: true,
        desc: 'Workspace to start the session in; the session inherits the project directory.'
      },
      {
        name: 'action',
        type: 'string',
        desc: 'Name of the action to run. Omitted, the server launches the Interactive Shell.'
      },
      {
        name: 'environment',
        type: 'enum',
        desc: 'Name of one of the configured environments (see Environments). Defaults to host-login-shell.'
      },
      { name: 'params', type: 'object', desc: 'Action-specific parameter values. Requires action.' },
      {
        name: 'prompt',
        type: 'string',
        desc: 'Initial prompt for actions that declare prompt support. Requires action.'
      },
      { name: 'name', type: 'string', desc: 'Optional human label.' }
    ],
    fields: [
      { key: 'workspaceId', label: 'workspaceId', kind: 'text', placeholder: 'wsp_…', required: true },
      { key: 'action', label: 'action', kind: 'select', options: ['(interactive shell)', 'codex', 'claude'] },
      { key: 'environment', label: 'environment', kind: 'select', options: ['host-login-shell'] },
      { key: 'name', label: 'name', kind: 'text', placeholder: 'optional' },
      { key: 'prompt', label: 'prompt', kind: 'textarea', placeholder: 'optional starting prompt…' }
    ],
	returns: '{\n  "id": "ses_8f3a2c",\n  "status": "live",\n  "action": "codex"\n}',
    cli: {
      cmd: 'atc sessions start',
      example: 'atc sessions start \\\n  --workspace wsp_8f3a2c --action codex'
    }
  },
  {
    key: 'list',
    group: 'Sessions',
    method: 'GET',
    path: '/api/sessions',
    title: 'List sessions',
	desc: 'Return all Live and Ended Sessions. Provisional launch attempts are never returned.',
	params: [{ name: 'status', type: 'enum', desc: 'Optional live or ended filter.' }],
    fields: [
      {
        key: 'status',
        label: 'status',
        kind: 'select',
		options: ['(any)', 'live', 'ended']
      }
    ],
    returns: '{ "sessions": [ … ] }',
    cli: { cmd: 'atc sessions list', example: 'atc sessions list' }
  },
  {
    key: 'read',
    group: 'Sessions',
    method: 'GET',
    path: '/api/sessions/{id}',
    title: 'Read session',
    desc: 'Fetch full detail for one session, including its params and prompt.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Session id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'ses_…', required: true }],
	returns: '{ "id": "ses_…", "status": "live", … }',
    cli: { cmd: 'atc sessions show', example: 'atc sessions show ses_8f3a2c' }
  },
  {
    key: 'sendText',
    group: 'Sessions',
    method: 'POST',
    path: '/api/sessions/{id}/send-text',
    title: 'Send text',
    desc: 'Inject text into the session terminal as if a human typed it.',
    params: [
      { name: 'id', type: 'string', required: true, desc: 'Session id (path).' },
      { name: 'text', type: 'string', required: true, desc: 'Text to inject.' }
    ],
    fields: [
      { key: 'id', label: 'id', kind: 'text', placeholder: 'ses_…', required: true },
      { key: 'text', label: 'text', kind: 'text', placeholder: 'hello', required: true }
    ],
    returns: '{}',
    cli: { cmd: 'atc sessions send-text', example: 'atc sessions send-text ses_8f3a2c "hello"' }
  },
  {
    key: 'sendKey',
    group: 'Sessions',
    method: 'POST',
    path: '/api/sessions/{id}/send-key',
    title: 'Send key',
    desc: 'Send a named key. Current keys: enter, ctrl-c, escape.',
    params: [
      { name: 'id', type: 'string', required: true, desc: 'Session id (path).' },
      { name: 'key', type: 'string', required: true, desc: 'Named key.' }
    ],
    fields: [
      { key: 'id', label: 'id', kind: 'text', placeholder: 'ses_…', required: true },
      { key: 'key', label: 'key', kind: 'select', options: ['enter', 'ctrl-c', 'escape'], required: true }
    ],
    returns: '{}',
    cli: { cmd: 'atc sessions send-key', example: 'atc sessions send-key ses_8f3a2c enter' }
  },
  {
    key: 'deleteSession',
    group: 'Sessions',
    method: 'DELETE',
    path: '/api/sessions/{id}',
    title: 'Delete session',
	desc: 'Delete a Session. A Live process is ended before its record is removed; an Ended Session only has its record removed. Files on disk are never touched.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Session id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'ses_…', required: true }],
    returns: '{}',
    cli: { cmd: 'atc sessions delete', example: 'atc sessions delete ses_8f3a2c' }
  },
  {
    key: 'createWorkspace',
    group: 'Workspaces',
    method: 'POST',
    path: '/api/workspaces',
    title: 'Create workspace',
    desc: 'Create a named unit of work inside a project. Sessions start in workspaces and inherit the project directory. Names are not unique.',
    params: [
      { name: 'projectId', type: 'string', required: true, desc: 'Project the workspace belongs to.' },
      { name: 'name', type: 'string', required: true, desc: 'Human label; renameable later.' }
    ],
    fields: [
      { key: 'projectId', label: 'projectId', kind: 'text', placeholder: 'prj_…', required: true },
      { key: 'name', label: 'name', kind: 'text', placeholder: 'Fix the login bug', required: true }
    ],
    returns: '{\n  "id": "wsp_8f3a2c",\n  "projectId": "prj_8f3a2c",\n  "name": "Fix the login bug"\n}',
    cli: {
      cmd: 'atc workspaces create',
      example: 'atc workspaces create --project prj_8f3a2c --name "Fix the login bug"'
    }
  },
  {
    key: 'listWorkspaces',
    group: 'Workspaces',
    method: 'GET',
    path: '/api/workspaces',
    title: 'List workspaces',
    desc: 'Return workspaces, newest first. Omit projectId to list all workspaces across projects; archived workspaces are hidden unless requested.',
    params: [
      { name: 'projectId', type: 'string', desc: 'Restrict to one project’s workspaces.' },
      { name: 'includeArchived', type: 'bool', desc: 'Include archived workspaces.' }
    ],
    fields: [
      { key: 'projectId', label: 'projectId', kind: 'text', placeholder: 'prj_… (optional)' },
      { key: 'includeArchived', label: 'includeArchived', kind: 'select', options: ['false', 'true'] }
    ],
    returns: '{ "workspaces": [ … ] }',
    cli: { cmd: 'atc workspaces list', example: 'atc workspaces list --project prj_8f3a2c' }
  },
  {
    key: 'readWorkspace',
    group: 'Workspaces',
    method: 'GET',
    path: '/api/workspaces/{id}',
    title: 'Read workspace',
    desc: 'Fetch one workspace record.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Workspace id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'wsp_…', required: true }],
    returns: '{ "id": "wsp_…", "projectId": "prj_…", "name": "…" }',
    cli: { cmd: 'atc workspaces show', example: 'atc workspaces show wsp_8f3a2c' }
  },
  {
    key: 'renameWorkspace',
    group: 'Workspaces',
    method: 'PATCH',
    path: '/api/workspaces/{id}',
    title: 'Rename workspace',
    desc: 'Change a workspace name. The body accepts only name; renaming works while archived too.',
    params: [
      { name: 'id', type: 'string', required: true, desc: 'Workspace id (path).' },
      { name: 'name', type: 'string', required: true, desc: 'New name.' }
    ],
    fields: [
      { key: 'id', label: 'id', kind: 'text', placeholder: 'wsp_…', required: true },
      { key: 'name', label: 'name', kind: 'text', placeholder: 'New name', required: true }
    ],
    returns: '{ "id": "wsp_…", "name": "New name", … }',
    cli: { cmd: 'atc workspaces rename', example: 'atc workspaces rename wsp_8f3a2c "New name"' }
  },
  {
    key: 'archiveWorkspace',
    group: 'Workspaces',
    method: 'POST',
    path: '/api/workspaces/{id}/archive',
    title: 'Archive workspace',
	desc: 'Hide a workspace from default lists and block new Session starts in it. Fails with 409 workspace_has_active_sessions while the workspace has a provisional launch attempt or Live Session. Idempotent.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Workspace id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'wsp_…', required: true }],
    returns: '{ "id": "wsp_…", "archivedAt": "…" }',
    cli: { cmd: 'atc workspaces archive', example: 'atc workspaces archive wsp_8f3a2c' }
  },
  {
    key: 'unarchiveWorkspace',
    group: 'Workspaces',
    method: 'POST',
    path: '/api/workspaces/{id}/unarchive',
    title: 'Unarchive workspace',
    desc: 'Reactivate an archived workspace. Fails with 409 project_archived while the parent project is archived. Idempotent.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Workspace id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'wsp_…', required: true }],
    returns: '{ "id": "wsp_…", "name": "…", … }',
    cli: { cmd: 'atc workspaces unarchive', example: 'atc workspaces unarchive wsp_8f3a2c' }
  },
  {
    key: 'deleteWorkspace',
    group: 'Workspaces',
    method: 'DELETE',
    path: '/api/workspaces/{id}',
    title: 'Delete workspace',
    desc: 'End the workspace’s active sessions, then remove the workspace and all of its session metadata in one transaction. An end failure aborts the delete (502); a session started concurrently fails it with 409 — retry. Files on disk are never touched.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Workspace id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'wsp_…', required: true }],
    returns: '{}',
    cli: { cmd: 'atc workspaces delete', example: 'atc workspaces delete wsp_8f3a2c' }
  },
  {
    key: 'workspaceSessions',
    group: 'Workspaces',
    method: 'GET',
    path: '/api/workspaces/{id}/sessions',
    title: 'List workspace sessions',
	desc: 'Return one workspace’s Live and Ended Sessions. Unknown workspaces are a 404.',
    params: [
      { name: 'id', type: 'string', required: true, desc: 'Workspace id (path).' },
	  { name: 'status', type: 'enum', desc: 'Optional live or ended filter.' }
    ],
    fields: [
      { key: 'id', label: 'id', kind: 'text', placeholder: 'wsp_…', required: true },
      {
        key: 'status',
        label: 'status',
        kind: 'select',
		options: ['(any)', 'live', 'ended']
      }
    ],
    returns: '{ "sessions": [ … ] }',
    cli: { cmd: 'atc sessions list', example: 'atc sessions list --workspace wsp_8f3a2c' }
  },
  {
    key: 'createProject',
    group: 'Projects',
    method: 'POST',
    path: '/api/projects',
    title: 'Create project',
    desc: 'Name a directory on this machine. Sessions started in the project inherit it as their working directory.',
    params: [
      { name: 'name', type: 'string', required: true, desc: 'Human label; renameable later.' },
      {
        name: 'workingDir',
        type: 'string',
        required: true,
        desc: 'Absolute path to an existing directory. Fixed after creation.'
      }
    ],
    fields: [
      { key: 'name', label: 'name', kind: 'text', placeholder: 'atc', required: true },
      {
        key: 'workingDir',
        label: 'workingDir',
        kind: 'text',
        placeholder: '/home/you/projects/atc',
        required: true
      }
    ],
    returns: '{\n  "id": "prj_8f3a2c",\n  "name": "atc",\n  "workingDir": "/home/you/projects/atc"\n}',
    cli: {
      cmd: 'atc projects create',
      example: 'atc projects create --name "atc" --dir ~/projects/atc'
    }
  },
  {
    key: 'listProjects',
    group: 'Projects',
    method: 'GET',
    path: '/api/projects',
    title: 'List projects',
    desc: 'Return all projects, newest first. Archived projects are hidden unless requested.',
    params: [{ name: 'includeArchived', type: 'bool', desc: 'Include archived projects.' }],
    fields: [
      { key: 'includeArchived', label: 'includeArchived', kind: 'select', options: ['false', 'true'] }
    ],
    returns: '{ "projects": [ … ] }',
    cli: { cmd: 'atc projects list', example: 'atc projects list' }
  },
  {
    key: 'readProject',
    group: 'Projects',
    method: 'GET',
    path: '/api/projects/{id}',
    title: 'Read project',
    desc: 'Fetch one project record.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Project id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'prj_…', required: true }],
    returns: '{ "id": "prj_…", "name": "atc", … }',
    cli: { cmd: 'atc projects show', example: 'atc projects show prj_8f3a2c' }
  },
  {
    key: 'renameProject',
    group: 'Projects',
    method: 'PATCH',
    path: '/api/projects/{id}',
    title: 'Rename project',
    desc: 'Change a project name. The body accepts only name — the working directory is fixed after creation.',
    params: [
      { name: 'id', type: 'string', required: true, desc: 'Project id (path).' },
      { name: 'name', type: 'string', required: true, desc: 'New name.' }
    ],
    fields: [
      { key: 'id', label: 'id', kind: 'text', placeholder: 'prj_…', required: true },
      { key: 'name', label: 'name', kind: 'text', placeholder: 'New name', required: true }
    ],
    returns: '{ "id": "prj_…", "name": "New name", … }',
    cli: { cmd: 'atc projects rename', example: 'atc projects rename prj_8f3a2c "New name"' }
  },
  {
    key: 'archiveProject',
    group: 'Projects',
    method: 'POST',
    path: '/api/projects/{id}/archive',
    title: 'Archive project',
    desc: 'Hide a project from default lists and block new workspace creation in it. Fails with 409 project_has_unarchived_workspaces while any of the project’s workspaces is unarchived. Idempotent.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Project id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'prj_…', required: true }],
    returns: '{ "id": "prj_…", "archivedAt": "…" }',
    cli: { cmd: 'atc projects archive', example: 'atc projects archive prj_8f3a2c' }
  },
  {
    key: 'deleteProject',
    group: 'Projects',
    method: 'DELETE',
    path: '/api/projects/{id}',
    title: 'Delete project',
    desc: 'Remove a project record. Allowed only when the project has zero workspaces (409 project_has_workspaces otherwise). Files on disk are never touched.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Project id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'prj_…', required: true }],
    returns: '{}',
    cli: { cmd: 'atc projects delete', example: 'atc projects delete prj_8f3a2c' }
  },
  {
    key: 'unarchiveProject',
    group: 'Projects',
    method: 'POST',
    path: '/api/projects/{id}/unarchive',
    title: 'Unarchive project',
    desc: 'Reactivate an archived project. Idempotent.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Project id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'prj_…', required: true }],
    returns: '{ "id": "prj_…", "name": "atc", … }',
    cli: { cmd: 'atc projects unarchive', example: 'atc projects unarchive prj_8f3a2c' }
  },
  {
    key: 'projectSessions',
    group: 'Projects',
    method: 'GET',
    path: '/api/projects/{id}/sessions',
    title: 'List project sessions',
	desc: 'Return one project’s Live and Ended Sessions. Unknown projects are a 404.',
    params: [
      { name: 'id', type: 'string', required: true, desc: 'Project id (path).' },
	  { name: 'status', type: 'enum', desc: 'Optional live or ended filter.' }
    ],
    fields: [
      { key: 'id', label: 'id', kind: 'text', placeholder: 'prj_…', required: true },
      {
        key: 'status',
        label: 'status',
        kind: 'select',
		options: ['(any)', 'live', 'ended']
      }
    ],
    returns: '{ "sessions": [ … ] }',
    cli: { cmd: 'atc sessions list', example: 'atc sessions list --project prj_8f3a2c' }
  },
  {
    key: 'listActions',
    group: 'Actions',
    method: 'GET',
    path: '/api/actions',
    title: 'List actions',
    desc: 'Return every configured action — built-ins, overrides, and custom ones — with its type ("action" or "agent"), origin, and enabled state.',
    params: [],
    fields: [],
    returns: '{ "actions": [ … ] }',
    cli: { cmd: 'atc actions list', example: 'atc actions list' }
  },
  {
    key: 'readAction',
    group: 'Actions',
    method: 'GET',
    path: '/api/actions/{name}',
    title: 'Read action',
    desc: 'Fetch one action’s full effective definition, including command and args, which the list omits.',
    params: [{ name: 'name', type: 'string', required: true, desc: 'Action name (path).' }],
    fields: [{ key: 'name', label: 'name', kind: 'text', placeholder: 'claude', required: true }],
    returns: '{ "name": "claude", "type": "agent", "command": "claude", … }'
  },
  {
    key: 'createAction',
    group: 'Actions',
    method: 'POST',
    path: '/api/actions',
    title: 'Create action',
    desc: 'Create a custom action. Name is optional — omitted, it is derived from the label. Type ("action" or "agent") defaults to "action" and is immutable afterwards.',
    params: [
      { name: 'name', type: 'string', desc: 'Action id; derived from label when omitted.' },
      { name: 'type', type: 'string', desc: '"action" or "agent". Defaults to "action"; immutable.' },
      { name: 'label', type: 'string', desc: 'Human label.' },
      { name: 'command', type: 'string', required: true, desc: 'Executable to launch.' },
      { name: 'args', type: 'array', desc: 'Fixed base arguments.' },
      { name: 'prompt', type: 'object', desc: 'Prompt spec; omit for no-prompt actions.' },
      { name: 'params', type: 'object', desc: 'Closed set of typed parameters.' },
      { name: 'enabled', type: 'bool', desc: 'Omitted means enabled.' }
    ],
    fields: [
      { key: 'label', label: 'label', kind: 'text', placeholder: 'My Agent', required: true },
      { key: 'command', label: 'command', kind: 'text', placeholder: 'my-agent', required: true }
    ],
    returns: '{ "name": "my-agent", "type": "action", "origin": "custom", … }'
  },
  {
    key: 'updateAction',
    group: 'Actions',
    method: 'PUT',
    path: '/api/actions/{name}',
    title: 'Update action',
    desc: 'Full replace of an action definition. Updating a built-in name creates an override. Type is immutable: a different type is rejected (400).',
    params: [
      { name: 'name', type: 'string', required: true, desc: 'Action name (path).' },
      { name: 'command', type: 'string', required: true, desc: 'Executable to launch.' }
    ],
    fields: [
      { key: 'name', label: 'name', kind: 'text', placeholder: 'my-agent', required: true },
      { key: 'command', label: 'command', kind: 'text', placeholder: 'my-agent', required: true }
    ],
    returns: '{ "name": "my-agent", "origin": "custom", … }'
  },
  {
    key: 'setActionEnabled',
    group: 'Actions',
    method: 'PUT',
    path: '/api/actions/{name}/enabled',
    title: 'Enable or disable action',
    desc: 'Toggle whether an action can launch sessions. Disabled actions stay visible in discovery.',
    params: [
      { name: 'name', type: 'string', required: true, desc: 'Action name (path).' },
      { name: 'enabled', type: 'bool', required: true, desc: 'New enabled state.' }
    ],
    fields: [
      { key: 'name', label: 'name', kind: 'text', placeholder: 'claude', required: true },
      { key: 'enabled', label: 'enabled', kind: 'select', options: ['true', 'false'], required: true }
    ],
    returns: '{ "name": "claude", "enabled": false, … }'
  },
  {
    key: 'deleteAction',
    group: 'Actions',
    method: 'DELETE',
    path: '/api/actions/{name}',
    title: 'Delete action',
	desc: 'Delete a custom action, or revert a modified built-in to its default. Deleting a custom action is rejected with 409 action_in_use while a provisional launch attempt or Live Session references it.',
    params: [{ name: 'name', type: 'string', required: true, desc: 'Action name (path).' }],
    fields: [{ key: 'name', label: 'name', kind: 'text', placeholder: 'my-agent', required: true }],
    returns: '{}'
  },
  {
    key: 'listEnvironments',
    group: 'Environments',
    method: 'GET',
    path: '/api/environments',
    title: 'List environments',
    desc: 'Return the configured launch environments sessions can run in.',
    params: [],
    fields: [],
    returns: '{ "environments": [ … ] }',
    cli: { cmd: 'atc environments list', example: 'atc environments list' }
  },
  {
    key: 'fsList',
    group: 'Filesystem',
    method: 'GET',
    path: '/api/fs/list',
    title: 'List directory',
    desc: 'Browse a directory on the host, for pickers that choose project working directories.',
    params: [
      { name: 'path', type: 'string', required: true, desc: 'Absolute directory path.' },
      { name: 'showHidden', type: 'bool', desc: 'Include dotfiles.' }
    ],
    fields: [
      { key: 'path', label: 'path', kind: 'text', placeholder: '/home/you', required: true },
      { key: 'showHidden', label: 'showHidden', kind: 'select', options: ['false', 'true'] }
    ],
    returns: '{ "path": "/home/you", "entries": [ … ] }'
  },
  {
    key: 'health',
    group: 'Diagnostics',
    method: 'GET',
    path: '/api/health',
    title: 'Health',
    desc: 'Liveness check for the atc service.',
    params: [],
    fields: [],
    returns: '{ "status": "ok" }',
    cli: { cmd: 'atc health', example: 'atc health' }
  },
  {
    key: 'version',
    group: 'Diagnostics',
    method: 'GET',
    path: '/api/version',
    title: 'Version',
    desc: 'Build version of the running service.',
    params: [],
    fields: [],
    returns: '{ "version": "0.1.0-dev" }'
  }
];

export function endpointByKey(key: string): Endpoint | undefined {
  return ENDPOINTS.find((endpoint) => endpoint.key === key);
}

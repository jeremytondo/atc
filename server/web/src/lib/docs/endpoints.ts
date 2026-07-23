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
    desc: 'Launch a persistent terminal in a workspace. The session runs in the workspace project’s working directory. Supply an action ID to run its fixed command and literal args, or omit actionId to launch the Interactive Shell. The launch is synchronous: the response is the new Live Session.',
    params: [
      {
        name: 'workspaceId',
        type: 'string',
        required: true,
        desc: 'Workspace to start the session in; the session inherits the project directory.'
      },
      {
        name: 'actionId',
        type: 'string',
        desc: 'Opaque ID of the action to run. Omitted, the server launches the Interactive Shell.'
      },
      { name: 'name', type: 'string', desc: 'Optional human label.' }
    ],
    fields: [
      { key: 'workspaceId', label: 'workspaceId', kind: 'text', placeholder: 'wsp_…', required: true },
      { key: 'actionId', label: 'actionId', kind: 'text', placeholder: 'act_… (optional)' },
      { key: 'name', label: 'name', kind: 'text', placeholder: 'optional' }
    ],
    returns:
      '{\n  "id": "ses_8f3a2c",\n  "actionId": "act_fh9g7e6571qo53r0t647ughtfg",\n  "actionName": "Codex",\n  "isAgent": true,\n  "status": "live"\n}',
    cli: {
      cmd: 'atc sessions start',
      example:
        'atc sessions start \\\n  --workspace wsp_8f3a2c \\\n  --action act_fh9g7e6571qo53r0t647ughtfg'
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
    desc: 'Fetch one session, including its copied launch-time action name and agent classification. Interactive Shell sessions omit actionId and actionName and return isAgent false.',
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
    key: 'renameSession',
    group: 'Sessions',
    method: 'PATCH',
    path: '/api/sessions/{id}',
    title: 'Rename session',
    desc: 'Change only a Session’s persisted display name. Live and Ended Sessions can both be renamed; names are trimmed, must not be blank, and need not be unique.',
    params: [
      { name: 'id', type: 'string', required: true, desc: 'Session id (path).' },
      { name: 'name', type: 'string', required: true, desc: 'New display name.' }
    ],
    fields: [
      { key: 'id', label: 'id', kind: 'text', placeholder: 'ses_…', required: true },
      { key: 'name', label: 'name', kind: 'text', placeholder: 'New name', required: true }
    ],
    returns: '{ "id": "ses_…", "name": "New name", "status": "live", … }',
    cli: { cmd: 'atc sessions rename', example: 'atc sessions rename ses_8f3a2c "New name"' }
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
    desc: 'Return workspaces, newest first. Omit projectId to list all workspaces across projects.',
    params: [{ name: 'projectId', type: 'string', desc: 'Restrict to one project’s workspaces.' }],
    fields: [{ key: 'projectId', label: 'projectId', kind: 'text', placeholder: 'prj_… (optional)' }],
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
    desc: 'Change a workspace name. The body accepts only name.',
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
    desc: 'Return all projects, newest first.',
    params: [],
    fields: [],
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
    desc: 'Return every server-wide action in stable name order. List and detail use the same complete shape, including command, literal args, enabled state, and agent classification.',
    params: [],
    fields: [],
    returns:
      '{ "actions": [{ "id": "act_…", "name": "Codex", "enabled": true, "command": "codex", "args": [], "isAgent": true }] }',
    cli: { cmd: 'atc actions list', example: 'atc actions list' }
  },
  {
    key: 'readAction',
    group: 'Actions',
    method: 'GET',
    path: '/api/actions/{id}',
    title: 'Read action',
    desc: 'Fetch one complete action by its opaque, immutable ID.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Action ID (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'act_…', required: true }],
    returns:
      '{ "id": "act_fh9g7e6571qo53r0t647ughtfg", "name": "Codex", "enabled": true, "command": "codex", "args": [], "isAgent": true }',
    cli: {
      cmd: 'atc actions show',
      example: 'atc actions show act_fh9g7e6571qo53r0t647ughtfg'
    }
  },
  {
    key: 'createAction',
    group: 'Actions',
    method: 'POST',
    path: '/api/actions',
    title: 'Create action',
    desc: 'Create a server-wide action. The server generates its immutable ID. enabled defaults to true, args to an empty array, and isAgent to false.',
    params: [
      { name: 'name', type: 'string', required: true, desc: 'Editable human-facing name; need not be unique.' },
      { name: 'description', type: 'string', desc: 'Optional description.' },
      { name: 'command', type: 'string', required: true, desc: 'Executable to launch.' },
      { name: 'args', type: 'array', desc: 'Ordered literal arguments. Defaults to [].' },
      { name: 'enabled', type: 'bool', desc: 'Whether new sessions may use the action. Defaults to true.' },
      { name: 'isAgent', type: 'bool', desc: 'Frontend classification hint. Defaults to false.' }
    ],
    fields: [
      { key: 'name', label: 'name', kind: 'text', placeholder: 'Neovim', required: true },
      { key: 'command', label: 'command', kind: 'text', placeholder: 'nvim', required: true }
    ],
    returns:
      '{ "id": "act_…", "name": "Neovim", "enabled": true, "command": "nvim", "args": [], "isAgent": false }',
    cli: {
      cmd: 'atc actions create',
      example: 'atc actions create --name "Neovim" --command nvim'
    }
  },
  {
    key: 'updateAction',
    group: 'Actions',
    method: 'PATCH',
    path: '/api/actions/{id}',
    title: 'Update action',
    desc: 'Update any action field except its ID. Omitted fields stay unchanged; description null clears the description. Existing sessions keep their copied launch identity.',
    params: [
      { name: 'id', type: 'string', required: true, desc: 'Action ID (path).' },
      { name: 'name', type: 'string', desc: 'New human-facing name.' },
      { name: 'description', type: 'string|null', desc: 'New description, or null to clear it.' },
      { name: 'command', type: 'string', desc: 'New executable.' },
      { name: 'args', type: 'array', desc: 'Replacement ordered literal arguments.' },
      { name: 'enabled', type: 'bool', desc: 'New enabled state.' },
      { name: 'isAgent', type: 'bool', desc: 'New agent classification.' }
    ],
    fields: [
      { key: 'id', label: 'id', kind: 'text', placeholder: 'act_…', required: true },
      { key: 'name', label: 'name', kind: 'text', placeholder: 'New action name' }
    ],
    returns: '{ "id": "act_…", "name": "New action name", … }',
    cli: {
      cmd: 'atc actions update',
      example: 'atc actions update act_123456789abcdefghijklmnopq --name "New action name"'
    }
  },
  {
    key: 'deleteAction',
    group: 'Actions',
    method: 'DELETE',
    path: '/api/actions/{id}',
    title: 'Delete action',
    desc: 'Permanently delete an action. Existing sessions and their copied launch identity are not affected.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Action ID (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'act_…', required: true }],
    returns: '{}',
    cli: {
      cmd: 'atc actions delete',
      example: 'atc actions delete act_123456789abcdefghijklmnopq'
    }
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

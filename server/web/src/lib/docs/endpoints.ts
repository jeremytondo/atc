// API reference content, ported from "Atelier Code Docs Console - Linear.dc.html".
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
  method: 'GET' | 'POST' | 'PATCH';
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
    desc: 'Launch a persistent terminal from an action, an environment, and a working directory. The launch is synchronous: the response is the new session, already running and attachable.',
    params: [
      { name: 'action', type: 'string', required: true, desc: 'Name of the action to run.' },
      {
        name: 'workingDir',
        type: 'string',
        desc: 'Directory the session starts in. Exactly one of workingDir and projectId is required.'
      },
      {
        name: 'projectId',
        type: 'string',
        desc: 'Project to start the session in; the session inherits the project directory.'
      },
      {
        name: 'environment',
        type: 'enum',
        desc: 'Name of one of the configured environments (see Environments). Defaults to host-login-shell.'
      },
      { name: 'params', type: 'object', desc: 'Action-specific parameter values.' },
      { name: 'prompt', type: 'string', desc: 'Initial prompt for actions that declare prompt support.' },
      { name: 'name', type: 'string', desc: 'Optional human label.' }
    ],
    fields: [
      { key: 'action', label: 'action', kind: 'select', options: ['codex', 'claude'], required: true },
      { key: 'environment', label: 'environment', kind: 'select', options: ['host-login-shell'] },
      { key: 'workingDir', label: 'workingDir', kind: 'text', placeholder: '~/project', required: true },
      { key: 'name', label: 'name', kind: 'text', placeholder: 'optional' },
      { key: 'prompt', label: 'prompt', kind: 'textarea', placeholder: 'optional starting prompt…' }
    ],
    returns: '{\n  "id": "ses_8f3a2c",\n  "status": "running",\n  "action": "codex",\n  "attachable": true\n}',
    cli: {
      cmd: 'atc sessions start',
      example: 'atc sessions start \\\n  --action codex --env host-login-shell --dir .'
    }
  },
  {
    key: 'list',
    group: 'Sessions',
    method: 'GET',
    path: '/api/sessions',
    title: 'List sessions',
    desc: 'Return all unarchived sessions. Filter by status or include archived ones with query params.',
    params: [
      { name: 'includeArchived', type: 'bool', desc: 'Include archived sessions.' },
      { name: 'status', type: 'string', desc: 'Filter by status.' }
    ],
    fields: [
      { key: 'includeArchived', label: 'includeArchived', kind: 'select', options: ['false', 'true'] },
      {
        key: 'status',
        label: 'status',
        kind: 'select',
        options: ['(any)', 'running', 'starting', 'failed', 'terminated']
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
    returns: '{ "id": "ses_…", "status": "running", … }',
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
    key: 'terminate',
    group: 'Sessions',
    method: 'POST',
    path: '/api/sessions/{id}/terminate',
    title: 'Terminate session',
    desc: 'Stop a running session. It then becomes archivable.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Session id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'ses_…', required: true }],
    returns: '{ "id": "ses_…", "status": "terminated" }',
    cli: { cmd: 'atc sessions terminate', example: 'atc sessions terminate ses_8f3a2c' }
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
      { key: 'name', label: 'name', kind: 'text', placeholder: 'Atelier Code', required: true },
      {
        key: 'workingDir',
        label: 'workingDir',
        kind: 'text',
        placeholder: '/home/you/projects/atelier-code',
        required: true
      }
    ],
    returns: '{\n  "id": "prj_8f3a2c",\n  "name": "Atelier Code",\n  "workingDir": "/home/you/projects/atelier-code"\n}',
    cli: {
      cmd: 'atc projects create',
      example: 'atc projects create --name "Atelier Code" --dir ~/projects/atelier-code'
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
    returns: '{ "id": "prj_…", "name": "Atelier Code", … }',
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
    desc: 'Hide a project from default lists and block new session starts in it. Fails with 409 project_has_active_sessions while the project has a starting or running session. Idempotent.',
    params: [{ name: 'id', type: 'string', required: true, desc: 'Project id (path).' }],
    fields: [{ key: 'id', label: 'id', kind: 'text', placeholder: 'prj_…', required: true }],
    returns: '{ "id": "prj_…", "archivedAt": "…" }',
    cli: { cmd: 'atc projects archive', example: 'atc projects archive prj_8f3a2c' }
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
    returns: '{ "id": "prj_…", "name": "Atelier Code", … }',
    cli: { cmd: 'atc projects unarchive', example: 'atc projects unarchive prj_8f3a2c' }
  },
  {
    key: 'projectSessions',
    group: 'Projects',
    method: 'GET',
    path: '/api/projects/{id}/sessions',
    title: 'List project sessions',
    desc: 'Return one project’s sessions, same shape and filters as the sessions list. Unknown projects are a 404.',
    params: [
      { name: 'id', type: 'string', required: true, desc: 'Project id (path).' },
      { name: 'includeArchived', type: 'bool', desc: 'Include archived sessions.' },
      { name: 'status', type: 'string', desc: 'Filter by status.' }
    ],
    fields: [
      { key: 'id', label: 'id', kind: 'text', placeholder: 'prj_…', required: true },
      { key: 'includeArchived', label: 'includeArchived', kind: 'select', options: ['false', 'true'] },
      {
        key: 'status',
        label: 'status',
        kind: 'select',
        options: ['(any)', 'running', 'starting', 'failed', 'terminated']
      }
    ],
    returns: '{ "sessions": [ … ] }',
    cli: { cmd: 'atc sessions list', example: 'atc sessions list --project prj_8f3a2c' }
  },
  {
    key: 'health',
    group: 'Diagnostics',
    method: 'GET',
    path: '/api/health',
    title: 'Health',
    desc: 'Liveness check for the Atelier Code service.',
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

# ATC-238 findings

Status: deterministic prototype and race-tested API workflow passing

## Proposed minimum model

ATC should normalize durable things that clients need to inventory, inspect,
or act on. The initial useful kinds are coding/agent sessions and terminal
sessions. Turns, messages, permission requests, terminal bytes, and provider
tool calls should not all become generic resources; they need typed APIs owned
by their domains when ATC supports them.

Every normalized resource needs only:

- one opaque ATC ID plus the source plugin and its native ID;
- a kind and human title;
- an owner, independently from ATC's control relationship;
- a control mode and the actions available in the resource's current state;
- normalized lifecycle status;
- links for attachment or opening the native tool; and
- optional plugin-namespaced extension data.

The prototype derives the stable ATC ID from plugin ID and an encoded native
ID. A production system can instead persist an assigned ID, but must preserve
the same two-part source identity and reject a native identity moving between
plugins.

### Capabilities and control

A plugin descriptor advertises a finite ATC capability vocabulary per resource
kind: `discover`, `create`, `control`, `respond`, `cancel`, `attach`, and
`open_external`. Registration fails when an implementation advertises an
operation whose narrow interface it does not implement. A resource's `actions`
is the dynamic subset currently legal; for example, a cancelled agent session
no longer advertises `cancel` or `respond`.

Read-only is not ownership and should not be a single `controllable` boolean.
The prototype uses three control modes:

- `observed`: ATC can inspect the resource but cannot mutate it;
- `delegated`: an external owner retains authority but permits specific ATC
  actions; and
- `managed`: ATC owns the lifecycle it asks the plugin to provide.

The exact `actions` list remains authoritative. This represents an externally
owned terminal that ATC may attach to without pretending ATC owns it, while an
external editor thread remains observed and can only be opened in its native
tool.

### Lifecycle

Status is a product of three independent dimensions instead of one flattened
enum:

- `phase`: `starting`, `active`, `ended`, `unavailable`, or `unknown`;
- `activity`: `idle`, `working`, `needs_input`, or `unknown`; and
- terminal `outcome`: `succeeded`, `failed`, or `cancelled`.

`detail` retains a short native distinction for people without changing core
logic. Plugin-specific raw state belongs in the plugin's extension namespace.
The core rejects impossible projections such as an outcome on an active
resource or an ended resource without an outcome.

Plugins feed protocol or polling observations through one normalized update
seam. The core owns ordered event sequence numbers and snapshots. The demo
proves `idle -> needs_input -> working -> ended/cancelled`, including an
unsolicited fake-provider update and cursor-based event catch-up.

## Plugin boundary

The core owns registration validation, canonical IDs, snapshot storage,
capability enforcement, lifecycle validation, event ordering, and HTTP
projection. A plugin owns discovery, native identity, protocol transport,
status reduction, and execution of the capabilities it advertises.

There is deliberately no generic `execute(pluginPayload)` interface. Each
capability has a narrow Go interface, and the HTTP layer accepts only the small
canonical input for that capability. Extension data is an inspection escape
hatch; core policy and generic clients must not depend on it.

This boundary fits heterogeneous integrations behind one inventory and coarse
orchestration API. It should not be stretched into one universal domain API.
Rich agent conversation, approval, and terminal lifecycle behavior still
deserve typed ATC resources and operations behind the same plugin registration
model.

## What the prototype establishes

- Capability discovery works at plugin-kind level while current availability
  remains resource-specific.
- Managed, delegated, and observed resources coexist in one list and inspection
  representation without conflating ownership with control.
- Both plugin-originated observations and client actions produce the same
  normalized lifecycle snapshots and ordered events.
- Static claims, dynamic actions, status combinations, resource kinds, and
  extension namespaces can be checked centrally before a plugin result enters
  ATC state.
- A read-only external resource and an ATC-managed agent can use the same API
  for discovery and inspection while retaining distinct behavior.
- The first sign of excessive genericity is action input: `respond` text and a
  terminal `control` command already differ. Keeping typed capability methods
  avoids pushing arbitrary plugin JSON into the core.

## Deliberate prototype omissions

This experiment is in-process and in-memory. It has no persistence,
authentication, plugin installation, process isolation, version negotiation,
streaming transport, retries, timeouts, backpressure, or removal
reconciliation. `open_external` and `attach` return links but do not invoke
them. Discovery is additive, and plugin updates are submitted directly rather
than through a supervised event loop.

## Assumptions for later integration spikes

1. Native IDs are durable and are not silently reused for different resources.
2. Real plugins can map their evidence into the lifecycle dimensions without
   inventing certainty; missing evidence must map to `unknown` or
   `unavailable`.
3. Discovery can define authoritative disappearance, stale, and tombstone
   semantics without losing durable ATC identity.
4. Capability inputs and results can be versioned as a small ATC vocabulary;
   integrations needing richer operations will add typed domain APIs rather
   than opaque payloads.
5. A process boundary can preserve capability validation while adding plugin
   handshake, health, crash isolation, deadlines, and permission policy.
6. Event delivery can resume from a durable cursor and reconcile snapshots
   after missed or duplicated native events.
7. Ownership and delegated control can be authorized independently, especially
   for cancel, terminal control, external links, and remote clients.
8. Integration-specific metadata can remain optional for generic clients and
   can be redacted before crossing trust boundaries.

The next useful spike is not a larger generic core. It is one real ACP-backed
managed resource and one real discovered/observed resource using this boundary,
with special attention to identity stability, plugin failure, lifecycle
ambiguity, and reconciliation.

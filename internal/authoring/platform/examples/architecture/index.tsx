// Example: an architecture explanation. Sections with navigation, a
// diagram drawn in SVG, callouts, a code block, and a collapsible
// appendix. Everything is static content; the reader's interactions are
// navigation and disclosure.
import { Callout, CodeBlock, Collapsible, CollapsibleContent, CollapsibleTrigger, Document, Figure, KeyValue, Section } from "@/platform";

const boxes = [
  { id: "cli", label: "CLI / clients", x: 20, y: 40 },
  { id: "api", label: "ATC API", x: 220, y: 40 },
  { id: "core", label: "Domains", x: 420, y: 40 },
  { id: "int", label: "Integrations", x: 620, y: 40 },
];

export default function Page() {
  return (
    <Document summary="A walkthrough of the artifact pipeline: what an agent publishes, what the server stores, and what a reader receives.">
      <Section id="shape" title="The shape of the system">
        <p>
          ATC keeps one authenticated API for every client and a separate, unprivileged document origin for browsers. The two never share a port, so a page served from the document origin cannot reach the API with the reader's credentials — it has none.
        </p>
        <Figure caption="Request flow: clients publish through the API; readers fetch static content from the document origin.">
          <svg viewBox="0 0 800 180" className="figure w-full" role="img" aria-label="Boxes for clients, API, domains, and integrations connected by arrows, with a document origin below the domains.">
            <defs>
              <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="8" markerHeight="8" orient="auto-start-reverse">
                <path d="M 0 0 L 10 5 L 0 10 z" fill="currentColor" />
              </marker>
            </defs>
            {boxes.map((b) => (
              <g key={b.id}>
                <rect x={b.x} y={b.y} width={160} height={48} rx={8} className="fill-card stroke-border" strokeWidth={1.5} />
                <text x={b.x + 80} y={b.y + 29} textAnchor="middle" className="fill-foreground text-[14px] font-medium">
                  {b.label}
                </text>
              </g>
            ))}
            {[0, 1, 2].map((i) => (
              <line key={i} x1={boxes[i].x + 160} y1={64} x2={boxes[i + 1].x} y2={64} className="stroke-muted-foreground" strokeWidth={1.5} markerEnd="url(#arrow)" />
            ))}
            <rect x={420} y={120} width={160} height={44} rx={8} className="fill-primary/10 stroke-primary" strokeWidth={1.5} />
            <text x={500} y={147} textAnchor="middle" className="fill-foreground text-[14px] font-medium">
              Document origin
            </text>
            <line x1={500} y1={88} x2={500} y2={120} className="stroke-primary" strokeWidth={1.5} markerEnd="url(#arrow)" />
            <line x1={100} y1={88} x2={100} y2={142} className="stroke-muted-foreground" strokeWidth={1.5} strokeDasharray="4 4" />
            <line x1={100} y1={142} x2={420} y2={142} className="stroke-muted-foreground" strokeWidth={1.5} strokeDasharray="4 4" markerEnd="url(#arrow)" />
            <text x={250} y={136} textAnchor="middle" className="fill-muted-foreground text-[12px]">
              readers (no token)
            </text>
          </svg>
        </Figure>
      </Section>

      <Section id="publish" title="Publishing">
        <p>
          A publication is one multipart request: the static build, the authoring snapshot, and a small JSON document naming the title, the base version, and a retry identity. The server validates both archives before anything becomes visible.
        </p>
        <KeyValue
          items={[
            { label: "Build", value: "gzip tar with index.html at its root; regular files only" },
            { label: "Source", value: "gzip tar of the document, the platform it used, configuration, manifest, lockfile" },
            { label: "Base version", value: "must be the current version, or the request fails with a conflict" },
            { label: "Publication id", value: "a repeat returns the same result instead of a new version" },
          ]}
        />
        <Callout kind="note" title="Why the base version matters">
          Two authors can hold working copies of the same artifact. Naming the version each started from is what lets the server refuse the second publication instead of silently replacing the first.
        </Callout>
        <CodeBlock
          title="publish (abridged)"
          language="go"
          code={`staged := stage(build, source)      // validated, outside the lock
lock()
if current != base { return StaleBase(current) }
rename(staged, versionDir(id, current+1))
appendVersion(tx, id, current+1)   // the row makes it visible
unlock()`}
        />
      </Section>

      <Section id="serve" title="Serving">
        <p>
          The document origin resolves the main link to the current version at request time and pins every asset reference in the page to that version's permanent path. A version's assets are immutable and cached for a year; pages are never cached.
        </p>
        <Section id="serve-metadata" title="Reader metadata" level={3}>
          <p>
            The current title and the whole history are injected into the page as a JSON script. The build's own scripts and styles stay frozen; only what the reader header shows is live.
          </p>
        </Section>
        <Section id="serve-policy" title="Response policy" level={3}>
          <p>
            A content security policy restricts the page to its own published assets: no network requests, no workers, no embedding, no form submission. Inline styles are allowed because the platform's component library sets them.
          </p>
        </Section>
      </Section>

      <Section id="appendix" title="Appendix">
        <Collapsible>
          <CollapsibleTrigger>Storage layout</CollapsibleTrigger>
          <CollapsibleContent>
            <CodeBlock
              title="~/.local/share/atc/artifacts"
              language="bash"
              code={`artifacts/
  artf-k2m4p/
    1/build/index.html
    1/build/assets/...
    1/source.tar.gz
    2/...
  .staging/          # interrupted publications, removed on start`}
            />
          </CollapsibleContent>
        </Collapsible>
      </Section>
    </Document>
  );
}

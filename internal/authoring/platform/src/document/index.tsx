// The document. Export a default component; everything under src/document
// is yours. Import shared components from "@/platform".
import { Callout, Document, Section } from "@/platform";

export default function Page() {
  return (
    <Document summary="Replace this with a one-paragraph summary of what the document explains.">
      <Section id="overview" title="Overview">
        <p>Start writing here. The heading above is the title the document is published under; sections become the navigation on the left.</p>
        <Callout kind="tip" title="Shared components">
          Tables, code blocks, callouts, tabs, and collapsible sections are available from <code>@/platform</code>. For a worked example, start a copy with <code>atc artifact new --example architecture</code> (or comparison, or demo).
        </Callout>
      </Section>
    </Document>
  );
}

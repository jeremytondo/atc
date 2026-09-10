// Example: a research comparison. Stat tiles, a filterable and sortable
// table, tabs for per-option detail, and a recommendation. The filter and
// sort are reader interactions over the published data; they reset on
// reload, as every reader interaction does.
import * as React from "react";
import { Badge, Callout, Document, Section, Stat, Table, TableBody, TableCell, TableHead, TableHeader, TableRow, Tabs, TabsContent, TabsList, TabsTrigger, Columns, cn } from "@/platform";
import { options, type Option } from "./data";

type Column = "name" | "bundleKb" | "latencyMs" | "score";

export default function Page() {
  return (
    <Document summary="Five candidates evaluated on bundle size, render latency, license, and overall fit for the reader platform. Data gathered on 2026-09-08 against the prototype build.">
      <Section id="summary" title="Summary">
        <Columns count={3}>
          <Stat label="Candidates" value={options.length} detail="3 libraries, 2 hosted services" />
          <Stat label="Recommended" value="Alpha" detail="score 4.5 / 5" />
          <Stat label="Smallest bundle" value="5 kB" detail="Ember (a service; the work happens remotely)" />
        </Columns>
        <Callout kind="success" title="Recommendation">
          Adopt <strong>Alpha</strong>. It is the smallest self-contained option, its latency is within budget, and its license is compatible with redistribution inside published documents.
        </Callout>
      </Section>

      <Section id="matrix" title="Comparison matrix">
        <p>Filter by kind and sort any numeric column. Services are shown for completeness; they cannot run inside a published document, which has no network access.</p>
        <Matrix />
      </Section>

      <Section id="detail" title="Per-option notes">
        <Tabs defaultValue={options[0].name}>
          <TabsList>
            {options.map((o) => (
              <TabsTrigger key={o.name} value={o.name}>
                {o.name}
              </TabsTrigger>
            ))}
          </TabsList>
          {options.map((o) => (
            <TabsContent key={o.name} value={o.name}>
              <div className="not-prose rounded-lg border p-4 text-sm">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{o.name}</span>
                  <Badge variant={o.kind === "library" ? "secondary" : "outline"}>{o.kind}</Badge>
                  <Badge variant={o.license === "MIT" || o.license === "Apache-2.0" ? "success" : "warning"}>{o.license}</Badge>
                </div>
                <p className="mt-2 text-muted-foreground">{o.notes}</p>
              </div>
            </TabsContent>
          ))}
        </Tabs>
      </Section>

      <Section id="method" title="Method">
        <p>Bundle sizes are gzip-compressed production output of a one-component page. Latency is the median of 50 renders of a 200-row table on the reference laptop. Scores weigh size 30%, latency 30%, license 20%, and maintenance signals 20%.</p>
      </Section>
    </Document>
  );
}

function Matrix() {
  const [kind, setKind] = React.useState<"all" | Option["kind"]>("all");
  const [sort, setSort] = React.useState<{ column: Column; descending: boolean }>({ column: "score", descending: true });
  const rows = options
    .filter((o) => kind === "all" || o.kind === kind)
    .sort((a, b) => {
      const direction = sort.descending ? -1 : 1;
      const av = a[sort.column];
      const bv = b[sort.column];
      return (typeof av === "number" && typeof bv === "number" ? av - bv : String(av).localeCompare(String(bv))) * direction;
    });
  const toggle = (column: Column) => setSort((s) => ({ column, descending: s.column === column ? !s.descending : column !== "name" }));
  const header = (column: Column, label: string) => (
    <TableHead>
      <button type="button" onClick={() => toggle(column)} className={cn("inline-flex items-center gap-1 hover:text-foreground", sort.column === column && "text-foreground")}>
        {label}
        {sort.column === column && <span aria-hidden="true">{sort.descending ? "↓" : "↑"}</span>}
      </button>
    </TableHead>
  );
  return (
    <div className="not-prose space-y-3">
      <div className="flex items-center gap-2 text-sm">
        <span className="text-muted-foreground">Show</span>
        {(["all", "library", "service"] as const).map((k) => (
          <button key={k} type="button" onClick={() => setKind(k)} className={cn("rounded-md border px-2.5 py-1", kind === k ? "bg-primary text-primary-foreground border-transparent" : "hover:bg-accent")}>
            {k}
          </button>
        ))}
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            {header("name", "Option")}
            <TableHead>Kind</TableHead>
            {header("bundleKb", "Bundle (kB)")}
            {header("latencyMs", "Latency (ms)")}
            <TableHead>License</TableHead>
            {header("score", "Score")}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((o) => (
            <TableRow key={o.name}>
              <TableCell className="font-medium">{o.name}</TableCell>
              <TableCell>{o.kind}</TableCell>
              <TableCell className="tabular-nums">{o.bundleKb}</TableCell>
              <TableCell className="tabular-nums">{o.latencyMs}</TableCell>
              <TableCell>{o.license}</TableCell>
              <TableCell className="tabular-nums">{o.score.toFixed(1)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

// Example: an interactive demonstration. A small simulation with controls
// and a live SVG chart, plus a step-through explanation. All state is
// local to the page and resets on reload.
import * as React from "react";
import { Button, Callout, Card, CardContent, CardDescription, CardHeader, CardTitle, Collapsible, CollapsibleContent, CollapsibleTrigger, Document, Section, Stat, Columns } from "@/platform";

/** Exponential backoff with full jitter on the upper half, as ATC's
 *  supervisors use it: never below base/2, capped at max. */
function schedule(base: number, max: number, attempts: number, random: () => number): number[] {
  const delays: number[] = [];
  let delay = Math.min(base, max);
  for (let i = 0; i < attempts; i++) {
    delays.push(delay / 2 + random() * (delay / 2));
    delay = Math.min(delay * 2, max);
  }
  return delays;
}

/** A tiny deterministic generator so "randomize" is reproducible per seed. */
function mulberry32(seed: number) {
  return () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export default function Page() {
  const [base, setBase] = React.useState(500);
  const [max, setMax] = React.useState(30000);
  const [attempts, setAttempts] = React.useState(10);
  const [seed, setSeed] = React.useState(1);
  const delays = React.useMemo(() => schedule(base, max, attempts, mulberry32(seed)), [base, max, attempts, seed]);
  const total = delays.reduce((a, b) => a + b, 0);
  return (
    <Document summary="How jittered exponential backoff spreads retries out. Adjust the parameters and watch the schedule change.">
      <Section id="controls" title="Try it">
        <Columns count={3}>
          <Stat label="Attempts" value={attempts} detail={`base ${base} ms, cap ${(max / 1000).toFixed(0)} s`} />
          <Stat label="Total wait" value={`${(total / 1000).toFixed(1)} s`} detail="sum of all delays" />
          <Stat label="Longest delay" value={`${(Math.max(...delays) / 1000).toFixed(1)} s`} detail="never above the cap" />
        </Columns>
        <Card className="not-prose">
          <CardHeader>
            <CardTitle>Parameters</CardTitle>
            <CardDescription>Changes apply immediately; reload the page to reset.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-4 md:grid-cols-3">
            <Slider label="Base delay" value={base} min={100} max={5000} step={100} unit="ms" onChange={setBase} />
            <Slider label="Maximum delay" value={max} min={1000} max={60000} step={1000} unit="ms" onChange={setMax} />
            <Slider label="Attempts" value={attempts} min={1} max={20} step={1} onChange={setAttempts} />
            <div className="md:col-span-3">
              <Button variant="secondary" size="sm" onClick={() => setSeed((s) => s + 1)}>
                Re-roll the jitter
              </Button>
            </div>
          </CardContent>
        </Card>
        <Chart delays={delays} base={base} max={max} />
      </Section>

      <Section id="how" title="How it works">
        <p>
          Each failed attempt doubles the delay up to the cap. Before sleeping, the delay is jittered across its upper half: a random value between half the delay and the whole of it. That keeps the floor predictable while stopping a fleet of clients from retrying in lockstep.
        </p>
        <Callout kind="tip" title="Read the chart">
          Bars are the actual delay; the faint line is the un-jittered doubling curve. Every bar sits between half the line and the line itself.
        </Callout>
        <Collapsible>
          <CollapsibleTrigger>Why the upper half rather than full jitter?</CollapsibleTrigger>
          <CollapsibleContent>
            <p>
              Full jitter (anywhere from zero to the delay) can retry almost immediately after a long wait was intended, which defeats the purpose of backing off for a struggling dependency. Halving the range trades a little spread for a guaranteed minimum.
            </p>
          </CollapsibleContent>
        </Collapsible>
      </Section>
    </Document>
  );
}

function Slider({ label, value, min, max, step, unit, onChange }: { label: string; value: number; min: number; max: number; step: number; unit?: string; onChange: (v: number) => void }) {
  const id = React.useId();
  return (
    <label htmlFor={id} className="block text-sm">
      <span className="flex justify-between">
        <span>{label}</span>
        <span className="tabular-nums text-muted-foreground">
          {value}
          {unit ? ` ${unit}` : ""}
        </span>
      </span>
      <input id={id} type="range" min={min} max={max} step={step} value={value} onChange={(e) => onChange(Number(e.target.value))} className="mt-1 w-full accent-primary" />
    </label>
  );
}

function Chart({ delays, base, max }: { delays: number[]; base: number; max: number }) {
  const width = 720;
  const height = 220;
  const pad = { left: 44, right: 12, top: 12, bottom: 28 };
  const plotW = width - pad.left - pad.right;
  const plotH = height - pad.top - pad.bottom;
  const barW = plotW / delays.length;
  const y = (ms: number) => pad.top + plotH - (ms / max) * plotH;
  let curve = Math.min(base, max);
  const curvePoints = delays.map((_, i) => {
    const point = `${pad.left + barW * (i + 0.5)},${y(Math.min(curve, max))}`;
    curve = Math.min(curve * 2, max);
    return point;
  });
  return (
    <figure className="not-prose overflow-x-auto rounded-lg border bg-card p-3">
      <svg viewBox={`0 0 ${width} ${height}`} className="w-full" role="img" aria-label="Bar chart of retry delays by attempt">
        {[0, 0.5, 1].map((f) => (
          <g key={f}>
            <line x1={pad.left} x2={width - pad.right} y1={y(max * f)} y2={y(max * f)} className="stroke-border" />
            <text x={pad.left - 6} y={y(max * f) + 4} textAnchor="end" className="fill-muted-foreground text-[11px]">
              {((max * f) / 1000).toFixed(0)}s
            </text>
          </g>
        ))}
        <polyline points={curvePoints.join(" ")} fill="none" className="stroke-muted-foreground/50" strokeDasharray="3 3" />
        {delays.map((d, i) => (
          <g key={i}>
            <rect x={pad.left + barW * i + 3} y={y(d)} width={barW - 6} height={pad.top + plotH - y(d)} rx={2} className="fill-primary/70" />
            <text x={pad.left + barW * (i + 0.5)} y={height - 10} textAnchor="middle" className="fill-muted-foreground text-[11px]">
              {i + 1}
            </text>
          </g>
        ))}
      </svg>
      <figcaption className="mt-2 text-xs text-muted-foreground">Delay before each attempt. Bars are jittered; the dashed line is the pure doubling schedule.</figcaption>
    </figure>
  );
}

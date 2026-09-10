export interface Option {
  name: string;
  kind: "library" | "service";
  bundleKb: number;
  latencyMs: number;
  license: string;
  score: number; // 0-5
  notes: string;
}

export const options: Option[] = [
  { name: "Alpha", kind: "library", bundleKb: 42, latencyMs: 12, license: "MIT", score: 4.5, notes: "Small, well documented, no server component." },
  { name: "Beacon", kind: "service", bundleKb: 8, latencyMs: 95, license: "Proprietary", score: 3, notes: "Hosted; adds a network dependency to every render." },
  { name: "Cobalt", kind: "library", bundleKb: 130, latencyMs: 20, license: "Apache-2.0", score: 3.5, notes: "Feature-rich, heavy; tree-shakes poorly." },
  { name: "Delta", kind: "library", bundleKb: 61, latencyMs: 15, license: "MIT", score: 4, notes: "Close second; smaller community." },
  { name: "Ember", kind: "service", bundleKb: 5, latencyMs: 140, license: "BSL", score: 2, notes: "License restricts redistribution." },
];

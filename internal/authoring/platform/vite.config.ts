// The fixed build configuration of the ATC artifact platform. Relative
// asset references (base "./") let the document origin pin every asset to
// the version's permanent path at serve time; dependencies resolve through
// the working copy's node_modules link into the shared installation.
import { realpathSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

const root = realpathSync(fileURLToPath(new URL(".", import.meta.url)));
const modules = realpathSync(new URL("./node_modules", import.meta.url));

export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "./",
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  build: { outDir: "dist", emptyOutDir: true },
  server: { fs: { allow: [root, modules] }, host: "127.0.0.1" },
  envPrefix: "VITE_",
});

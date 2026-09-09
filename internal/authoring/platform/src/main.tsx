// Entry point: the reader shell around the document. Authors do not edit
// this file; the document lives in src/document.
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/platform/index.css";
import { Reader } from "@/platform/reader/Reader";
import Document from "@/document";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Reader>
      <Document />
    </Reader>
  </StrictMode>,
);

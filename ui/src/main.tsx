import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { loadConfig } from "./config";
import App from "./App";
import "./index.css";

async function main() {
  await loadConfig();
  const root = document.getElementById("root");
  if (!root) throw new Error("No #root element");
  createRoot(root).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
}

void main();

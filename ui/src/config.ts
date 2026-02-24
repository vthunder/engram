interface Config {
  apiUrl: string;
  apiKey: string;
}

let config: Config | null = null;

export async function loadConfig(): Promise<void> {
  try {
    const res = await fetch("/config.json");
    if (!res.ok) throw new Error("Failed to load config.json");
    config = (await res.json()) as Config;
  } catch {
    config = { apiUrl: "", apiKey: "" };
  }
}

export function getConfig(): Config {
  if (!config) throw new Error("Config not loaded — call loadConfig() first");
  return config;
}

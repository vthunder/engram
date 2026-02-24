import { formatDistanceToNow, format } from "date-fns";

export function relativeTime(dateStr: string | undefined): string {
  if (!dateStr) return "—";
  try {
    const d = new Date(dateStr);
    return formatDistanceToNow(d, { addSuffix: true });
  } catch {
    return "—";
  }
}

export function absoluteTime(dateStr: string | undefined): string {
  if (!dateStr) return "—";
  try {
    return format(new Date(dateStr), "MMM d, yyyy HH:mm");
  } catch {
    return "—";
  }
}

export function shortId(id: string): string {
  return id.slice(0, 8);
}

export function truncate(text: string, maxLength: number): string {
  if (text.length <= maxLength) return text;
  return text.slice(0, maxLength) + "…";
}

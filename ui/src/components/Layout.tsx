import { NavLink, Outlet } from "react-router";
import {
  LayoutDashboard,
  Brain,
  FileText,
  Users,
  Layers,
  Search,
  Network,
  Settings,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useHealth } from "@/hooks/useHealth";

const navItems = [
  { to: "/", label: "Dashboard", icon: LayoutDashboard, end: true },
  { to: "/engrams", label: "Engrams", icon: Brain },
  { to: "/episodes", label: "Episodes", icon: FileText },
  { to: "/entities", label: "Entities", icon: Users },
  { to: "/schemas", label: "Schemas", icon: Layers },
  { to: "/search", label: "Search", icon: Search },
  { to: "/graph", label: "Graph", icon: Network },
  { to: "/admin", label: "Admin", icon: Settings },
];

export default function Layout() {
  const { data: health, isError } = useHealth();

  return (
    <div className="flex h-screen overflow-hidden">
      {/* Sidebar */}
      <aside className="flex w-60 flex-col border-r bg-card">
        {/* Brand */}
        <div className="flex items-center gap-2 px-4 py-5">
          <Brain className="h-6 w-6 text-primary" />
          <span className="text-lg font-semibold tracking-tight">Engram</span>
          {/* Health dot */}
          <span
            className={cn(
              "ml-auto h-2 w-2 rounded-full",
              isError
                ? "bg-destructive"
                : health
                  ? "bg-emerald-500"
                  : "bg-muted",
            )}
            title={isError ? "API unreachable" : health ? "API healthy" : "Checking…"}
          />
        </div>

        {/* Nav */}
        <nav className="flex-1 space-y-0.5 px-2 py-2">
          {navItems.map(({ to, label, icon: Icon, end }) => (
            <NavLink
              key={to}
              to={to}
              end={end}
              className={({ isActive }) =>
                cn(
                  "flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors",
                  isActive
                    ? "bg-primary text-primary-foreground"
                    : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
                )
              }
            >
              <Icon className="h-4 w-4" />
              {label}
            </NavLink>
          ))}
        </nav>
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto">
        <Outlet />
      </main>
    </div>
  );
}

import { createBrowserRouter } from "react-router";
import Layout from "@/components/Layout";
import Dashboard from "@/pages/Dashboard";
import Engrams from "@/pages/Engrams";
import Episodes from "@/pages/Episodes";
import Entities from "@/pages/Entities";
import Search from "@/pages/Search";
import Graph from "@/pages/Graph";
import Admin from "@/pages/Admin";

export const router = createBrowserRouter([
  {
    path: "/",
    element: <Layout />,
    children: [
      { index: true, element: <Dashboard /> },
      { path: "engrams", element: <Engrams /> },
      { path: "episodes", element: <Episodes /> },
      { path: "entities", element: <Entities /> },
      { path: "search", element: <Search /> },
      { path: "graph", element: <Graph /> },
      { path: "admin", element: <Admin /> },
    ],
  },
]);

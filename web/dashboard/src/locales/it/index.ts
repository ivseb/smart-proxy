import { common } from "./common";
import { routes } from "./routes";
import { routeForm } from "./routeForm";
import { routeDetail } from "./routeDetail";
import { features } from "./features";

// Italian texts, by the English text they translate. Split by area of the dashboard.
export const it: Record<string, string> = { ...common, ...routes, ...routeForm, ...routeDetail, ...features };

// vite empties the output folder; restore the placeholder that keeps the Go
// embed valid in a fresh checkout.
import { writeFileSync } from "node:fs";

writeFileSync(new URL("../../internal/analytics/web/dist/.gitkeep", import.meta.url), "");

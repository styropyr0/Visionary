import { createContext, useContext } from "react";
import type { Meta } from "./api";

/** The server's /api/meta, loaded once by App for every page. */
export const MetaContext = createContext<Meta | undefined>(undefined);

export function useMeta() {
  return useContext(MetaContext);
}

/**
 * False only once the server has said this kind of data never arrived, so a
 * page shows how to connect it; true or not yet known otherwise.
 */
export function useConnected(source: keyof NonNullable<Meta["sources"]>) {
  return useMeta()?.sources?.[source] !== false;
}

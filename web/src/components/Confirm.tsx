import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from "react";
import { Modal } from "./Modal";

interface ConfirmOptions {
  title: string;
  message: ReactNode;
  confirm?: string;
  danger?: boolean;
}

type Ask = (opts: ConfirmOptions) => Promise<boolean>;

const Ctx = createContext<Ask>(() => Promise.resolve(false));

/** The app's own confirmation dialog, used instead of the browser's confirm() */
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState<ConfirmOptions | null>(null);
  const settle = useRef<(ok: boolean) => void>(() => undefined);

  const ask = useCallback<Ask>(
    (opts) =>
      new Promise<boolean>((resolve) => {
        settle.current(false);
        settle.current = resolve;
        setOpen(opts);
      }),
    [],
  );

  const close = useCallback((ok: boolean) => {
    settle.current(ok);
    settle.current = () => undefined;
    setOpen(null);
  }, []);

  return (
    <Ctx.Provider value={ask}>
      {children}
      {open && (
        <Modal
          title={open.title}
          narrow
          onClose={() => close(false)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => close(false)}>
                Cancel
              </button>
              <button type="button" className={`btn ${open.danger ? "danger" : "primary"}`} autoFocus onClick={() => close(true)}>
                {open.confirm ?? "Continue"}
              </button>
            </>
          }
        >
          <p className="confirm-text">{open.message}</p>
        </Modal>
      )}
    </Ctx.Provider>
  );
}

export const useConfirm = () => useContext(Ctx);

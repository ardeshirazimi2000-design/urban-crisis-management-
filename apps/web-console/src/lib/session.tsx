import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, hasToken, setToken, type Me, type Org } from "./api";

type Session = {
  me: Me | null;
  orgs: Org[];
  loading: boolean;
  can: (perm: string) => boolean;
  login: (token: string) => Promise<void>;
  logout: () => void;
  orgName: (id: string | null | undefined) => string;
};

const Ctx = createContext<Session | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null);
  const [orgs, setOrgs] = useState<Org[]>([]);
  const [loading, setLoading] = useState(hasToken());

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [m, o] = await Promise.all([api<Me>("GET", "/me"), api<{ items: Org[] }>("GET", "/organizations")]);
      setMe(m);
      setOrgs(o.items);
    } catch {
      setToken(null);
      setMe(null);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (hasToken()) void load();
  }, [load]);

  const value: Session = {
    me, orgs, loading,
    can: (perm) => !!me?.permissions.includes(perm),
    login: async (tok) => {
      setToken(tok);
      await load();
    },
    logout: () => {
      setToken(null);
      setMe(null);
    },
    orgName: (id) => orgs.find((o) => o.id === id)?.name ?? "—",
  };
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useSession(): Session {
  const s = useContext(Ctx);
  if (!s) throw new Error("SessionProvider missing");
  return s;
}

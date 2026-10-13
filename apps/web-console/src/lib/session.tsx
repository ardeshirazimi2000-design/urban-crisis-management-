import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, hasToken, setRefreshToken, setSessionEndedHandler, setToken, type AccessHistory, type Me, type Org, type Tokens } from "./api";

type Session = {
  me: Me | null;
  orgs: Org[];
  loading: boolean;
  /** Why the last session ended (idle timeout, replaced, …); shown on the sign-in page. */
  endedReason: string;
  /** Access history reported at the last sign-in (FTA_TAH.1); shown once after signing in. */
  history: AccessHistory | null;
  dismissHistory: () => void;
  can: (perm: string) => boolean;
  login: (token: string) => Promise<void>;
  loginSession: (tokens: Tokens, history: AccessHistory | null) => Promise<void>;
  reload: () => Promise<void>;
  logout: () => void;
  orgName: (id: string | null | undefined) => string;
};

const Ctx = createContext<Session | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null);
  const [orgs, setOrgs] = useState<Org[]>([]);
  const [loading, setLoading] = useState(hasToken());
  const [endedReason, setEndedReason] = useState("");
  const [history, setHistory] = useState<AccessHistory | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const m = await api<Me>("GET", "/me");
      setMe(m);
      // A temporary or expired password allows nothing but changing it.
      if (!m.password_change_required) setOrgs((await api<{ items: Org[] }>("GET", "/organizations")).items);
    } catch {
      setToken(null);
      setRefreshToken(null);
      setMe(null);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    setSessionEndedHandler((reason) => {
      setMe(null);
      setEndedReason(reason);
    });
    if (hasToken()) void load();
  }, [load]);

  const value: Session = {
    me, orgs, loading, endedReason, history,
    dismissHistory: () => setHistory(null),
    can: (perm) => !!me?.permissions.includes(perm),
    login: async (tok) => {
      setToken(tok);
      setEndedReason("");
      await load();
    },
    loginSession: async (tokens, h) => {
      setToken(tokens.access_token);
      setRefreshToken(tokens.refresh_token);
      setEndedReason("");
      setHistory(h);
      await load();
    },
    reload: load,
    logout: () => {
      if (me?.channel) void api("POST", "/auth/logout").catch(() => undefined);
      setToken(null);
      setRefreshToken(null);
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

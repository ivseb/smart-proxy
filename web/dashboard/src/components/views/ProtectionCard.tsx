import { useState } from "react";
import { toast } from "sonner";
import { AlertTriangle, Copy, KeyRound, Lock, LockOpen, Plus, Trash2, UserRound, X } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { apiRequest, errorMessage } from "@/lib/api";
import { copyText } from "@/lib/clipboard";
import { formatRelative, splitHosts } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import type { Credential, Protection, RouteConfig, RouteStatus } from "@/types/api";

const inputClass = "bg-gray-900 border border-gray-700 rounded-lg px-3 py-1.5 text-sm text-white placeholder:text-gray-500 focus:ring-2 focus:ring-blue-500 outline-none";

function generatePassword(): string {
    const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    return Array.from(bytes, b => alphabet[b % alphabet.length]).join("");
}

interface ProtectionCardProps {
    route: RouteStatus;
    onSave: (route: Partial<RouteConfig>) => Promise<boolean>;
    onChanged: () => void;
}

// ProtectionCard puts a route behind a login (browsers) and access tokens (scripts).
export function ProtectionCard({ route, onSave, onChanged }: ProtectionCardProps) {
    const protection: Protection = route.protection || { enabled: false };
    const enabled = protection.enabled;
    const [setup, setSetup] = useState(false);
    const users = route.protection_users || [];
    const tokens = route.protection_tokens || [];
    const host = splitHosts(route.host)[0];
    const loginURL = host ? `https://${host}${(route.path || "/").replace(/\/$/, "")}/__smart_proxy/login` : null;

    const save = (p: Protection) => onSave({ ...route, protection: p });

    if (route.declarative) {
        return null; // Not configurable from annotations
    }

    const header = (
        <CardHeader className="flex flex-col md:flex-row md:items-start justify-between gap-3">
            <div className="space-y-1">
                <CardTitle className="flex items-center gap-2">
                    {enabled ? <Lock size={18} className="text-green-400" /> : <LockOpen size={18} className="text-gray-400" />}
                    Access
                </CardTitle>
                <p className="text-sm text-gray-400">
                    {enabled
                        ? "Only people who sign in, and scripts with an access token, reach the application. Others never wake it."
                        : "Anyone who reaches this route can use the application."}
                </p>
            </div>
            {enabled ? (
                <Button variant="secondary" size="sm" className="gap-1.5 shrink-0"
                    onClick={() => confirm("Make the application reachable without signing in again?") && save({ ...protection, enabled: false })}>
                    <LockOpen size={14} /> Turn off
                </Button>
            ) : !setup && (
                <Button size="sm" className="gap-1.5 shrink-0" onClick={() => setSetup(true)} disabled={!route.protection_available}>
                    <Lock size={14} /> Require sign-in
                </Button>
            )}
        </CardHeader>
    );

    if (!route.protection_available) {
        return (
            <Card>
                {header}
                <CardContent>
                    <p className="text-sm text-orange-300 bg-orange-400/10 border border-orange-400/20 rounded-lg px-3 py-2">
                        Unavailable: Smart Proxy can't use its Secret ({"<release>"}-state) in its namespace. Check its permissions (the Helm chart grants them).
                    </p>
                </CardContent>
            </Card>
        );
    }

    if (!enabled && !setup) {
        return <Card>{header}</Card>;
    }

    return (
        <Card>
            {header}
            <CardContent className="space-y-6">
                {!enabled && (
                    <p className="text-sm text-blue-100 bg-blue-900/20 border border-blue-800 rounded-lg px-3 py-2">
                        Add who can sign in and, for scripts, access tokens. Then turn protection on.
                    </p>
                )}

                <UsersSection route={route} users={users} loginURL={loginURL} onChanged={onChanged} />
                <TokensSection route={route} tokens={tokens} onChanged={onChanged} />
                <OpenPaths protection={protection} onSave={save} />

                <div className="flex items-start gap-2 text-xs text-yellow-200/90 bg-yellow-400/5 border border-yellow-400/20 rounded-lg px-3 py-2">
                    <AlertTriangle size={14} className="shrink-0 mt-0.5" />
                    <span>
                        Only traffic going through Smart Proxy is protected. If the route is unpatched or Smart Proxy is uninstalled,
                        the application is public again; inside the cluster its Service stays reachable (use a NetworkPolicy for that).
                    </span>
                </div>

                {!enabled && (
                    <div className="flex justify-end gap-2">
                        <Button variant="ghost" onClick={() => setSetup(false)}>Cancel</Button>
                        <Button className="gap-1.5" disabled={users.length + tokens.length === 0}
                            title={users.length + tokens.length === 0 ? "Add a user or a token first" : undefined}
                            onClick={async () => { if (await save({ ...protection, enabled: true })) setSetup(false); }}>
                            <Lock size={14} /> Turn protection on
                        </Button>
                    </div>
                )}
            </CardContent>
        </Card>
    );
}

function CredentialRow({ icon, title, detail, onRemove, removeLabel }: { icon: React.ReactNode; title: string; detail: string; onRemove: () => void; removeLabel: string }) {
    return (
        <li className="flex items-center justify-between gap-3 py-2 text-sm">
            <span className="flex items-center gap-2 min-w-0">
                {icon}
                <span className="font-medium text-white truncate">{title}</span>
                <span className="text-xs text-gray-500 truncate">{detail}</span>
            </span>
            <Button variant="ghost" size="sm" className="gap-1 text-red-300 hover:text-red-200" onClick={onRemove}>
                <Trash2 size={14} /> {removeLabel}
            </Button>
        </li>
    );
}

function UsersSection({ route, users, loginURL, onChanged }: { route: RouteStatus; users: Credential[]; loginURL: string | null; onChanged: () => void }) {
    const now = useNow(60000);
    const [name, setName] = useState(users.length === 0 ? "team" : "");
    const [password, setPassword] = useState("");
    const [busy, setBusy] = useState(false);

    const add = async () => {
        setBusy(true);
        try {
            await apiRequest("/api/routes/protection/users", {
                method: "POST", headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ id: route.id, name: name.trim(), password }),
            });
            toast.success(`${name.trim()} can sign in`);
            setName(""); setPassword("");
            onChanged();
        } catch (e) {
            toast.error(errorMessage(e));
        } finally {
            setBusy(false);
        }
    };
    const remove = async (user: string) => {
        if (!confirm(`Remove ${user}? They are signed out at once.`)) return;
        try {
            await apiRequest(`/api/routes/protection/users?${new URLSearchParams({ id: route.id, name: user })}`, { method: "DELETE" });
            onChanged();
        } catch (e) {
            toast.error(errorMessage(e));
        }
    };

    return (
        <section className="space-y-2">
            <h3 className="text-sm font-medium text-gray-200">People (browsers)</h3>
            <p className="text-xs text-gray-500">
                They sign in on a page served by Smart Proxy, on the application's own address
                {loginURL && <> (<span className="font-mono">{loginURL}</span>)</>}. With a single person, the page only asks for the
                password: use it as a shared password for your team.
            </p>
            {users.length > 0 && (
                <ul className="divide-y divide-gray-700/70">
                    {users.map(u => (
                        <CredentialRow key={u.name} icon={<UserRound size={14} className="text-gray-400" />} title={u.name}
                            detail={`added ${formatRelative(u.created, now)}`} removeLabel="Remove" onRemove={() => remove(u.name)} />
                    ))}
                </ul>
            )}
            <div className="flex flex-wrap items-center gap-2">
                <input className={`${inputClass} w-40`} placeholder="Name" value={name} onChange={e => setName(e.target.value)} aria-label="Name" />
                <div className="flex items-center gap-1">
                    <input className={`${inputClass} w-52 font-mono`} placeholder="Password (8+ characters)" value={password}
                        onChange={e => setPassword(e.target.value)} aria-label="Password" autoComplete="new-password" />
                    <Button variant="ghost" size="sm" onClick={() => setPassword(generatePassword())} title="Generate a strong password">Generate</Button>
                    {password && <Button variant="ghost" size="icon" onClick={() => copyText(password)} aria-label="Copy password"><Copy size={14} /></Button>}
                </div>
                <Button size="sm" className="gap-1.5" onClick={add} disabled={busy || !name.trim() || password.length < 8}>
                    <Plus size={14} /> {users.some(u => u.name === name.trim()) ? "Change password" : "Add"}
                </Button>
            </div>
        </section>
    );
}

function TokensSection({ route, tokens, onChanged }: { route: RouteStatus; tokens: Credential[]; onChanged: () => void }) {
    const now = useNow(60000);
    const [name, setName] = useState("");
    const [created, setCreated] = useState<{ name: string; token: string } | null>(null);
    const [busy, setBusy] = useState(false);

    const create = async () => {
        setBusy(true);
        try {
            const res = await apiRequest("/api/routes/protection/tokens", {
                method: "POST", headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ id: route.id, name: name.trim() }),
            });
            setCreated({ name: name.trim(), token: (await res.json()).token });
            setName("");
            onChanged();
        } catch (e) {
            toast.error(errorMessage(e));
        } finally {
            setBusy(false);
        }
    };
    const revoke = async (token: string) => {
        if (!confirm(`Revoke the token ${token}? Scripts using it are refused at once.`)) return;
        try {
            await apiRequest(`/api/routes/protection/tokens?${new URLSearchParams({ id: route.id, name: token })}`, { method: "DELETE" });
            onChanged();
        } catch (e) {
            toast.error(errorMessage(e));
        }
    };

    return (
        <section className="space-y-2">
            <h3 className="text-sm font-medium text-gray-200">Access tokens (scripts, other services)</h3>
            <p className="text-xs text-gray-500">
                Sent as <span className="font-mono">Authorization: Bearer &lt;token&gt;</span>, or <span className="font-mono">X-Api-Key: &lt;token&gt;</span> if
                the application uses Authorization itself. The application receives the caller in <span className="font-mono">X-Smart-Proxy-User</span>.
            </p>
            {created && (
                <div className="bg-green-900/20 border border-green-800 rounded-lg px-3 py-2 space-y-1">
                    <div className="flex items-center justify-between gap-2">
                        <p className="text-sm text-green-200">Token <span className="font-medium">{created.name}</span>: copy it now, it won't be shown again.</p>
                        <Button variant="ghost" size="icon" onClick={() => setCreated(null)} aria-label="Dismiss"><X size={14} /></Button>
                    </div>
                    <div className="flex items-center gap-2">
                        <code className="font-mono text-sm text-white break-all">{created.token}</code>
                        <Button variant="secondary" size="sm" className="gap-1 shrink-0" onClick={() => copyText(created.token)}><Copy size={12} /> Copy</Button>
                    </div>
                </div>
            )}
            {tokens.length > 0 && (
                <ul className="divide-y divide-gray-700/70">
                    {tokens.map(t => (
                        <CredentialRow key={t.name} icon={<KeyRound size={14} className="text-gray-400" />} title={t.name}
                            detail={`…${t.hint} · created ${formatRelative(t.created, now)}`} removeLabel="Revoke" onRemove={() => revoke(t.name)} />
                    ))}
                </ul>
            )}
            <div className="flex flex-wrap items-center gap-2">
                <input className={`${inputClass} w-56`} placeholder="Who uses it, e.g. jenkins" value={name} onChange={e => setName(e.target.value)} aria-label="Token name" />
                <Button size="sm" variant="secondary" className="gap-1.5" onClick={create} disabled={busy || !name.trim() || tokens.some(t => t.name === name.trim())}>
                    <Plus size={14} /> New token
                </Button>
            </div>
        </section>
    );
}

function OpenPaths({ protection, onSave }: { protection: Protection; onSave: (p: Protection) => Promise<boolean> }) {
    const [path, setPath] = useState("");
    const open = protection.open || [];
    const valid = path.startsWith("/");
    const add = async () => {
        if (await onSave({ ...protection, open: [...open, path.trim()] })) setPath("");
    };
    return (
        <section className="space-y-2">
            <h3 className="text-sm font-medium text-gray-200">Reachable without signing in</h3>
            <p className="text-xs text-gray-500">
                Paths others must reach directly: an identity provider's callback (e.g. <span className="font-mono">/saml/acs</span>), webhooks,
                health checks. End with <span className="font-mono">*</span> for everything under a path.
            </p>
            {open.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                    {open.map(p => (
                        <span key={p} className="flex items-center gap-1 font-mono text-xs px-2 py-1 rounded bg-gray-700 text-gray-200">
                            {p}
                            <button onClick={() => onSave({ ...protection, open: open.filter(x => x !== p) })} aria-label={`Remove ${p}`} className="text-gray-400 hover:text-white"><X size={12} /></button>
                        </span>
                    ))}
                </div>
            )}
            <div className="flex items-center gap-2">
                <input className={`${inputClass} w-56 font-mono`} placeholder="/saml/acs" value={path} onChange={e => setPath(e.target.value)} aria-label="Open path"
                    onKeyDown={e => { if (e.key === "Enter" && valid) add(); }} />
                <Button size="sm" variant="secondary" className="gap-1.5" onClick={add} disabled={!valid}><Plus size={14} /> Add</Button>
            </div>
        </section>
    );
}

import { useSyncExternalStore } from "react";
import { it } from "@/locales/it";

// The dashboard speaks English and Italian. Texts are written in English in the code, wrapped in
// t(); the Italian dictionary maps each English text to its translation (a missing one shows the
// English text). `npm run lint` checks that every text has its translation.

export type Lang = "en" | "it";

export const LANGS: { value: Lang; label: string }[] = [{ value: "en", label: "English" }, { value: "it", label: "Italiano" }];

const STORAGE_KEY = "dashboard.lang";

function detect(): Lang {
    try {
        const stored = window.localStorage.getItem(STORAGE_KEY);
        if (stored === "en" || stored === "it") return stored;
    } catch {
        // Storage unavailable: follow the browser.
    }
    const preferred = navigator.languages?.length ? navigator.languages : [navigator.language];
    for (const l of preferred) {
        if (l?.toLowerCase().startsWith("it")) return "it";
        if (l?.toLowerCase().startsWith("en")) return "en";
    }
    return "en";
}

let current: Lang = detect();
document.documentElement.lang = current;
const listeners = new Set<() => void>();

export function getLang(): Lang {
    return current;
}

export function setLang(lang: Lang) {
    current = lang;
    document.documentElement.lang = lang;
    try {
        window.localStorage.setItem(STORAGE_KEY, lang);
    } catch {
        // Not remembered; the choice still applies until the page is reloaded.
    }
    listeners.forEach(notify => notify());
}

// useLang re-renders a component when the language changes.
export function useLang(): Lang {
    return useSyncExternalStore(notify => {
        listeners.add(notify);
        return () => listeners.delete(notify);
    }, getLang);
}

type Vars = Record<string, string | number>;

// t translates a text; {name} placeholders are filled from vars: t("Waking up {name}", { name }).
export function t(text: string, vars?: Vars): string {
    const translated = current === "it" ? it[text] ?? text : text;
    return vars ? translated.replace(/\{(\w+)\}/g, (match, key) => (key in vars ? String(vars[key]) : match)) : translated;
}

// msg marks a text to translate later, where it can't be translated yet (e.g. in a constant
// defined once, translated with t(text) when shown).
export const msg = (text: string) => text;

// tn picks the singular or plural text by count, then translates it; {n} is the count.
export function tn(n: number, singular: string, plural: string, vars?: Vars): string {
    return t(n === 1 ? singular : plural, { n, ...vars });
}

// Checks that every text the dashboard translates — t("…"), msg("…") and tn(n, "…", "…") with
// literal texts — has its Italian translation, and that the dictionary has no texts nobody uses.
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const SRC = new URL("../src", import.meta.url).pathname;
const files = dir => readdirSync(dir).flatMap(name => {
    const path = join(dir, name);
    return statSync(path).isDirectory() ? files(path) : /\.tsx?$/.test(name) ? [path] : [];
});

const literal = String.raw`("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|\x60(?:[^\x60\\$]|\\.)*\x60)`;
const call = new RegExp(String.raw`\b(?:t|msg)\(\s*${literal}|\btn\([^,]+,\s*${literal}\s*,\s*${literal}`, "g");
const unquote = s => JSON.parse(s[0] === "'" || s[0] === "`" ? '"' + s.slice(1, -1).replace(/\\'/g, "'").replace(/"/g, '\\"') + '"' : s);

const used = new Set();
for (const file of files(SRC)) {
    if (file.includes("/locales/")) continue;
    for (const m of readFileSync(file, "utf8").matchAll(call)) {
        for (const s of m.slice(1).filter(Boolean)) used.add(unquote(s));
    }
}

const dictionary = new Map();
const conflicts = [];
const entry = /^\s*("(?:[^"\\]|\\.)*")\s*:\s*"(?:[^"\\]|\\.)*",?\s*$/;
for (const file of files(join(SRC, "locales/it"))) {
    readFileSync(file, "utf8").split("\n").forEach((line, i) => {
        const m = line.match(entry);
        if (!m) return;
        const [key, value] = [JSON.parse(m[1]), line.slice(line.indexOf(m[1]) + m[1].length)];
        const where = `${file.slice(SRC.length + 1)}:${i + 1}`;
        const previous = dictionary.get(key);
        if (previous && previous.value !== value.trim().replace(/,$/, "")) conflicts.push(`${where}: ${JSON.stringify(key)} translated differently in ${previous.where}`);
        dictionary.set(key, { where, value: value.trim().replace(/,$/, "") });
    });
}

const missing = [...used].filter(s => !dictionary.has(s));
const unused = [...dictionary].filter(([s]) => !used.has(s)).map(([s, { where }]) => [s, where]);
missing.forEach(s => console.error(`missing Italian translation: ${JSON.stringify(s)}`));
unused.forEach(([s, where]) => console.error(`${where}: not used: ${JSON.stringify(s)}`));
conflicts.forEach(c => console.error(c));
if (missing.length || unused.length || conflicts.length) process.exit(1);
console.log(`i18n: ${used.size} texts, all translated`);

// Shared texts: the dashboard's frame, statuses, durations.
export const common: Record<string, string> = {
    // Statuses
    "Awake": "Attiva",
    "Waking": "Si sveglia",
    "Asleep": "Dorme",
    "Error": "Errore",
    "Not managed": "Non gestita",
    "Offline": "Offline",
    "Unknown": "Sconosciuto",

    // Durations and days
    "now": "ora",
    "in {time}": "tra {time}",
    "{time} ago": "{time} fa",
    "Every day": "Tutti i giorni",
    "Mon": "Lun",
    "Tue": "Mar",
    "Wed": "Mer",
    "Thu": "Gio",
    "Fri": "Ven",
    "Sat": "Sab",
    "Sun": "Dom",

    // Frame
    "Overview": "Panoramica",
    "Routes": "Route",
    "Patching": "Patching",
    "Logs": "Log",
    "Language": "Lingua",
    "Not connected to a cluster": "Non connesso a un cluster",
    "Namespace {name}": "Namespace {name}",
    "{n} namespaces": "{n} namespace",
    "Signed in ({mode})": "Accesso effettuato ({mode})",
    "Sign out": "Esci",
    "Cancel": "Annulla",

    // Route actions
    "Route saved": "Route salvata",
    "Failed to save route": "Salvataggio della route non riuscito",
    "This route is defined by smart-proxy/* annotations on its Ingress/Route: set smart-proxy/enabled to \"false\" there to remove it.": "Questa route è definita dalle annotazioni smart-proxy/* del suo Ingress/Route: per rimuoverla imposta lì smart-proxy/enabled a \"false\".",
    "Delete the route for {name}?": "Eliminare la route di {name}?",
    "Failed to delete route": "Eliminazione della route non riuscita",
    "Route deleted, {n} resource restored": "Route eliminata, {n} risorsa ripristinata",
    "Route deleted, {n} resources restored": "Route eliminata, {n} risorse ripristinate",
    "Route deleted": "Route eliminata",
    "Put {name} to sleep now?": "Mettere a dormire {name} adesso?",
    "{name} is going to sleep": "{name} va a dormire",
    "Failed to stop {name}": "Non è stato possibile fermare {name}",
    "Waking up {name}": "Risveglio di {name}",
    "Failed to wake {name}": "Non è stato possibile svegliare {name}",
    "Delete route?": "Eliminare la route?",
    "These resources point at Smart Proxy for this route and get their original backend back:": "Queste risorse puntano a Smart Proxy per questa route e torneranno al loro backend originale:",
    "any host": "qualsiasi host",
    "Delete and restore {kind}": "Elimina e ripristina {kind}",
    "Delete and restore {n} resources": "Elimina e ripristina {n} risorse",

    // Logs
    "Live logs": "Log in tempo reale",
    "replica": "replica",
    "each replica streams its own": "ogni replica mostra i propri",
    "Clear": "Svuota",
    "Waiting for logs…": "In attesa dei log…",
};

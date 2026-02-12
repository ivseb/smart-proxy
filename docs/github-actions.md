# Configurazione GitHub Actions

Per far funzionare correttamente le pipeline di CI/CD (pubblicazione Docker image e Helm Chart), è necessario configurare alcuni **Secret** nel repository GitHub.

## 1. Token DockerHub

La action `.github/workflows/docker-publish.yml` ha bisogno delle credenziali per poter inviare l'immagine compilata su DockerHub.

1.  Vai su [hub.docker.com](https://hub.docker.com/) -> **Account Settings** -> **Security**.
2.  Clicca su **New Access Token**.
3.  Dai una descrizione (es. "GitHub Actions") e permessi **Read & Write**.
4.  Copia il token generato.

## 2. Configurazione GitHub

1.  Vai nel tuo repository GitHub: `https://github.com/ivseb/smart-proxy`.
2.  Clicca su **Settings** (in alto a destra).
3.  Nel menu a sinistra, espandi **Secrets and variables** e clicca su **Actions**.
4.  Clicca sul pulsante verde **New repository secret**.
5.  Aggiungi le seguenti variabili:

| Nome Secret | Valore | Descrizione |
| :--- | :--- | :--- |
| `DOCKERHUB_USERNAME` | `isebben` | Il tuo username DockerHub. |
| `DOCKERHUB_TOKEN` | `dckr_pat_...` | Il token copiato al passo precedente. |

## 3. GitHub Pages (Token Automatico)

Per la pubblicazione della documentazione e del chart Helm, utilizziamo il `GITHUB_TOKEN` che è generato automaticamente da GitHub per ogni esecuzione.

Tuttavia, devi abilitare i permessi di scrittura:

1.  Vai su **Settings** -> **Actions** -> **General**.
2.  Scorri fino a **Workflow permissions**.
3.  Seleziona **Read and write permissions**.
4.  Clicca **Save**.

Inoltre conferma che GitHub Pages sia attivo (dopo la prima esecuzione della action):
1.  Vai su **Settings** -> **Pages**.
2.  Assicurati che **Build and deployment** sia impostato su **Deploy from a branch**.
3.  Il branch dovrebbe essere `gh-pages` (verrà creato automaticamente dalla action).

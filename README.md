# MedBook — Banca Dati Farmaci AIFA offline

Consultazione locale dei farmaci italiani autorizzati da AIFA, senza accesso a internet.
I dati vengono scaricati una tantum (o periodicamente) dai dataset open data di AIFA.

## Funzionalità

- **Ricerca** per nome commerciale, principio attivo, codice AIC, codice ATC, azienda
- **Dettaglio farmaco**: tutte le confezioni, principi attivi con dosaggi, tipo di fornitura
- **Link a FI/RCP** (Foglio Illustrativo / Riassunto Caratteristiche Prodotto) — richiede internet
- **Completamente offline** dopo il primo sync (~1-2 minuti)

## Sorgenti dati

Tre file CSV aggiornati quotidianamente da AIFA (licenza CC-BY 4.0):

| File | Descrizione | Righe |
|------|-------------|-------|
| `confezioni_fornitura.csv` | Anagrafica confezioni farmaci | ~158.000 |
| `PA_confezioni.csv` | Principi attivi per confezione | ~336.000 |
| `atc.csv` | Classificazione ATC fino al 5° livello | ~7.200 |

URL: `https://drive.aifa.gov.it/farmaci/`

## Prerequisiti

- **Go** 1.22+
- **PostgreSQL** 13+

## Installazione e avvio

```bash
# 1. Clonare il repository
git clone https://github.com/openbear-it/medbook
cd medbook

# 2. Configurare il database
export DATABASE_URL="postgres://utente:password@localhost/medbook?sslmode=disable"

# 3. Creare il database PostgreSQL (se non esiste)
createdb medbook

# 4. Compilare il binario
go build -o medbook ./cmd/

# 5. Creare lo schema
./medbook migrate

# 6. Sincronizzare i dati AIFA (richiede internet, ~1-2 minuti)
./medbook sync

# 7. Avviare il server locale
./medbook serve
# → Apri http://localhost:8080
```

Oppure con Docker:

```bash
# Avviare PostgreSQL con Docker
docker run -d --name medbook-pg \
  -e POSTGRES_DB=medbook -e POSTGRES_USER=medbook -e POSTGRES_PASSWORD=medbook \
  -p 5432:5432 postgres:15-alpine

export DATABASE_URL="postgres://medbook:medbook@localhost/medbook?sslmode=disable"

go build -o medbook ./cmd/
./medbook migrate
./medbook sync
./medbook serve
```

## Comandi disponibili

```
medbook migrate   Crea o aggiorna lo schema del database
medbook sync      Scarica i dati AIFA (truncate + reimport)
medbook serve     Avvia il server web locale (default: porta 8080)
medbook stats     Mostra statistiche del database
```

## Variabili d'ambiente

| Variabile | Default | Descrizione |
|-----------|---------|-------------|
| `DATABASE_URL` | `postgres://localhost/medbook?sslmode=disable` | Connessione PostgreSQL |
| `MEDBOOK_PORT` | `8080` | Porta del server web |

## Struttura del progetto

```
medbook/
├── cmd/main.go                          # Entry point, router comandi
├── internal/
│   ├── db/store.go                      # Accesso PostgreSQL (schema, query, bulk insert)
│   ├── syncer/syncer.go                 # Download CSV AIFA e import DB
│   └── web/
│       ├── server.go                    # Server HTTP + handler
│       └── assets/templates/
│           ├── index.html               # Pagina di ricerca
│           └── detail.html              # Dettaglio farmaco
├── go.mod
├── Makefile
└── README.md
```

## Schema database

```sql
confezioni        -- 1 riga per confezione (AIC, nome, ditta, ATC, PA, fornitura, link FI/RCP)
principi_attivi   -- principi attivi per confezione con dosaggio e unità di misura
atc_codes         -- classificazione ATC (codice → descrizione)
sync_log          -- storico delle sincronizzazioni
```

La ricerca utilizza **PostgreSQL full-text search** (indice GIN su `tsvector`) più
ricerca esatta per codice AIC e ATC.

## Aggiornamento dati

```bash
# Riscarica i CSV AIFA e aggiorna il database (eseguire periodicamente)
./medbook sync
```

Il comando `sync` esegue un **reimport completo** (truncate + copy), quindi durante l'esecuzione
il database è temporaneamente vuoto. La durata è tipicamente 1-3 minuti.

## Licenza

Dati AIFA: [CC-BY 4.0](https://creativecommons.org/licenses/by/4.0/) —
Agenzia Italiana del Farmaco.

Codice: MIT

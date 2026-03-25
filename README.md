# MedBook — Banca Dati Farmaci AIFA offline

Consultazione locale dei farmaci italiani autorizzati da AIFA, senza accesso a internet.
I dati vengono scaricati una tantum (o periodicamente) dai dataset open data di AIFA.

## Funzionalità

- **Ricerca** per nome commerciale, principio attivo, codice AIC, codice ATC, azienda
- **Catalogo A–Z**: sfoglia tutti i farmaci con filtro per lettera iniziale o testo libero
- **Ricerca per principio attivo** (`/drugs/ingredient`) con autocomplete
- **Ricerca per azienda** (`/drugs/company`) con autocomplete
- **Dettaglio farmaco**: confezioni, principi attivi con dosaggi, tipo di fornitura, link FI/RCP
- **Prezzi SSN** dalla Lista Trasparenza AIFA (prezzo SSN e pubblico per confezione)
- **Gruppi di equivalenza**: lista dei farmaci equivalenti dello stesso gruppo terapeutico
- **Farmaci orfani**: segnalazione dei medicinali per malattie rare (AIFA)
- **Carenze AIFA**: alert per farmaci temporaneamente non disponibili
- **REST API** con spec OpenAPI 3.0 (`/api/docs`)
- **Completamente offline** dopo il primo sync (~2-4 minuti)

## Sorgenti dati

File CSV AIFA (licenza CC-BY 4.0):

| File | Descrizione | Righe | Frequenza |
|------|-------------|-------|-----------|
| `confezioni_fornitura.csv` | Anagrafica confezioni farmaci | ~158.000 | quotidiana |
| `PA_confezioni.csv` | Principi attivi per confezione | ~336.000 | quotidiana |
| `atc.csv` | Classificazione ATC fino al 5° livello | ~7.200 | quotidiana |
| `elenco_medicinali_carenti.csv` | Medicinali carenti (shortage list) | variabile | quotidiana |
| `Lista_farmaci_equivalenti.csv` | Lista Trasparenza (prezzi SSN + gruppi equivalenza) | ~8.400 | mensile |
| `Lista-farmaci-orfani-2024.csv` | Farmaci orfani (medicinali per malattie rare) | ~115 | annuale |

URL base anagrafica: `https://drive.aifa.gov.it/farmaci/`  
Documenti AIFA: `https://www.aifa.gov.it/documents/20142/`

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
medbook migrate          Crea o aggiorna lo schema del database
medbook sync             Scarica tutti i dati AIFA (truncate + reimport)
medbook sync-prezzi      Aggiorna solo la Lista Trasparenza (prezzi + equivalenti)
medbook sync-orfani      Aggiorna solo la lista farmaci orfani
medbook serve            Avvia il server web locale (default: porta 8080)
medbook stats            Mostra statistiche del database
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
│           ├── search.html          # Pagina di ricerca principale
│           ├── drug.html            # Dettaglio farmaco
│           ├── browse.html          # Catalogo A–Z sfogliabile
│           ├── list.html            # Lista farmaci per principio attivo / azienda
│           ├── equivalents.html     # Gruppo di equivalenza (Lista Trasparenza)
│           └── swagger.html         # Documentazione API (Swagger UI)
├── go.mod
├── Makefile
└── README.md
```

## Rotte principali

| Rotta | Descrizione |
|-------|-------------|
| `GET /search` | Pagina di ricerca principale |
| `GET /drugs` | Catalogo farmaci A–Z (paginato, filtro lettera/testo) |
| `GET /drugs/ingredient?q=` | Farmaci per principio attivo (con autocomplete) |
| `GET /drugs/company?q=` | Farmaci per azienda (con autocomplete) |
| `GET /drug/{codFarmaco}` | Dettaglio farmaco |
| `GET /equivalents/{codiceGruppo}` | Gruppo di equivalenza |
| `GET /api/search?q=` | Ricerca JSON fulltext |
| `GET /api/autocomplete?q=` | Suggerimenti nome farmaco |
| `GET /api/autocomplete/pa?q=` | Suggerimenti principi attivi |
| `GET /api/autocomplete/company?q=` | Suggerimenti aziende |
| `GET /api/drug/{codFarmaco}` | Dettaglio farmaco JSON |
| `GET /api/pa/{nome}` | Farmaci per principio attivo JSON |
| `GET /api/company/{nome}` | Farmaci per azienda JSON |
| `GET /api/stats` | Statistiche database |
| `GET /api/docs` | Documentazione API (Swagger UI) |

## Schema database

```sql
confezioni           -- 1 riga per confezione (AIC, nome, ditta, ATC, PA, fornitura, link FI/RCP)
principi_attivi       -- principi attivi per confezione con dosaggio e unità di misura
atc_codes            -- classificazione ATC (codice → descrizione)
carenze              -- shortage list AIFA (elenco_medicinali_carenti)
prezzi_equivalenti   -- Lista Trasparenza: prezzi SSN, prezzi pubblici, gruppi di equivalenza
farmaci_orfani       -- medicinali orfani (malattie rare), chiave: codice AIC 6 cifre
sync_log             -- storico delle sincronizzazioni
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

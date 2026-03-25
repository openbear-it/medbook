// Package db provides PostgreSQL data access for MedBook.
package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps a PostgreSQL connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a connection pool and verifies connectivity.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connessione database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases pool resources.
func (s *Store) Close() { s.pool.Close() }

// ─── Row types ────────────────────────────────────────────────────────────────

// Confezione represents one drug package row (from confezioni_fornitura.csv).
type Confezione struct {
	CodiceAIC           string
	CodFarmaco          string
	CodConfezione       string
	Denominazione       string
	Descrizione         string
	CodiceDitta         string
	RagioneSociale      string
	StatoAmministrativo string
	TipoProcedura       string
	Forma               string
	CodiceATC           string
	PaAssociati         string
	Fornitura           string
	LinkFI              string
	LinkRCP             string
}

// PrincipioAttivo represents one active-ingredient row (from PA_confezioni.csv).
type PrincipioAttivo struct {
	CodiceAIC   string
	Nome        string
	Quantita    string
	UnitaMisura string
}

// ATCCode represents one ATC classification row (from atc.csv).
type ATCCode struct {
	Codice      string
	Descrizione string
}

// SearchResult is one grouped drug result returned by SearchFarmaci.
type SearchResult struct {
	CodFarmaco      string
	Denominazione   string
	RagioneSociale  string
	StatoAmm        string
	Forme           []string
	CodiceATC       string
	PaAssociati     string
	NumConfezioni   int64
}

// FormaGroup groups packages that share the same pharmaceutical form.
type FormaGroup struct {
	Forma      string
	Confezioni []ConfezioneDetail
}

// DrugDetail holds all packages and their active ingredients for one drug.
type DrugDetail struct {
	CodFarmaco     string          `json:"codFarmaco"`
	Denominazione  string          `json:"denominazione"`
	RagioneSociale string          `json:"ragioneSociale"`
	CodiceATC      string          `json:"codiceATC"`
	ATCDesc        string          `json:"atcDesc"`
	PaAssociati    string          `json:"paAssociati"`
	Confezioni     []ConfezioneDetail `json:"confezioni"`
	FormaGroups    []FormaGroup       `json:"formaGroups"`
	Shortages      []Shortage         `json:"shortages,omitempty"`
}

// ConfezioneDetail pairs a package with its active ingredients.
type ConfezioneDetail struct {
	Confezione
	PA []PrincipioAttivo
}

// Shortage represents one entry in the AIFA shortage list (elenco_medicinali_carenti.csv).
type Shortage struct {
	NomeMedicinale    string `json:"nomeMedicinale"`
	CodiceAIC         string `json:"codiceAIC"`
	PrincipioAttivo   string `json:"principioAttivo"`
	FormaDosaggio     string `json:"formaDosaggio"`
	TitolareAIC       string `json:"titolareAIC"`
	DataInizio        string `json:"dataInizio"`
	FinePresunta      string `json:"finePresunta"`
	Equivalente       string `json:"equivalente"`
	Motivazioni       string `json:"motivazioni"`
	SuggerimentiAIFA  string `json:"suggerimentiAIFA"`
	NotaAIFA          string `json:"notaAIFA"`
	Fascia            string `json:"fascia"`
	CodiceATC         string `json:"codiceATC"`
}

// Stats holds database row counts for the stats command.
type Stats struct {
	Confezioni     int64  `json:"confezioni"`
	PrincipiAttivi int64  `json:"principiAttivi"`
	CodicATC       int64  `json:"codiciATC"`
	UltimoSync     string `json:"ultimoSync"`
}

// ─── Schema migration ─────────────────────────────────────────────────────────

const schema = `
CREATE TABLE IF NOT EXISTS confezioni (
    codice_aic           TEXT PRIMARY KEY,
    cod_farmaco          TEXT NOT NULL,
    cod_confezione       TEXT,
    denominazione        TEXT NOT NULL,
    descrizione          TEXT,
    codice_ditta         TEXT,
    ragione_sociale      TEXT,
    stato_amministrativo TEXT,
    tipo_procedura       TEXT,
    forma                TEXT,
    codice_atc           TEXT,
    pa_associati         TEXT,
    fornitura            TEXT,
    link_fi              TEXT,
    link_rcp             TEXT,
    search_vector        tsvector GENERATED ALWAYS AS (
        to_tsvector('simple',
            coalesce(denominazione,   '') || ' ' ||
            coalesce(pa_associati,    '') || ' ' ||
            coalesce(codice_atc,      '') || ' ' ||
            coalesce(ragione_sociale, '') || ' ' ||
            coalesce(descrizione,     '')
        )
    ) STORED,
    synced_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_confezioni_fts
    ON confezioni USING GIN(search_vector);
CREATE INDEX IF NOT EXISTS idx_confezioni_denominazione
    ON confezioni (denominazione text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_confezioni_cod_farmaco
    ON confezioni (cod_farmaco);
CREATE INDEX IF NOT EXISTS idx_confezioni_atc
    ON confezioni (codice_atc);

CREATE TABLE IF NOT EXISTS principi_attivi (
    codice_aic       TEXT NOT NULL,
    principio_attivo TEXT NOT NULL,
    quantita         TEXT,
    unita_misura     TEXT,
    PRIMARY KEY (codice_aic, principio_attivo)
);

CREATE INDEX IF NOT EXISTS idx_pa_nome
    ON principi_attivi (principio_attivo text_pattern_ops);

CREATE TABLE IF NOT EXISTS atc_codes (
    codice_atc  TEXT PRIMARY KEY,
    descrizione TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_log (
    id             SERIAL PRIMARY KEY,
    started_at     TIMESTAMPTZ DEFAULT NOW(),
    finished_at    TIMESTAMPTZ,
    confezioni_cnt INTEGER DEFAULT 0,
    pa_cnt         INTEGER DEFAULT 0,
    atc_cnt        INTEGER DEFAULT 0,
    status         TEXT DEFAULT 'running',
    error_msg      TEXT
);

CREATE TABLE IF NOT EXISTS shortages (
    id                SERIAL PRIMARY KEY,
    nome_medicinale   TEXT NOT NULL,
    codice_aic        TEXT,
    principio_attivo  TEXT,
    forma_dosaggio    TEXT,
    titolare_aic      TEXT,
    data_inizio       TEXT,
    fine_presunta     TEXT,
    equivalente       TEXT,
    motivazioni       TEXT,
    suggerimenti_aifa TEXT,
    nota_aifa         TEXT,
    fascia            TEXT,
    codice_atc        TEXT,
    synced_at         TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_shortages_aic
    ON shortages (codice_aic) WHERE codice_aic IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_shortages_nome
    ON shortages (upper(nome_medicinale));
`

// Migrate creates or updates the database schema.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, schema)
	return err
}

// ─── Bulk inserts ─────────────────────────────────────────────────────────────

var confezioniCols = []string{
	"codice_aic", "cod_farmaco", "cod_confezione", "denominazione",
	"descrizione", "codice_ditta", "ragione_sociale", "stato_amministrativo",
	"tipo_procedura", "forma", "codice_atc", "pa_associati",
	"fornitura", "link_fi", "link_rcp",
}

// BulkInsertConfezioni writes a batch using PostgreSQL COPY (fast path).
func (s *Store) BulkInsertConfezioni(ctx context.Context, rows []Confezione) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	data := make([][]any, len(rows))
	for i, r := range rows {
		data[i] = []any{
			r.CodiceAIC, r.CodFarmaco, r.CodConfezione, r.Denominazione,
			r.Descrizione, r.CodiceDitta, r.RagioneSociale, r.StatoAmministrativo,
			r.TipoProcedura, r.Forma, r.CodiceATC, r.PaAssociati,
			r.Fornitura, r.LinkFI, r.LinkRCP,
		}
	}
	return s.pool.CopyFrom(ctx, pgx.Identifier{"confezioni"}, confezioniCols, pgx.CopyFromRows(data))
}

var paCols = []string{"codice_aic", "principio_attivo", "quantita", "unita_misura"}

// BulkInsertPA writes a batch of active-ingredient rows.
func (s *Store) BulkInsertPA(ctx context.Context, rows []PrincipioAttivo) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	data := make([][]any, len(rows))
	for i, r := range rows {
		data[i] = []any{r.CodiceAIC, r.Nome, r.Quantita, r.UnitaMisura}
	}
	return s.pool.CopyFrom(ctx, pgx.Identifier{"principi_attivi"}, paCols, pgx.CopyFromRows(data))
}

var atcCols = []string{"codice_atc", "descrizione"}

// BulkInsertATC writes a batch of ATC classification rows.
func (s *Store) BulkInsertATC(ctx context.Context, rows []ATCCode) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	data := make([][]any, len(rows))
	for i, r := range rows {
		data[i] = []any{r.Codice, r.Descrizione}
	}
	return s.pool.CopyFrom(ctx, pgx.Identifier{"atc_codes"}, atcCols, pgx.CopyFromRows(data))
}

// TruncateTables deletes all rows in bulk tables (used before a full re-sync).
func (s *Store) TruncateTables(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, "TRUNCATE confezioni, principi_attivi, atc_codes RESTART IDENTITY")
	return err
}

// ─── Queries ─────────────────────────────────────────────────────────────────

const searchSQL = `
SELECT
    cod_farmaco,
    max(denominazione)        AS denominazione,
    max(ragione_sociale)      AS ragione_sociale,
    max(stato_amministrativo) AS stato_amministrativo,
    array_agg(DISTINCT forma ORDER BY forma) FILTER (WHERE forma <> '') AS forme,
    max(codice_atc)           AS codice_atc,
    max(pa_associati)         AS pa_associati,
    count(*)                  AS num_confezioni
FROM confezioni
WHERE
    search_vector @@ plainto_tsquery('simple', $1)
    OR upper(codice_aic)  = upper($1)
    OR upper(codice_atc)  = upper($1)
GROUP BY cod_farmaco
ORDER BY max(denominazione)
LIMIT $2 OFFSET $3
`

const countSQL = `
SELECT COUNT(DISTINCT cod_farmaco)
FROM confezioni
WHERE
    search_vector @@ plainto_tsquery('simple', $1)
    OR upper(codice_aic)  = upper($1)
    OR upper(codice_atc)  = upper($1)
`

// SearchFarmaci returns paginated drug search results and total hit count.
func (s *Store) SearchFarmaci(ctx context.Context, query string, page, pageSize int) ([]SearchResult, int64, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, 0, nil
	}

	var total int64
	if err := s.pool.QueryRow(ctx, countSQL, query).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("conteggio: %w", err)
	}

	rows, err := s.pool.Query(ctx, searchSQL, query, pageSize, page*pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("ricerca: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(
			&r.CodFarmaco, &r.Denominazione, &r.RagioneSociale, &r.StatoAmm,
			&r.Forme, &r.CodiceATC, &r.PaAssociati, &r.NumConfezioni,
		); err != nil {
			return nil, 0, fmt.Errorf("scan risultato: %w", err)
		}
		results = append(results, r)
	}
	return results, total, rows.Err()
}

// AutocompleteSuggestion is one item returned by the autocomplete endpoint.
type AutocompleteSuggestion struct {
	Denominazione string `json:"denominazione"`
	CodFarmaco    string `json:"codFarmaco"`
	PaAssociati   string `json:"paAssociati"`
	CodiceATC     string `json:"codiceATC"`
	RagioneSociale string `json:"ragioneSociale"`
}

const autocompleteSQL = `
SELECT DISTINCT ON (upper(denominazione))
    denominazione,
    cod_farmaco,
    coalesce(pa_associati, '')     AS pa_associati,
    coalesce(codice_atc,  '')      AS codice_atc,
    coalesce(ragione_sociale, '')  AS ragione_sociale
FROM confezioni
WHERE upper(denominazione) LIKE upper($1) || '%'
   OR upper(pa_associati)  LIKE upper($1) || '%'
   OR search_vector @@ plainto_tsquery('simple', $1)
ORDER BY upper(denominazione)
LIMIT $2
`

// Autocomplete returns up to limit suggestions matching the query prefix/FTS.
func (s *Store) Autocomplete(ctx context.Context, query string, limit int) ([]AutocompleteSuggestion, error) {
	query = strings.TrimSpace(query)
	if len(query) < 3 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, autocompleteSQL, query, limit)
	if err != nil {
		return nil, fmt.Errorf("autocomplete: %w", err)
	}
	defer rows.Close()
	var out []AutocompleteSuggestion
	for rows.Next() {
		var s AutocompleteSuggestion
		if err := rows.Scan(&s.Denominazione, &s.CodFarmaco, &s.PaAssociati, &s.CodiceATC, &s.RagioneSociale); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const detailSQL = `
SELECT
    codice_aic, cod_farmaco, cod_confezione, denominazione,
    descrizione, codice_ditta, ragione_sociale, stato_amministrativo,
    tipo_procedura, forma, codice_atc, pa_associati,
    fornitura, coalesce(link_fi,''), coalesce(link_rcp,'')
FROM confezioni
WHERE cod_farmaco = $1
ORDER BY forma, descrizione
`

// GetDrugDetail returns all packages (and their active ingredients) for a drug.
func (s *Store) GetDrugDetail(ctx context.Context, codFarmaco string) (*DrugDetail, error) {
	rows, err := s.pool.Query(ctx, detailSQL, codFarmaco)
	if err != nil {
		return nil, fmt.Errorf("dettaglio farmaco: %w", err)
	}
	defer rows.Close()

	detail := &DrugDetail{CodFarmaco: codFarmaco}
	for rows.Next() {
		var c Confezione
		if err := rows.Scan(
			&c.CodiceAIC, &c.CodFarmaco, &c.CodConfezione, &c.Denominazione,
			&c.Descrizione, &c.CodiceDitta, &c.RagioneSociale, &c.StatoAmministrativo,
			&c.TipoProcedura, &c.Forma, &c.CodiceATC, &c.PaAssociati,
			&c.Fornitura, &c.LinkFI, &c.LinkRCP,
		); err != nil {
			return nil, fmt.Errorf("scan confezione: %w", err)
		}
		if detail.Denominazione == "" {
			detail.Denominazione = c.Denominazione
			detail.RagioneSociale = c.RagioneSociale
			detail.CodiceATC = c.CodiceATC
			detail.PaAssociati = c.PaAssociati
		}
		detail.Confezioni = append(detail.Confezioni, ConfezioneDetail{Confezione: c})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(detail.Confezioni) == 0 {
		return nil, nil
	}

	// Load ATC description
	_ = s.pool.QueryRow(ctx, "SELECT descrizione FROM atc_codes WHERE codice_atc = $1", detail.CodiceATC).
		Scan(&detail.ATCDesc)

	// Load active ingredients for each package
	for i := range detail.Confezioni {
		aic := detail.Confezioni[i].CodiceAIC
		paRows, err := s.pool.Query(ctx,
			`SELECT principio_attivo, coalesce(quantita,''), coalesce(unita_misura,'')
			 FROM principi_attivi WHERE codice_aic = $1 ORDER BY principio_attivo`, aic)
		if err != nil {
			return nil, fmt.Errorf("principi attivi per %s: %w", aic, err)
		}
		for paRows.Next() {
			var pa PrincipioAttivo
			pa.CodiceAIC = aic
			if err := paRows.Scan(&pa.Nome, &pa.Quantita, &pa.UnitaMisura); err != nil {
				paRows.Close()
				return nil, err
			}
			detail.Confezioni[i].PA = append(detail.Confezioni[i].PA, pa)
		}
		paRows.Close()
	}

	// Build FormaGroups for easier template rendering
	groupIdx := map[string]int{}
	for _, cd := range detail.Confezioni {
		if _, ok := groupIdx[cd.Forma]; !ok {
			groupIdx[cd.Forma] = len(detail.FormaGroups)
			detail.FormaGroups = append(detail.FormaGroups, FormaGroup{Forma: cd.Forma})
		}
		i := groupIdx[cd.Forma]
		detail.FormaGroups[i].Confezioni = append(detail.FormaGroups[i].Confezioni, cd)
	}

	// Load shortage info for any package in this drug
	aics := make([]string, 0, len(detail.Confezioni))
	for _, cd := range detail.Confezioni {
		aics = append(aics, cd.CodiceAIC)
	}
	if shortages, err := s.GetShortagesByAIC(ctx, aics); err == nil {
		detail.Shortages = shortages
	}

	return detail, nil
}

// GetStats returns aggregate counts from the database.
func (s *Store) GetStats(ctx context.Context) (*Stats, error) {
	st := &Stats{}
	_ = s.pool.QueryRow(ctx, "SELECT count(*) FROM confezioni").Scan(&st.Confezioni)
	_ = s.pool.QueryRow(ctx, "SELECT count(*) FROM principi_attivi").Scan(&st.PrincipiAttivi)
	_ = s.pool.QueryRow(ctx, "SELECT count(*) FROM atc_codes").Scan(&st.CodicATC)
	var t time.Time
	if err := s.pool.QueryRow(ctx, "SELECT max(synced_at) FROM confezioni").Scan(&t); err == nil && !t.IsZero() {
		st.UltimoSync = t.Format("02/01/2006 15:04")
	}
	return st, nil
}

// ─── Sync log ────────────────────────────────────────────────────────────────

// StartSyncLog inserts a new sync log entry and returns its ID.
func (s *Store) StartSyncLog(ctx context.Context) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, "INSERT INTO sync_log DEFAULT VALUES RETURNING id").Scan(&id)
	return id, err
}

// FinishSyncLog updates a sync log entry on completion or error.
func (s *Store) FinishSyncLog(ctx context.Context, id, confCnt, paCnt, atcCnt int64, errMsg string) {
	status := "completed"
	if errMsg != "" {
		status = "error"
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE sync_log
		 SET finished_at=NOW(), confezioni_cnt=$2, pa_cnt=$3, atc_cnt=$4, status=$5, error_msg=$6
		 WHERE id=$1`,
		id, confCnt, paCnt, atcCnt, status, errMsg)
}

// TruncateShortages removes all rows from the shortages table.
func (s *Store) TruncateShortages(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, "TRUNCATE shortages RESTART IDENTITY")
	return err
}

var shortageCols = []string{
	"nome_medicinale", "codice_aic", "principio_attivo", "forma_dosaggio",
	"titolare_aic", "data_inizio", "fine_presunta", "equivalente",
	"motivazioni", "suggerimenti_aifa", "nota_aifa", "fascia", "codice_atc",
}

// BulkInsertShortages writes a batch of shortage rows.
func (s *Store) BulkInsertShortages(ctx context.Context, rows []Shortage) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	data := make([][]any, len(rows))
	for i, r := range rows {
		data[i] = []any{
			r.NomeMedicinale, r.CodiceAIC, r.PrincipioAttivo, r.FormaDosaggio,
			r.TitolareAIC, r.DataInizio, r.FinePresunta, r.Equivalente,
			r.Motivazioni, r.SuggerimentiAIFA, r.NotaAIFA, r.Fascia, r.CodiceATC,
		}
	}
	return s.pool.CopyFrom(ctx, pgx.Identifier{"shortages"}, shortageCols, pgx.CopyFromRows(data))
}

// GetShortagesByAIC returns all shortage entries that match one or more AIC codes.
func (s *Store) GetShortagesByAIC(ctx context.Context, aics []string) ([]Shortage, error) {
	if len(aics) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT nome_medicinale, codice_aic, principio_attivo, forma_dosaggio,
		        titolare_aic, data_inizio, fine_presunta, equivalente,
		        motivazioni, suggerimenti_aifa, nota_aifa, fascia, codice_atc
		 FROM shortages
		 WHERE codice_aic = ANY($1)
		 ORDER BY data_inizio DESC`,
		aics,
	)
	if err != nil {
		return nil, fmt.Errorf("shortages by AIC: %w", err)
	}
	defer rows.Close()
	return scanShortages(rows)
}

// ListShortages returns paginated shortage records, optionally filtered by text.
func (s *Store) ListShortages(ctx context.Context, q string, page, pageSize int) ([]Shortage, int64, error) {
	var (
		rows pgx.Rows
		total int64
		err   error
	)
	if q == "" {
		_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM shortages`).Scan(&total)
		rows, err = s.pool.Query(ctx,
			`SELECT nome_medicinale, codice_aic, principio_attivo, forma_dosaggio,
			        titolare_aic, data_inizio, fine_presunta, equivalente,
			        motivazioni, suggerimenti_aifa, nota_aifa, fascia, codice_atc
			 FROM shortages ORDER BY nome_medicinale LIMIT $1 OFFSET $2`,
			pageSize, page*pageSize,
		)
	} else {
		_ = s.pool.QueryRow(ctx,
			`SELECT count(*) FROM shortages
			 WHERE upper(nome_medicinale) LIKE upper($1)||'%'
			    OR upper(principio_attivo) LIKE upper($1)||'%'`,
			q,
		).Scan(&total)
		rows, err = s.pool.Query(ctx,
			`SELECT nome_medicinale, codice_aic, principio_attivo, forma_dosaggio,
			        titolare_aic, data_inizio, fine_presunta, equivalente,
			        motivazioni, suggerimenti_aifa, nota_aifa, fascia, codice_atc
			 FROM shortages
			 WHERE upper(nome_medicinale) LIKE upper($1)||'%'
			    OR upper(principio_attivo) LIKE upper($1)||'%'
			 ORDER BY nome_medicinale LIMIT $2 OFFSET $3`,
			q, pageSize, page*pageSize,
		)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("list shortages: %w", err)
	}
	defer rows.Close()
	out, err := scanShortages(rows)
	return out, total, err
}

func scanShortages(rows pgx.Rows) ([]Shortage, error) {
	var out []Shortage
	for rows.Next() {
		var s Shortage
		if err := rows.Scan(
			&s.NomeMedicinale, &s.CodiceAIC, &s.PrincipioAttivo, &s.FormaDosaggio,
			&s.TitolareAIC, &s.DataInizio, &s.FinePresunta, &s.Equivalente,
			&s.Motivazioni, &s.SuggerimentiAIFA, &s.NotaAIFA, &s.Fascia, &s.CodiceATC,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetPackageByAIC returns the package and its active ingredients for a single codice_aic.
// Returns nil, nil when not found.
func (s *Store) GetPackageByAIC(ctx context.Context, aic string) (*ConfezioneDetail, error) {
	var c Confezione
	err := s.pool.QueryRow(ctx, `
		SELECT codice_aic, cod_farmaco, cod_confezione, denominazione,
		       descrizione, codice_ditta, ragione_sociale, stato_amministrativo,
		       tipo_procedura, forma, codice_atc, pa_associati,
		       fornitura, coalesce(link_fi,''), coalesce(link_rcp,'')
		FROM confezioni WHERE codice_aic = $1`, aic).Scan(
		&c.CodiceAIC, &c.CodFarmaco, &c.CodConfezione, &c.Denominazione,
		&c.Descrizione, &c.CodiceDitta, &c.RagioneSociale, &c.StatoAmministrativo,
		&c.TipoProcedura, &c.Forma, &c.CodiceATC, &c.PaAssociati,
		&c.Fornitura, &c.LinkFI, &c.LinkRCP,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("package by AIC: %w", err)
	}
	cd := &ConfezioneDetail{Confezione: c}
	paRows, err := s.pool.Query(ctx,
		`SELECT principio_attivo, coalesce(quantita,''), coalesce(unita_misura,'')
		 FROM principi_attivi WHERE codice_aic = $1 ORDER BY principio_attivo`, aic)
	if err != nil {
		return nil, fmt.Errorf("PAs for AIC %s: %w", aic, err)
	}
	defer paRows.Close()
	for paRows.Next() {
		var pa PrincipioAttivo
		pa.CodiceAIC = aic
		if err := paRows.Scan(&pa.Nome, &pa.Quantita, &pa.UnitaMisura); err != nil {
			return nil, err
		}
		cd.PA = append(cd.PA, pa)
	}
	return cd, paRows.Err()
}

// ListByATC returns paginated drugs filtered by exact ATC code.
func (s *Store) ListByATC(ctx context.Context, code string, page, pageSize int) ([]SearchResult, int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(DISTINCT cod_farmaco) FROM confezioni WHERE upper(codice_atc) = upper($1)`,
		code,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count by ATC: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT
		    cod_farmaco,
		    max(denominazione)        AS denominazione,
		    max(ragione_sociale)      AS ragione_sociale,
		    max(stato_amministrativo) AS stato_amministrativo,
		    array_agg(DISTINCT forma ORDER BY forma) FILTER (WHERE forma <> '') AS forme,
		    max(codice_atc)           AS codice_atc,
		    max(pa_associati)         AS pa_associati,
		    count(*)                  AS num_confezioni
		FROM confezioni
		WHERE upper(codice_atc) = upper($1)
		GROUP BY cod_farmaco
		ORDER BY max(denominazione)
		LIMIT $2 OFFSET $3`,
		code, pageSize, page*pageSize,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list by ATC: %w", err)
	}
	defer rows.Close()
	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(
			&r.CodFarmaco, &r.Denominazione, &r.RagioneSociale, &r.StatoAmm,
			&r.Forme, &r.CodiceATC, &r.PaAssociati, &r.NumConfezioni,
		); err != nil {
			return nil, 0, err
		}
		results = append(results, r)
	}
	return results, total, rows.Err()
}

// GetATCCode looks up the description for an ATC code. Returns nil when not found.
func (s *Store) GetATCCode(ctx context.Context, code string) (*ATCCode, error) {
	var a ATCCode
	err := s.pool.QueryRow(ctx,
		`SELECT codice_atc, descrizione FROM atc_codes WHERE upper(codice_atc) = upper($1)`,
		code,
	).Scan(&a.Codice, &a.Descrizione)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ATC code: %w", err)
	}
	return &a, nil
}

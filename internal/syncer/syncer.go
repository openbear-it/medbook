// Package syncer downloads and imports AIFA drug registry CSV files into PostgreSQL.
//
// Data sources (updated daily/monthly by AIFA):
//   - https://drive.aifa.gov.it/farmaci/confezioni_fornitura.csv  (~158 K packages)
//   - https://drive.aifa.gov.it/farmaci/PA_confezioni.csv         (~336 K active ingredients)
//   - https://drive.aifa.gov.it/farmaci/atc.csv                   (~7 K ATC codes)
//   - https://www.aifa.gov.it/…/elenco_medicinali_carenti.csv     (shortages, daily)
//   - https://www.aifa.gov.it/…/Lista_farmaci_equivalenti.csv     (prices+equivalents, monthly)
//   - https://www.aifa.gov.it/…/Lista-farmaci-orfani-2024.csv     (orphan drugs, annual)
package syncer

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"

	"medbook/internal/db"
)
const (
	urlConfezioni = "https://drive.aifa.gov.it/farmaci/confezioni_fornitura.csv"
	urlPA         = "https://drive.aifa.gov.it/farmaci/PA_confezioni.csv"
	urlATC        = "https://drive.aifa.gov.it/farmaci/atc.csv"

	// AIFA shortage list — stable URL, updated daily.
	urlShortages = "https://www.aifa.gov.it/documents/20142/847339/elenco_medicinali_carenti.csv"

	// Lista Trasparenza (prezzi + equivalenti) — stable document ID, updated monthly.
	urlPrezziEquivalenti = "https://www.aifa.gov.it/documents/20142/825643/Lista_farmaci_equivalenti.csv"

	// Lista Medicinali Orfani — stable document ID, updated annually.
	urlFarmaciOrfani = "https://www.aifa.gov.it/documents/20142/842593/Lista-farmaci-orfani-2024.csv"

	batchSize = 5000
)

// Syncer orchestrates the CSV download and DB import.
type Syncer struct {
	store  *db.Store
	client *http.Client
}

// New creates a Syncer with a generous HTTP timeout for large files.
func New(store *db.Store) *Syncer {
	return &Syncer{
		store:  store,
		client: &http.Client{Timeout: 15 * time.Minute},
	}
}

// Run executes the full sync pipeline: truncate → import 3 CSV files + auxiliary lists.
func (s *Syncer) Run(ctx context.Context) error {
	sep := strings.Repeat("─", 55)
	fmt.Printf("\nMedBook Sync — Aggiornamento Banca Dati Farmaci AIFA\n%s\n", sep)

	logID, _ := s.store.StartSyncLog(ctx)

	// ── 1. Truncate ────────────────────────────────────────
	fmt.Print("[1/4] Pulizia tabelle esistenti... ")
	if err := s.store.TruncateTables(ctx); err != nil {
		return s.fail(ctx, logID, 0, 0, 0, fmt.Errorf("truncate: %w", err))
	}
	fmt.Println("✓")

	// ── 2. Confezioni ──────────────────────────────────────
	fmt.Println("[2/4] Scaricando confezioni_fornitura.csv ...")
	confCnt, err := s.syncConfezioni(ctx)
	if err != nil {
		return s.fail(ctx, logID, confCnt, 0, 0, err)
	}
	fmt.Printf("      ✓ %s confezioni importate\n", fmtN(confCnt))

	// ── 3. Principi attivi ─────────────────────────────────
	fmt.Println("[3/4] Scaricando PA_confezioni.csv ...")
	paCnt, err := s.syncPA(ctx)
	if err != nil {
		return s.fail(ctx, logID, confCnt, paCnt, 0, err)
	}
	fmt.Printf("      ✓ %s principi attivi importati\n", fmtN(paCnt))

	// ── 4. ATC codes ───────────────────────────────────────
	fmt.Println("[4/4] Scaricando atc.csv ...")
	atcCnt, err := s.syncATC(ctx)
	if err != nil {
		return s.fail(ctx, logID, confCnt, paCnt, atcCnt, err)
	}
	fmt.Printf("      ✓ %s codici ATC importati\n", fmtN(atcCnt))

	s.store.FinishSyncLog(ctx, logID, confCnt, paCnt, atcCnt, "")
	fmt.Printf("%s\n✓ Sincronizzazione completata\n\n", sep)

	// ── Auxiliary lists (non-fatal: errors are warnings, not failures) ─

	fmt.Println("[5/5] Scaricando elenco_medicinali_carenti.csv ...")
	shortCnt, err := s.syncShortages(ctx)
	if err != nil {
		fmt.Printf("      ⚠ carenze non sincronizzate: %v\n", err)
	} else {
		fmt.Printf("      ✓ %s farmaci carenti importati\n", fmtN(shortCnt))
	}

	fmt.Println("[6/6] Scaricando Lista_farmaci_equivalenti.csv ...")
	prezziCnt, err := s.syncPrezziEquivalenti(ctx)
	if err != nil {
		fmt.Printf("      ⚠ lista trasparenza non sincronizzata: %v\n", err)
	} else {
		fmt.Printf("      ✓ %s prezzi equivalenti importati\n", fmtN(prezziCnt))
	}

	fmt.Println("[7/7] Scaricando Lista-farmaci-orfani.csv ...")
	orfaniCnt, err := s.syncFarmaciOrfani(ctx)
	if err != nil {
		fmt.Printf("      ⚠ farmaci orfani non sincronizzati: %v\n", err)
	} else {
		fmt.Printf("      ✓ %s farmaci orfani importati\n", fmtN(orfaniCnt))
	}
	fmt.Printf("%s\n✓ Sincronizzazione completata\n\n", sep)
	return nil
}

// SyncShortages refreshes only the shortage list (faster than a full sync).
func (s *Syncer) SyncShortages(ctx context.Context) (int64, error) {
	fmt.Println("MedBook — Aggiornamento carenze farmaci AIFA")
	fmt.Println("Scaricando elenco_medicinali_carenti.csv ...")
	n, err := s.syncShortages(ctx)
	if err != nil {
		return 0, err
	}
	fmt.Printf("✓ %s farmaci carenti importati\n", fmtN(n))
	return n, nil
}

// SyncPrezziEquivalenti refreshes only the Lista Trasparenza pricing data.
func (s *Syncer) SyncPrezziEquivalenti(ctx context.Context) (int64, error) {
	fmt.Println("MedBook — Aggiornamento Lista Trasparenza (prezzi equivalenti)")
	fmt.Println("Scaricando Lista_farmaci_equivalenti.csv ...")
	n, err := s.syncPrezziEquivalenti(ctx)
	if err != nil {
		return 0, err
	}
	fmt.Printf("✓ %s prezzi importati\n", fmtN(n))
	return n, nil
}

// SyncFarmaciOrfani refreshes only the orphan drugs list.
func (s *Syncer) SyncFarmaciOrfani(ctx context.Context) (int64, error) {
	fmt.Println("MedBook — Aggiornamento Lista Medicinali Orfani")
	fmt.Println("Scaricando Lista-farmaci-orfani.csv ...")
	n, err := s.syncFarmaciOrfani(ctx)
	if err != nil {
		return 0, err
	}
	fmt.Printf("✓ %s farmaci orfani importati\n", fmtN(n))
	return n, nil
}

func (s *Syncer) fail(ctx context.Context, logID, c, p, a int64, err error) error {
	s.store.FinishSyncLog(ctx, logID, c, p, a, err.Error())
	return err
}

// ─── CSV importers ────────────────────────────────────────────────────────────

func (s *Syncer) syncConfezioni(ctx context.Context) (int64, error) {
	return s.streamCSV(ctx, urlConfezioni, func(r []string) (any, error) {
		if len(r) < 15 {
			return nil, nil // skip malformed
		}
		return db.Confezione{
			CodiceAIC:           clean(r[0]),
			CodFarmaco:          clean(r[1]),
			CodConfezione:       clean(r[2]),
			Denominazione:       clean(r[3]),
			Descrizione:         clean(r[4]),
			CodiceDitta:         clean(r[5]),
			RagioneSociale:      clean(r[6]),
			StatoAmministrativo: clean(r[7]),
			TipoProcedura:       clean(r[8]),
			Forma:               clean(r[9]),
			CodiceATC:           clean(r[10]),
			PaAssociati:         clean(r[11]),
			Fornitura:           clean(r[12]),
			LinkFI:              clean(r[13]),
			LinkRCP:             clean(r[14]),
		}, nil
	}, func(ctx context.Context, batch []any) (int64, error) {
		rows := make([]db.Confezione, len(batch))
		for i, v := range batch {
			rows[i] = v.(db.Confezione)
		}
		return s.store.BulkInsertConfezioni(ctx, rows)
	})
}

func (s *Syncer) syncPA(ctx context.Context) (int64, error) {
	// Il CSV di AIFA contiene righe duplicate: deduplicare usando una chiave
	// (codice_aic, principio_attivo) per evitare violazioni di PRIMARY KEY.
	seen := make(map[string]struct{}, 350000)

	return s.streamCSV(ctx, urlPA, func(r []string) (any, error) {
		if len(r) < 4 {
			return nil, nil
		}
		nome := clean(r[1])
		if nome == "" || nome == "N.D." {
			return nil, nil // skip unknown
		}
		key := clean(r[0]) + "\x00" + nome
		if _, dup := seen[key]; dup {
			return nil, nil // skip duplicate
		}
		seen[key] = struct{}{}
		return db.PrincipioAttivo{
			CodiceAIC:   clean(r[0]),
			Nome:        nome,
			Quantita:    clean(r[2]),
			UnitaMisura: clean(r[3]),
		}, nil
	}, func(ctx context.Context, batch []any) (int64, error) {
		rows := make([]db.PrincipioAttivo, len(batch))
		for i, v := range batch {
			rows[i] = v.(db.PrincipioAttivo)
		}
		return s.store.BulkInsertPA(ctx, rows)
	})
}

func (s *Syncer) syncATC(ctx context.Context) (int64, error) {
	return s.streamCSV(ctx, urlATC, func(r []string) (any, error) {
		if len(r) < 2 {
			return nil, nil
		}
		return db.ATCCode{Codice: clean(r[0]), Descrizione: clean(r[1])}, nil
	}, func(ctx context.Context, batch []any) (int64, error) {
		rows := make([]db.ATCCode, len(batch))
		for i, v := range batch {
			rows[i] = v.(db.ATCCode)
		}
		return s.store.BulkInsertATC(ctx, rows)
	})
}

func (s *Syncer) syncShortages(ctx context.Context) (int64, error) {
	if err := s.store.TruncateShortages(ctx); err != nil {
		return 0, fmt.Errorf("truncate shortages: %w", err)
	}
	// The carenze CSV has 2 preamble rows before the header and is ISO-8859-1 encoded.
	return s.streamCSVSkip(ctx, urlShortages, 2, true, func(r []string) (any, error) {
		if len(r) < 13 {
			return nil, nil
		}
		name := clean(r[0])
		if name == "" {
			return nil, nil
		}
		return db.Shortage{
			NomeMedicinale:   name,
			CodiceAIC:        clean(r[1]),
			PrincipioAttivo:  clean(r[2]),
			FormaDosaggio:    clean(r[3]),
			TitolareAIC:      clean(r[4]),
			DataInizio:       clean(r[5]),
			FinePresunta:     clean(r[6]),
			Equivalente:      clean(r[7]),
			Motivazioni:      clean(r[8]),
			SuggerimentiAIFA: clean(r[9]),
			NotaAIFA:         clean(r[10]),
			Fascia:           clean(r[11]),
			CodiceATC:        clean(r[12]),
		}, nil
	}, func(ctx context.Context, batch []any) (int64, error) {
		rows := make([]db.Shortage, len(batch))
		for i, v := range batch {
			rows[i] = v.(db.Shortage)
		}
		return s.store.BulkInsertShortages(ctx, rows)
	})
}

func (s *Syncer) syncPrezziEquivalenti(ctx context.Context) (int64, error) {
	if err := s.store.TruncatePrezziEquivalenti(ctx); err != nil {
		return 0, fmt.Errorf("truncate prezzi: %w", err)
	}
	// Lista Trasparenza CSV: semicolon-delimited, ISO-8859-1, 1 header row.
	// Columns: PA, Confezione rif., ATC, AIC, Farmaco, Confezione, Ditta,
	//          Prezzo SSN, Prezzo Pubblico (+ date in header), Differenza, Nota, Codice gruppo
	seen := make(map[string]struct{}, 30000)
	return s.streamCSVSkip(ctx, urlPrezziEquivalenti, 0, true, func(r []string) (any, error) {
		if len(r) < 12 {
			return nil, nil
		}
		aic := fmt.Sprintf("%09s", clean(r[3]))
		if aic == "         " || aic == "" {
			return nil, nil
		}
		if _, dup := seen[aic]; dup {
			return nil, nil
		}
		seen[aic] = struct{}{}
		return db.PrezzoEquivalente{
			CodiceAIC:       aic,
			PrincipioAttivo: clean(r[0]),
			ATC:             clean(r[2]),
			NomeFarmaco:     clean(r[4]),
			Confezione:      clean(r[5]),
			Ditta:           clean(r[6]),
			PrezzoSSN:       clean(r[7]),
			PrezzoPubblico:  clean(r[8]),
			Differenza:      clean(r[9]),
			Nota:            clean(r[10]),
			CodiceGruppo:    clean(r[11]),
		}, nil
	}, func(ctx context.Context, batch []any) (int64, error) {
		rows := make([]db.PrezzoEquivalente, len(batch))
		for i, v := range batch {
			rows[i] = v.(db.PrezzoEquivalente)
		}
		return s.store.BulkInsertPrezziEquivalenti(ctx, rows)
	})
}

func (s *Syncer) syncFarmaciOrfani(ctx context.Context) (int64, error) {
	if err := s.store.TruncateFarmaciOrfani(ctx); err != nil {
		return 0, fmt.Errorf("truncate orfani: %w", err)
	}
	// Orfani CSV: semicolon-delimited, ISO-8859-1, 1 header row.
	// Columns: Descrizione farmaco, Data inizio reg, AIC 6 digit, ATC, PA, Classe, Data fine
	seen := make(map[string]struct{}, 1000)
	return s.streamCSVSkip(ctx, urlFarmaciOrfani, 0, true, func(r []string) (any, error) {
		if len(r) < 6 {
			return nil, nil
		}
		desc := clean(r[0])
		if desc == "" {
			return nil, nil
		}
		aic6 := fmt.Sprintf("%06s", clean(r[2]))
		if _, dup := seen[aic6]; dup {
			return nil, nil
		}
		seen[aic6] = struct{}{}
		dataFine := ""
		if len(r) >= 7 {
			dataFine = clean(r[6])
		}
		return db.FarmacoOrfano{
			CodiceAIC6:      aic6,
			Descrizione:     desc,
			DataInizio:      clean(r[1]),
			ATC:             clean(r[3]),
			PrincipioAttivo: clean(r[4]),
			Classe:          clean(r[5]),
			DataFine:        dataFine,
		}, nil
	}, func(ctx context.Context, batch []any) (int64, error) {
		rows := make([]db.FarmacoOrfano, len(batch))
		for i, v := range batch {
			rows[i] = v.(db.FarmacoOrfano)
		}
		return s.store.BulkInsertFarmaciOrfani(ctx, rows)
	})
}

// ─── Generic CSV streaming helper ────────────────────────────────────────────

type parseFn func(record []string) (any, error)
type flushFn func(ctx context.Context, batch []any) (int64, error)

// streamCSV downloads a CSV, parses each row with parseFn, and flushes batches with flushFn.
// Returns the total number of rows successfully inserted.
func (s *Syncer) streamCSV(ctx context.Context, url string, parse parseFn, flush flushFn) (int64, error) {
	return s.streamCSVSkip(ctx, url, 0, false, parse, flush)
}

// streamCSVSkip is like streamCSV but skips `skip` additional lines after the header.
// Set latin1=true for CSVs encoded in ISO-8859-1 (e.g. the AIFA carenze file).
func (s *Syncer) streamCSVSkip(ctx context.Context, url string, skip int, latin1 bool, parse parseFn, flush flushFn) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "MedBook/1.0 (offline drug database; github.com/daniele/medbook)")

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("server returned %d for %s", resp.StatusCode, url)
	}

	var bodyReader io.Reader = resp.Body
	if latin1 {
		bodyReader = transform.NewReader(resp.Body, charmap.ISO8859_1.NewDecoder())
	}

	r := csv.NewReader(bodyReader)
	r.Comma = ';'
	r.LazyQuotes = true
	r.FieldsPerRecord = -1 // allow variable fields

	// Skip preamble + header row(s)
	for i := 0; i <= skip; i++ {
		if _, err := r.Read(); err != nil {
			return 0, fmt.Errorf("skip row %d: %w", i, err)
		}
	}

	var (
		batch   []any
		total   int64
		lineNum int64
	)

	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Skip lines that can't be parsed (e.g., embedded newlines in notes)
			continue
		}
		lineNum++

		parsed, err := parse(record)
		if err != nil {
			return total, fmt.Errorf("parse riga %d: %w", lineNum, err)
		}
		if parsed == nil {
			continue
		}

		batch = append(batch, parsed)
		if len(batch) >= batchSize {
			n, err := flush(ctx, batch)
			if err != nil {
				return total, fmt.Errorf("flush batch a riga %d: %w", lineNum, err)
			}
			total += n
			batch = batch[:0]
			fmt.Printf("      %s righe elaborate...\r", fmtN(total))
		}
	}

	// Flush remaining rows
	if len(batch) > 0 {
		n, err := flush(ctx, batch)
		if err != nil {
			return total, fmt.Errorf("flush batch finale: %w", err)
		}
		total += n
	}

	return total, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func clean(s string) string { return strings.TrimSpace(s) }

func fmtN(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	start := len(s) % 3
	if start > 0 {
		b.WriteString(s[:start])
	}
	for i := start; i < len(s); i += 3 {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

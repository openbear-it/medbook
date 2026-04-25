// Package web provides the local HTTP server for drug consultation.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"strings"

	"medbook/internal/db"
)

//go:embed assets
var assets embed.FS

const pageSize = 20

// Server wraps the HTTP mux and shared dependencies.
type Server struct {
	store     *db.Store
	port      string
	templates *template.Template
}

// NewServer prepares the HTTP server and parses embedded templates.
func NewServer(store *db.Store, port string) *Server {
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"add":      func(a, b int) int { return a + b },
		"sub":      func(a, b int) int { return a - b },
		"seq":      pageSeq,
		"join":     strings.Join,
		"hasLinks": func(fi, rcp string) bool { return fi != "" || rcp != "" },
		"alpha": func() []string {
			s := make([]string, 26)
			for i := range s {
				s[i] = string(rune('A' + i))
			}
			return s
		},
		"uniqueDitte": func(items []db.PrezzoEquivalente) int {
			seen := map[string]struct{}{}
			for _, it := range items {
				if it.Ditta != "" {
					seen[it.Ditta] = struct{}{}
				}
			}
			return len(seen)
		},
		"firstPA": func(s string) string {
			for _, sep := range []string{" + ", "+", ", ", ","} {
				if i := strings.Index(s, sep); i > 0 {
					return strings.TrimSpace(s[:i])
				}
			}
			return s
		},
		"splitPAList": func(s string) []string {
			s = strings.TrimSpace(s)
			if s == "" {
				return nil
			}
			for _, sep := range []string{" + ", " / ", "/"} {
				if strings.Contains(s, sep) {
					var out []string
					for _, p := range strings.Split(s, sep) {
						if t := strings.TrimSpace(p); t != "" {
							out = append(out, t)
						}
					}
					return out
				}
			}
			return []string{s}
		},
		"badgeClass": func(stato string) string {
			switch strings.ToUpper(stato) {
			case "AUTORIZZATA":
				return "badge-green"
			case "REVOCATA", "SOSPESA":
				return "badge-red"
			default:
				return "badge-gray"
			}
		},
		"cardClass": func(stato string) string {
			switch strings.ToUpper(stato) {
			case "AUTORIZZATA":
				return "drug-card drug-card--ok"
			case "REVOCATA", "SOSPESA":
				return "drug-card drug-card--ko"
			default:
				return "drug-card"
			}
		},
	}).ParseFS(assets, "assets/templates/*.html"))

	return &Server{store: store, port: port, templates: tmpl}
}

// Listen starts the HTTP server and blocks until an error occurs.
func (s *Server) Listen() error {
	mux := http.NewServeMux()
	// ── English routes (canonical) ───────────────────────────────────────────
	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /drug/{codFarmaco}", s.handleDrug)
	mux.HandleFunc("GET /drugs", s.handleDrugsAll)
	mux.HandleFunc("GET /drugs/ingredient", s.handleFarmaciPA)
	mux.HandleFunc("GET /drugs/company", s.handleFarmaciAzienda)
	mux.HandleFunc("GET /equivalents/{codiceGruppo}", s.handleEquivalenti)
	// ── Italian routes (redirect to English) ─────────────────────────────────
	mux.HandleFunc("GET /medbook", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/search"+"?"+r.URL.RawQuery, http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /farmaco/{codFarmaco}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/drug/"+r.PathValue("codFarmaco"), http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /farmaci/pa", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/drugs/ingredient?"+r.URL.RawQuery, http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /farmaci/azienda", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/drugs/company?"+r.URL.RawQuery, http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /equivalenti/{codiceGruppo}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/equivalents/"+r.PathValue("codiceGruppo"), http.StatusMovedPermanently)
	})
	// ── API ───────────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /api/search", s.handleAPISearch)
	mux.HandleFunc("GET /api/autocomplete", s.handleAutocomplete)
	mux.HandleFunc("GET /api/autocomplete/pa", s.handleAutocompletePA)
	mux.HandleFunc("GET /api/autocomplete/company", s.handleAutocompleteCompany)
	mux.HandleFunc("GET /api/drug/{codFarmaco}", s.handleAPIDrug)
	mux.HandleFunc("GET /api/package/{codiceAIC}", s.handleAPIPackage)
	mux.HandleFunc("GET /api/atc/{code}", s.handleAPIByATC)
	mux.HandleFunc("GET /api/stats", s.handleAPIStats)
	mux.HandleFunc("GET /api/shortages", s.handleAPIShortages)
	mux.HandleFunc("GET /api/equivalents/{codiceGruppo}", s.handleAPIEquivalents)
	mux.HandleFunc("GET /api/pa/{nome}", s.handleAPIByPA)
	mux.HandleFunc("GET /api/company/{nome}", s.handleAPIByCompany)
	mux.HandleFunc("GET /api/openapi.json", s.handleOpenAPISpec)
	mux.HandleFunc("GET /api/docs", s.handleSwaggerUI)
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	return http.ListenAndServe(":"+s.port, mux)
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/search", http.StatusFound)
}

type searchPageData struct {
	Query   string
	PA      string
	Azienda string
	Results []db.SearchResult
	Total   int64
	Page    int
	Pages   int
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	pa := strings.TrimSpace(r.URL.Query().Get("pa"))
	azienda := strings.TrimSpace(r.URL.Query().Get("azienda"))
	page := intParam(r, "p", 0)

	data := searchPageData{Query: q, PA: pa, Azienda: azienda, Page: page}

	if q != "" || pa != "" || azienda != "" {
		results, total, err := s.store.SearchFarmaciFiltered(r.Context(), db.SearchParams{
			Query:   q,
			PA:      pa,
			Azienda: azienda,
		}, page, pageSize)
		if err != nil {
			http.Error(w, "Errore nella ricerca: "+err.Error(), http.StatusInternalServerError)
			return
		}
		data.Results = results
		data.Total = total
		data.Pages = int(math.Ceil(float64(total) / float64(pageSize)))
	}

	s.render(w, "search.html", data)
}

type detailPageData struct {
	Detail *db.DrugDetail
	Query  string
}

func (s *Server) handleDrug(w http.ResponseWriter, r *http.Request) {
	cod := r.PathValue("codFarmaco")
	if cod == "" {
		http.NotFound(w, r)
		return
	}

	detail, err := s.store.GetDrugDetail(r.Context(), cod)
	if err != nil {
		http.Error(w, "Errore: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if detail == nil {
		http.NotFound(w, r)
		return
	}

	s.render(w, "drug.html", detailPageData{
		Detail: detail,
		Query:  r.URL.Query().Get("q"),
	})
}

// handleAPISearch returns JSON search results. Supports q (fulltext), pa (principio attivo), azienda (company) filters.
func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	pa := strings.TrimSpace(r.URL.Query().Get("pa"))
	azienda := strings.TrimSpace(r.URL.Query().Get("azienda"))
	page := intParam(r, "p", 0)
	if q == "" && pa == "" && azienda == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[],"total":0}`))
		return
	}

	results, total, err := s.store.SearchFarmaciFiltered(r.Context(), db.SearchParams{
		Query:   q,
		PA:      pa,
		Azienda: azienda,
	}, page, pageSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"results": results,
		"total":   total,
		"page":    page,
	})
}

// handleAutocomplete returns up to 5 suggestions for live search (min 3 chars).
func (s *Server) handleAutocomplete(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "application/json")
	if len(q) < 3 {
		w.Write([]byte(`[]`))
		return
	}
	suggestions, err := s.store.Autocomplete(r.Context(), q, 5)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if suggestions == nil {
		suggestions = []db.AutocompleteSuggestion{}
	}
	json.NewEncoder(w).Encode(suggestions)
}

// handleAutocompletePA returns up to 8 distinct principio_attivo names matching prefix q.
func (s *Server) handleAutocompletePA(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "application/json")
	if len(q) < 2 {
		w.Write([]byte(`[]`))
		return
	}
	results, err := s.store.AutocompletePA(r.Context(), q, 8)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []string{}
	}
	json.NewEncoder(w).Encode(results)
}

// handleAutocompleteCompany returns up to 8 distinct ragione_sociale names matching substring q.
func (s *Server) handleAutocompleteCompany(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "application/json")
	if len(q) < 2 {
		w.Write([]byte(`[]`))
		return
	}
	results, err := s.store.AutocompleteCompany(r.Context(), q, 8)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []string{}
	}
	json.NewEncoder(w).Encode(results)
}

// handleOpenAPISpec serves the OpenAPI 3.0 specification as JSON.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write([]byte(openAPISpec))
}

// handleSwaggerUI serves an embedded Swagger UI page.
func (s *Server) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	s.render(w, "swagger.html", nil)
}

// handleAPIDrug returns full JSON detail for a drug (all packages + active ingredients).
func (s *Server) handleAPIDrug(w http.ResponseWriter, r *http.Request) {
	cod := r.PathValue("codFarmaco")
	w.Header().Set("Content-Type", "application/json")
	detail, err := s.store.GetDrugDetail(r.Context(), cod)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if detail == nil {
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(detail)
}

// handleAPIPackage returns JSON for a single package by codice_aic.
func (s *Server) handleAPIPackage(w http.ResponseWriter, r *http.Request) {
	aic := r.PathValue("codiceAIC")
	w.Header().Set("Content-Type", "application/json")
	pkg, err := s.store.GetPackageByAIC(r.Context(), aic)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if pkg == nil {
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(pkg)
}

// handleAPIByATC returns drugs under a given ATC code together with its description.
func (s *Server) handleAPIByATC(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	page := intParam(r, "p", 0)
	w.Header().Set("Content-Type", "application/json")
	atcEntry, err := s.store.GetATCCode(r.Context(), code)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	results, total, err := s.store.ListByATC(r.Context(), code, page, pageSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []db.SearchResult{}
	}
	var atcDesc string
	if atcEntry != nil {
		atcDesc = atcEntry.Descrizione
	}
	json.NewEncoder(w).Encode(map[string]any{
		"code":        code,
		"description": atcDesc,
		"total":       total,
		"page":        page,
		"results":     results,
	})
}

// handleAPIStats returns JSON database statistics.
func (s *Server) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleAPIShortages returns the current shortage list, with optional text filter.
func (s *Server) handleAPIShortages(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page := intParam(r, "p", 0)
	w.Header().Set("Content-Type", "application/json")
	results, total, err := s.store.ListShortages(r.Context(), q, page, pageSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []db.Shortage{}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"total":   total,
		"page":    page,
		"results": results,
	})
}

type listaPageData struct {
	Tipo    string
	Titolo  string
	Icona   string
	BaseURL string
	Query   string
	Results []db.SearchResult
	Total   int64
	Page    int
	Pages   int
}

// handleFarmaciPA renders the paginated list of drugs for a given active ingredient.
func (s *Server) handleFarmaciPA(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page := intParam(r, "p", 0)
	data := listaPageData{
		Tipo: "pa", Titolo: "Principio attivo", Icona: "🧪",
		BaseURL: "/drugs/ingredient", Query: q, Page: page,
	}
	if q != "" {
		results, total, err := s.store.SearchByPA(r.Context(), q, page, pageSize)
		if err != nil {
			http.Error(w, "Errore: "+err.Error(), http.StatusInternalServerError)
			return
		}
		data.Results = results
		data.Total = total
		data.Pages = int(math.Ceil(float64(total) / float64(pageSize)))
	}
	s.render(w, "list.html", data)
}

// handleFarmaciAzienda renders the paginated list of drugs for a given company.
func (s *Server) handleFarmaciAzienda(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page := intParam(r, "p", 0)
	data := listaPageData{
		Tipo: "azienda", Titolo: "Azienda farmaceutica", Icona: "🏭",
		BaseURL: "/drugs/company", Query: q, Page: page,
	}
	if q != "" {
		results, total, err := s.store.SearchByCompany(r.Context(), q, page, pageSize)
		if err != nil {
			http.Error(w, "Errore: "+err.Error(), http.StatusInternalServerError)
			return
		}
		data.Results = results
		data.Total = total
		data.Pages = int(math.Ceil(float64(total) / float64(pageSize)))
	}
	s.render(w, "list.html", data)
}

type browsePageData struct {
	Query   string
	Letter  string
	Results []db.SearchResult
	Total   int64
	Page    int
	Pages   int
}

// handleDrugsAll renders the full catalogue paginated A-Z, with optional text and letter filters.
func (s *Server) handleDrugsAll(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	letter := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("l")))
	if len(letter) > 1 {
		letter = letter[:1]
	}
	page := intParam(r, "p", 0)

	results, total, err := s.store.ListDrugs(r.Context(), q, letter, page, pageSize)
	if err != nil {
		http.Error(w, "Errore: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "browse.html", browsePageData{
		Query:   q,
		Letter:  letter,
		Results: results,
		Total:   total,
		Page:    page,
		Pages:   int(math.Ceil(float64(total) / float64(pageSize))),
	})
}

type equivalentiPageData struct {
	CodiceGruppo string
	Results      []db.PrezzoEquivalente
}

// handleEquivalenti renders the HTML equivalents page for a given group.
func (s *Server) handleEquivalenti(w http.ResponseWriter, r *http.Request) {
	codiceGruppo := r.PathValue("codiceGruppo")
	results, err := s.store.GetEquivalentiByGruppo(r.Context(), codiceGruppo)
	if err != nil {
		http.Error(w, "Errore: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []db.PrezzoEquivalente{}
	}
	s.render(w, "equivalents.html", equivalentiPageData{
		CodiceGruppo: codiceGruppo,
		Results:      results,
	})
}

// handleAPIEquivalents returns all drugs in the same Lista Trasparenza equivalence group.
func (s *Server) handleAPIEquivalents(w http.ResponseWriter, r *http.Request) {
	codiceGruppo := r.PathValue("codiceGruppo")
	w.Header().Set("Content-Type", "application/json")
	results, err := s.store.GetEquivalentiByGruppo(r.Context(), codiceGruppo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []db.PrezzoEquivalente{}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"codiceGruppo": codiceGruppo,
		"results":      results,
	})
}

// handleAPIByPA returns paginated drugs that contain the specified active ingredient.
func (s *Server) handleAPIByPA(w http.ResponseWriter, r *http.Request) {
	nome := r.PathValue("nome")
	page := intParam(r, "p", 0)
	w.Header().Set("Content-Type", "application/json")
	results, total, err := s.store.SearchByPA(r.Context(), nome, page, pageSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []db.SearchResult{}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"principioAttivo": nome,
		"total":           total,
		"page":            page,
		"results":         results,
	})
}

// handleAPIByCompany returns paginated drugs for a given pharmaceutical company (substring match).
func (s *Server) handleAPIByCompany(w http.ResponseWriter, r *http.Request) {
	nome := r.PathValue("nome")
	page := intParam(r, "p", 0)
	w.Header().Set("Content-Type", "application/json")
	results, total, err := s.store.SearchByCompany(r.Context(), nome, page, pageSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if results == nil {
		results = []db.SearchResult{}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"azienda": nome,
		"total":   total,
		"page":    page,
		"results": results,
	})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		fmt.Fprintf(w, "Errore rendering: %v", err)
	}
}

func intParam(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	var n int
	fmt.Sscanf(v, "%d", &n)
	if n < 0 {
		return 0
	}
	return n
}

// pageSeq returns a slice of page indices for pagination controls.
func pageSeq(current, total int) []int {
	const window = 5
	start := current - window/2
	if start < 0 {
		start = 0
	}
	end := start + window
	if end > total {
		end = total
	}
	pages := make([]int, end-start)
	for i := range pages {
		pages[i] = start + i
	}
	return pages
}

// openAPISpec is the OpenAPI 3.0 JSON description of the MedBook REST API.
const openAPISpec = `{
  "openapi": "3.0.3",
  "info": {
    "title": "MedBook API",
    "description": "API per la consultazione offline dei farmaci AIFA.",
    "version": "1.0.0",
    "license": { "name": "CC-BY 4.0", "url": "https://creativecommons.org/licenses/by/4.0/" }
  },
  "servers": [{ "url": "/", "description": "Istanza locale" }],
  "paths": {
    "/api/search": {
      "get": {
        "summary": "Ricerca farmaci",
        "description": "Ricerca avanzata: fulltext + filtri opzionali per principio attivo e azienda farmaceutica.",
        "parameters": [
          { "name": "q",       "in": "query", "required": false, "description": "Testo di ricerca (denominazione, AIC, ATC)", "schema": { "type": "string" } },
          { "name": "pa",      "in": "query", "required": false, "description": "Principio attivo (sottostringa, case-insensitive)", "schema": { "type": "string" } },
          { "name": "azienda", "in": "query", "required": false, "description": "Azienda farmaceutica (sottostringa, case-insensitive)", "schema": { "type": "string" } },
          { "name": "p",       "in": "query", "required": false, "description": "Pagina (0-based)", "schema": { "type": "integer", "default": 0 } }
        ],
        "responses": {
          "200": {
            "description": "Risultati di ricerca",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "total": { "type": "integer" },
                    "page":  { "type": "integer" },
                    "results": {
                      "type": "array",
                      "items": { "$ref": "#/components/schemas/SearchResult" }
                    }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/api/autocomplete": {
      "get": {
        "summary": "Suggerimenti autocomplete",
        "description": "Restituisce fino a 5 suggerimenti per la ricerca live. Richiede almeno 3 caratteri.",
        "parameters": [
          { "name": "q", "in": "query", "required": true, "description": "Prefisso di ricerca (min 3 caratteri)", "schema": { "type": "string", "minLength": 3 } }
        ],
        "responses": {
          "200": {
            "description": "Lista di suggerimenti",
            "content": {
              "application/json": {
                "schema": {
                  "type": "array",
                  "items": { "$ref": "#/components/schemas/AutocompleteSuggestion" }
                }
              }
            }
          }
        }
      }
    },
    "/api/autocomplete/pa": {
      "get": {
        "summary": "Suggerimenti principi attivi",
        "description": "Restituisce fino a 8 nomi distinti di principi attivi che iniziano con il prefisso fornito. Richiede almeno 2 caratteri.",
        "parameters": [
          { "name": "q", "in": "query", "required": true, "description": "Prefisso (min 2 caratteri)", "schema": { "type": "string", "minLength": 2 } }
        ],
        "responses": {
          "200": {
            "description": "Array di nomi",
            "content": { "application/json": { "schema": { "type": "array", "items": { "type": "string" } } } }
          }
        }
      }
    },
    "/api/autocomplete/company": {
      "get": {
        "summary": "Suggerimenti aziende farmaceutiche",
        "description": "Restituisce fino a 8 nomi distinti di aziende che contengono la sottostringa fornita. Richiede almeno 2 caratteri.",
        "parameters": [
          { "name": "q", "in": "query", "required": true, "description": "Sottostringa (min 2 caratteri)", "schema": { "type": "string", "minLength": 2 } }
        ],
        "responses": {
          "200": {
            "description": "Array di nomi",
            "content": { "application/json": { "schema": { "type": "array", "items": { "type": "string" } } } }
          }
        }
      }
    },
    "/api/drug/{codFarmaco}": {
      "get": {
        "summary": "Dettaglio farmaco (JSON)",
        "description": "Tutte le confezioni e i principi attivi di un farmaco.",
        "parameters": [
          { "name": "codFarmaco", "in": "path", "required": true, "schema": { "type": "string" } }
        ],
        "responses": {
          "200": { "description": "DrugDetail JSON" },
          "404": { "description": "Farmaco non trovato" }
        }
      }
    },
    "/api/package/{codiceAIC}": {
      "get": {
        "summary": "Singola confezione (JSON)",
        "description": "Dati di una confezione con i principi attivi.",
        "parameters": [
          { "name": "codiceAIC", "in": "path", "required": true, "schema": { "type": "string" } }
        ],
        "responses": {
          "200": { "description": "ConfezioneDetail JSON" },
          "404": { "description": "Confezione non trovata" }
        }
      }
    },
    "/api/atc/{code}": {
      "get": {
        "summary": "Farmaci per codice ATC",
        "description": "Farmaci classificati sotto il codice ATC specificato.",
        "parameters": [
          { "name": "code", "in": "path", "required": true, "schema": { "type": "string" }, "example": "N02BE01" },
          { "name": "p", "in": "query", "required": false, "schema": { "type": "integer", "default": 0 } }
        ],
        "responses": {
          "200": {
            "description": "Farmaci per ATC",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "code":        { "type": "string" },
                    "description": { "type": "string" },
                    "total":   { "type": "integer" },
                    "page":    { "type": "integer" },
                    "results": { "type": "array", "items": { "$ref": "#/components/schemas/SearchResult" } }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/api/stats": {
      "get": {
        "summary": "Statistiche database",
        "description": "Conteggi di confezioni, principi attivi, codici ATC e data ultimo sync.",
        "responses": {
          "200": {
            "description": "Statistiche",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "confezioni":     { "type": "integer" },
                    "principiAttivi": { "type": "integer" },
                    "codiciATC":      { "type": "integer" },
                    "ultimoSync":     { "type": "string" }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/api/shortages": {
      "get": {
        "summary": "Farmaci carenti / shortage list",
        "description": "Elenco AIFA dei farmaci temporaneamente carenti, con fascia, motivazioni e suggerimenti.",
        "parameters": [
          { "name": "q", "in": "query", "required": false, "description": "Filtro per nome o principio attivo", "schema": { "type": "string" } },
          { "name": "p", "in": "query", "required": false, "schema": { "type": "integer", "default": 0 } }
        ],
        "responses": {
          "200": {
            "description": "Lista carenze",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "total":   { "type": "integer" },
                    "page":    { "type": "integer" },
                    "results": { "type": "array", "items": { "$ref": "#/components/schemas/Shortage" } }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/api/equivalents/{codiceGruppo}": {
      "get": {
        "summary": "Equivalenti terapeutici",
        "description": "Tutti i farmaci dello stesso gruppo di equivalenza (Lista Trasparenza AIFA).",
        "parameters": [
          { "name": "codiceGruppo", "in": "path", "required": true, "schema": { "type": "string" } }
        ],
        "responses": {
          "200": {
            "description": "Farmaci nel gruppo di equivalenza",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "codiceGruppo": { "type": "string" },
                    "results": { "type": "array", "items": { "$ref": "#/components/schemas/PrezzoEquivalente" } }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/api/pa/{nome}": {
      "get": {
        "summary": "Farmaci per principio attivo",
        "description": "Ricerca paginata per principio attivo (prefisso, case-insensitive).",
        "parameters": [
          { "name": "nome", "in": "path", "required": true, "schema": { "type": "string" }, "description": "Nome o prefisso del principio attivo" },
          { "name": "p", "in": "query", "required": false, "schema": { "type": "integer", "default": 0 } }
        ],
        "responses": {
          "200": {
            "description": "Risultati per principio attivo",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "principioAttivo": { "type": "string" },
                    "total":   { "type": "integer" },
                    "page":    { "type": "integer" },
                    "results": { "type": "array", "items": { "$ref": "#/components/schemas/SearchResult" } }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/api/company/{nome}": {
      "get": {
        "summary": "Farmaci per azienda farmaceutica",
        "description": "Ricerca paginata per ragione sociale (sottostringa, case-insensitive).",
        "parameters": [
          { "name": "nome", "in": "path", "required": true, "schema": { "type": "string" }, "description": "Nome o parte della ragione sociale" },
          { "name": "p", "in": "query", "required": false, "schema": { "type": "integer", "default": 0 } }
        ],
        "responses": {
          "200": {
            "description": "Risultati per azienda farmaceutica",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "azienda": { "type": "string" },
                    "total":   { "type": "integer" },
                    "page":    { "type": "integer" },
                    "results": { "type": "array", "items": { "$ref": "#/components/schemas/SearchResult" } }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/api/openapi.json": {
      "get": {
        "summary": "Specifica OpenAPI",
        "description": "Restituisce questa specifica OpenAPI 3.0 in formato JSON.",
        "responses": { "200": { "description": "OpenAPI 3.0 JSON" } }
      }
    },
    "/ping": {
      "get": {
        "summary": "Health check",
        "responses": { "200": { "description": "ok" } }
      }
    }
  },
  "components": {
    "schemas": {
      "SearchResult": {
        "type": "object",
        "properties": {
          "codFarmaco":     { "type": "string" },
          "denominazione":  { "type": "string" },
          "ragioneSociale": { "type": "string" },
          "codiceAtc":      { "type": "string" },
          "paAssociati":    { "type": "string" },
          "statoAmmvo":     { "type": "string" },
          "forma":          { "type": "string" },
          "PrezzoSSN":      { "type": "string", "description": "Prezzo SSN minimo dalla Lista Trasparenza AIFA (vuoto se non disponibile)" }
        }
      },
      "AutocompleteSuggestion": {
        "type": "object",
        "properties": {
          "denominazione":  { "type": "string" },
          "codFarmaco":     { "type": "string" },
          "paAssociati":    { "type": "string" },
          "codiceATC":      { "type": "string" },
          "ragioneSociale": { "type": "string" }
        }
      },
      "Shortage": {
        "type": "object",
        "properties": {
          "nomeMedicinale":   { "type": "string" },
          "codiceAIC":        { "type": "string" },
          "principioAttivo":  { "type": "string" },
          "formaDosaggio":    { "type": "string" },
          "titolareAIC":      { "type": "string" },
          "dataInizio":       { "type": "string" },
          "finePresunta":     { "type": "string" },
          "equivalente":      { "type": "string" },
          "motivazioni":      { "type": "string" },
          "suggerimentiAIFA": { "type": "string" },
          "notaAIFA":         { "type": "string" },
          "fascia":           { "type": "string" },
          "codiceATC":        { "type": "string" }
        }
      },
      "PrezzoEquivalente": {
        "type": "object",
        "description": "Voce dalla Lista Trasparenza AIFA (prezzi SSN e gruppi di equivalenza).",
        "properties": {
          "CodiceAIC":       { "type": "string" },
          "PrincipioAttivo": { "type": "string" },
          "ATC":             { "type": "string" },
          "NomeFarmaco":     { "type": "string" },
          "Confezione":      { "type": "string" },
          "Ditta":           { "type": "string" },
          "PrezzoSSN":       { "type": "string" },
          "PrezzoPubblico":  { "type": "string" },
          "Differenza":      { "type": "string" },
          "Nota":            { "type": "string" },
          "CodiceGruppo":    { "type": "string" }
        }
      },
      "FarmacoOrfano": {
        "type": "object",
        "description": "Medicinale orfano designato per malattia rara (Lista AIFA).",
        "properties": {
          "CodiceAIC6":      { "type": "string" },
          "Descrizione":     { "type": "string" },
          "DataInizio":      { "type": "string" },
          "ATC":             { "type": "string" },
          "PrincipioAttivo": { "type": "string" },
          "Classe":          { "type": "string" },
          "DataFine":        { "type": "string" }
        }
      }
    }
  }
}`

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
	}).ParseFS(assets, "assets/templates/*.html"))

	return &Server{store: store, port: port, templates: tmpl}
}

// Listen starts the HTTP server and blocks until an error occurs.
func (s *Server) Listen() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("GET /medbook", s.handleSearch)
	mux.HandleFunc("GET /farmaco/{codFarmaco}", s.handleDrug)
	mux.HandleFunc("GET /api/search", s.handleAPISearch)
	mux.HandleFunc("GET /api/autocomplete", s.handleAutocomplete)
	mux.HandleFunc("GET /api/drug/{codFarmaco}", s.handleAPIDrug)
	mux.HandleFunc("GET /api/package/{codiceAIC}", s.handleAPIPackage)
	mux.HandleFunc("GET /api/atc/{code}", s.handleAPIByATC)
	mux.HandleFunc("GET /api/stats", s.handleAPIStats)
	mux.HandleFunc("GET /api/shortages", s.handleAPIShortages)
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
	http.Redirect(w, r, "/medbook", http.StatusFound)
}

type searchPageData struct {
	Query   string
	Results []db.SearchResult
	Total   int64
	Page    int
	Pages   int
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	page := intParam(r, "p", 0)

	data := searchPageData{Query: q, Page: page}

	if q != "" {
		results, total, err := s.store.SearchFarmaci(r.Context(), q, page, pageSize)
		if err != nil {
			http.Error(w, "Errore nella ricerca: "+err.Error(), http.StatusInternalServerError)
			return
		}
		data.Results = results
		data.Total = total
		data.Pages = int(math.Ceil(float64(total) / float64(pageSize)))
	}

	s.render(w, "index.html", data)
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

	s.render(w, "detail.html", detailPageData{
		Detail: detail,
		Query:  r.URL.Query().Get("q"),
	})
}

// handleAPISearch returns JSON search results (for progressive enhancement / external tools).
func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page := intParam(r, "p", 0)
	if q == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[],"total":0}`))
		return
	}

	results, total, err := s.store.SearchFarmaci(r.Context(), q, page, pageSize)
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
        "description": "Ricerca fulltext per denominazione, principio attivo o codice ATC/AIC.",
        "parameters": [
          { "name": "q", "in": "query", "required": true, "description": "Testo di ricerca", "schema": { "type": "string", "minLength": 1 } },
          { "name": "p", "in": "query", "required": false, "description": "Pagina (0-based)", "schema": { "type": "integer", "default": 0 } }
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
          "forma":          { "type": "string" }
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
      }
    }
  }
}`

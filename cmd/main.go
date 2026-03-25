package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"medbook/internal/db"
	"medbook/internal/syncer"
	"medbook/internal/web"
)

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	ctx := context.Background()
	dsn := envOr("DATABASE_URL", "postgres://localhost/medbook?sslmode=disable")

	switch os.Args[1] {
	case "migrate":
		store, err := db.New(ctx, dsn)
		exitOn(err, "connessione DB")
		defer store.Close()
		exitOn(store.Migrate(ctx), "migrazione")
		fmt.Println("✓ Schema creato/aggiornato correttamente")

	case "sync":
		store, err := db.New(ctx, dsn)
		exitOn(err, "connessione DB")
		defer store.Close()
		s := syncer.New(store)
		exitOn(s.Run(ctx), "sincronizzazione")

	case "sync-shortages":
		store, err := db.New(ctx, dsn)
		exitOn(err, "connessione DB")
		defer store.Close()
		s := syncer.New(store)
		_, err = s.SyncShortages(ctx)
		exitOn(err, "sync carenze")

	case "serve":
		store, err := db.New(ctx, dsn)
		exitOn(err, "connessione DB")
		defer store.Close()
		port := envOr("MEDBOOK_PORT", "8080")
		srv := web.NewServer(store, port)
		fmt.Printf("MedBook in ascolto su http://localhost:%s\n", port)
		exitOn(srv.Listen(), "server web")

	case "stats":
		store, err := db.New(ctx, dsn)
		exitOn(err, "connessione DB")
		defer store.Close()
		stats, err := store.GetStats(ctx)
		exitOn(err, "statistiche")
		fmt.Printf("Confezioni:      %s\n", fmtNum(stats.Confezioni))
		fmt.Printf("Principi attivi: %s\n", fmtNum(stats.PrincipiAttivi))
		fmt.Printf("Codici ATC:      %s\n", fmtNum(stats.CodicATC))
		fmt.Printf("Ultimo sync:     %s\n", stats.UltimoSync)

	default:
		fmt.Fprintf(os.Stderr, "Comando sconosciuto: %s\n\n", os.Args[1])
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Fprintln(os.Stderr, strings.TrimLeft(`
MedBook - Consultazione farmaci AIFA offline

Comandi:
  medbook migrate          Crea o aggiorna lo schema del database
  medbook sync             Scarica i dati AIFA e popola il database (una tantum/periodico)
  medbook sync-shortages   Aggiorna solo le carenze (più veloce del sync completo)
  medbook serve            Avvia il server web locale per consultazione
  medbook stats            Mostra statistiche del database

Variabili d'ambiente:
  DATABASE_URL    Stringa di connessione PostgreSQL
                  (default: postgres://localhost/medbook?sslmode=disable)
  MEDBOOK_PORT    Porta del server web (default: 8080)

Utilizzo tipico:
  export DATABASE_URL="postgres://user:pass@localhost/medbook?sslmode=disable"
  medbook migrate          # prima volta: crea le tabelle
  medbook sync             # scarica dati AIFA (~1-2 minuti)
  medbook serve            # avvia server, poi apri http://localhost:8080
`, "\n"))
}

func exitOn(err error, context string) {
	if err != nil {
		log.Fatalf("Errore %s: %v", context, err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// fmtNum formats an integer with thousands separator (dot notation, Italian style).
func fmtNum(n int64) string {
	s := strconv.FormatInt(n, 10)
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

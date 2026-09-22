// Command chronicle is the metrics observer: it pulls the network's metrics
// tree from one known node, decrypts snapshots, and exposes/exports them.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"sporemachine/internal/chronicle"
	"sporemachine/internal/storage"
)

const pullInterval = 3 * time.Minute

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "export-csv":
		exportCSVCmd(os.Args[2:])
	case "render-video":
		renderVideoCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: chronicle <run|export-csv|render-video> [flags]")
	os.Exit(1)
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	settingsPath := fs.String("settings", "chronicle-settings.yaml", "path to chronicle-settings.yaml")
	dbPath := fs.String("db", "chronicle.db", "path to Chronicle's own bbolt file")
	httpAddr := fs.String("http-addr", ":9090", "Prometheus /metrics listen address")
	fs.Parse(args)

	settings, err := chronicle.LoadSettings(*settingsPath)
	if err != nil {
		log.Fatalf("chronicle: %v", err)
	}
	pub, priv, err := settings.MetricsKeyPair()
	if err != nil {
		log.Fatalf("chronicle: %v", err)
	}

	store, err := storage.Open(*dbPath)
	if err != nil {
		log.Fatalf("chronicle: open storage: %v", err)
	}
	defer store.Close()

	syncer := chronicle.NewSyncer(store, settings.NodeAddress)
	exporter := chronicle.NewExporter()

	http.Handle("/metrics", exporter.Handler())
	go func() {
		log.Printf("chronicle: serving Prometheus metrics on %s", *httpAddr)
		if err := http.ListenAndServe(*httpAddr, nil); err != nil {
			log.Fatalf("chronicle: http: %v", err)
		}
	}()

	ctx := context.Background()
	cycle := func() {
		fetched, err := syncer.Pull(ctx)
		if err != nil {
			log.Printf("chronicle: pull failed: %v", err)
			return
		}
		if fetched > 0 {
			log.Printf("chronicle: fetched %d new metrics object(s)", fetched)
		}
		snapshots, err := chronicle.ReadSnapshots(store, pub, priv)
		if err != nil {
			log.Printf("chronicle: read snapshots failed: %v", err)
			return
		}
		for _, s := range snapshots {
			exporter.Observe(s)
		}
	}

	cycle()
	ticker := time.NewTicker(pullInterval)
	defer ticker.Stop()
	for range ticker.C {
		cycle()
	}
}

func exportCSVCmd(args []string) {
	fs := flag.NewFlagSet("export-csv", flag.ExitOnError)
	settingsPath := fs.String("settings", "chronicle-settings.yaml", "path to chronicle-settings.yaml")
	dbPath := fs.String("db", "chronicle.db", "path to Chronicle's own bbolt file")
	out := fs.String("out", "growth.csv", "output CSV path")
	fs.Parse(args)

	settings, err := chronicle.LoadSettings(*settingsPath)
	if err != nil {
		log.Fatalf("chronicle: %v", err)
	}
	pub, priv, err := settings.MetricsKeyPair()
	if err != nil {
		log.Fatalf("chronicle: %v", err)
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		log.Fatalf("chronicle: open storage: %v", err)
	}
	defer store.Close()

	snapshots, err := chronicle.ReadSnapshots(store, pub, priv)
	if err != nil {
		log.Fatalf("chronicle: %v", err)
	}
	if err := chronicle.WriteGrowthCSV(snapshots, *out); err != nil {
		log.Fatalf("chronicle: %v", err)
	}
	log.Printf("chronicle: wrote %s", *out)
}

func renderVideoCmd(args []string) {
	fs := flag.NewFlagSet("render-video", flag.ExitOnError)
	csvPath := fs.String("csv", "growth.csv", "input growth CSV")
	out := fs.String("out", "growth.gif", "output GIF path")
	fs.Parse(args)

	if err := chronicle.RenderGrowthVideo(*csvPath, *out); err != nil {
		log.Fatalf("chronicle: %v", err)
	}
	log.Printf("chronicle: wrote %s", *out)
}

// forwarder is the lite-addon-forwarder daemon: it reads the system's datapoint events, its
// statistics and the host's figures (and, in a later version, its logs), labels the events with
// names, rooms and functions, and forwards what the configured selections match to the
// configured targets - InfluxDB, HTTP webhooks, Elasticsearch, Prometheus and more. It serves its configuration page and API on the loopback,
// behind lighttpd's proxy of /addons/forwarder/. openccu-lite only.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/addonkit"
	"github.com/hobbyquaker/ccu-addon-kit/go/litesource"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/httpapi"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/metrics"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/pipeline"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/source"
)

// version is set by the build (-ldflags "-X main.version=…").
var version = "dev"

func main() {
	log.SetFlags(0) // the journal adds the time
	addonDir := flag.String("addon-dir", "/usr/local/addons/forwarder", "the addon's directory (etc/, var/, app/)")
	id := flag.String("id", "forwarder", "the addon id (token file, base path)")
	port := flag.Int("port", 8295, "the loopback port lighttpd proxies to")
	base := flag.String("system", "http://127.0.0.1", "openccu-lite's URL")
	basePath := flag.String("base-path", "", "the path the app is served at (/addons/<id> by default)")
	www := flag.String("www", "", "the built web app (<addon-dir>/app by default)")
	tokenFile := flag.String("token-file", "", "a token file for the system's API (the addon's own token by default)")
	noSession := flag.Bool("no-session-check", false, "serve without the session check (development)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *basePath == "" {
		*basePath = "/addons/" + *id
	}
	if *www == "" {
		*www = filepath.Join(*addonDir, "app")
	}
	if err := run(*addonDir, *id, *port, *base, *basePath, *www, *tokenFile, *noSession); err != nil {
		log.Fatalf("forwarder: %v", err)
	}
}

func run(addonDir, id string, port int, base, basePath, www, tokenFile string, noSession bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := config.Open(addonDir)
	if err != nil {
		return err
	}
	logf := func(format string, args ...any) { log.Printf("forwarder: "+format, args...) }

	var token addonkit.TokenSource
	if tokenFile != "" {
		token = &addonkit.FileToken{Path: tokenFile}
	} else {
		token = addonkit.AddonToken(id)
	}
	lite := litesource.New(base, token)
	lite.Log = logf
	// wait for the system: at boot its web server answers 503 until occulited is up, and the
	// token may not exist yet; on openccu-lite that is never a reason to give up
	if _, err := lite.Await(ctx, addonkit.IsOpenccuLite()); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("%s is not openccu-lite", base)
	}

	hub := httpapi.NewHub()
	pipe := pipeline.New(logf)
	pipe.OnChange = hub.Broadcast
	pipe.SpillDir = filepath.Join(addonDir, "var", "spill")
	apply := func() { pipe.Apply(store.TargetsWithSecrets(), store.Selections()) }
	store.OnChange = func() { apply(); hub.Broadcast() }
	apply()

	src := source.New(source.Options{
		Client:   lite,
		Emit:     pipe.Dispatch,
		Snapshot: func() bool { return store.Settings().Snapshot },
		Lang:     func() string { return store.Settings().Lang },
		Recent:   func() int { return store.Settings().Recent },
		Log:      logf,
	})
	go src.Run(ctx)

	// the statistics and the host's figures, polled while a target wants them
	poller := metrics.New(metrics.Options{
		SystemURL: base,
		HTTP:      &http.Client{Transport: &addonkit.AuthTransport{Source: token}, Timeout: 20 * time.Second},
		Interval:  func() time.Duration { return time.Duration(store.Settings().MetricsIntervalS) * time.Second },
		Wants:     pipe.WantsMetric,
		Emit:      src.Emit,
		Events:    src.EventsByInterface,
		Log:       logf,
	})
	go poller.Run(ctx)
	store.OnChange = func() { apply(); poller.Kick(); hub.Broadcast() }

	wrap := func(h http.Handler) http.Handler { return h }
	wrapScrape := wrap
	if !noSession {
		// the shell's admin session, or a Full-access API token (a program that configures targets)
		refused := func(r *http.Request, reason string) { logf("refused %s %s: %s", r.Method, r.URL.Path, reason) }
		c := addonkit.NewChecker(addonkit.SessionOptions{Base: base, Need: addonkit.LevelAdmin, AddonID: id, OnRefused: refused})
		wrap = c.Handler
		// the scrape endpoints: any session, or an API token of the scope addon:forwarder - what
		// the system's gate lets through to /addons/forwarder/ from the LAN
		cu := addonkit.NewChecker(addonkit.SessionOptions{Base: base, Need: addonkit.LevelUser, AddonID: id, OnRefused: refused})
		wrapScrape = cu.Handler
	}
	h := httpapi.New(httpapi.Options{BasePath: basePath, WWW: www, Store: store, Pipeline: pipe, Source: src, Metrics: poller, Wrap: wrap, WrapScrape: wrapScrape, Version: version, Started: time.Now(), Hub: hub})
	l, err := addonkit.Listen(port, "")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	logf("%s: serving %s on %s, %d targets, %d selections", version, basePath, l.Addr(), len(store.Targets()), len(store.Selections()))
	if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
		return err
	}
	pipe.Stop()
	logf("stopping")
	os.Stdout.Sync()
	return nil
}

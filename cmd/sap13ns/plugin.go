package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"sap13ns/internal/db"
	"sap13ns/internal/plugin"
	"sap13ns/internal/pluginskel"
	"sap13ns/internal/store"
)

const pluginUsage = `usage:
  sap13ns plugin new <id> [-dir D] [-hooks shortcode,widget,...] [-sdk PATH] [-module M] [-name N]
  sap13ns plugin pack [-o DIR] [-no-build] <dir>
  sap13ns plugin check [-json] [-skip-dry-run] <dir|file.zip>
  sap13ns plugin list [-env FILE]
  sap13ns plugin install [-env FILE] [-allow-downgrade] <file.zip>
  sap13ns plugin hash <dir>`

// pluginCmd implements "sap13ns plugin new|pack|check|list|install|hash".
func pluginCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(pluginUsage)
	}
	sub, args := args[0], args[1:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch sub {
	case "new":
		return pluginNew(args)
	case "pack":
		return pluginPack(ctx, args)
	case "check":
		return pluginCheck(ctx, args)
	case "list", "install":
		return pluginSite(ctx, sub, args)
	case "hash":
		return pluginHash(args)
	}
	return errors.New(pluginUsage)
}

// parseArgs parses flags placed before or after the positional arguments.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func pluginNew(args []string) error {
	fs := flag.NewFlagSet("plugin new", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory to create (default: ./<id>)")
	hooks := fs.String("hooks", "", "examples to keep, comma-separated (default: all of "+strings.Join(pluginskel.Hooks(), ",")+")")
	sdk := fs.String("sdk", "", "path of the pluginsdk directory for the go.mod replace (default: found in a parent sap13ns checkout)")
	module := fs.String("module", "", "Go module path (default: example.com/<id>)")
	name := fs.String("name", "", "display name (default: from the id)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: sap13ns plugin new <id> [-dir D] [-hooks shortcode,widget,...] [-sdk PATH]")
	}
	id := pos[0]
	if *dir == "" {
		*dir = id
	}
	sdkPath := *sdk
	if sdkPath == "" {
		sdkPath = pluginskel.FindSDK(filepath.Dir(filepath.Clean(*dir)))
		if sdkPath != "" {
			// FindSDK answers relative to the parent; go.mod lives one level down.
			sdkPath = filepath.Join("..", sdkPath)
		}
	} else if !filepath.IsAbs(sdkPath) {
		// A relative -sdk is relative to the current directory; go.mod
		// needs it relative to the new project.
		if abs, err := filepath.Abs(sdkPath); err == nil {
			if target, err := filepath.Abs(*dir); err == nil {
				if rel, err := filepath.Rel(target, abs); err == nil {
					sdkPath = rel
				}
			}
		}
	}
	var hookList []string
	if *hooks != "" {
		hookList = strings.Split(*hooks, ",")
	}
	if err := pluginskel.Generate(*dir, pluginskel.Options{ID: id, Name: *name, Module: *module, SDK: sdkPath, Hooks: hookList}); err != nil {
		return err
	}
	fmt.Printf("created %s\n", *dir)
	if sdkPath == "" {
		fmt.Printf("note: no sap13ns checkout found, so go.mod has no replace line for the SDK.\n"+
			"      Add one with: go mod edit -replace %s=/path/to/sap13ns/pluginsdk\n", pluginskel.SDKModule)
	}
	fmt.Printf("next:\n  cd %s\n  make test      # run the tests\n  make pack      # build plugin.wasm, scan it and write the .zip to upload\n", *dir)
	return nil
}

func pluginPack(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin pack", flag.ContinueOnError)
	out := fs.String("o", "", "directory for the .zip (default: the plugin directory)")
	noBuild := fs.Bool("no-build", false, "package the existing module instead of running go build")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: sap13ns plugin pack [-o DIR] [-no-build] <dir>")
	}
	dir := pos[0]
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return err
	}
	m, err := plugin.ParseManifest(raw)
	if err != nil {
		return err
	}
	if !*noBuild {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
			return fmt.Errorf("%s has no go.mod; build the module yourself and use -no-build", dir)
		}
		cmd := exec.CommandContext(ctx, "go", "build", "-buildmode=c-shared", "-trimpath", "-o", m.Wasm, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		fmt.Fprintf(os.Stderr, "building %s/%s\n", dir, m.Wasm)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go build: %w", err)
		}
	}
	var buf bytes.Buffer
	if m, err = plugin.Pack(dir, &buf); err != nil {
		return err
	}
	// Scan the archive exactly as an upload would be scanned.
	rep, err := plugin.ScanPackage(ctx, bytes.NewReader(buf.Bytes()), int64(buf.Len()), plugin.ScanOptions{}, cliBlocklist())
	if err != nil {
		return err
	}
	printReport(os.Stdout, rep)
	if rep.Blocked() {
		return errors.New("the package has critical findings; it would be refused")
	}
	if *out == "" {
		*out = dir
	}
	name := m.ID + "-" + m.Version + ".zip"
	path := filepath.Join(*out, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return err
	}
	sum := sha256.Sum256(buf.Bytes())
	if err := os.WriteFile(path+".sha256", []byte(hex.EncodeToString(sum[:])+"  "+name+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("\nwrote %s (%d KiB)\nsha256 %s\nUpload it in Admin → Plugins, or run: sap13ns plugin install %s\n",
		path, (buf.Len()+1023)/1024, hex.EncodeToString(sum[:]), path)
	return nil
}

func pluginCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plugin check", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	skipDry := fs.Bool("skip-dry-run", false, "do not run the hooks")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: sap13ns plugin check [-json] <dir|file.zip>")
	}
	target := pos[0]
	opts := plugin.ScanOptions{SkipDryRun: *skipDry}
	var rep *plugin.Report
	if fi, err := os.Stat(target); err != nil {
		return err
	} else if fi.IsDir() {
		rep, err = plugin.Scan(ctx, target, opts, cliBlocklist())
		if err != nil {
			return err
		}
	} else {
		f, err := os.Open(target)
		if err != nil {
			return err
		}
		defer f.Close()
		if rep, err = plugin.ScanPackage(ctx, f, fi.Size(), opts, cliBlocklist()); err != nil {
			return err
		}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rep)
	} else {
		printReport(os.Stdout, rep)
	}
	if rep.Blocked() {
		return fmt.Errorf("blocked: %d critical findings", rep.Count(plugin.SevCritical))
	}
	return nil
}

// pluginSite runs list and install against a configured site.
func pluginSite(ctx context.Context, sub string, args []string) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(args[1:], args[0]) // the zip may come before the flags
	}
	var allowDowngrade bool
	cfg, log, fs, err := setup("plugin "+sub, args, false, func(f *flag.FlagSet) {
		if sub == "install" {
			f.BoolVar(&allowDowngrade, "allow-downgrade", false, "install a version older than the installed one")
		}
	})
	if err != nil {
		return err
	}
	if sub == "install" && fs.NArg() != 1 {
		return errors.New("usage: sap13ns plugin install [-env FILE] [-allow-downgrade] <file.zip>")
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}
	st := store.New(pool)
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		return err
	}
	site := func() plugin.SiteInfo {
		return plugin.SiteInfo{URL: cfg.BaseURL.String(), Title: settings.SiteTitle, Language: settings.Language, Timezone: settings.Timezone}
	}
	host, err := plugin.NewHost(ctx, cfg.PluginsDir, filepath.Join(cfg.DataDir, "cache", "wasm"), st, log, site, nil)
	if err != nil {
		return err
	}
	defer host.Close(context.WithoutCancel(ctx))
	host.SetBlocklistFile(filepath.Join(cfg.DataDir, "plugin-blocklist.txt"))
	if err := host.Load(ctx); err != nil {
		return err
	}

	if sub == "list" {
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tVERSION\tSTATUS\tSCAN\tSOURCE\tPERMISSIONS")
		for _, p := range host.List() {
			verdict, source := "-", "manual"
			if p.Report != nil {
				verdict = p.Report.Verdict
			}
			if p.Record != nil {
				source = p.Record.Source
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, p.Version, p.Status, verdict, source, strings.Join(p.EffectivePermissions(), " "))
		}
		return tw.Flush()
	}

	path := fs.Arg(0)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	res, err := host.Install(ctx, f, fi.Size(), plugin.InstallOptions{Source: "cli", AllowDowngrade: allowDowngrade})
	if res != nil && res.Report != nil {
		printReport(os.Stdout, res.Report)
	}
	switch {
	case errors.Is(err, plugin.ErrRefused):
		return errors.New("refused: the package has critical findings")
	case errors.Is(err, plugin.ErrDowngrade):
		return fmt.Errorf("version %s is older than the installed %s; use -allow-downgrade", res.Version, res.Previous)
	case err != nil:
		return err
	}
	action := "installed"
	if res.Previous != "" {
		action = "updated from " + res.Previous
	}
	fmt.Printf("\n%s %s %s, disabled until an administrator enables it in Admin → Plugins.\n", res.ID, res.Version, action)
	fmt.Println("A running server shows it after Admin → Plugins → Rescan.")
	return nil
}

func pluginHash(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: sap13ns plugin hash <plugin-dir>")
	}
	raw, err := os.ReadFile(filepath.Join(args[0], "plugin.json"))
	if err != nil {
		return err
	}
	m, err := plugin.ParseManifest(raw)
	if err != nil {
		return err
	}
	wasm, err := os.ReadFile(filepath.Join(args[0], m.Wasm))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(wasm)
	fmt.Printf("%s  %s@%s (%s)\n", hex.EncodeToString(sum[:]), m.ID, m.Version, m.Wasm)
	fmt.Printf("permissions: %s\n", strings.Join(m.EffectivePermissions(), ", "))
	return nil
}

// cliBlocklist is the operator's extra blocklist when SAP13NS_DATA_DIR is set.
func cliBlocklist() string {
	if d := os.Getenv("SAP13NS_DATA_DIR"); d != "" {
		return filepath.Join(d, "plugin-blocklist.txt")
	}
	return ""
}

func printReport(w io.Writer, rep *plugin.Report) {
	id := rep.PluginID
	if id == "" {
		id = "(package)"
	}
	fmt.Fprintf(w, "%s %s: %s (%d critical, %d high, %d medium, %d info)\n", id, rep.Version, strings.ToUpper(rep.Verdict),
		rep.Count(plugin.SevCritical), rep.Count(plugin.SevHigh), rep.Count(plugin.SevMedium), rep.Count(plugin.SevInfo))
	if rep.WasmSHA256 != "" {
		fmt.Fprintf(w, "  wasm sha256 %s\n", rep.WasmSHA256)
	}
	if len(rep.Imports) > 0 {
		fmt.Fprintf(w, "  host functions: %s\n", strings.Join(rep.Imports, " "))
	}
	for _, f := range rep.Findings {
		loc := ""
		if f.Location != "" {
			loc = " (" + f.Location + ")"
		}
		fmt.Fprintf(w, "  %-8s %s: %s%s\n", f.Severity, f.Rule, f.Message, loc)
	}
}

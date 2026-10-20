package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sap13ns/internal/geoip"
	"sap13ns/internal/update"
)

// geoipCmd implements "sap13ns geoip update|status|lookup IP": the country
// database of the analytics (SAP13NS_GEOIP_DB). update downloads DB-IP's
// free country database; run it monthly (deploy/systemd/sap13ns-geoip.timer).
// A running server picks the new file up by itself.
func geoipCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sap13ns geoip update|status|lookup IP [-env file]")
	}
	sub, args := args[0], args[1:]
	cfg, _, fs, err := setup("geoip "+sub, args, false, nil)
	if err != nil {
		return err
	}
	if cfg.GeoIPDB == "" {
		return errors.New("SAP13NS_GEOIP_DB is not set: choose where the country database goes, such as /var/lib/sap13ns/geoip/country.mmdb")
	}
	switch sub {
	case "update":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		in, err := geoip.Update(ctx, update.NewHTTPClient(), cfg.GeoIPDB, time.Now().UTC())
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s, built %s\n", in.Path, in.Type, in.Built.Format(time.DateOnly))
	case "status":
		r, err := geoip.Load(cfg.GeoIPDB)
		if err != nil {
			return fmt.Errorf("%s: %w", cfg.GeoIPDB, err)
		}
		fmt.Printf("%s: %s, built %s\n", cfg.GeoIPDB, r.Metadata.DatabaseType, r.Metadata.BuildTime().Format(time.DateOnly))
	case "lookup":
		if fs.NArg() != 1 {
			return errors.New("usage: sap13ns geoip lookup IP")
		}
		ip, err := netip.ParseAddr(fs.Arg(0))
		if err != nil {
			return err
		}
		if _, err := geoip.Load(cfg.GeoIPDB); err != nil {
			return fmt.Errorf("%s: %w", cfg.GeoIPDB, err)
		}
		c := geoip.Open(cfg.GeoIPDB, nil).Country(ip)
		if c == "" {
			c = "unknown"
		}
		fmt.Println(c)
	default:
		return fmt.Errorf("unknown geoip command %q", sub)
	}
	return nil
}

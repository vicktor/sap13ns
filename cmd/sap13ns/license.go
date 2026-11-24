package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"sap13ns/internal/config"
	"sap13ns/internal/license"
)

// licenseCmd implements "sap13ns license [show] [-file F] [-env file]": what
// the install's licence file (SAP13NS_LICENSE_FILE) holds and whether it
// applies to this install. The licensor issues licences with
// scripts/licensesign.
func licenseCmd(args []string) error {
	if len(args) > 0 && args[0] == "show" {
		args = args[1:]
	}
	fl := flag.NewFlagSet("license", flag.ContinueOnError)
	envFile := fl.String("env", "", "load SAP13NS_* variables from this file")
	file := fl.String("file", "", "the licence file (default SAP13NS_LICENSE_FILE)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() != 0 {
		return errors.New("usage: sap13ns license [show] [-file F] [-env file]")
	}
	// The install's settings, when there are any: the file, and the host
	// the licence must cover.
	var host string
	if *envFile != "" {
		if err := config.LoadEnvFile(*envFile); err != nil {
			return err
		}
	}
	if os.Getenv("SAP13NS_DATABASE_URL") != "" {
		cfg, err := config.Load(false)
		if err != nil {
			return err
		}
		if cfg.Multisite {
			return errors.New("a network does not read a licence file: its plans decide whether sites show the credit (Network → Plans)")
		}
		host = cfg.BaseURL.Host
		if *file == "" {
			*file = cfg.LicenseFile
		}
	}
	if *file == "" {
		*file = license.DefaultPath
	}
	l, err := license.ReadFile(*file)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Printf("%s: no licence file; the \"Powered by sap13ns\" credit is shown\n", *file)
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", *file, err)
	}
	now := time.Now()
	fmt.Printf("file:      %s\nid:        %s\ntype:      %s\nlicensee:  %s\ndomains:   %s\nissued:    %s\nexpires:   %s\n",
		*file, l.ID, l.Type, l.Licensee, strings.Join(l.Domains, ", "),
		l.Issued.Format(time.DateOnly), l.Expires.Format(time.DateOnly))
	switch {
	case !l.InForce(now):
		fmt.Println("status:    not in force (expired, or not yet valid): the credit is shown")
	case host == "":
		fmt.Println("status:    signed and in force (pass -env to check it against this install's domain)")
	case !l.Covers(host):
		fmt.Printf("status:    not for this install (%s): the credit is shown\n", host)
	case l.HidesCredit(host, now):
		fmt.Printf("status:    in force for %s, %d days left: the credit is left out\n", host, int(l.Expires.Sub(now).Hours()/24))
	default:
		fmt.Printf("status:    in force for %s; a %s licence does not leave out the credit\n", host, l.Type)
	}
	return nil
}

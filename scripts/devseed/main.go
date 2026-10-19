// Command devseed fills a development database with realistic sample content
// so that the admin panel and every theme can be reviewed. Never run it in
// production: it creates an "admin" user with a known password.
//
//	go run ./scripts/devseed -env data/dev.env
//
// A development network of sites is made by scripts/devnetwork instead.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/db"
	"sap13ns/internal/media"
	"sap13ns/internal/store"
	"sap13ns/scripts/internal/devdata"
)

func main() {
	env := flag.String("env", "data/dev.env", "env file")
	flag.Parse()
	if err := config.LoadEnvFile(*env); err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load(false)
	if err != nil {
		log.Fatal(err)
	}
	if !cfg.Dev {
		log.Fatal("refusing to seed: SAP13NS_DEV is not true")
	}
	if cfg.Multisite {
		log.Fatal("refusing to seed: this is a network; scripts/devnetwork makes a development network")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	st := store.New(pool)
	if n, _ := st.CountUsers(ctx); n > 0 {
		log.Fatal("database already has users; reset it first")
	}

	mustUser := func(username, email, name, role, bio string) int64 {
		id, err := devdata.User(ctx, st, username, email, name, role, bio)
		if err != nil {
			log.Fatal(err)
		}
		return id
	}
	admin := mustUser("admin", "admin@example.org", "Lucía Ferrer", auth.RoleAdmin, "Escribe sobre software sencillo, seguridad y la web abierta.")
	editor := mustUser("marta", "marta@example.org", "Marta Gil", auth.RoleEditor, "Editora. Le gustan los sistemas que duran.")
	mustUser("pablo", "pablo@example.org", "Pablo Ríos", auth.RoleContributor, "")
	mustUser("ana", "ana@example.org", "Ana Soler", auth.RoleSubscriber, "")

	proc := &media.Processor{Dir: cfg.UploadsDir, MaxBytes: cfg.MaxUploadBytes}
	if err := devdata.Blog(ctx, st, proc, admin, editor); err != nil {
		log.Fatal(err)
	}
	fmt.Println("seeded: users admin/marta/pablo/ana with password " + devdata.Password)
}

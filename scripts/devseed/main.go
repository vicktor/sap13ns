// Command devseed fills a development database with realistic sample content
// so that the admin panel and every theme can be reviewed. Never run it in
// production: it creates an "admin" user with a known password.
//
//	go run ./scripts/devseed -env data/dev.env
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"net/netip"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/db"
	"sap13ns/internal/media"
	"sap13ns/internal/store"
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
		hash, err := auth.HashPassword("dev-password-123")
		if err != nil {
			log.Fatal(err)
		}
		id, err := st.CreateUser(ctx, store.NewUser{Username: username, Email: email, DisplayName: name, Role: role, PasswordHash: hash})
		if err != nil {
			log.Fatal(err)
		}
		st.UpdateUserProfile(ctx, id, store.UserProfile{Email: email, DisplayName: name, Bio: bio, Website: "https://example.org"})
		return id
	}
	admin := mustUser("admin", "admin@example.org", "Lucía Ferrer", auth.RoleAdmin, "Escribe sobre software sencillo, seguridad y la web abierta.")
	editor := mustUser("marta", "marta@example.org", "Marta Gil", auth.RoleEditor, "Editora. Le gustan los sistemas que duran.")
	mustUser("pablo", "pablo@example.org", "Pablo Ríos", auth.RoleContributor, "")
	mustUser("ana", "ana@example.org", "Ana Soler", auth.RoleSubscriber, "")

	st.SaveSettings(ctx, func() store.Settings {
		s := store.DefaultSettings()
		s.SiteTitle = "Cuaderno Ligero"
		s.Tagline = "Notas sobre software rápido, seguro y sin adornos."
		s.FooterText = "© 2026 Cuaderno Ligero · Hecho con sap13ns"
		return s
	}())

	cat := map[string]int64{}
	for _, c := range [][2]string{{"Seguridad", "seguridad"}, {"Rendimiento", "rendimiento"}, {"Diseño", "diseno"}, {"Linux", "linux"}, {"Notas", "notas"}} {
		id, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "category", Name: c[0], Slug: c[1], Description: "Artículos sobre " + c[0] + "."})
		if err != nil {
			log.Fatal(err)
		}
		cat[c[1]] = id
	}

	proc := &media.Processor{Dir: cfg.UploadsDir, MaxBytes: cfg.MaxUploadBytes}
	img := func(name string, hue float64) *int64 {
		var buf bytes.Buffer
		png.Encode(&buf, gradient(1600, 1000, hue))
		res, err := proc.Save(&buf)
		if err != nil {
			log.Fatal(err)
		}
		id, err := st.CreateMedia(ctx, &store.Media{UploaderID: &admin, Path: res.Path, OriginalName: name, MIME: res.MIME, Size: res.Size,
			Width: res.Width, Height: res.Height, Variants: res.Variants, SHA256: res.SHA256, Alt: "Ilustración abstracta en degradado"})
		if err != nil {
			log.Fatal(err)
		}
		return &id
	}

	now := time.Now()
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	type seed struct {
		title, slug, excerpt, body, catSlug string
		tags                                []string
		at                                  *time.Time
		hue                                 float64
		author                              int64
		status                              string
	}
	posts := []seed{
		{"Un CMS que cabe en un solo binario", "un-cms-en-un-binario", "Por qué compilar todo —plantillas, estilos e iconos— en un ejecutable simplifica el despliegue y reduce la superficie de ataque.",
			bodyIntro, "rendimiento", []string{"go", "despliegue", "sencillez"}, ago(2 * time.Hour), 215, admin, store.StatusPublished},
		{"Plugins en WebAssembly sin miedo", "plugins-webassembly", "Un sandbox donde los plugins no pueden leer archivos, abrir sockets ni ver la base de datos si no se lo permites.",
			bodyPlugins, "seguridad", []string{"wasm", "seguridad", "plugins"}, ago(26 * time.Hour), 280, admin, store.StatusPublished},
		{"Modo claro y oscuro sin parpadeos", "modo-claro-oscuro", "Un script de diez líneas antes del primer pintado y variables CSS bien pensadas.",
			bodyTheme, "diseno", []string{"css", "accesibilidad"}, ago(3 * 24 * time.Hour), 330, editor, store.StatusPublished},
		{"nginx + socket Unix: la combinación aburrida que funciona", "nginx-socket-unix", "Menos puertos abiertos, permisos de grupo y archivos estáticos servidos por quien mejor sabe hacerlo.",
			bodyNginx, "linux", []string{"nginx", "linux", "despliegue"}, ago(6 * 24 * time.Hour), 150, admin, store.StatusPublished},
		{"Contraseñas: Argon2id y nada de reglas absurdas", "contrasenas-argon2id", "Longitud mínima, lista de contraseñas comunes y un hash lento. Sin obligar a poner un símbolo raro.",
			bodyPasswords, "seguridad", []string{"seguridad", "contraseñas"}, ago(9 * 24 * time.Hour), 20, editor, store.StatusPublished},
		{"Feeds que la gente realmente usa", "feeds-rss-atom-json", "RSS, Atom y JSON Feed para todo el sitio, cada categoría, etiqueta y autor.",
			bodyFeeds, "notas", []string{"rss", "web abierta"}, ago(14 * 24 * time.Hour), 95, admin, store.StatusPublished},
		{"PostgreSQL como buscador", "postgresql-busqueda", "Búsqueda de texto completo con tsvector y un índice GIN: suficiente para la mayoría de sitios.",
			bodySearch, "rendimiento", []string{"postgresql", "búsqueda"}, ago(20 * 24 * time.Hour), 190, admin, store.StatusPublished},
		{"Borrador: ideas para la próxima versión", "ideas-proxima-version", "", "- Importador desde WordPress\n- Más temas\n", "notas", nil, nil, 0, admin, store.StatusDraft},
		{"Programada: notas de la versión 1.1", "notas-version-1-1", "Lo que llega en la próxima versión.", "Pronto.", "notas", nil, func() *time.Time { t := now.Add(72 * time.Hour); return &t }(), 250, admin, store.StatusPublished},
	}
	var firstID int64
	for i, p := range posts {
		var featured *int64
		if p.hue > 0 {
			featured = img(p.slug+".png", p.hue)
		}
		tagIDs := []int64{}
		if len(p.tags) > 0 {
			slugs := make([]string, len(p.tags))
			for j, t := range p.tags {
				slugs[j] = slugify(t)
			}
			tagIDs, _ = st.EnsureTags(ctx, p.tags, slugs)
		}
		id, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Lang: "es", Status: p.status, Title: p.title, Slug: p.slug, Excerpt: p.excerpt,
			Content: p.body, AuthorID: p.author, FeaturedMediaID: featured, CommentStatus: "open", PublishedAt: p.at,
			TermIDs: append([]int64{cat[p.catSlug]}, tagIDs...)})
		if err != nil {
			log.Fatalf("post %s: %v", p.slug, err)
		}
		if i == 0 {
			firstID = id
		}
	}

	for _, pg := range []struct {
		title, slug, body string
		order             int
	}{
		{"Sobre este sitio", "sobre", pageAbout, 1},
		{"Contacto", "contacto", "Escríbenos a **hola@example.org**. Respondemos en uno o dos días laborables.", 2},
		{"Privacidad", "privacidad", "Este sitio no usa cookies de seguimiento. Solo se guarda una cookie de sesión si inicias sesión.", 3},
	} {
		_, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePage, Lang: "es", Status: store.StatusPublished, Title: pg.title, Slug: pg.slug,
			Content: pg.body, AuthorID: admin, CommentStatus: "closed", MenuOrder: pg.order, PublishedAt: ago(30 * 24 * time.Hour)})
		if err != nil {
			log.Fatal(err)
		}
	}

	ip := netip.MustParseAddr("192.0.2.10")
	c1, _ := st.CreateComment(ctx, store.NewComment{PostID: firstID, AuthorName: "Jorge Martín", AuthorEmail: "jorge@example.net", Content: "Muy buena explicación. ¿Cómo gestionáis las migraciones al actualizar el binario?", Status: "approved", IP: ip})
	st.CreateComment(ctx, store.NewComment{PostID: firstID, ParentID: &c1, UserID: &admin, AuthorName: "Lucía Ferrer", AuthorEmail: "admin@example.org", Content: "Se aplican solas al arrancar, dentro de una transacción y con un bloqueo para que dos procesos no choquen.", Status: "approved", IP: ip})
	st.CreateComment(ctx, store.NewComment{PostID: firstID, AuthorName: "Elena", AuthorEmail: "elena@example.net", Content: "Me encanta que no haga falta Node en el servidor.", Status: "approved", IP: ip})
	st.CreateComment(ctx, store.NewComment{PostID: firstID, AuthorName: "Visitante", AuthorEmail: "visitante@example.net", Content: "¿Tiene importador de WordPress?", Status: "pending", IP: ip})
	st.CreateComment(ctx, store.NewComment{PostID: firstID, AuthorName: "cheap pills", AuthorEmail: "spam@example.com", AuthorURL: "https://spam.example.com", Content: "Buy now!!!", Status: "spam", IP: ip})

	st.AddMenuItem(ctx, "primary", "", "Inicio", "/")
	st.AddMenuItem(ctx, "primary", "", "Seguridad", "/category/seguridad")
	st.AddMenuItem(ctx, "primary", "", "Rendimiento", "/category/rendimiento")
	st.AddMenuItem(ctx, "primary", "", "Sobre", "/sobre")
	st.AddMenuItem(ctx, "footer", "", "Contacto", "/contacto")
	st.AddMenuItem(ctx, "footer", "", "Privacidad", "/privacidad")
	for _, area := range []string{"sidebar", "footer"} {
		st.AddWidget(ctx, area, "", "recent_posts", "Lo último", map[string]string{"count": "4"})
		st.AddWidget(ctx, area, "", "categories", "Categorías", nil)
		st.AddWidget(ctx, area, "", "feeds", "Suscríbete", nil)
	}
	st.AddWidget(ctx, "sidebar", "", "tags", "Etiquetas", nil)
	st.AddWidget(ctx, "footer", "", "text", "Sobre el cuaderno", map[string]string{"text": "Un blog de ejemplo para **sap13ns**: rápido, seguro y sin adornos."})
	fmt.Println("seeded: users admin/marta/pablo/ana with password dev-password-123")
}

// gradient draws a soft two-colour diagonal gradient with a few circles.
func gradient(w, h int, hue float64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	a, b := hsl(hue, 0.75, 0.6), hsl(math.Mod(hue+50, 360), 0.8, 0.45)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := (float64(x)/float64(w) + float64(y)/float64(h)) / 2
			c := lerp(a, b, t)
			for _, cc := range [][3]float64{{0.72, 0.3, 0.22}, {0.25, 0.75, 0.3}} {
				dx, dy := float64(x)/float64(w)-cc[0], (float64(y)/float64(h)-cc[1])*float64(h)/float64(w)
				if d := math.Sqrt(dx*dx + dy*dy); d < cc[2] {
					c = lerp(c, color.RGBA{255, 255, 255, 255}, 0.12*(1-d/cc[2]))
				}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func lerp(a, b color.RGBA, t float64) color.RGBA {
	f := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.RGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), 255}
}

func hsl(h, s, l float64) color.RGBA {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g = c, x
	case h < 120:
		r, g = x, c
	case h < 180:
		g, b = c, x
	case h < 240:
		g, b = x, c
	case h < 300:
		r, b = x, c
	default:
		r, b = c, x
	}
	return color.RGBA{uint8((r + m) * 255), uint8((g + m) * 255), uint8((b + m) * 255), 255}
}

func slugify(s string) string {
	repl := map[rune]string{'á': "a", 'é': "e", 'í': "i", 'ó': "o", 'ú': "u", 'ñ': "n", ' ': "-"}
	var out []byte
	for _, r := range s {
		if v, ok := repl[r]; ok {
			out = append(out, v...)
		} else if r < 128 {
			out = append(out, byte(r))
		}
	}
	return string(out)
}

const bodyIntro = `La mayoría de sitios no necesitan un ecosistema de cientos de dependencias. Necesitan **publicar rápido**, no caerse y no ser una puerta de entrada para atacantes.

## Todo dentro

Plantillas, hojas de estilo, iconos y traducciones viajan *dentro* del ejecutable. Desplegar es copiar un archivo y reiniciar un servicio.

> Lo más seguro es el código que no existe.

### Lo que ganas

- Arranque en milisegondos y memoria contenida.
- Nada de ` + "`node_modules`" + ` en el servidor.
- Actualizaciones atómicas: el binario viejo o el nuevo, nunca una mezcla.

` + "```bash\nsudo systemctl stop sap13ns\nsudo install -m 0755 sap13ns /usr/local/bin/\nsudo systemctl start sap13ns\n```" + `

| Métrica | Valor |
|---|---|
| Binario | ~25 MB |
| Memoria en reposo | ~30 MB |
| Página en caché | < 1 ms |

Si quieres saber más, lee la [documentación de despliegue](/sobre).[^1]

[^1]: Las cifras dependen del número de plugins activos.
`

const bodyPlugins = `Los plugins son módulos **WebAssembly** que se ejecutan con límites estrictos de memoria y tiempo.

1. No pueden abrir archivos.
2. No pueden abrir sockets.
3. Solo ven los datos que el núcleo les envía.

Cada permiso —guardar datos, leer entradas publicadas o hacer peticiones HTTPS a un dominio concreto— se aprueba a mano y queda **ligado al hash** del módulo. Si alguien cambia el archivo, el plugin se detiene hasta que vuelvas a revisarlo.`

const bodyTheme = "Un pequeño script en el `<head>` aplica la clase `dark` antes de pintar.\n\n```js\nconst saved = localStorage.getItem('themeMode');\n```\n\nEl resto son variables CSS: cada tema define sus colores para claro y oscuro."

const bodyNginx = "nginx habla con sap13ns por un **socket Unix** con permisos `0660`. Ningún puerto queda expuesto.\n\nLos archivos subidos se sirven directamente desde nginx con una cabecera `Content-Security-Policy: sandbox`."

const bodyPasswords = "Usamos **Argon2id** con parámetros conservadores, una longitud mínima de 10 caracteres y una lista de contraseñas comunes.\n\nNada de obligar a usar mayúsculas o símbolos: una frase larga es más segura y más fácil de recordar."

const bodyFeeds = "Cada categoría, etiqueta y autor tiene sus propios feeds en tres formatos. Añade `/feed.xml`, `/feed.atom` o `/feed.json` a la dirección."

const bodySearch = "La columna `search` es un `tsvector` generado con pesos para título, extracto y contenido. Con un índice GIN, buscar entre miles de entradas tarda milisegundos."

const pageAbout = `**Cuaderno Ligero** es un sitio de ejemplo creado con sap13ns, un gestor de contenidos inspirado en WordPress pero centrado en tres ideas:

- **Sencillez**: un binario, PostgreSQL y nginx.
- **Seguridad**: CSP estricta, 2FA, plugins aislados.
- **Rapidez**: caché en memoria y HTML sin JavaScript innecesario.`

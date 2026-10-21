package network

import (
	"context"
	"testing"

	"sap13ns/internal/config"
)

// The paid plans give the network's AI with their daily allowances; the
// free one only says what it is.
func TestPlanAI(t *testing.T) {
	e := newNetEnv(t, func(o *RegistryOptions) {
		o.Config.NetworkAI = config.NetworkAI{Provider: "anthropic", APIKey: "k", ImageProvider: "alibaba", ImageAPIKey: "q", ImageModel: "qwen-image-2.0"}
	})
	ctx := context.Background()
	for _, c := range []struct {
		plan           string
		included       bool
		requests, imgs int
	}{{PlanPersonal, false, 0, 0}, {"pro", true, 200, 3}, {"business", true, 1000, 15}} {
		s := e.tenant("ai-"+c.plan, c.plan)
		p, err := siteHooks{r: e.reg, id: s.ID}.AIPlan(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if p.Included() != c.included || p.RequestsPerDay != c.requests || p.ImagesPerDay != c.imgs {
			t.Errorf("%s: %+v", c.plan, p)
		}
		if c.included && (p.Image == nil || p.Image.Provider != "alibaba" || p.Text.ImageModel != "") {
			t.Errorf("%s images: %+v %+v", c.plan, p.Text, p.Image)
		}
	}
}

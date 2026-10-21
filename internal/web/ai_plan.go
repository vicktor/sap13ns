package web

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sap13ns/internal/ai"
	"sap13ns/internal/store"
)

// The AI of a site comes from its own provider and key (any plan, and a
// single-site install), or from the network's provider, which the paid
// plans include within daily allowances (SiteLimits.AIPlan). Every
// feature that asks a model goes through aiSettings and aiProvider, so a
// site whose plan lost the network's AI sees it switched off.

var (
	errAIDaily       = errors.New("the plan's AI for today is used up")
	errAIImagesDaily = errors.New("the plan's images for today are used up")
)

// aiPlan is the AI the site's plan includes; none on a single-site
// install or the portal.
func (s *Server) aiPlan(ctx context.Context) (AIPlan, error) {
	if s.limits == nil {
		return AIPlan{}, nil
	}
	s.ai.mu.Lock()
	cached, at := s.ai.plan, s.ai.planAt
	s.ai.mu.Unlock()
	if cached != nil && time.Since(at) < time.Minute {
		return *cached, nil
	}
	p, err := s.limits.AIPlan(ctx)
	if err != nil {
		return AIPlan{}, err
	}
	s.ai.mu.Lock()
	s.ai.plan, s.ai.planAt = &p, time.Now()
	s.ai.mu.Unlock()
	return p, nil
}

// newAIProvider builds the provider of st: the network's, or the site's
// own, with images from another provider when one is set.
func (s *Server) newAIProvider(ctx context.Context, st store.AISettings) (ai.Provider, error) {
	if st.Source == store.AISourceNetwork {
		plan, err := s.aiPlan(ctx)
		if err != nil {
			return nil, err
		}
		if !plan.Included() {
			return nil, errors.New("the site's plan does not include the network's AI")
		}
		text, err := ai.New(*plan.Text, nil)
		if err != nil {
			return nil, fmt.Errorf("network AI: %w", err)
		}
		if plan.Image == nil {
			return text, nil
		}
		img, err := ai.New(*plan.Image, nil)
		if err != nil {
			return nil, fmt.Errorf("network AI images: %w", err)
		}
		return ai.Split(text, img), nil
	}
	key, err := s.aiKey(st)
	if err != nil {
		return nil, fmt.Errorf("decrypt AI key: %w", err)
	}
	cfg := ai.Config{Provider: st.Provider, APIKey: key, BaseURL: st.BaseURL, TextModel: st.TextModel,
		ImageModel: st.ImageModel, AllowInsecureLocal: st.AllowLocal, NoThinking: st.NoThinking}
	if st.AllowLocal {
		cfg.Timeout = 5 * time.Minute // local models on a laptop are slow
	}
	if st.ImageProvider != "" {
		cfg.ImageModel = ""
	}
	text, err := ai.New(cfg, nil)
	if err != nil || st.ImageProvider == "" {
		return text, err
	}
	ikey, err := s.aiImageKey(st)
	if err != nil {
		return nil, fmt.Errorf("decrypt AI image key: %w", err)
	}
	img, err := ai.New(ai.Config{Provider: st.ImageProvider, APIKey: ikey, BaseURL: st.ImageBaseURL, ImageModel: st.ImageModel}, nil)
	if err != nil {
		return nil, fmt.Errorf("images: %w", err)
	}
	return ai.Split(text, img), nil
}

func (s *Server) aiImageKey(st store.AISettings) (string, error) {
	if len(st.ImageKeyEnc) == 0 {
		return "", nil
	}
	b, err := s.keys.Decrypt(st.ImageKeyEnc, []byte("ai-image-key"))
	return string(b), err
}

// today is the date in the site's time zone.
func (s *Server) today() time.Time { return time.Now().In(s.loc()) }

// aiDaily checks the plan's allowance for today, with the network's AI:
// one more request, and images more images.
func (s *Server) aiDaily(ctx context.Context, st store.AISettings, images int) error {
	if st.Source != store.AISourceNetwork {
		return nil
	}
	plan, err := s.aiPlan(ctx)
	if err != nil {
		return err
	}
	use, err := s.store.AINetworkUsageOn(ctx, s.today())
	if err != nil {
		return err
	}
	if use.Requests >= plan.RequestsPerDay {
		return errAIDaily
	}
	if images > 0 && use.Images+images > plan.ImagesPerDay {
		return errAIImagesDaily
	}
	return nil
}

// chargeDaily counts what the network's AI did today.
func (s *Server) chargeDaily(ctx context.Context, requests, images int) {
	st, err := s.aiSettingsSaved(ctx)
	if err != nil || st.Source != store.AISourceNetwork || requests == 0 && images == 0 {
		return
	}
	if err := s.store.AddAINetworkUsage(ctx, s.today(), requests, images); err != nil {
		s.log.Error("network AI usage", "err", err)
	}
}

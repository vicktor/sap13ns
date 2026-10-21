package ai

import "context"

// Split answers text with one provider and images with another (Claude
// for the writing, Qwen-Image for the pictures, say). It streams when the
// text provider does.
func Split(text, image Provider) Provider {
	if image == nil || image == text {
		return text
	}
	return &split{Provider: text, image: image}
}

type split struct {
	Provider
	image Provider
}

func (s *split) Image(ctx context.Context, req ImageRequest) ([]Image, error) {
	return s.image.Image(ctx, req)
}

func (s *split) TextStream(ctx context.Context, req TextRequest, fn func(Delta)) (*TextResponse, error) {
	if st, ok := s.Provider.(Streamer); ok {
		return st.TextStream(ctx, req, fn)
	}
	res, err := s.Provider.Text(ctx, req)
	if err == nil && res.Text != "" {
		fn(Delta{Text: res.Text})
	}
	return res, err
}

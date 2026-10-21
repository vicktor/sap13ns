package ai

import (
	"context"
	"net/http"
	"strings"
)

// alibaba is Alibaba Cloud Model Studio (DashScope). Text goes through its
// OpenAI-compatible mode; images through the synchronous Qwen-Image API,
// whose answer is a URL, valid for a day, that is downloaded at once.
type alibaba struct {
	*openAI
	root       string // the API host, without /compatible-mode/v1
	imageModel string
}

// The default host is Model Studio's international (Singapore) region;
// another one (dashscope-intl.aliyuncs.com, the Beijing
// dashscope.aliyuncs.com, a workspace's) goes in the base URL.
const alibabaBase = "https://ap-southeast-1.maas.qwencloudapi.com"

// alibabaSize maps an aspect ratio to a size Qwen-Image takes: any width
// and height with between 512×512 and 2048×2048 pixels in all.
var alibabaSize = map[string]string{
	Aspect16x9: "1664*928", AspectOG: "1696*888", Aspect1x1: "1328*1328",
	Aspect4x3: "1472*1104", Aspect3x2: "1584*1056",
}

type alibabaImageRequest struct {
	Model string `json:"model"`
	Input struct {
		Messages []alibabaMessage `json:"messages"`
	} `json:"input"`
	Parameters struct {
		Size      string `json:"size"`
		N         int    `json:"n"`
		Watermark bool   `json:"watermark"`
	} `json:"parameters"`
}

type alibabaMessage struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

type alibabaImageResponse struct {
	Output struct {
		Choices []struct {
			Message struct {
				Content []struct {
					Image string `json:"image"`
				} `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	} `json:"output"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (a *alibaba) Name() string { return ProviderAlibaba }

func (a *alibaba) Image(ctx context.Context, req ImageRequest) ([]Image, error) {
	if a.imageModel == "" {
		return nil, a.t.errorf("%w: no image model configured", ErrUnsupported)
	}
	if err := validateImage(&req); err != nil {
		return nil, err
	}
	var body alibabaImageRequest
	body.Model = a.imageModel
	body.Input.Messages = []alibabaMessage{{Role: "user", Content: []map[string]any{{"text": req.Prompt}}}}
	body.Parameters.Size = alibabaSize[req.Aspect]
	body.Parameters.N = req.N
	ctx, cancel := context.WithTimeout(ctx, a.t.imageTimeout)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+a.t.key)
	var out alibabaImageResponse
	if err := a.t.post(ctx, a.root+"/api/v1/services/aigc/multimodal-generation/generation", hdr, body, maxImageResponse, &out); err != nil {
		return nil, err
	}
	var urls []string
	for _, c := range out.Output.Choices {
		for _, part := range c.Message.Content {
			if part.Image != "" {
				urls = append(urls, part.Image)
			}
		}
	}
	if len(urls) == 0 {
		if out.Code != "" {
			return nil, a.t.errorf("%w: %s", ErrBadResponse, truncateRunes(out.Code+": "+out.Message, 200))
		}
		return nil, a.t.errorf("%w: no images", ErrBadResponse)
	}
	var imgs []Image
	for _, u := range urls[:min(len(urls), req.N)] {
		data, err := a.t.fetch(ctx, u)
		if err != nil {
			return nil, err
		}
		img, err := checkImage(data)
		if err != nil {
			return nil, a.t.errorf("%w", err)
		}
		imgs = append(imgs, img)
	}
	return imgs, nil
}

// alibabaRoot is the API host of a base URL that may name the
// OpenAI-compatible endpoint instead.
func alibabaRoot(base string) string {
	return strings.TrimSuffix(strings.TrimSuffix(base, "/v1"), "/compatible-mode")
}

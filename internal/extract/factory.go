package extract

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	qwenBase  = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	qwenModel = "qwen-vl-plus"

	doubaoBase = "https://ark.cn-beijing.volces.com/api/v3"

	glmBase  = "https://open.bigmodel.cn/api/paas/v4"
	glmModel = "glm-4v-plus"
)

// New returns the extractor named by opts.Provider.
// Unknown names fail. Remote providers fail when the API key (and, for 豆包,
// the model / endpoint id) is missing, so the server will not start half-configured.
func New(opts Options) (Extractor, error) {
	switch normalizeProvider(opts.Provider) {
	case "", "mock":
		return NewMock(), nil
	case "qwen", "tongyi", "dashscope", "qwen-vl":
		return newRemote("qwen", opts, qwenBase, qwenModel)
	case "doubao", "ark", "volcengine":
		return newRemote("doubao", opts, doubaoBase, "")
	case "glm", "zhipu", "bigmodel", "glm-4v":
		return newRemote("glm", opts, glmBase, glmModel)
	default:
		return nil, fmt.Errorf("unknown VISION_PROVIDER %q (want mock, qwen, doubao, or glm)", opts.Provider)
	}
}

func newRemote(name string, opts Options, defaultBase, defaultModel string) (Extractor, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, fmt.Errorf("%s: VISION_API_KEY is empty; the client is wired but API details are not filled in", name)
	}
	modelName := strings.TrimSpace(opts.Model)
	if modelName == "" {
		modelName = defaultModel
	}
	if modelName == "" {
		return nil, fmt.Errorf("%s: VISION_MODEL is required (豆包 uses an Ark endpoint id such as ep-...)", name)
	}
	base := strings.TrimSpace(opts.BaseURL)
	if base == "" {
		base = defaultBase
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	loc := opts.Location
	if loc == nil {
		loc = time.UTC
	}
	return &openAICompat{
		name:    name,
		apiKey:  strings.TrimSpace(opts.APIKey),
		baseURL: base,
		model:   modelName,
		loc:     loc,
		client:  client,
	}, nil
}

func normalizeProvider(p string) string {
	return strings.ToLower(strings.TrimSpace(p))
}

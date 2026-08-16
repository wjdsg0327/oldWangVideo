package domain

// DefaultProviderPresets 返回常见 OpenAI 兼容平台的地址模板。
func DefaultProviderPresets() []ProviderPreset {
	return []ProviderPreset{
		{
			ID:             "apikeyfun",
			Name:           "APIKEY.FUN",
			BaseURL:        "https://api.apikey.fun/v1",
			Description:    "已适配图片与视频生成的 OpenAI 兼容服务",
			APIKeyRequired: true,
		},
		{
			ID:             "aixoras",
			Name:           "AIXORAS",
			BaseURL:        "https://api.aixoras.com/v1",
			Description:    "已独立适配文生视频与图生视频",
			APIKeyRequired: true,
		},
		{
			ID:             "siliconflow",
			Name:           "硅基流动",
			BaseURL:        "https://api.siliconflow.cn/v1",
			Description:    "硅基流动 OpenAI 兼容接口",
			APIKeyRequired: true,
		},
		{
			ID:             "together",
			Name:           "Together AI",
			BaseURL:        "https://api.together.xyz/v1",
			Description:    "Together AI OpenAI 兼容接口",
			APIKeyRequired: true,
		},
		{
			ID:             "fireworks",
			Name:           "Fireworks AI",
			BaseURL:        "https://api.fireworks.ai/inference/v1",
			Description:    "Fireworks AI OpenAI 兼容接口",
			APIKeyRequired: true,
		},
		{
			ID:             "vllm",
			Name:           "本地 vLLM",
			BaseURL:        "http://127.0.0.1:8000/v1",
			Description:    "本机或局域网 vLLM 服务",
			Local:          true,
			APIKeyRequired: false,
		},
		{
			ID:             "ollama",
			Name:           "本地 Ollama",
			BaseURL:        "http://127.0.0.1:11434/v1",
			Description:    "Ollama OpenAI 兼容接口",
			Local:          true,
			APIKeyRequired: false,
		},
	}
}

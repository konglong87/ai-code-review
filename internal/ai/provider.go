package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"github.com/GuLuGuLuGit/review-go/internal/config"
)

// LLMProvider 抽象出一个最小的 LLM 能力接口，便于在不同提供商之间切换。
//
// 后续如果需要更多能力（流式输出、工具调用等），可以在不破坏现有调用方的前提下
// 通过扩展新接口或在实现内部做适配。
type LLMProvider interface {
	// Chat 发送一个简单的文本 prompt，返回完整的文本回复。
	Chat(prompt string) (string, error)

	// ChatStream 发送一个简单的文本 prompt，通过通道返回流式文本回复
	ChatStream(prompt string) (<-chan string, <-chan error)
}

// OpenAICompatibleProvider 使用 go-openai 客户端访问任意 OpenAI 兼容的后端。
//
// 通过配置 BaseURL、APIKey、Model 即可接入：
//   - 官方 OpenAI（默认 BaseURL，不必显式设置）
//   - DeepSeek: https://api.deepseek.com
//   - 通义千问 / Qwen (兼容模式): https://dashscope.aliyuncs.com/compatible-mode/v1
type OpenAICompatibleProvider struct {
	client *openai.Client
	model  string
}

// NewOpenAICompatibleProvider 创建一个基于 go-openai 的通用 Provider。
//
// baseURL 为空时，将使用 go-openai 的默认地址（即官方 OpenAI）。
// model 为空时，会退回到包内的 defaultModel。
func NewOpenAICompatibleProvider(baseURL, apiKey, model string) (*OpenAICompatibleProvider, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("apiKey 不能为空")
	}

	cfg := openai.DefaultConfig(apiKey)
	baseURL = strings.TrimSpace(baseURL)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}

	client := openai.NewClientWithConfig(cfg)

	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultModel
	}
	fmt.Println("model: ", model, "baseURL: ", baseURL, "apiKey: ", apiKey)
	return &OpenAICompatibleProvider{
		client: client,
		model:  model,
	}, nil
}

// Chat 调用兼容的 Chat Completions 接口，返回单轮对话结果。
func (p *OpenAICompatibleProvider) Chat(prompt string) (string, error) {
	if p == nil || p.client == nil {
		return "", errors.New("OpenAICompatibleProvider 未正确初始化：client 为空")
	}

	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", errors.New("prompt 不能为空")
	}

	req := openai.ChatCompletionRequest{
		Model:       p.model,
		Temperature: float32(defaultTemperature),
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: prompt,
			},
		},
	}

	ctx := context.Background()
	resp, err := p.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("调用 OpenAI 兼容接口失败: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", errors.New("LLM 返回结果为空：没有任何 choices")
	}

	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	if content == "" {
		return "", errors.New("LLM 返回的内容为空")
	}

	return content, nil
}

// ChatStream 调用兼容的 Chat Completions 流式接口，通过通道返回逐步生成的文本。
func (p *OpenAICompatibleProvider) ChatStream(prompt string) (<-chan string, <-chan error) {
	textChan := make(chan string)
	errChan := make(chan error, 1) // 缓冲为1，确保至少能发送一个错误

	go func() {
		defer close(textChan)
		defer close(errChan)

		if p == nil || p.client == nil {
			errChan <- errors.New("OpenAICompatibleProvider 未正确初始化：client 为空")
			return
		}

		prompt = strings.TrimSpace(prompt)
		if prompt == "" {
			errChan <- errors.New("prompt 不能为空")
			return
		}

		req := openai.ChatCompletionRequest{
			Model:       p.model,
			Temperature: float32(defaultTemperature),
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleUser,
					Content: prompt,
				},
			},
			Stream: true, // 启用流式响应
		}

		ctx := context.Background()
		stream, err := p.client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			errChan <- fmt.Errorf("调用 OpenAI 兼容流式接口失败: %w", err)
			return
		}
		defer stream.Close()

		for {
			resp, err := stream.Recv()
			if err != nil {
				if err.Error() == "EOF" {
					// 正常结束
					return
				}
				errChan <- fmt.Errorf("接收流式响应失败: %w", err)
				return
			}

			if len(resp.Choices) > 0 {
				content := resp.Choices[0].Delta.Content
				if content != "" {
					textChan <- content
				}
			}
		}
	}()

	return textChan, errChan
}

// NewProvider 根据配置创建一个合适的 LLMProvider 实例。
//
// 该工厂函数基于 ~/.review-go.yaml 中的配置：
//
//	provider: "deepseek" # 或 "openai" / "qwen" / 自定义名称
//
//	# 下方 providers.* 由 config.Load() 解析并“扁平化”到 cfg.APIKey / BaseURL / Model
//	providers:
//	  deepseek:
//	    api_key: "sk-..."
//	    base_url: "https://api.deepseek.com"
//	    model: "deepseek-coder"
//
// 对于已知厂商（deepseek / qwen），如果 BaseURL 或 Model 缺失，会在此处补齐默认值。
func NewProvider(cfg config.Config) (LLMProvider, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, errors.New("配置中的 api_key 不能为空")
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	model := strings.TrimSpace(cfg.Model)

	providerName := strings.ToLower(strings.TrimSpace(cfg.Provider))

	switch providerName {
	case "deepseek":
		// DeepSeek 默认兼容 OpenAI 接口
		if baseURL == "" {
			baseURL = "https://api.deepseek.com"
		}
		if model == "" {
			model = "deepseek-coder"
		}

	case "qwen", "tongyi", "ali", "aliyun":
		// 通义千问（Qwen）兼容模式
		if baseURL == "" {
			baseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
		}
		if model == "" {
			model = "qwen-turbo"
		}

	case "openai", "":
		// 官方 OpenAI 或老版本单一配置（provider 可能为空）。
		// - baseURL 留空时，go-openai 会自动使用官方默认地址。
		// - model 仍可使用 defaultModel 作为兜底。
		if model == "" {
			model = defaultModel
		}

	default:
		// 自定义兼容服务：完全尊重配置文件中的 base_url 与 model。
		// 如果用户未填写，则仍然用包内 defaultModel 兜底。
		if model == "" {
			model = defaultModel
		}
	}

	return NewOpenAICompatibleProvider(baseURL, apiKey, model)
}

package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// LoadFromFile 从指定路径读取配置。
func LoadFromFile(configPath string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")

	// 提前设置默认值，避免 key 不存在时返回空字符串。
	// 对于多提供商结构，我们只给最顶层 provider 设置一个空默认值，
	// 实际必填校验在后面进行。
	v.SetDefault("provider", "")
	v.SetDefault("api_key", "")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config file %s: %w", configPath, err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// 如果配置中定义了 providers，则走多提供商逻辑。
	if len(cfg.Providers) > 0 {
		if cfg.Provider == "" {
			return nil, fmt.Errorf("provider is empty in %s", configPath)
		}

		providerCfg, ok := cfg.Providers[cfg.Provider]
		if !ok {
			return nil, fmt.Errorf("provider %q not found under providers in %s", cfg.Provider, configPath)
		}

		if providerCfg.APIKey == "" {
			return nil, fmt.Errorf("api_key for provider %q is empty in %s", cfg.Provider, configPath)
		}

		// 将当前 provider 的配置"扁平化"到顶层字段，方便其他模块直接使用。
		cfg.APIKey = providerCfg.APIKey
		cfg.BaseURL = providerCfg.BaseURL
		cfg.Model = providerCfg.Model

		return &cfg, nil
	}

	// 兼容旧版：没有 providers 字段时，仍然要求存在顶层 api_key。
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("api_key is empty in %s", configPath)
	}

	return &cfg, nil
}

package cmd

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/konglong87/ai-code-review/internal/ai"
	"github.com/konglong87/ai-code-review/internal/config"
	"github.com/konglong87/ai-code-review/internal/ui"
)

var rootCmd = &cobra.Command{
	Use:   "review-go",
	Short: "review-go 是一个基于 LLM 的 Git 暂存区代码审查工具",
	Long: `review-go 是一个命令行工具，用于读取本地 Git 仓库暂存区的代码，
将分阶段变更发送给 LLM 进行代码审查，并在终端 TUI 中展示审查结果。

使用 'review-go config' 命令管理配置文件。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// 获取命令行参数
		targetBranch, _ := cmd.Flags().GetString("target-branch")
		noOutputIfSuccess, _ := cmd.Flags().GetBool("no-output-if-success")
		level, _ := cmd.Flags().GetString("level")
		stream, _ := cmd.Flags().GetBool("stream")
		configPath, _ := cmd.Flags().GetString("config")
		modelVersion, _ := cmd.Flags().GetString("model")
		fmt.Println("targetBranch===>: ", targetBranch)
		fmt.Println("noOutputIfSuccess===>", noOutputIfSuccess)
		fmt.Println("level===>", level)
		fmt.Println("stream===>", stream)
		fmt.Println("configPath===>", configPath)
		fmt.Println("modelVersion===>", modelVersion)

		// 读取配置并创建对应的 LLM Provider（支持 openai/deepseek/qwen 等）
		var cfg *config.Config
		var err error

		// 使用指定的配置文件路径
		cfg, err = config.LoadFromFile(configPath)

		if err != nil {
			return fmt.Errorf("加载配置失败: %w", err)
		}

		provider, err := ai.NewProvider(*cfg)
		if err != nil {
			return fmt.Errorf("初始化 LLM Provider 失败: %w", err)
		}
		fmt.Println("provider init success: ")

		// 根据stream参数决定使用哪种模式
		if stream {
			// 使用流式输出模式，启动 Bubble Tea TUI 主界面
			var m tea.Model

			// 根据modelVersion参数选择使用哪个Model
			switch modelVersion {
			case "v2":
				m = ui.NewModelV2(provider, targetBranch, noOutputIfSuccess, level)
			default:
				m = ui.NewModel(provider, targetBranch, noOutputIfSuccess, level)
			}

			p := tea.NewProgram(m, tea.WithAltScreen())

			if _, err := p.Run(); err != nil {
				return fmt.Errorf("启动 TUI 失败: %w", err)
			}
		} else {
			// 使用非流式输出模式，直接处理并输出结果
			if err := ui.ProcessFilesDirectly(provider, targetBranch, level); err != nil {
				return fmt.Errorf("处理文件失败: %w", err)
			}
		}

		return nil
	},
}

// Execute 是 CLI 的入口，由 main.go 调用。
func Execute() error {
	return rootCmd.Execute()
}

// init 添加命令行参数
func init() {
	// 添加 target-branch 参数
	rootCmd.Flags().StringP("target-branch", "t", "", "指定目标分支，用于比较当前分支与目标分支的差异")

	// 添加 no-output-if-success 参数
	rootCmd.Flags().BoolP("no-output-if-success", "s", false, "当代码审查通过时不输出任何内容")

	// 添加 level 参数
	rootCmd.Flags().StringP("level", "l", "CRITICAL", "输出级别 (CRITICAL, MAJOR, MINOR),默认CRITICAL")

	// 添加 stream 参数
	rootCmd.Flags().BoolP("stream", "r", false, "是否使用流式输出 (默认为 false)")

	// 添加 config 参数
	rootCmd.Flags().StringP("config", "c", "./review-go.yaml", "指定配置文件路径 (默认为 ./review-go.yaml)")

	// 添加 model 参数
	rootCmd.Flags().StringP("model", "m", "v2", "选择使用的 Model 版本 (v1 或 v2, 默认为 v1)")
}

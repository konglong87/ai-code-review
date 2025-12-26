package cmd

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/GuLuGuLuGit/review-go/internal/ai"
	"github.com/GuLuGuLuGit/review-go/internal/config"
	"github.com/GuLuGuLuGit/review-go/internal/ui"
)

var rootCmd = &cobra.Command{
	Use:   "review-go",
	Short: "review-go 是一个基于 LLM 的 Git 暂存区代码审查工具",
	Long: `review-go 是一个命令行工具，用于读取本地 Git 仓库暂存区的代码，
将分阶段变更发送给 LLM 进行代码审查，并在终端 TUI 中展示审查结果。

使用 'review-go config' 命令管理配置文件。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// 读取配置并创建对应的 LLM Provider（支持 openai/deepseek/qwen 等）
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("加载配置失败: %w", err)
		}

		provider, err := ai.NewProvider(*cfg)
		if err != nil {
			return fmt.Errorf("初始化 LLM Provider 失败: %w", err)
		}
		fmt.Println("provider init success: ")

		res, err := provider.Chat("你好")
		if err != nil {
			fmt.Println("调用 Chat 失败: ", err)
			return fmt.Errorf("调用 Chat 失败: %w", err)
		}
		fmt.Println("调用 Chat 成功: ", res)
		// 获取命令行参数
		targetBranch, _ := cmd.Flags().GetString("target-branch")
		noOutputIfSuccess, _ := cmd.Flags().GetBool("no-output-if-success")
		level, _ := cmd.Flags().GetString("level")
		fmt.Println("targetBranch=======>>>: ", targetBranch)
		fmt.Println("noOutputIfSuccess: ==?>>", noOutputIfSuccess)
		fmt.Println("level: ==?>>", level)
		// 启动 Bubble Tea TUI 主界面
		m := ui.NewModel(provider, targetBranch, noOutputIfSuccess, level)
		p := tea.NewProgram(m, tea.WithAltScreen())

		if _, err := p.Run(); err != nil {
			return fmt.Errorf("启动 TUI 失败: %w", err)
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
	rootCmd.Flags().StringP("level", "l", "MINOR", "过滤输出级别 (CRITICAL, MAJOR, MINOR)")
}

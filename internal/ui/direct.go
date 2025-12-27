package ui

import (
	"fmt"
	"github.com/konglong87/ai-code-review/internal/ai"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/konglong87/ai-code-review/internal/gitops"
)

// ProcessFilesDirectly 直接处理文件并输出结果，不使用 TUI
func ProcessFilesDirectly(provider ai.LLMProvider, targetBranch, level string) error {
	// 获取待审查的文件列表
	var files []string
	var err error

	if targetBranch != "" {
		// 比较指定分支与当前分支的差异
		files, err = gitops.GetBranchChangedFiles(targetBranch)
		if err != nil {
			return fmt.Errorf("获取分支差异文件失败: %w", err)
		}
	} else {
		// 获取暂存区中的文件
		files, err = gitops.GetChangedFiles()
		if err != nil {
			return fmt.Errorf("获取暂存区文件失败: %w", err)
		}
	}

	if len(files) == 0 {
		fmt.Println("没有找到需要审查的文件")
		return nil
	}

	// 处理每个文件
	for i, file := range files {
		fmt.Printf("正在审查文件 [%d/%d]: %s\n", i+1, len(files), file)

		// 获取文件差异
		var diff string
		if targetBranch != "" {
			diff, err = gitops.GetBranchFileDiff(targetBranch, file)
			if err != nil {
				fmt.Printf("获取文件 %s 的分支 diff 失败: %v\n", file, err)
				continue
			}
		} else {
			diff, err = getFileStagedDiff(file)
			if err != nil {
				fmt.Printf("获取文件 %s 的 diff 失败: %v", file, err)
				continue
			}
		}

		// 构建审查提示词
		reviewPrompt := buildReviewPromptDirect(diff, level)

		// 调用 AI 进行审查
		fmt.Println("正在调用 AI 进行审查...--\n", reviewPrompt)
		fmt.Printf("正在调用 AI 进行审查..prompt is. %s--\n", reviewPrompt)
		result, err := provider.Chat(reviewPrompt)
		if err != nil {
			fmt.Printf("调用 AI 审查文件 %s 失败: %v\n", file, err)
			continue
		}

		// 输出审查结果
		fmt.Printf("=== %s 审查结果 === ", file)
		rendered, err := glamour.Render(result, "dark")
		if err != nil {
			// 如果渲染失败，直接输出原始文本
			fmt.Println("渲染失败,原始文本==>", result)
		} else {
			fmt.Println("渲染 succ==> ", rendered)
		}
	}

	fmt.Println("所有文件审查完成!\n")
	return nil
}

// buildReviewPrompt 构建代码审查提示词
func buildReviewPromptDirect(diff string, level string) string {
	// 根据不同的级别构建不同的提示词
	levelPrompt := ""
	switch strings.ToUpper(level) {
	case "CRITICAL":
		levelPrompt = "请只关注[阻断]问题，如panic、逻辑错误、内存泄漏等。"
	case "MAJOR":
		levelPrompt = "请关注[高风险]问题，如性能瓶颈、设计模式、锁竞争等。"
	case "MINOR":
		levelPrompt = "请关注[所有]问题，包括代码风格、命名规范、注释等。"
	default:
		levelPrompt = "请关注所有问题，包括代码风格、命名规范、注释等。"
	}

	return fmt.Sprintf(`你是一个高级专业的Go研发工程师，熟悉微服务编码规范、请审查以下 Go 代码变更。

%s

请从以下几个方面评审维度进行审查：
1. 必查：并发安全（Goroutine/Mutex/Channel，符合Go并发规范V2.1）；
2. 必查：Error处理（必须封装业务错误码，禁止直接返回nil）；
3. 必查：性能（切片预分配、避免频繁GC，符合性能优化白皮书）；
4. 建议：代码可读性（命名符合Go命名规范）。

请以 Markdown 格式输出审查结果，包括：
- 问题等级（如果有）：阻断/高风险/中风险/建议
- 问题描述（如果有）：明确到行号+违反的规范编号
- 修复示例（如果有）：贴合go规范的代码片段

代码变更内容如下：

%s`, levelPrompt, diff)
}

// getFileStagedDiff 获取单个文件在暂存区中的 diff（仅该文件），等价于：
// git diff --cached --unified=0 -- <file>
//func getFileStagedDiff(file string) (string, error) {
//	cmd := exec.Command("git", "diff", "--cached", "--unified=0", "--", file)
//	output, err := cmd.CombinedOutput()
//	if err != nil {
//		return "", fmt.Errorf("执行 git diff 失败: %w, 输出: %s", err, string(output))
//	}
//	return string(output), nil
//}

package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/konglong87/ai-code-review/internal/ai"
	"github.com/konglong87/ai-code-review/internal/gitops"
)

// ModelV2 是 Bubble Tea 的主状态机，用于流式输出模式
type ModelV2 struct {
	files    []string
	reviews  map[string]string
	loading  bool
	selected int

	spinner  spinner.Model
	width    int
	height   int
	provider ai.LLMProvider

	targetBranch      string
	noOutputIfSuccess bool
	level             string

	// 进度跟踪
	totalFiles    int
	currentFile   int
	currentFileNm string

	// 流式响应状态
	streamingReview string        // 当前正在流式接收的审查内容
	isStreaming     bool          // 是否正在接收流式响应
	streamTextChan  <-chan string // 流式文本通道
	streamErrChan   <-chan error  // 流式错误通道

	err error

	// 定时器
	currentTime time.Time
}

// tickMsg 是定时器消息
type tickMsg time.Time

// tickEvery 创建一个定时器命令
func tickEvery() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// NewModelV2 创建一个带有初始 loading 状态和 Spinner 的 ModelV2
func NewModelV2(provider ai.LLMProvider, targetBranch string, noOutputIfSuccess bool, level string) tea.Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = spinnerStyle

	return &ModelV2{
		files:             nil,
		reviews:           make(map[string]string),
		loading:           true,
		selected:          0,
		spinner:           s,
		provider:          provider,
		targetBranch:      targetBranch,
		noOutputIfSuccess: noOutputIfSuccess,
		level:             level,
		currentTime:       time.Now(),
	}
}

// Init 在程序启动时被调用，这里启动：
// 1. spinner 的 Tick
// 2. 后台 Git + AI 审核任务
func (m ModelV2) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		loadReviewsCmdV2(m.provider, m.targetBranch, m.level),
		tickEvery(),
	)
}

// Update 处理来自 Bubble Tea 运行时的事件和命令
func (m ModelV2) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// 用户按下 "q" 键退出
		if msg.Type == tea.KeyCtrlC || msg.String() == "q" {
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case reviewLoadedMsg:
		m.loading = false
		if msg.err == nil {
			m.files = msg.files
			m.reviews = msg.reviews
			if len(m.files) > 0 && m.selected >= len(m.files) {
				m.selected = 0
			}
		}
		return m, nil

	case reviewProgressMsg:
		// 更新进度信息
		m.totalFiles = msg.total
		m.currentFile = msg.current
		m.currentFileNm = msg.file

		// 如果是第一个进度消息，初始化reviews并开始处理第一个文件
		if msg.current == 0 {
			m.reviews = make(map[string]string)
			// 获取文件列表
			var files []string
			var err error

			if m.targetBranch != "" {
				files, err = gitops.GetBranchChangedFiles(m.targetBranch)
			} else {
				files, err = gitops.GetChangedFiles()
			}

			if err != nil {
				m.err = err
				m.loading = false
				return m, nil
			}

			m.files = files
			return m, processNextFileCmdV2(files, 0, m.provider, m.targetBranch, m.level)
		}

		return m, nil

	case reviewStreamStartMsg:
		// 开始流式审查一个文件
		m.isStreaming = true
		m.currentFileNm = msg.file
		m.streamingReview = ""

		// 获取文件差异
		var diff string
		var err error

		if m.targetBranch != "" {
			diff, err = gitops.GetBranchFileDiff(m.targetBranch, msg.file)
			if err != nil {
				m.err = fmt.Errorf("获取文件 %s 的分支 diff 失败: %w", msg.file, err)
				m.loading = false
				return m, nil
			}
		} else {
			diff, err = getFileStagedDiff(msg.file)
			if err != nil {
				m.err = fmt.Errorf("获取文件 %s 的 diff 失败: %w", msg.file, err)
				m.loading = false
				return m, nil
			}
		}

		// 组合审查提示词
		reviewPrompt := buildReviewPrompt(diff, m.level)

		// 开始流式调用
		textChan, errChan := m.provider.ChatStream(reviewPrompt)

		// 保存流式通道
		m.streamTextChan = textChan
		m.streamErrChan = errChan

		return m, nil

	case reviewStreamChunkMsg:
		// 处理流式审查的一个文本块
		if m.isStreaming && m.currentFileNm == msg.file {
			m.streamingReview += msg.chunk
		}
		return m, nil

	case reviewStreamEndMsg:
		// 流式响应结束
		if m.isStreaming && m.currentFileNm == msg.file {
			m.isStreaming = false
			// 保存完整的审查结果
			m.reviews[msg.file] = m.streamingReview
			m.streamingReview = ""

			// 清理流式通道
			m.streamTextChan = nil
			m.streamErrChan = nil

			// 处理下一个文件
			nextIndex := m.currentFile + 1
			if nextIndex < len(m.files) {
				// 更新进度信息并处理下一个文件
				m.currentFile = nextIndex
				m.currentFileNm = m.files[nextIndex]
				return m, processNextFileCmdV2(m.files, nextIndex, m.provider, m.targetBranch, m.level)
			}

			// 所有文件处理完成
			m.loading = false
		}
		return m, nil

	case reviewResultMsg:
		// 处理单个文件的审查结果
		if msg.err != nil {
			m.err = msg.err
			m.loading = false
			return m, nil
		}

		// 保存审查结果
		m.reviews[msg.file] = msg.review

		// 处理下一个文件
		nextIndex := m.currentFile + 1
		if nextIndex < len(m.files) {
			return m, processNextFileCmdV2(m.files, nextIndex, m.provider, m.targetBranch, m.level)
		}

		// 所有文件处理完成
		m.loading = false
		return m, nil

	case tickMsg:
		// 1. 更新时间（确认状态变化）
		m.currentTime = time.Time(msg)

		// 2. 如果正在流式接收，检查流式响应
		if m.isStreaming {
			// 使用保存的流式通道
			textChan := m.streamTextChan
			errChan := m.streamErrChan

			// 直接检查流式响应，不使用命令
			select {
			case chunk, ok := <-textChan:
				if !ok {
					// 流结束
					m.isStreaming = false
					// 保存完整的审查结果
					m.reviews[m.currentFileNm] = m.streamingReview
					m.streamingReview = ""

					// 清理流式通道
					m.streamTextChan = nil
					m.streamErrChan = nil

					// 处理下一个文件
					nextIndex := m.currentFile + 1
					if nextIndex < len(m.files) {
						// 更新进度信息并处理下一个文件
						m.currentFile = nextIndex
						m.currentFileNm = m.files[nextIndex]
						return m, processNextFileCmdV2(m.files, nextIndex, m.provider, m.targetBranch, m.level)
					}

					// 所有文件处理完成
					m.loading = false
					return m, nil
				}
				m.streamingReview += chunk
				return m, nil
			case err, ok := <-errChan:
				if !ok {
					// 错误通道已关闭，没有错误
					m.isStreaming = false
					// 保存完整的审查结果
					m.reviews[m.currentFileNm] = m.streamingReview
					m.streamingReview = ""

					// 清理流式通道
					m.streamTextChan = nil
					m.streamErrChan = nil

					// 处理下一个文件
					nextIndex := m.currentFile + 1
					if nextIndex < len(m.files) {
						// 更新进度信息并处理下一个文件
						m.currentFile = nextIndex
						m.currentFileNm = m.files[nextIndex]
						return m, processNextFileCmdV2(m.files, nextIndex, m.provider, m.targetBranch, m.level)
					}

					// 所有文件处理完成
					m.loading = false
					return m, nil
				}
				m.err = fmt.Errorf("流式审查失败: %w", err)
				m.isStreaming = false
				m.loading = false
				return m, nil
			default:
				// 没有新数据，继续等待
				// 不需要返回命令，因为我们已经有定时器了
				return m, nil
			}
		}

		// 3. 核心：返回 下一次定时命令（手动维持持续更新）
		return m, tickEvery()
	}

	return m, nil
}

// View 渲染整个 UI
func (m ModelV2) View() string {
	if m.width == 0 {
		return "正在初始化..."
	}

	var content strings.Builder

	// 标题
	title := "🔍 代码审查"
	content.WriteString(lipgloss.NewStyle().
		Foreground(lipgloss.Color("205")).
		Bold(true).
		Padding(0, 1).
		Width(m.width).
		Render(title))
	content.WriteString("\n")

	// 加载状态
	if m.loading {
		content.WriteString(m.spinner.View())
		content.WriteString(" 正在审查代码...")
		content.WriteString("\n")

		// 显示当前进度
		if m.totalFiles > 0 {
			progress := fmt.Sprintf("进度: %d/%d", m.currentFile, m.totalFiles)
			if m.currentFileNm != "" {
				progress += fmt.Sprintf(" (%s)", m.currentFileNm)
			}
			content.WriteString(infoStyle.Render(progress))
			content.WriteString("\n")
		}
	}

	// 错误信息
	if m.err != nil {
		content.WriteString(errorStyle.Render(fmt.Sprintf("错误: %v", m.err)))
		content.WriteString("\n")
	}

	// 文件列表
	if len(m.files) > 0 {
		// 文件列表标题
		fileListTitle := fmt.Sprintf("文件列表 (%d)", len(m.files))
		content.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Bold(true).
			Render(fileListTitle))
		content.WriteString("\n")

		// 文件列表
		for i, file := range m.files {
			var fileStyle lipgloss.Style
			if i == m.selected {
				fileStyle = selectedFileStyle
			} else {
				fileStyle = normalFileStyle
			}
			content.WriteString(fileStyle.Render(fmt.Sprintf("  %s", file)))
			content.WriteString("\n")
		}
		content.WriteString("\n")
	}

	// 审查结果
	if m.selected < len(m.files) {
		selectedFile := m.files[m.selected]

		// 审查结果标题
		reviewTitle := fmt.Sprintf("审查结果 - %s", selectedFile)
		content.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Bold(true).
			Render(reviewTitle))
		content.WriteString("\n")

		// 如果正在流式接收，显示流式内容
		if m.isStreaming && m.currentFileNm == selectedFile {
			if m.streamingReview != "" {
				// 渲染 Markdown 格式的审查结果
				rendered, err := glamour.Render(m.streamingReview, "dark")
				if err != nil {
					// 如果渲染失败，直接输出原始文本
					content.WriteString(m.streamingReview)
				} else {
					content.WriteString(rendered)
				}

				// 添加光标表示正在生成
				content.WriteString(" ▌")
			} else {
				content.WriteString("正在生成审查结果...")
			}
		} else if review, ok := m.reviews[selectedFile]; ok {
			// 渲染 Markdown 格式的审查结果
			rendered, err := glamour.Render(review, "dark")
			if err != nil {
				// 如果渲染失败，直接输出原始文本
				content.WriteString(review)
			} else {
				content.WriteString(rendered)
			}
		} else {
			content.WriteString("暂无审查结果")
		}
	}

	// 添加调试信息（可选）
	debugInfo := fmt.Sprintf("[调试] 当前时间: %s", m.currentTime.Format("2006-01-02 15:04:05.000"))
	content.WriteString("\n")
	content.WriteString(lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Italic(true).
		Render(debugInfo))

	return content.String()
}

// loadReviewsCmdV2 在后台执行 Git + AI 审核逻辑，完成后发送 reviewLoadedMsg
func loadReviewsCmdV2(provider ai.LLMProvider, targetBranch string, level string) tea.Cmd {
	return func() tea.Msg {
		if provider == nil {
			return reviewLoadedMsg{err: fmt.Errorf("LLM Provider 未初始化")}
		}

		var files []string
		var err error

		// 如果指定了目标分支，则比较分支差异；否则使用暂存区
		if targetBranch != "" {
			files, err = gitops.GetBranchChangedFiles(targetBranch)
			if err != nil {
				return reviewLoadedMsg{err: fmt.Errorf("获取分支差异文件失败：%w", err)}
			}
		} else {
			files, err = gitops.GetChangedFiles()
			if err != nil {
				return reviewLoadedMsg{err: fmt.Errorf("获取暂存区文件失败：%w", err)}
			}
		}

		if len(files) == 0 {
			return reviewLoadedMsg{
				files:   []string{},
				reviews: map[string]string{},
				err:     nil,
			}
		}

		// 发送初始进度消息
		return reviewProgressMsg{
			current: 0,
			total:   len(files),
			file:    "",
		}
	}
}

// processNextFileCmdV2 处理下一个文件的审查
func processNextFileCmdV2(files []string, currentIndex int, provider ai.LLMProvider, targetBranch string, level string) tea.Cmd {
	return func() tea.Msg {
		if currentIndex >= len(files) {
			// 所有文件处理完成
			return reviewLoadedMsg{
				files:   files,
				reviews: make(map[string]string), // 这个map会在Update中被填充
				err:     nil,
			}
		}

		f := files[currentIndex]

		var err error

		// 验证文件是否存在且可获取diff
		if targetBranch != "" {
			_, err = gitops.GetBranchFileDiff(targetBranch, f)
			if err != nil {
				return reviewResultMsg{
					file: f,
					err:  fmt.Errorf("获取文件 %s 的分支 diff 失败：%w", f, err),
				}
			}
		} else {
			_, err = getFileStagedDiff(f)
			if err != nil {
				return reviewResultMsg{
					file: f,
					err:  fmt.Errorf("获取文件 %s 的 diff 失败：%w", f, err),
				}
			}
		}

		// 使用流式响应
		return reviewStreamStartMsg{
			file: f,
		}
	}
}

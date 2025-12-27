package ui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/konglong87/ai-code-review/internal/ai"
	"github.com/konglong87/ai-code-review/internal/gitops"
)

// reviewLoadedMsg 是后台审核任务完成后发送给 UI 的消息。
type reviewLoadedMsg struct {
	files   []string
	reviews map[string]string
	err     error
}

// reviewProgressMsg 用于报告审查进度的消息
type reviewProgressMsg struct {
	current int
	total   int
	file    string
}

// reviewResultMsg 用于传递单个文件审查结果的消息
type reviewResultMsg struct {
	file   string
	review string
	err    error
}

// reviewStreamStartMsg 表示开始流式审查一个文件
type reviewStreamStartMsg struct {
	file string
}

// reviewStreamChunkMsg 表示流式审查的一个文本块
type reviewStreamChunkMsg struct {
	file  string
	chunk string
}

// reviewStreamEndMsg 表示流式审查结束
type reviewStreamEndMsg struct {
	file string
}

// Model 是 Bubble Tea 的主状态机。
//
// - files: 暂存区中有变更的文件列表
// - reviews: 每个文件对应的 LLM 审查结果（Markdown）
// - loading: 是否处于加载状态（调用 Git + AI 中）
// - selected: 当前选中的文件索引
// - err: 加载过程中的错误（如果有）
// - provider: 用于实际调用 LLM 的接口实现
// - targetBranch: 目标分支，用于比较分支差异
// - noOutputIfSuccess: 审查通过时不输出
// - level: 过滤输出级别 (CRITICAL, MAJOR, MINOR)
type Model struct {
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
}

// 一些简单的样式定义，使用 lipgloss。
var (
	spinnerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205")).
			Padding(1, 2)

	infoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Padding(0, 2)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("1")).
			Padding(1, 2)

	fileListStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)

	selectedFileStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("229")).
				Background(lipgloss.Color("57")).
				Bold(true)

	normalFileStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	reviewStyle = lipgloss.NewStyle().
			Padding(0, 1)
)

// NewModel 创建一个带有初始 loading 状态和 Spinner 的 Model。
// 通过依赖注入的方式传入一个实现了 LLMProvider 接口的实例，
// 方便后续在不同 AI 提供商之间切换。
func NewModel(provider ai.LLMProvider, targetBranch string, noOutputIfSuccess bool, level string) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = spinnerStyle

	return Model{
		files:             nil,
		reviews:           make(map[string]string),
		loading:           true,
		selected:          0,
		spinner:           s,
		provider:          provider,
		targetBranch:      targetBranch,
		noOutputIfSuccess: noOutputIfSuccess,
		level:             level,
	}
}

// Init 在程序启动时被调用，这里启动：
// 1. spinner 的 Tick
// 2. 后台 Git + AI 审核任务
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		loadReviewsCmd(m.provider, m.targetBranch, m.level),
	)
}

// loadReviewsCmd 在后台执行 Git + AI 审核逻辑，完成后发送 reviewLoadedMsg。
func loadReviewsCmd(provider ai.LLMProvider, targetBranch string, level string) tea.Cmd {
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

// processNextFileCmd 处理下一个文件的审查
func processNextFileCmd(files []string, currentIndex int, provider ai.LLMProvider, targetBranch string, level string) tea.Cmd {
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

		// 发送进度更新
		if currentIndex > 0 {
			return reviewProgressMsg{
				current: currentIndex,
				total:   len(files),
				file:    f,
			}
		}

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

// processStreamCmd 处理流式响应
func processStreamCmd(file string, textChan <-chan string, errChan <-chan error) tea.Cmd {
	if textChan == nil && errChan == nil {
		// 这是一个继续处理的调用，返回一个空命令
		return nil
	}

	return func() tea.Msg {
		select {
		case chunk, ok := <-textChan:
			if !ok {
				// 流结束
				return reviewStreamEndMsg{
					file: file,
				}
			}
			return reviewStreamChunkMsg{
				file:  file,
				chunk: chunk,
			}
		case err, ok := <-errChan:
			if !ok {
				// 错误通道已关闭，没有错误
				return reviewStreamEndMsg{
					file: file,
				}
			}
			return reviewResultMsg{
				file: file,
				err:  fmt.Errorf("流式审查失败: %w", err),
			}
		}
	}
}

// processStreamMsg 处理流式响应消息
func processStreamMsg(file string, textChan <-chan string, errChan <-chan error) tea.Msg {
	select {
	case chunk, ok := <-textChan:
		if !ok {
			// 流结束
			return reviewStreamEndMsg{
				file: file,
			}
		}
		fmt.Println("--chunk===>", chunk, "file", file)
		return reviewStreamChunkMsg{
			file:  file,
			chunk: chunk,
		}
	case err, ok := <-errChan:
		if !ok {
			// 错误通道已关闭，没有错误
			return reviewStreamEndMsg{
				file: file,
			}
		}
		return reviewResultMsg{
			file: file,
			err:  fmt.Errorf("流式审查失败: %w", err),
		}
	default:
		fmt.Println("没有新数据，返回一个继续检查的命令")
		// 没有新数据，返回一个继续检查的命令
		return continueStreamCheckMsg{file: file}
	}
}

// continueStreamCheckMsg 表示继续检查流式响应
type continueStreamCheckMsg struct {
	file string
}

// continueStreamCheckCmd 创建一个继续检查流式响应的命令
func continueStreamCheckCmd(file string, textChan <-chan string, errChan <-chan error) tea.Cmd {
	return func() tea.Msg {
		select {
		case chunk, ok := <-textChan:
			if !ok {
				// 流结束
				return reviewStreamEndMsg{
					file: file,
				}
			}
			return reviewStreamChunkMsg{
				file:  file,
				chunk: chunk,
			}
		case err, ok := <-errChan:
			if !ok {
				// 错误通道已关闭，没有错误
				return reviewStreamEndMsg{
					file: file,
				}
			}
			return reviewResultMsg{
				file: file,
				err:  fmt.Errorf("流式审查失败: %w", err),
			}
		default:
			// 没有新数据，返回一个继续检查的命令
			return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return continueStreamCheckMsg{file: file}
			})
		}
	}
}

// getFileStagedDiff 获取单个文件在暂存区中的 diff（仅该文件），等价于：
// git diff --cached --unified=0 -- <file>
func getFileStagedDiff(file string) (string, error) {
	cmd := exec.Command("git", "diff", "--cached", "--unified=0", "--", file)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))

	if err != nil {
		if strings.Contains(output, "not a git repository") {
			return "", fmt.Errorf("当前目录不是 git 仓库：%s", output)
		}
		if output != "" {
			return "", fmt.Errorf("执行 git diff 失败：%s", output)
		}
		return "", fmt.Errorf("执行 git diff 失败：%w", err)
	}

	if output == "" {
		return "", fmt.Errorf("文件 %s 在暂存区没有 diff 输出", file)
	}

	return output, nil
}

// Update 处理所有消息（键盘事件、窗口大小变化、后台任务结果等）。
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		if m.loading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case reviewLoadedMsg:
		m.loading = false
		m.err = msg.err

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
			return m, processNextFileCmd(files, 0, m.provider, m.targetBranch, m.level)
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
			return m, processNextFileCmd(m.files, nextIndex, m.provider, m.targetBranch, m.level)
		}

		// 所有文件处理完成
		m.loading = false
		return m, nil

	case reviewStreamStartMsg:
		// 开始流式审查一个文件
		m.isStreaming = true
		m.streamingReview = ""
		// 更新当前文件名
		m.currentFileNm = msg.file

		// 获取流式通道
		f := msg.file
		var diff string
		var err error

		if m.targetBranch != "" {
			diff, err = gitops.GetBranchFileDiff(m.targetBranch, f)
			if err != nil {
				return m, func() tea.Msg {
					return reviewResultMsg{
						file: f,
						err:  fmt.Errorf("获取文件 %s 的分支 diff 失败：%w", f, err),
					}
				}
			}
		} else {
			diff, err = getFileStagedDiff(f)
			if err != nil {
				return m, func() tea.Msg {
					return reviewResultMsg{
						file: f,
						err:  fmt.Errorf("获取文件 %s 的 diff 失败：%w", f, err),
					}
				}
			}
		}

		// 组合审查提示词
		reviewPrompt := buildReviewPrompt(diff, m.level)

		// 开始流式调用
		textChan, errChan := m.provider.ChatStream(reviewPrompt)

		// 保存流式通道
		m.streamTextChan = textChan
		m.streamErrChan = errChan

		// 返回处理流式响应的命令
		return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
			return processStreamMsg(f, textChan, errChan)
		})

	case reviewStreamChunkMsg:
		// 处理流式响应的一个文本块
		if m.isStreaming && m.currentFileNm == msg.file {
			m.streamingReview += msg.chunk
		}
		// 不需要返回命令，因为我们使用定时器来检查流
		return m, nil

	case continueStreamCheckMsg:
		// 继续检查流式响应
		if m.isStreaming && m.currentFileNm == msg.file {
			// 使用保存的流式通道
			textChan := m.streamTextChan
			errChan := m.streamErrChan

			// 检查流式响应
			return m, continueStreamCheckCmd(msg.file, textChan, errChan)
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
				return m, processNextFileCmd(m.files, nextIndex, m.provider, m.targetBranch, m.level)
			}

			// 所有文件处理完成
			m.loading = false
		}
		return m, nil

	case tea.KeyMsg:
		// 全局退出快捷键
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}

		if m.loading || m.err != nil {
			// 加载中或出错时，不处理上下选择
			return m, nil
		}

		switch msg.String() {
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.selected < len(m.files)-1 {
				m.selected++
			}
		}
	}

	return m, nil
}

// View 根据当前状态渲染 TUI。
func (m Model) View() string {
	if m.loading {
		return m.viewLoading()
	}

	if m.err != nil {
		return m.viewError()
	}

	return m.viewContent()
}

func (m Model) viewLoading() string {
	sp := m.spinner.View()
	var text string

	if m.isStreaming {
		// 正在接收流式响应
		progress := fmt.Sprintf("[%d/%d] ", m.currentFile+1, m.totalFiles)
		text = fmt.Sprintf("正在接收 AI 对文件「%s」的审查结果...\n%s实时生成中，请稍候...",
			m.currentFileNm, progress)
	} else if m.targetBranch != "" {
		if m.totalFiles > 0 {
			// 显示进度信息
			progress := fmt.Sprintf("[%d/%d] ", m.currentFile, m.totalFiles)
			if m.currentFileNm != "" {
				text = fmt.Sprintf("正在用AI分析指定分支 「%s」 与 当前分支 的 代码差异，请稍候...\n%s正在审查: %s",
					m.targetBranch, progress, m.currentFileNm)
			} else {
				text = fmt.Sprintf("正在用AI分析指定分支 「%s」 与 当前分支 的 代码差异，请稍候...\n%s准备开始审查...",
					m.targetBranch, progress)
			}
		} else {
			text = fmt.Sprintf("正在用AI分析指定分支 「%s」 与 当前分支 的 代码差异，请稍候...", m.targetBranch)
		}
	} else {
		if m.totalFiles > 0 {
			// 显示进度信息
			progress := fmt.Sprintf("[%d/%d] ", m.currentFile, m.totalFiles)
			if m.currentFileNm != "" {
				text = fmt.Sprintf("正在分析暂存区中的 Go 代码并调用 AI 进行审查，请稍候...\n%s正在审查: %s",
					progress, m.currentFileNm)
			} else {
				text = fmt.Sprintf("正在分析暂存区中的 Go 代码并调用 AI 进行审查，请稍候...、\n%s准备开始审查...",
					progress)
			}
		} else {
			text = "正在分析暂存区中的 Go 代码并调用 AI 进行审查，请稍候..."
		}
	}
	text = text + "(按 q 退出)" + "\n(按 q 退出)"

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		spinnerStyle.Render(sp),
		infoStyle.Render(text),
	)

	return centerInTerminal(content, m.width, m.height)
}

func (m Model) viewError() string {
	msg := fmt.Sprintf("发生错误：\n\n%s\n\n按 q 退出。", m.err.Error())
	content := errorStyle.Render(msg)
	return centerInTerminal(content, m.width, m.height)
}

func (m Model) viewContent() string {
	if len(m.files) == 0 {
		if m.noOutputIfSuccess {
			// 如果设置了 noOutputIfSuccess，且没有文件变更，静默退出
			return ""
		}
		msg := "暂存区中没有 .go 文件的变更。\n\n请在 Git 暂存区中添加一些 Go 代码的修改后重新运行。\n\n按 q 退出。"
		return centerInTerminal(infoStyle.Render(msg), m.width, m.height)
	}

	// 简单的左右布局：左侧文件列表，右侧审查内容
	totalWidth := m.width
	if totalWidth <= 0 {
		totalWidth = 100
	}

	leftWidth := totalWidth / 4
	if leftWidth < 20 {
		leftWidth = 20
	}

	rightWidth := totalWidth - leftWidth - 4
	if rightWidth < 20 {
		rightWidth = 20
	}

	// 构造文件列表
	var fileLines []string
	for i, f := range m.files {
		line := f
		if i == m.selected {
			line = selectedFileStyle.Render("> " + f)
		} else {
			line = normalFileStyle.Render("  " + f)
		}
		fileLines = append(fileLines, line)
	}

	fileList := strings.Join(fileLines, "\n")
	fileListBox := fileListStyle.
		Width(leftWidth).
		Render(fileList)

	// 当前选中文件对应的审查结果
	var reviewMD string
	if m.selected >= 0 && m.selected < len(m.files) {
		file := m.files[m.selected]

		// 检查是否正在流式接收当前选中文件的内容
		if m.isStreaming && m.currentFileNm == file {
			// 显示正在流式接收的内容
			reviewMD = m.streamingReview
			if strings.TrimSpace(reviewMD) == "" {
				reviewMD = "_正在接收 AI 审查结果..._"
			} else {
				// 添加流式指示器
				reviewMD += "▌" // 光标指示器
			}
		} else {
			reviewMD = m.reviews[file]
			if strings.TrimSpace(reviewMD) == "" {
				reviewMD = "_该文件暂无审查结果。_"
			}
		}
	} else {
		reviewMD = "_未选中文件。_"
	}

	renderedReview := renderMarkdown(reviewMD, rightWidth)
	reviewBox := reviewStyle.
		Width(rightWidth).
		Render(renderedReview)

	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		fileListBox,
		reviewBox,
	)
}

// renderMarkdown 使用 glamour 渲染 Markdown 内容，如果失败则退回原始文本。
func renderMarkdown(md string, width int) string {
	if width <= 0 {
		width = 80
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return md
	}

	out, err := r.Render(md)
	if err != nil {
		return md
	}

	return out
}

// centerInTerminal 尝试把内容在终端中居中显示（如果知道终端宽高）。
func centerInTerminal(content string, width, height int) string {
	if width <= 0 || height <= 0 {
		return content
	}

	box := lipgloss.NewStyle().
		Width(width).
		Height(height).
		Align(lipgloss.Center, lipgloss.Center).
		Render(content)

	return box
}

// buildReviewPrompt 根据 Git diff 构造发送给 LLM 的审查提示词。
// 这里复用原先 Reviewer 中的系统说明，只是将其合并为一条用户消息，
// 以便通过通用的 Chat 接口发送。
func buildReviewPrompt(diff string, level string) string {
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return "暂存区 diff 为空，无需审查。"
	}

	levelFilter := ""
	switch strings.ToUpper(level) {
	case "CRITICAL":
		levelFilter = "请只关注 CRITICAL 级别的问题，即最严重的安全漏洞、错误处理缺陷和性能瓶颈。"
	case "MAJOR":
		levelFilter = "请关注 CRITICAL 和 MAJOR 级别的问题，即严重的安全问题、错误处理缺陷和性能问题。"
	case "MINOR":
		levelFilter = "请关注所有级别的问题，包括 CRITICAL、MAJOR 和 MINOR 级别的问题。"
	default:
		levelFilter = "请关注所有级别的问题，包括 CRITICAL、MAJOR 和 MINOR 级别的问题。"
	}

	systemPrompt := `你是一名资深 Golang 专家，擅长设计高可读性、可维护且鲁棒的 Go 代码。
现在请你扮演“代码审查助手”，针对给定的 Git diff 进行严格的代码评审，重点关注：

1. 安全性：
   - 输入校验是否充分
   - 是否存在潜在的注入风险、越界访问、竞争条件等
   - 敏感信息（如密钥、token、密码）是否有泄露风险

2. 错误处理：
   - 错误是否被忽略或吞掉
   - 错误信息是否清晰、能帮助定位问题
   - 是否合理使用 error wrapping 以及日志

3. 性能与资源使用：
   - 算法与数据结构是否合理
   - 是否存在明显的多余分配或重复计算
   - I/O、网络、并发是否可能成为瓶颈

` + levelFilter + `

请以 Markdown 格式输出审查结果，建议结构示例：

## 总体评价
- 简要评价这次变更的整体质量。

## 主要风险与问题
- 按严重程度列出主要问题，并引用相关代码片段或行号（如果 diff 中有）。

## 优化建议
- 给出可以改进的地方，包括安全、错误处理和性能方面的具体建议。

## 认可的优点
- 指出本次改动中值得保留或学习的写法。

回复时只需要给出审查内容，无需重复贴出完整 diff。`

	userPrompt := fmt.Sprintf(
		"请审查以下 Git diff（只读即可，不需要给出可直接应用的 patch），并按照上述要求返回 Markdown 格式的审查报告：\n\n```diff\n%s\n```",
		diff,
	)

	return systemPrompt + "\n\n" + userPrompt
}

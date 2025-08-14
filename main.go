package main

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

//go:embed contexts/*.md
var embeddedContexts embed.FS

// Helper functions
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Provider types
type ProviderType string

const (
	ProviderDigitalOcean ProviderType = "digitalocean"
)

// Provider configuration
type Provider struct {
	Type    ProviderType `json:"type"`
	BaseURL string       `json:"base_url"`
	APIKey  string       `json:"api_key,omitempty"`
	Models  []string     `json:"models"`
}

// Configuration
type Config struct {
	Providers       map[string]Provider  `json:"providers"`
	DefaultModel    string               `json:"default_model"`
	DefaultProvider string               `json:"default_provider"`
	MCPServers      map[string]MCPServer `json:"mcp_servers,omitempty"`
	AutoStartMCPs   []string             `json:"auto_start_mcps,omitempty"`
}

// Message represents a chat message
type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// MCP (Model Context Protocol) structures
type MCPServer struct {
	Name    string   `json:"name"`
	Command []string `json:"command"`
	Env     []string `json:"env,omitempty"`
	Active  bool     `json:"active"`
}

type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Schema      map[string]interface{} `json:"inputSchema,omitempty"`
}

type MCPResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type MCPClient struct {
	Server    *MCPServer
	Process   *exec.Cmd
	Stdin     io.WriteCloser
	Stdout    *bufio.Scanner
	Tools     []MCPTool
	Resources []MCPResource
	RequestID int
	Mutex     sync.Mutex
	DebugLog  func(level, message string, args ...interface{})
}

// JSON-RPC structures for MCP
type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type JSONRPCNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id,omitempty"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Orchestrator types for feedback loops
type OrchestratorStep struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`        // "mcp", "llm", "condition", "transform"
	Action      string                 `json:"action"`      // tool name, prompt template, or condition
	Input       interface{}            `json:"input,omitempty"`
	Condition   string                 `json:"condition,omitempty"`   // for conditional steps
	NextStep    map[string]string      `json:"next_step,omitempty"`   // conditional routing: {"success": "step2", "error": "step3"}
	Transform   string                 `json:"transform,omitempty"`   // data transformation template
	Timeout     time.Duration          `json:"timeout,omitempty"`     // step-specific timeout
	Parallel    bool                   `json:"parallel,omitempty"`    // can run in parallel
}

type OrchestratorPipeline struct {
	Name        string                 `json:"name"`
	Steps       map[string]OrchestratorStep `json:"steps"`
	StartStep   string                 `json:"start_step"`
	Context     map[string]interface{} `json:"context,omitempty"`
	MaxSteps    int                    `json:"max_steps,omitempty"`    // prevent infinite loops
	GlobalTimeout time.Duration        `json:"global_timeout,omitempty"` // 3 minutes default
}

type ExecutionContext struct {
	Variables       map[string]interface{}
	History         []StepResult
	CurrentStep     string
	StepCount       int
	StartTime       time.Time
	MaxSteps        int
	GlobalTimeout   time.Duration
	RateLimiter     *time.Ticker
	LastCallTime    time.Time
}

type StepResult struct {
	StepID      string      `json:"step_id"`
	Type        string      `json:"type"`
	Success     bool        `json:"success"`
	Output      interface{} `json:"output"`
	Error       error       `json:"error,omitempty"`
	Duration    time.Duration `json:"duration"`
	Timestamp   time.Time   `json:"timestamp"`
}

type OrchestratorState struct {
	Pipeline    *OrchestratorPipeline
	Context     *ExecutionContext
	Status      string // "running", "completed", "failed", "timeout"
	CurrentStep string
	Results     []StepResult
}

// For pending MCP format operations
type MCPFormatData struct {
	ToolName string
	Data     interface{}
	KeyMap   map[string]string
}

// Styles for the TUI
var (
	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8B4513")).
			Background(lipgloss.Color("#F5F5DC")).
			Padding(0, 1).
			Bold(true)

	smallInfoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			MarginLeft(2)

	userMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666")).
			Bold(true).
			MarginLeft(0).
			MarginRight(0)

	assistantMsgStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#666666")).
				Bold(true).
				MarginLeft(0).
				MarginRight(0)

	systemMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666")).
			Bold(true).
			MarginLeft(0).
			MarginRight(0)

	msgContentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666")).
			MarginLeft(0).
			MarginBottom(0).
			Width(0) // No width limit - allow full wrapping

	inputStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#FAFAF7")).
			Padding(0, 1).
			Margin(0)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262")).
			MarginTop(1).
			Italic(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF5555")).
			Bold(true)
)

// Chat model for the TUI
type ChatModel struct {
	config          *Config
	conversation    []Message
	currentProvider string
	currentModel    string
	viewport        viewport.Model
	textarea        textarea.Model
	ready           bool
	loading         bool
	client          *http.Client
	width           int
	height          int
	err             error
	spinnerIndex    int
	mcpClients      map[string]*MCPClient
	debugEnabled    bool
	debugWindow     viewport.Model
	debugLogs       []string
	
	// Tool call processing state
	toolCallActive     bool
	toolCallStep       string
	toolCallData       interface{}
	toolCallStepIndex  int
	processingToolResult bool // Flag to prevent tool call detection loops
	pendingToolCalls   []ToolCall // Queue of tool calls to execute in sequence
	
	// Orchestrator state
	orchestratorState  *OrchestratorState
	
	// Pending MCP formatting
	pendingMCPFormat   *MCPFormatData
	
	// Glamour markdown renderer
	markdownRenderer   *glamour.TermRenderer
	
	// Native tools
	nativeTools        map[string]NativeTool
}

type responseMsg struct {
	content string
	err     error
}

type toolCallStepMsg struct {
	step string
	data interface{}
}

type orchestratorStepMsg struct {
	stepID string
	result *StepResult
}

type tickMsg time.Time

var spinnerFrames = []string{"[+]", "[/]", "[-]", "[\\]"}

func initialModel(config *Config, debugEnabled bool) ChatModel {
	ta := textarea.New()
	ta.Placeholder = "Type your message here..."
	ta.Focus()
	ta.Prompt = ""
	ta.CharLimit = 4000
	ta.SetWidth(80) // Will be updated on first window size message
	ta.SetHeight(1)
	ta.MaxHeight = 10
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetEnabled(true)

	vp := viewport.New(80, 20)
	vp.SetContent("")

	// Initialize debug window
	debugVp := viewport.New(80, 3)
	if debugEnabled {
		debugVp.SetContent("🐛 Debug mode enabled...")
	}

	// Set default provider and model
	provider := config.DefaultProvider
	if provider == "" {
		for name := range config.Providers {
			provider = name
			break
		}
	}

	model := ""
	if len(config.Providers[provider].Models) > 0 {
		model = config.Providers[provider].Models[0]
	}

	model_instance := ChatModel{
		config:          config,
		conversation:    []Message{},
		currentProvider: provider,
		currentModel:    model,
		textarea:        ta,
		viewport:        vp,
		client:          &http.Client{Timeout: 120 * time.Second},
		mcpClients:      make(map[string]*MCPClient),
		debugEnabled:    debugEnabled,
		debugWindow:     debugVp,
		debugLogs:       []string{},
		nativeTools:     make(map[string]NativeTool),
	}
	
	// Initialize Glamour markdown renderer
	model_instance.initializeMarkdownRenderer()
	
	// Initialize native tools
	model_instance.initializeNativeTools()

	// Add initial debug logs
	if debugEnabled {
		model_instance.debugLog("CONFIG", "Configuration loaded with %d providers", len(config.Providers))
		model_instance.debugLog("CONFIG", "Default provider: %s, model: %s", provider, model)
		if len(config.MCPServers) > 0 {
			model_instance.debugLog("CONFIG", "Found %d MCP servers configured", len(config.MCPServers))
		}
		if len(config.AutoStartMCPs) > 0 {
			model_instance.debugLog("CONFIG", "Auto-starting %d MCP servers", len(config.AutoStartMCPs))
		}
	}

	// Auto-start configured MCP servers
	for _, serverName := range config.AutoStartMCPs {
		if _, exists := config.MCPServers[serverName]; exists {
			if debugEnabled {
				model_instance.debugLog("MCP", "Auto-starting MCP server: %s", serverName)
			}
			go model_instance.startMCPServer(serverName)
		} else {
			if debugEnabled {
				model_instance.debugLog("ERROR", "Auto-start MCP server '%s' not found in configuration", serverName)
			}
		}
	}

	return model_instance
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Millisecond*200, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m ChatModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m ChatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
	)

	m.textarea, tiCmd = m.textarea.Update(msg)
	m.updateTextareaHeight()
	m.viewport, vpCmd = m.viewport.Update(msg)

	switch msg := msg.(type) {
	case tickMsg:
		if m.loading || m.toolCallActive {
			m.spinnerIndex = (m.spinnerIndex + 1) % len(spinnerFrames)
			m.updateViewport()
			return m, tickCmd()
		}

	case tea.WindowSizeMsg:
		m.debugLog("TUI", "Window resize: %dx%d", msg.Width, msg.Height)
		m.width = msg.Width
		m.height = msg.Height

		headerHeight := 5
		footerHeight := 8
		debugHeight := 0
		if m.debugEnabled {
			debugHeight = 4 // 3 lines + 1 for border
		}
		verticalMarginHeight := headerHeight + footerHeight + debugHeight

		if !m.ready {
			m.viewport = viewport.New(msg.Width-4, msg.Height-verticalMarginHeight)
			m.viewport.YPosition = headerHeight
			if m.debugEnabled {
				m.debugWindow = viewport.New(msg.Width-4, 3)
			}
			m.ready = true
		} else {
			m.viewport.Width = msg.Width - 4
			m.viewport.Height = msg.Height - verticalMarginHeight
			if m.debugEnabled {
				m.debugWindow.Width = msg.Width - 4
			}
		}

		// Always update textarea width to fit the terminal, accounting for border (2 chars) and padding (2 chars)
		textareaWidth := msg.Width - 4
		if textareaWidth < 10 { // Minimum width
			textareaWidth = 10
		}
		m.textarea.SetWidth(textareaWidth)

		// Update viewport content when ready is first set to true
		if m.ready {
			m.updateViewport()
		}
		
		// Update markdown renderer width
		m.updateMarkdownWidth()

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit

		case tea.KeyEsc:
			if m.loading {
				return m, nil // Don't quit while loading
			}
			return m, tea.Quit

		case tea.KeyEnter:
			if m.loading {
				break
			}

			userInput := strings.TrimSpace(m.textarea.Value())
			if userInput == "" {
				m.debugLog("TUI", "Empty user input, ignoring")
				break
			}

			// Handle commands
			if strings.HasPrefix(userInput, "/") {
				cmdName := "unknown"
				if parts := strings.Fields(userInput[1:]); len(parts) > 0 {
					cmdName = parts[0]
				}
				m.debugLog("TUI", "Processing command: /%s", cmdName)
				m.textarea.Reset() // Clear the textarea after command
				return m.handleCommand(userInput[1:])
			}

			// Add user message
			userMsg := Message{
				Role:      "user",
				Content:   userInput,
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, userMsg)
			m.debugLog("TUI", "User message added (length: %d chars)", len(userInput))
			m.textarea.Reset()
			m.loading = true
			m.spinnerIndex = 0
			m.updateViewport()

			return m, tea.Batch(m.sendMessage(), tickCmd())
		}

	case responseMsg:
		m.loading = false
		if msg.err != nil {
			m.debugLog("ERROR", "API response error: %v", msg.err)
			m.err = msg.err
		} else if strings.TrimSpace(msg.content) == "" {
			m.debugLog("ERROR", "Received empty response from API")
			m.err = fmt.Errorf("received empty response from API")
		} else {
			m.debugLog("SUCCESS", "Received API response (%d chars)", len(msg.content))
			
			// Check if this is a response to MCP field analysis
			if m.pendingMCPFormat != nil {
				return m.processMCPFieldAnalysis(msg.content)
			}
			
			// Debug: Log the LLM response
			if m.processingToolResult {
				m.debugLog("LLM_RESPONSE", "LLM responded to tool result (length: %d chars): %s", len(msg.content), msg.content[:min(200, len(msg.content))])
			}
			
			// Check if this is a pipeline response (contains JSON pipeline)
			if m.isPipelineResponse(msg.content) {
				return m.processPipelineResponse(msg.content)
			}
			
			// Check for tool calls in the response (but not when processing tool results)
			if !m.processingToolResult && m.containsToolCalls(msg.content) {
				return m.processToolCallResponse(msg.content)
			}
			
			assistantMsg := Message{
				Role:      "assistant",
				Content:   msg.content,
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, assistantMsg)
			m.err = nil
			
			// Reset tool result processing flag
			if m.processingToolResult {
				m.debugLog("LLM_RESPONSE", "Completed tool result processing")
				m.processingToolResult = false
			}
		}
		m.updateViewport()
		
	case toolCallStepMsg:
		return m.processToolCallStep()
		
	case orchestratorStepMsg:
		return m.handleStepResult(msg.result)
		
	case autoToolCallResult:
		return m.handleAutoToolCallResult(msg)
		
	case nativeToolResult:
		return m.handleNativeToolResult(msg)
	}

	return m, tea.Batch(tiCmd, vpCmd)
}

func (m *ChatModel) handleCommand(cmd string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		m.debugLog("TUI", "Empty command received")
		return *m, nil
	}

	m.debugLog("TUI", "Executing command: %s with %d args", parts[0], len(parts)-1)

	switch parts[0] {
	case "clear":
		m.debugLog("TUI", "Clearing conversation history")
		m.conversation = []Message{}
		m.err = nil
		m.updateViewport()

	case "model":
		if len(parts) > 1 {
			oldModel := m.currentModel
			m.currentModel = parts[1]
			m.debugLog("TUI", "Switched model: %s -> %s", oldModel, m.currentModel)
			systemMsg := Message{
				Role:      "system",
				Content:   fmt.Sprintf("Switched to model: %s", m.currentModel),
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
			m.updateViewport()
		} else {
			m.debugLog("TUI", "Showing current model: %s", m.currentModel)
			systemMsg := Message{
				Role:      "system",
				Content:   fmt.Sprintf("Current model: %s", m.currentModel),
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
			m.updateViewport()
		}

	case "provider":
		if len(parts) > 1 {
			if _, exists := m.config.Providers[parts[1]]; exists {
				oldProvider := m.currentProvider
				m.currentProvider = parts[1]
				if len(m.config.Providers[parts[1]].Models) > 0 {
					m.currentModel = m.config.Providers[parts[1]].Models[0]
				}
				m.debugLog("TUI", "Switched provider: %s -> %s (model: %s)", oldProvider, m.currentProvider, m.currentModel)
				systemMsg := Message{
					Role:      "system",
					Content:   fmt.Sprintf("Switched to provider: %s (model: %s)", m.currentProvider, m.currentModel),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, systemMsg)
				m.updateViewport()
			} else {
				m.debugLog("WARN", "Provider not found: %s", parts[1])
				systemMsg := Message{
					Role:      "system",
					Content:   fmt.Sprintf("Provider '%s' not found", parts[1]),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, systemMsg)
				m.updateViewport()
			}
		} else {
			m.debugLog("TUI", "Showing current provider: %s", m.currentProvider)
			systemMsg := Message{
				Role:      "system",
				Content:   fmt.Sprintf("Current provider: %s", m.currentProvider),
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
			m.updateViewport()
		}

	case "providers":
		var providers []string
		for name := range m.config.Providers {
			providers = append(providers, name)
		}
		systemMsg := Message{
			Role:      "system",
			Content:   fmt.Sprintf("Available providers: %s", strings.Join(providers, ", ")),
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)
		m.updateViewport()

	case "models":
		if provider, exists := m.config.Providers[m.currentProvider]; exists {
			systemMsg := Message{
				Role:      "system",
				Content:   fmt.Sprintf("Available models for %s: %s", m.currentProvider, strings.Join(provider.Models, ", ")),
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
			m.updateViewport()
		}

	case "help":
		helpText := `Available commands:
/clear - Clear conversation
/model <name> - Switch model
/provider <name> - Switch provider
/providers - List providers
/models - List models for current provider
/tools - List all available tools (native + MCP)
/mcp start <server> - Start MCP server
/mcp stop <server> - Stop MCP server
/mcp list - List MCP servers
/mcp tools - List available MCP tools
/mcp call <tool> [args] - Call MCP tool (formatted output)
/mcp raw <tool> [args] - Call MCP tool (raw JSON output)
/deploy - Deploy current project to DigitalOcean App Platform
/plan <task> - Create and execute dynamic pipeline for complex tasks
/orchestrate <pipeline-json> - Execute orchestrator pipeline
/help - Show this help
/quit - Exit voyager

Controls:
Enter - Send message
Ctrl+C - Quit
Esc - Quit`

		systemMsg := Message{
			Role:      "system",
			Content:   helpText,
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)
		m.updateViewport()

	case "tools":
		// List all available tools (native + MCP)
		var toolList []string
		
		// Add native tools
		for name, tool := range m.nativeTools {
			toolList = append(toolList, fmt.Sprintf("  %s (native) - %s", name, tool.Description))
		}
		
		// Add MCP tools
		mcpTools := m.listMCPTools()
		for _, tool := range mcpTools {
			desc := tool.Description
			if desc == "" {
				desc = "No description available"
			}
			toolList = append(toolList, fmt.Sprintf("  %s (mcp) - %s", tool.Name, desc))
		}
		
		systemMsg := Message{
			Role:      "system",
			Content:   fmt.Sprintf("Available tools:\n%s", strings.Join(toolList, "\n")),
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)
		m.updateViewport()

	case "mcp":
		if len(parts) < 2 {
			systemMsg := Message{
				Role:      "system",
				Content:   "MCP commands: /mcp start <server>, /mcp stop <server>, /mcp list, /mcp tools, /mcp call <tool> [args], /mcp raw <tool> [args]",
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
			m.updateViewport()
		} else {
			return m.handleMCPCommand(parts[1:])
		}

	case "orchestrate":
		if len(parts) < 2 {
			systemMsg := Message{
				Role:      "system",
				Content:   "Usage: /orchestrate <pipeline-json>",
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
			m.updateViewport()
		} else {
			// Parse pipeline JSON from remaining arguments
			pipelineJSON := strings.Join(parts[1:], " ")
			var pipeline OrchestratorPipeline
			if err := json.Unmarshal([]byte(pipelineJSON), &pipeline); err != nil {
				systemMsg := Message{
					Role:      "system",
					Content:   fmt.Sprintf("Failed to parse pipeline JSON: %v", err),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, systemMsg)
				m.updateViewport()
			} else {
				// Add user message showing the orchestrate command
				userMsg := Message{
					Role:      "user",
					Content:   fmt.Sprintf("/orchestrate %s", pipeline.Name),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, userMsg)
				return m.startOrchestrator(&pipeline)
			}
		}

	case "deploy":
		// Add user message showing the deploy command
		userMsg := Message{
			Role:      "user",
			Content:   "/deploy",
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, userMsg)
		
		return m.handleDeployCommand()

	case "plan":
		if len(parts) < 2 {
			systemMsg := Message{
				Role:      "system",
				Content:   "Usage: /plan <task description>",
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
			m.updateViewport()
		} else {
			taskDescription := strings.Join(parts[1:], " ")
			// Add user message showing the plan command
			userMsg := Message{
				Role:      "user",
				Content:   fmt.Sprintf("/plan %s", taskDescription),
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, userMsg)
			
			return m.handlePlanCommand(taskDescription)
		}

	case "quit", "exit":
		return *m, tea.Quit

	default:
		systemMsg := Message{
			Role:      "system",
			Content:   fmt.Sprintf("Unknown command: /%s. Type /help for available commands.", parts[0]),
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)
		m.updateViewport()
	}

	return *m, nil
}

func (m ChatModel) sendMessage() tea.Cmd {
	return func() tea.Msg {
		response, err := m.makeAPIRequest()
		return responseMsg{content: response, err: err}
	}
}

func (m ChatModel) makeAPIRequest() (string, error) {
	provider := m.config.Providers[m.currentProvider]

	// Cast to *ChatModel for debug logging
	model := &m
	model.debugLog("API", "Starting API request to %s/%s", m.currentProvider, m.currentModel)

	switch provider.Type {
	case ProviderDigitalOcean:
		return m.sendDigitalOceanRequest(provider)
	default:
		model.debugLog("ERROR", "Unknown provider type: %s", provider.Type)
		return "", fmt.Errorf("unknown provider type: %s", provider.Type)
	}
}

func (m ChatModel) sendDigitalOceanRequest(provider Provider) (string, error) {
	type DigitalOceanRequest struct {
		Model       string    `json:"model"`
		Messages    []Message `json:"messages"`
		Temperature float64   `json:"temperature,omitempty"`
		MaxTokens   int       `json:"max_tokens,omitempty"`
	}

	type DigitalOceanResponse struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	// Convert messages to API format (without timestamp)
	var apiMessages []Message
	
	// Add system message with available MCP tools
	toolsContext := m.buildToolsContext()
	if toolsContext != "" {
		apiMessages = append(apiMessages, Message{
			Role:    "system",
			Content: toolsContext,
		})
	}
	
	for _, msg := range m.conversation {
		if msg.Role != "system" {
			apiMessages = append(apiMessages, Message{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}

	request := DigitalOceanRequest{
		Model:       m.currentModel,
		Messages:    apiMessages,
		Temperature: 0.7,
		MaxTokens:   8000,
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Cast to *ChatModel for debug logging
	model := &m
	model.debugLog("NET", "Sending OpenAI request to %s", provider.BaseURL)

	req, err := http.NewRequest("POST", provider.BaseURL, bytes.NewBuffer(requestBody))
	if err != nil {
		model.debugLog("ERROR", "Failed to create OpenAI request: %v", err)
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	resp, err := m.client.Do(req)
	if err != nil {
		model.debugLog("ERROR", "OpenAI request failed: %v", err)
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	model.debugLog("NET", "OpenAI response received (status: %s)", resp.Status)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// First, try to parse as OpenAI-compatible response
	var apiResp DigitalOceanResponse
	if err := json.Unmarshal(body, &apiResp); err == nil {
		if apiResp.Error != nil {
			return "", fmt.Errorf("API error: %s", apiResp.Error.Message)
		}

		if len(apiResp.Choices) > 0 {
			return apiResp.Choices[0].Message.Content, nil
		}
	}

	// If OpenAI format fails, try generic response parsing
	var genericResp map[string]interface{}
	if err := json.Unmarshal(body, &genericResp); err == nil {
		// Try common response field names
		if content, ok := genericResp["content"].(string); ok {
			return content, nil
		}
		if response, ok := genericResp["response"].(string); ok {
			return response, nil
		}
		if message, ok := genericResp["message"].(string); ok {
			return message, nil
		}
		if text, ok := genericResp["text"].(string); ok {
			return text, nil
		}

		// Check for nested content
		if data, ok := genericResp["data"].(map[string]interface{}); ok {
			if content, ok := data["content"].(string); ok {
				return content, nil
			}
			if response, ok := data["response"].(string); ok {
				return response, nil
			}
		}
	}

	// If all parsing fails, return the raw response for debugging
	model.debugLog("ERROR", "Unexpected response format: %s", string(body))
	return string(body), nil
}

// MCP Client methods
func (m *ChatModel) handleMCPCommand(parts []string) (tea.Model, tea.Cmd) {
	if len(parts) == 0 {
		return *m, nil
	}

	switch parts[0] {
	case "start":
		if len(parts) < 2 {
			systemMsg := Message{
				Role:      "system",
				Content:   "Usage: /mcp start <server-name>",
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
		} else {
			serverName := parts[1]
			if err := m.startMCPServer(serverName); err != nil {
				systemMsg := Message{
					Role:      "system",
					Content:   fmt.Sprintf("Failed to start MCP server '%s': %v", serverName, err),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, systemMsg)
			} else {
				systemMsg := Message{
					Role:      "system",
					Content:   fmt.Sprintf("Started MCP server '%s'", serverName),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, systemMsg)
			}
		}

	case "stop":
		if len(parts) < 2 {
			systemMsg := Message{
				Role:      "system",
				Content:   "Usage: /mcp stop <server-name>",
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
		} else {
			serverName := parts[1]
			if err := m.stopMCPServer(serverName); err != nil {
				systemMsg := Message{
					Role:      "system",
					Content:   fmt.Sprintf("Failed to stop MCP server '%s': %v", serverName, err),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, systemMsg)
			} else {
				systemMsg := Message{
					Role:      "system",
					Content:   fmt.Sprintf("Stopped MCP server '%s'", serverName),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, systemMsg)
			}
		}

	case "list":
		var serverList []string
		for name, server := range m.config.MCPServers {
			status := "inactive"
			if _, active := m.mcpClients[name]; active {
				status = "active"
			}
			serverList = append(serverList, fmt.Sprintf("  %s (%s) - %s", name, server.Name, status))
		}

		content := "MCP Servers:\n" + strings.Join(serverList, "\n")
		if len(serverList) == 0 {
			content = "No MCP servers configured"
		}

		systemMsg := Message{
			Role:      "system",
			Content:   content,
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)

	case "tools":
		tools := m.listMCPTools()
		var toolList []string
		for _, tool := range tools {
			desc := tool.Description
			if desc == "" {
				desc = "No description"
			}
			toolList = append(toolList, fmt.Sprintf("  %s - %s", tool.Name, desc))
		}

		content := "Available MCP Tools:\n" + strings.Join(toolList, "\n")
		if len(toolList) == 0 {
			content = "No MCP tools available (start an MCP server first)"
		}

		systemMsg := Message{
			Role:      "system",
			Content:   content,
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)

	case "call":
		if len(parts) < 2 {
			systemMsg := Message{
				Role:      "system",
				Content:   "Usage: /mcp call <tool-name> [arguments-as-json]",
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
		} else {
			toolName := parts[1]
			var arguments map[string]interface{}

			if len(parts) > 2 {
				jsonArg := strings.Join(parts[2:], " ")
				if err := json.Unmarshal([]byte(jsonArg), &arguments); err != nil {
					systemMsg := Message{
						Role:      "system",
						Content:   fmt.Sprintf("Failed to parse arguments: %v", err),
						Timestamp: time.Now(),
					}
					m.conversation = append(m.conversation, systemMsg)
					break
				}
			}

			client := m.getMCPClientForTool(toolName)
			if client == nil {
				systemMsg := Message{
					Role:      "system",
					Content:   fmt.Sprintf("Tool '%s' not found in any active MCP server", toolName),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, systemMsg)
			} else {
				result, err := client.callTool(toolName, arguments)
				if err != nil {
					systemMsg := Message{
						Role:      "system",
						Content:   fmt.Sprintf("Failed to call tool '%s': %v", toolName, err),
						Timestamp: time.Now(),
					}
					m.conversation = append(m.conversation, systemMsg)
				} else {
					// Use smart formatting with LLM-assisted field prioritization
					return m.formatMCPResponseWithLLM(toolName, result)
				}
			}
		}

	case "raw":
		if len(parts) < 2 {
			systemMsg := Message{
				Role:      "system",
				Content:   "Usage: /mcp raw <tool-name> [arguments-as-json]",
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
		} else {
			toolName := parts[1]
			var arguments map[string]interface{}

			if len(parts) > 2 {
				jsonArg := strings.Join(parts[2:], " ")
				if err := json.Unmarshal([]byte(jsonArg), &arguments); err != nil {
					systemMsg := Message{
						Role:      "system",
						Content:   fmt.Sprintf("Failed to parse arguments: %v", err),
						Timestamp: time.Now(),
					}
					m.conversation = append(m.conversation, systemMsg)
					break
				}
			}

			client := m.getMCPClientForTool(toolName)
			if client == nil {
				systemMsg := Message{
					Role:      "system",
					Content:   fmt.Sprintf("Tool '%s' not found in any active MCP server", toolName),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, systemMsg)
			} else {
				result, err := client.callTool(toolName, arguments)
				if err != nil {
					systemMsg := Message{
						Role:      "system",
						Content:   fmt.Sprintf("Failed to call tool '%s': %v", toolName, err),
						Timestamp: time.Now(),
					}
					m.conversation = append(m.conversation, systemMsg)
				} else {
					// Show raw JSON result with Glamour rendering
					resultJSON, _ := json.MarshalIndent(result, "", "  ")
					markdownContent := fmt.Sprintf("**Raw JSON result from %s:**\n\n```json\n%s\n```", toolName, string(resultJSON))
					renderedContent := m.renderMarkdown(markdownContent)
					
					systemMsg := Message{
						Role:      "assistant",
						Content:   renderedContent,
						Timestamp: time.Now(),
					}
					m.conversation = append(m.conversation, systemMsg)
				}
			}
		}

	default:
		systemMsg := Message{
			Role:      "system",
			Content:   fmt.Sprintf("Unknown MCP command: %s", parts[0]),
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)
	}

	m.updateViewport()
	return *m, nil
}

func (m *ChatModel) startMCPServer(serverName string) error {
	m.debugLog("MCP", "Starting MCP server: %s", serverName)

	server, exists := m.config.MCPServers[serverName]
	if !exists {
		m.debugLog("ERROR", "MCP server '%s' not found in configuration", serverName)
		return fmt.Errorf("MCP server '%s' not found in configuration", serverName)
	}

	if len(server.Command) == 0 {
		m.debugLog("ERROR", "MCP server '%s' has no command configured", serverName)
		return fmt.Errorf("MCP server '%s' has no command configured", serverName)
	}

	// Start MCP server in background to avoid blocking TUI
	go func() {
		// Expand environment variables in command arguments
		expandedCommand := make([]string, len(server.Command))
		for i, arg := range server.Command {
			expandedCommand[i] = os.ExpandEnv(arg)
		}

		// Expand environment variables in server.Env
		expandedEnv := make([]string, len(server.Env))
		for i, envVar := range server.Env {
			expandedEnv[i] = expandMCPEnvVar(envVar)
		}

		// Start the MCP server process
		cmd := exec.Command(expandedCommand[0], expandedCommand[1:]...)
		cmd.Env = append(os.Environ(), expandedEnv...)

		// Debug: log the command being executed
		m.debugLog("MCP", "Starting MCP server with command: %v", expandedCommand)
		m.debugLog("MCP", "Environment: %v", expandedEnv)

		// Capture stderr for debugging
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		// Keep current working directory for environment access
		// cmd.Dir = os.TempDir() // Run in temp directory

		stdin, err := cmd.StdinPipe()
		if err != nil {
			return
		}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return
		}

		if err := cmd.Start(); err != nil {
			m.debugLog("ERROR", "Failed to start MCP server: %v", err)
			return
		}

		scanner := bufio.NewScanner(stdout)
		// Increase buffer size to handle large MCP responses (default is 64KB, set to 1MB)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		client := &MCPClient{
			Server:    &server,
			Process:   cmd,
			Stdin:     stdin,
			Stdout:    scanner,
			RequestID: 1,
			DebugLog:  m.debugLog,
		}

		m.mcpClients[serverName] = client

		// Give server time to start
		time.Sleep(1 * time.Second)

		// Try to initialize
		if err := client.initialize(); err != nil {
			m.debugLog("ERROR", "Failed to initialize MCP server: %v", err)
			m.debugLog("ERROR", "Server stderr: %s", stderr.String())
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			delete(m.mcpClients, serverName)
			return
		}

		// Try to discover capabilities
		client.discoverCapabilities()

		// Mark as active
		if server, exists := m.config.MCPServers[serverName]; exists {
			server.Active = true
			m.config.MCPServers[serverName] = server
		}
	}()

	return nil
}

func (m ChatModel) startToolCallProcess(toolName string, arguments map[string]interface{}, result interface{}) (tea.Model, tea.Cmd) {
	// Add user message showing the tool call
	argsJSON := ""
	if arguments != nil {
		if argsBytes, err := json.Marshal(arguments); err == nil {
			argsJSON = " " + string(argsBytes)
		}
	}
	
	userMsg := Message{
		Role:      "user",
		Content:   fmt.Sprintf("/mcp call %s%s", toolName, argsJSON),
		Timestamp: time.Now(),
	}
	m.conversation = append(m.conversation, userMsg)
	
	// Set up tool call state
	m.toolCallActive = true
	m.toolCallStep = "Making tool call..."
	m.toolCallData = result
	m.toolCallStepIndex = 0
	
	m.updateViewport()
	
	// Start the first step
	return m, func() tea.Msg {
		return toolCallStepMsg{step: "processing", data: result}
	}
}

func (m ChatModel) processToolCallStep() (tea.Model, tea.Cmd) {
	steps := []string{
		"Making tool call...",
		"Processing result...", 
		"Formatting response...",
		"Finalizing...",
	}
	
	if m.toolCallStepIndex < len(steps) {
		m.toolCallStep = steps[m.toolCallStepIndex]
		m.toolCallStepIndex++
		m.updateViewport()
		
		// Continue to next step after a short delay
		return m, tea.Tick(time.Millisecond*800, func(t time.Time) tea.Msg {
			return toolCallStepMsg{step: "continue", data: m.toolCallData}
		})
	} else {
		// Final step: send to LLM for formatting
		return m.finishToolCall()
	}
}

func (m ChatModel) finishToolCall() (tea.Model, tea.Cmd) {
	// Format the result as JSON
	resultJSON, _ := json.MarshalIndent(m.toolCallData, "", "  ")
	
	// Add system message with the tool result for LLM to format
	systemMsg := Message{
		Role:      "system",
		Content:   fmt.Sprintf("The MCP tool returned the following result. Please format this in a beautiful, human-readable way and explain what it means:\n\n```json\n%s\n```", string(resultJSON)),
		Timestamp: time.Now(),
	}
	m.conversation = append(m.conversation, systemMsg)
	
	// Reset tool call state and start LLM processing
	m.toolCallActive = false
	m.toolCallStep = ""
	m.toolCallData = nil
	m.toolCallStepIndex = 0
	m.loading = true
	
	m.updateViewport()
	return m, m.sendMessage()
}

// Orchestrator implementation
func (m *ChatModel) startOrchestrator(pipeline *OrchestratorPipeline) (tea.Model, tea.Cmd) {
	// Initialize execution context
	context := &ExecutionContext{
		Variables:     make(map[string]interface{}),
		History:       []StepResult{},
		CurrentStep:   pipeline.StartStep,
		StepCount:     0,
		StartTime:     time.Now(),
		MaxSteps:      pipeline.MaxSteps,
		GlobalTimeout: pipeline.GlobalTimeout,
		RateLimiter:   time.NewTicker(100 * time.Millisecond), // 10 calls per second max
		LastCallTime:  time.Now(),
	}
	
	// Set defaults
	if context.MaxSteps == 0 {
		context.MaxSteps = 50 // prevent infinite loops
	}
	if context.GlobalTimeout == 0 {
		context.GlobalTimeout = 3 * time.Minute
	}
	
	// Copy initial context variables
	for k, v := range pipeline.Context {
		context.Variables[k] = v
	}
	
	// Initialize orchestrator state
	m.orchestratorState = &OrchestratorState{
		Pipeline:    pipeline,
		Context:     context,
		Status:      "running",
		CurrentStep: pipeline.StartStep,
		Results:     []StepResult{},
	}
	
	m.debugLog("ORCHESTRATOR", "Starting pipeline '%s' with %d steps", pipeline.Name, len(pipeline.Steps))
	
	// Start the first step
	return m.executeCurrentStep()
}

func (m ChatModel) executeCurrentStep() (tea.Model, tea.Cmd) {
	if m.orchestratorState == nil {
		return m, nil
	}
	
	state := m.orchestratorState
	context := state.Context
	
	// Check global timeout
	if time.Since(context.StartTime) > context.GlobalTimeout {
		return m.finishOrchestrator("timeout", "Global timeout exceeded")
	}
	
	// Check max steps
	if context.StepCount >= context.MaxSteps {
		return m.finishOrchestrator("failed", "Maximum steps exceeded")
	}
	
	// Get current step
	step, exists := state.Pipeline.Steps[context.CurrentStep]
	if !exists {
		return m.finishOrchestrator("failed", fmt.Sprintf("Step '%s' not found", context.CurrentStep))
	}
	
	// Rate limiting - wait if needed
	<-context.RateLimiter.C
	
	m.debugLog("ORCHESTRATOR", "Executing step '%s' (type: %s)", step.ID, step.Type)
	context.StepCount++
	
	// Execute step based on type
	return m.executeStep(&step)
}

func (m ChatModel) executeStep(step *OrchestratorStep) (tea.Model, tea.Cmd) {
	startTime := time.Now()
	
	switch step.Type {
	case "mcp":
		return m.executeMCPStep(step, startTime)
	case "tool":
		return m.executeToolStep(step, startTime)
	case "llm":
		return m.executeLLMStep(step, startTime)
	case "condition":
		return m.executeConditionStep(step, startTime)
	case "transform":
		return m.executeTransformStep(step, startTime)
	default:
		result := &StepResult{
			StepID:    step.ID,
			Type:      step.Type,
			Success:   false,
			Error:     fmt.Errorf("unknown step type: %s", step.Type),
			Duration:  time.Since(startTime),
			Timestamp: time.Now(),
		}
		return m.handleStepResult(result)
	}
}

func (m ChatModel) executeMCPStep(step *OrchestratorStep, startTime time.Time) (tea.Model, tea.Cmd) {
	// Parse arguments from step input or context
	var arguments map[string]interface{}
	if step.Input != nil {
		if args, ok := step.Input.(map[string]interface{}); ok {
			arguments = args
		}
	}
	
	// Apply context variable substitution
	arguments = m.applyContextSubstitution(arguments)
	
	// Find MCP client for the tool
	client := m.getMCPClientForTool(step.Action)
	if client == nil {
		result := &StepResult{
			StepID:    step.ID,
			Type:      step.Type,
			Success:   false,
			Error:     fmt.Errorf("MCP tool '%s' not found", step.Action),
			Duration:  time.Since(startTime),
			Timestamp: time.Now(),
		}
		return m.handleStepResult(result)
	}
	
	// Execute MCP call asynchronously
	return m, func() tea.Msg {
		output, err := client.callTool(step.Action, arguments)
		result := &StepResult{
			StepID:    step.ID,
			Type:      step.Type,
			Success:   err == nil,
			Output:    output,
			Error:     err,
			Duration:  time.Since(startTime),
			Timestamp: time.Now(),
		}
		return orchestratorStepMsg{stepID: step.ID, result: result}
	}
}

func (m ChatModel) executeToolStep(step *OrchestratorStep, startTime time.Time) (tea.Model, tea.Cmd) {
	// Parse arguments from step input or context
	var arguments map[string]interface{}
	if step.Input != nil {
		// Apply context substitution to input
		arguments = m.applyContextSubstitution(step.Input.(map[string]interface{}))
	}
	
	toolName := step.Action
	
	// Check if it's a native tool first
	if tool, exists := m.nativeTools[toolName]; exists {
		m.debugLog("ORCHESTRATOR", "Executing native tool: %s", toolName)
		return m, func() tea.Msg {
			result, err := tool.Handler(arguments)
			duration := time.Since(startTime)
			stepResult := &StepResult{
				StepID:    step.ID,
				Type:      step.Type,
				Success:   err == nil,
				Output:    result,
				Error:     err,
				Duration:  duration,
				Timestamp: time.Now(),
			}
			return orchestratorStepMsg{stepID: step.ID, result: stepResult}
		}
	}
	
	// Otherwise try MCP tools
	client := m.getMCPClientForTool(toolName)
	if client == nil {
		duration := time.Since(startTime)
		result := &StepResult{
			StepID:    step.ID,
			Type:      step.Type,
			Success:   false,
			Output:    nil,
			Error:     fmt.Errorf("tool '%s' not found in native tools or MCP servers", toolName),
			Duration:  duration,
			Timestamp: time.Now(),
		}
		return m, func() tea.Msg {
			return orchestratorStepMsg{stepID: step.ID, result: result}
		}
	}
	
	// Execute MCP tool asynchronously
	m.debugLog("ORCHESTRATOR", "Executing MCP tool: %s", toolName)
	return m, func() tea.Msg {
		output, err := client.callTool(toolName, arguments)
		duration := time.Since(startTime)
		result := &StepResult{
			StepID:    step.ID,
			Type:      step.Type,
			Success:   err == nil,
			Output:    output,
			Error:     err,
			Duration:  duration,
			Timestamp: time.Now(),
		}
		return orchestratorStepMsg{stepID: step.ID, result: result}
	}
}

func (m ChatModel) executeLLMStep(step *OrchestratorStep, startTime time.Time) (tea.Model, tea.Cmd) {
	// Build prompt with context substitution
	prompt := m.applyStringSubstitution(step.Action)
	
	// Add to conversation for LLM call
	tempMsg := Message{
		Role:      "user",
		Content:   prompt,
		Timestamp: time.Now(),
	}
	m.conversation = append(m.conversation, tempMsg)
	
	// Make LLM call asynchronously
	return m, func() tea.Msg {
		response, err := m.makeAPIRequest()
		result := &StepResult{
			StepID:    step.ID,
			Type:      step.Type,
			Success:   err == nil,
			Output:    response,
			Error:     err,
			Duration:  time.Since(startTime),
			Timestamp: time.Now(),
		}
		return orchestratorStepMsg{stepID: step.ID, result: result}
	}
}

func (m ChatModel) executeConditionStep(step *OrchestratorStep, startTime time.Time) (tea.Model, tea.Cmd) {
	// Evaluate condition against context
	conditionResult := m.evaluateCondition(step.Condition)
	
	result := &StepResult{
		StepID:    step.ID,
		Type:      step.Type,
		Success:   true,
		Output:    conditionResult,
		Duration:  time.Since(startTime),
		Timestamp: time.Now(),
	}
	
	return m.handleStepResult(result)
}

func (m ChatModel) executeTransformStep(step *OrchestratorStep, startTime time.Time) (tea.Model, tea.Cmd) {
	// Apply transformation to context data
	transformed := m.applyTransformation(step.Transform)
	
	result := &StepResult{
		StepID:    step.ID,
		Type:      step.Type,
		Success:   true,
		Output:    transformed,
		Duration:  time.Since(startTime),
		Timestamp: time.Now(),
	}
	
	return m.handleStepResult(result)
}

func (m ChatModel) handleStepResult(result *StepResult) (tea.Model, tea.Cmd) {
	if m.orchestratorState == nil {
		return m, nil
	}
	
	// Add result to history
	m.orchestratorState.Context.History = append(m.orchestratorState.Context.History, *result)
	m.orchestratorState.Results = append(m.orchestratorState.Results, *result)
	
	// Update context variables with result
	m.orchestratorState.Context.Variables[result.StepID] = result.Output
	m.orchestratorState.Context.Variables["last_result"] = result.Output
	m.orchestratorState.Context.Variables["last_success"] = result.Success
	
	m.debugLog("ORCHESTRATOR", "Step '%s' completed (success: %v, duration: %v)", 
		result.StepID, result.Success, result.Duration)
	
	// Determine next step
	nextStep := m.determineNextStep(result)
	if nextStep == "" || nextStep == "end" {
		return m.finishOrchestrator("completed", "Pipeline completed successfully")
	}
	
	// Update current step and continue
	m.orchestratorState.Context.CurrentStep = nextStep
	m.orchestratorState.CurrentStep = nextStep
	
	return m.executeCurrentStep()
}

func (m ChatModel) determineNextStep(result *StepResult) string {
	step := m.orchestratorState.Pipeline.Steps[result.StepID]
	
	if len(step.NextStep) == 0 {
		return "end"
	}
	
	// Check for conditional routing
	if result.Success {
		if next, exists := step.NextStep["success"]; exists {
			return next
		}
	} else {
		if next, exists := step.NextStep["error"]; exists {
			return next
		}
	}
	
	// Default routing
	if next, exists := step.NextStep["default"]; exists {
		return next
	}
	
	return "end"
}

func (m ChatModel) finishOrchestrator(status, message string) (tea.Model, tea.Cmd) {
	if m.orchestratorState != nil {
		m.orchestratorState.Status = status
		
		// Cleanup
		if m.orchestratorState.Context.RateLimiter != nil {
			m.orchestratorState.Context.RateLimiter.Stop()
		}
		
		duration := time.Since(m.orchestratorState.Context.StartTime)
		m.debugLog("ORCHESTRATOR", "Pipeline finished: %s (%v, %d steps)", 
			status, duration, m.orchestratorState.Context.StepCount)
		
		// Add result to conversation with Glamour rendering
		markdownContent := m.formatOrchestratorResults(status, message)
		renderedContent := m.renderMarkdown(markdownContent)
		
		resultMsg := Message{
			Role:      "assistant",
			Content:   renderedContent,
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, resultMsg)
		
		// Reset state
		m.orchestratorState = nil
	}
	
	m.updateViewport()
	return m, nil
}

// Helper methods for context management and evaluation
func (m ChatModel) applyContextSubstitution(data map[string]interface{}) map[string]interface{} {
	if m.orchestratorState == nil || data == nil {
		return data
	}
	
	result := make(map[string]interface{})
	for k, v := range data {
		if str, ok := v.(string); ok {
			result[k] = m.applyStringSubstitution(str)
		} else {
			result[k] = v
		}
	}
	return result
}

func (m ChatModel) applyStringSubstitution(template string) string {
	if m.orchestratorState == nil {
		return template
	}
	
	// Simple template substitution - replace {{.variable}} with context values
	result := template
	for key, value := range m.orchestratorState.Context.Variables {
		placeholder := fmt.Sprintf("{{.%s}}", key)
		if str, ok := value.(string); ok {
			result = strings.ReplaceAll(result, placeholder, str)
		}
	}
	return result
}

func (m ChatModel) evaluateCondition(condition string) bool {
	// Simple condition evaluation - can be extended
	if condition == "" {
		return true
	}
	
	// Example: "last_success == true"
	if strings.Contains(condition, "last_success") {
		if success, exists := m.orchestratorState.Context.Variables["last_success"]; exists {
			return success.(bool)
		}
	}
	
	return true
}

func (m ChatModel) applyTransformation(transform string) interface{} {
	// Simple transformation - can be extended
	return m.applyStringSubstitution(transform)
}

func (m ChatModel) formatOrchestratorResults(status, message string) string {
	if m.orchestratorState == nil {
		return fmt.Sprintf("Orchestrator finished: %s - %s", status, message)
	}
	
	// Log detailed debug info
	m.debugLog("PIPELINE_COMPLETE", "Pipeline '%s' %s in %v (%d steps)", 
		m.orchestratorState.Pipeline.Name, 
		status,
		time.Since(m.orchestratorState.Context.StartTime),
		m.orchestratorState.Context.StepCount)
	
	// Extract and return only the clean user output
	var outputs []string
	var errors []string
	
	for _, res := range m.orchestratorState.Results {
		// Log step details to debug
		stepStatus := "SUCCESS"
		if !res.Success {
			stepStatus = "FAILED"
		}
		m.debugLog("PIPELINE_STEP", "Step %s (%s) %s in %v", res.StepID, res.Type, stepStatus, res.Duration)
		
		if res.Success && res.Output != nil {
			if outputStr, ok := res.Output.(string); ok && strings.TrimSpace(outputStr) != "" {
				// For bash date command, add context
				if res.Type == "tool" && strings.Contains(outputStr, "2025") {
					outputs = append(outputs, fmt.Sprintf("Current time: %s", strings.TrimSpace(outputStr)))
				} else {
					outputs = append(outputs, strings.TrimSpace(outputStr))
				}
			}
		}
		
		if res.Error != nil {
			errors = append(errors, fmt.Sprintf("Error: %v", res.Error))
			m.debugLog("PIPELINE_ERROR", "Step %s failed: %v", res.StepID, res.Error)
		}
	}
	
	// Return clean output for user
	var result strings.Builder
	for _, output := range outputs {
		result.WriteString(output + "\n")
	}
	for _, err := range errors {
		result.WriteString(err + "\n")
	}
	
	return strings.TrimSpace(result.String())
}

// LLM-assisted formatting for MCP tool results
func (m ChatModel) formatMCPResponseWithLLM(toolName string, result interface{}) (tea.Model, tea.Cmd) {
	// First, extract the actual data from MCP response format
	actualData := m.extractMCPContent(result)
	
	// Get a flat map of all available keys
	keyMap := m.extractAllKeys(actualData)
	
	if len(keyMap) == 0 {
		// Fallback to simple formatting with Glamour
		markdownContent := m.formatMCPResponse(toolName, actualData)
		renderedContent := m.renderMarkdown(markdownContent)
		
		systemMsg := Message{
			Role:      "assistant",
			Content:   renderedContent,
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)
		m.updateViewport()
		return m, nil
	}
	
	// Create prompt for LLM to analyze field importance
	keysJSON, _ := json.Marshal(keyMap)
	prompt := fmt.Sprintf(`I have data from a "%s" API call with the following fields: %s

Please analyze these fields and respond with ONLY a JSON array of the 5 most important/useful fields for displaying to a user, ordered by importance (most important first).

Consider:
- User-friendly identifiers (name, title, id)
- Status/state information  
- Key descriptive fields (url, description, type)
- Important metadata (date, size, count)

Example response format: ["name", "status", "url", "id", "created_at"]

Response:`, toolName, string(keysJSON))
	
	// Add the analysis request to conversation
	analysisMsg := Message{
		Role:      "user", 
		Content:   prompt,
		Timestamp: time.Now(),
	}
	m.conversation = append(m.conversation, analysisMsg)
	
	// Store the data for later formatting
	m.pendingMCPFormat = &MCPFormatData{
		ToolName: toolName,
		Data:     actualData,
		KeyMap:   keyMap,
	}
	
	// Trigger LLM call to get field priorities
	m.loading = true
	m.updateViewport()
	return m, m.sendMessage()
}

// Extract actual content from MCP response wrapper
func (m *ChatModel) extractMCPContent(result interface{}) interface{} {
	// Handle MCP response format with "content" field
	if resultMap, ok := result.(map[string]interface{}); ok {
		if content, exists := resultMap["content"]; exists {
			// If content is an array with text field
			if contentArray, ok := content.([]interface{}); ok && len(contentArray) > 0 {
				if firstContent, ok := contentArray[0].(map[string]interface{}); ok {
					if text, exists := firstContent["text"]; exists {
						// Try to parse the text as JSON
						if textStr, ok := text.(string); ok {
							var parsed interface{}
							if json.Unmarshal([]byte(textStr), &parsed) == nil {
								return parsed
							}
							return textStr
						}
					}
				}
			}
			return content
		}
	}
	return result
}

// Extract all unique keys from nested data structure
func (m *ChatModel) extractAllKeys(data interface{}) map[string]string {
	keyMap := make(map[string]string)
	m.extractKeysRecursive(data, keyMap, "")
	return keyMap
}

func (m *ChatModel) extractKeysRecursive(data interface{}, keyMap map[string]string, prefix string) {
	switch v := data.(type) {
	case map[string]interface{}:
		for key, value := range v {
			fullKey := key
			if prefix != "" {
				fullKey = prefix + "." + key
			}
			
			// Add sample value for LLM to understand the field
			sampleValue := m.getSampleValue(value)
			keyMap[fullKey] = sampleValue
			
			// Recursively extract nested keys (but limit depth)
			if len(strings.Split(fullKey, ".")) < 3 {
				m.extractKeysRecursive(value, keyMap, fullKey)
			}
		}
	case []interface{}:
		if len(v) > 0 {
			// Analyze first item in array
			m.extractKeysRecursive(v[0], keyMap, prefix)
		}
	}
}

func (m *ChatModel) getSampleValue(value interface{}) string {
	switch v := value.(type) {
	case string:
		if len(v) > 50 {
			return v[:50] + "..."
		}
		return v
	case int, int64, float64, bool:
		return fmt.Sprintf("%v", v)
	case []interface{}:
		return fmt.Sprintf("array[%d]", len(v))
	case map[string]interface{}:
		return "object"
	case nil:
		return "null"
	default:
		return "complex"
	}
}

// Format data using LLM-determined field priorities
func (m ChatModel) formatWithLLMPriorities(toolName string, data interface{}, priorityFields []string) string {
	switch actualData := data.(type) {
	case []interface{}:
		return m.formatArrayWithPriorities(toolName, actualData, priorityFields)
	case map[string]interface{}:
		return m.formatMapWithPriorities(toolName, actualData, priorityFields)
	default:
		return m.formatMCPResponse(toolName, data)
	}
}

func (m *ChatModel) formatArrayWithPriorities(toolName string, data []interface{}, priorityFields []string) string {
	var result strings.Builder
	result.WriteString(fmt.Sprintf("## %s Results\n\n", strings.Title(strings.ReplaceAll(toolName, "-", " "))))
	
	if len(data) == 0 {
		result.WriteString("*No results found*\n")
		return result.String()
	}
	
	// Use table format with priority fields
	result.WriteString("|")
	for _, field := range priorityFields {
		result.WriteString(fmt.Sprintf(" %s |", strings.Title(strings.ReplaceAll(field, "_", " "))))
	}
	result.WriteString("\n")
	
	// Separator
	result.WriteString("|")
	for range priorityFields {
		result.WriteString("-------|")
	}
	result.WriteString("\n")
	
	// Data rows
	for _, item := range data {
		if itemMap, ok := item.(map[string]interface{}); ok {
			result.WriteString("|")
			for _, field := range priorityFields {
				value := ""
				if val, exists := itemMap[field]; exists {
					value = m.simpleStringValue(val)
					if len(value) > 30 {
						value = value[:27] + "..."
					}
				}
				result.WriteString(fmt.Sprintf(" %s |", value))
			}
			result.WriteString("\n")
		}
	}
	
	return result.String()
}

func (m *ChatModel) formatMapWithPriorities(toolName string, data map[string]interface{}, priorityFields []string) string {
	var result strings.Builder
	result.WriteString(fmt.Sprintf("## %s Results\n\n", strings.Title(strings.ReplaceAll(toolName, "-", " "))))
	
	result.WriteString("| Field | Value |\n")
	result.WriteString("|-------|-------|\n")
	
	for _, field := range priorityFields {
		if value, exists := data[field]; exists {
			strValue := m.simpleStringValue(value)
			if strValue != "" {
				result.WriteString(fmt.Sprintf("| %s | %s |\n", 
					strings.Title(strings.ReplaceAll(field, "_", " ")), strValue))
			}
		}
	}
	
	return result.String()
}

// Smart response formatter for MCP tool results
func (m *ChatModel) formatMCPResponse(toolName string, result interface{}) string {
	// First, try to detect the data structure and format appropriately
	switch data := result.(type) {
	case map[string]interface{}:
		return m.formatMCPMapResponse(toolName, data)
	case []interface{}:
		return m.formatMCPArrayResponse(toolName, data)
	default:
		// Fallback to simple string representation
		if str, ok := result.(string); ok {
			return m.formatMCPStringResponse(toolName, str)
		}
		// For complex types, convert to JSON and format
		if jsonBytes, err := json.Marshal(result); err == nil {
			var parsed interface{}
			if json.Unmarshal(jsonBytes, &parsed) == nil {
				return m.formatMCPResponse(toolName, parsed)
			}
		}
		return fmt.Sprintf("**%s Result:**\n%v", toolName, result)
	}
}

func (m *ChatModel) formatMCPMapResponse(toolName string, data map[string]interface{}) string {
	var result strings.Builder
	result.WriteString(fmt.Sprintf("## %s Results\n\n", strings.Title(strings.ReplaceAll(toolName, "-", " "))))
	
	// Check if it contains a 'content' field (common MCP pattern)
	if content, exists := data["content"]; exists {
		return m.formatMCPResponse(toolName, content)
	}
	
	// Check if it's a list-like structure
	if items, exists := data["items"]; exists {
		return m.formatMCPResponse(toolName, items)
	}
	
	// Check for common array fields
	for key, value := range data {
		if arr, ok := value.([]interface{}); ok {
			if len(arr) > 0 {
				result.WriteString(fmt.Sprintf("### %s\n", strings.Title(key)))
				result.WriteString(m.formatMCPArrayResponse("", arr))
				result.WriteString("\n")
			}
		}
	}
	
	// If no arrays found, format as key-value pairs
	if result.Len() == len(fmt.Sprintf("## %s Results\n\n", strings.Title(strings.ReplaceAll(toolName, "-", " ")))) {
		result.WriteString("| Field | Value |\n")
		result.WriteString("|-------|-------|\n")
		
		for key, value := range data {
			// Skip complex nested objects for simple table view
			if str := m.simpleStringValue(value); str != "" {
				result.WriteString(fmt.Sprintf("| %s | %s |\n", strings.Title(key), str))
			}
		}
	}
	
	return result.String()
}

func (m *ChatModel) formatMCPArrayResponse(toolName string, data []interface{}) string {
	var result strings.Builder
	
	if toolName != "" {
		result.WriteString(fmt.Sprintf("## %s Results\n\n", strings.Title(strings.ReplaceAll(toolName, "-", " "))))
	}
	
	if len(data) == 0 {
		result.WriteString("*No results found*\n")
		return result.String()
	}
	
	// Check if all items are objects with similar structure (table format)
	if m.canFormatAsTable(data) {
		return result.String() + m.formatAsTable(data)
	}
	
	// Check if items are simple values (numbered list)
	if m.areSimpleValues(data) {
		return result.String() + m.formatAsNumberedList(data)
	}
	
	// Format as detailed list for complex objects
	return result.String() + m.formatAsDetailedList(data)
}

func (m *ChatModel) formatMCPStringResponse(toolName string, data string) string {
	// Try to parse as JSON first
	var parsed interface{}
	if json.Unmarshal([]byte(data), &parsed) == nil {
		return m.formatMCPResponse(toolName, parsed)
	}
	
	// Return as simple text
	return fmt.Sprintf("**%s:**\n%s", strings.Title(strings.ReplaceAll(toolName, "-", " ")), data)
}

func (m *ChatModel) canFormatAsTable(data []interface{}) bool {
	if len(data) == 0 {
		return false
	}
	
	// Check if first item is an object
	firstItem, ok := data[0].(map[string]interface{})
	if !ok {
		return false
	}
	
	// Get keys from first item
	firstKeys := make(map[string]bool)
	for key := range firstItem {
		firstKeys[key] = true
	}
	
	// Check if other items have similar structure (at least 50% key overlap)
	for _, item := range data[1:] {
		if itemMap, ok := item.(map[string]interface{}); ok {
			commonKeys := 0
			for key := range itemMap {
				if firstKeys[key] {
					commonKeys++
				}
			}
			// Require at least 50% common keys
			if float64(commonKeys)/float64(len(firstKeys)) < 0.5 {
				return false
			}
		} else {
			return false
		}
	}
	
	return len(data) <= 20 // Don't use tables for very long lists
}

func (m *ChatModel) formatAsTable(data []interface{}) string {
	if len(data) == 0 {
		return ""
	}
	
	// Get all unique keys
	allKeys := make(map[string]bool)
	for _, item := range data {
		if itemMap, ok := item.(map[string]interface{}); ok {
			for key := range itemMap {
				allKeys[key] = true
			}
		}
	}
	
	// Convert to sorted slice
	keys := make([]string, 0, len(allKeys))
	for key := range allKeys {
		keys = append(keys, key)
	}
	
	// Prioritize common fields
	priorityKeys := []string{"name", "id", "title", "type", "status", "url", "description"}
	finalKeys := []string{}
	
	// Add priority keys first
	for _, priority := range priorityKeys {
		for _, key := range keys {
			if strings.ToLower(key) == priority {
				finalKeys = append(finalKeys, key)
				break
			}
		}
	}
	
	// Add remaining keys
	for _, key := range keys {
		found := false
		for _, existing := range finalKeys {
			if key == existing {
				found = true
				break
			}
		}
		if !found {
			finalKeys = append(finalKeys, key)
		}
	}
	
	// Limit columns to prevent overly wide tables
	if len(finalKeys) > 5 {
		finalKeys = finalKeys[:5]
	}
	
	var result strings.Builder
	
	// Header
	result.WriteString("|")
	for _, key := range finalKeys {
		result.WriteString(fmt.Sprintf(" %s |", strings.Title(key)))
	}
	result.WriteString("\n")
	
	// Separator
	result.WriteString("|")
	for range finalKeys {
		result.WriteString("-------|")
	}
	result.WriteString("\n")
	
	// Rows
	for _, item := range data {
		if itemMap, ok := item.(map[string]interface{}); ok {
			result.WriteString("|")
			for _, key := range finalKeys {
				value := ""
				if val, exists := itemMap[key]; exists {
					value = m.simpleStringValue(val)
					// Limit cell content length
					if len(value) > 30 {
						value = value[:27] + "..."
					}
				}
				result.WriteString(fmt.Sprintf(" %s |", value))
			}
			result.WriteString("\n")
		}
	}
	
	return result.String()
}

func (m *ChatModel) areSimpleValues(data []interface{}) bool {
	for _, item := range data {
		switch item.(type) {
		case string, int, int64, float64, bool:
			continue
		default:
			return false
		}
	}
	return true
}

func (m *ChatModel) formatAsNumberedList(data []interface{}) string {
	var result strings.Builder
	
	for i, item := range data {
		result.WriteString(fmt.Sprintf("%d. %v\n", i+1, item))
	}
	
	return result.String()
}

func (m *ChatModel) formatAsDetailedList(data []interface{}) string {
	var result strings.Builder
	
	for i, item := range data {
		result.WriteString(fmt.Sprintf("### %d. ", i+1))
		
		if itemMap, ok := item.(map[string]interface{}); ok {
			// Try to find a title/name field
			title := ""
			for _, key := range []string{"name", "title", "id"} {
				if val, exists := itemMap[key]; exists {
					title = m.simpleStringValue(val)
					break
				}
			}
			
			if title != "" {
				result.WriteString(fmt.Sprintf("%s\n", title))
			} else {
				result.WriteString("Item\n")
			}
			
			// Add key details
			for key, value := range itemMap {
				if key != "name" && key != "title" && key != "id" {
					strVal := m.simpleStringValue(value)
					if strVal != "" && len(strVal) < 100 {
						result.WriteString(fmt.Sprintf("- **%s:** %s\n", strings.Title(key), strVal))
					}
				}
			}
		} else {
			result.WriteString(fmt.Sprintf("%v\n", item))
		}
		
		result.WriteString("\n")
	}
	
	return result.String()
}

func (m *ChatModel) simpleStringValue(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case int, int64:
		return fmt.Sprintf("%d", v)
	case float64:
		return fmt.Sprintf("%.2f", v)
	case bool:
		return fmt.Sprintf("%t", v)
	case nil:
		return ""
	default:
		// For complex types, don't include in simple view
		return ""
	}
}

// Process LLM response for MCP field analysis
func (m ChatModel) processMCPFieldAnalysis(response string) (tea.Model, tea.Cmd) {
	if m.pendingMCPFormat == nil {
		return m, nil
	}
	
	// Extract JSON array from LLM response
	priorityFields := m.extractFieldPriorities(response)
	
	if len(priorityFields) == 0 {
		m.debugLog("ERROR", "Could not parse field priorities from LLM response")
		// Fallback to simple formatting
		markdownContent := m.formatMCPResponse(m.pendingMCPFormat.ToolName, m.pendingMCPFormat.Data)
		renderedContent := m.renderMarkdown(markdownContent)
		
		systemMsg := Message{
			Role:      "assistant",
			Content:   renderedContent,
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)
	} else {
		m.debugLog("SUCCESS", "Got field priorities: %v", priorityFields)
		// Format using LLM-determined priorities
		markdownContent := m.formatWithLLMPriorities(
			m.pendingMCPFormat.ToolName, 
			m.pendingMCPFormat.Data, 
			priorityFields,
		)
		
		// Render with Glamour
		renderedContent := m.renderMarkdown(markdownContent)
		
		systemMsg := Message{
			Role:      "assistant",
			Content:   renderedContent,
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)
	}
	
	// Clear pending format data
	m.pendingMCPFormat = nil
	m.updateViewport()
	return m, nil
}

// Extract field priorities from LLM response
func (m *ChatModel) extractFieldPriorities(response string) []string {
	// Try to find JSON array in the response
	lines := strings.Split(response, "\n")
	
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			var fields []string
			if err := json.Unmarshal([]byte(line), &fields); err == nil {
				return fields
			}
		}
	}
	
	// Try to find JSON array anywhere in the response (more flexible parsing)
	start := strings.Index(response, "[")
	end := strings.LastIndex(response, "]")
	
	if start >= 0 && end > start {
		jsonStr := response[start : end+1]
		var fields []string
		if err := json.Unmarshal([]byte(jsonStr), &fields); err == nil {
			return fields
		}
	}
	
	// If JSON parsing fails, try to extract field names manually
	return m.extractFieldsManually(response)
}

// Fallback manual extraction of field names
func (m *ChatModel) extractFieldsManually(response string) []string {
	var fields []string
	
	// Look for quoted field names in the response
	re := regexp.MustCompile(`"([a-zA-Z_][a-zA-Z0-9_]*)"`)
	matches := re.FindAllStringSubmatch(response, -1)
	
	// Deduplicate and limit to 5 fields
	seen := make(map[string]bool)
	for _, match := range matches {
		if len(match) > 1 {
			field := match[1]
			if !seen[field] && len(fields) < 5 {
				// Verify this field exists in our key map
				if m.pendingMCPFormat != nil {
					for key := range m.pendingMCPFormat.KeyMap {
						if key == field || strings.HasSuffix(key, "."+field) {
							fields = append(fields, field)
							seen[field] = true
							break
						}
					}
				}
			}
		}
	}
	
	return fields
}

// Initialize Glamour markdown renderer
func (m *ChatModel) initializeMarkdownRenderer() {
	// Use available width for markdown rendering
	width := m.width - 8 // Account for padding and borders
	if width < 40 {
		width = 80 // Fallback for initial setup
	}
	
	// Create renderer with no color styling to let lipgloss handle colors
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("notty"), // No colors, plain text formatting only
		glamour.WithWordWrap(width),
	)
	if err != nil {
		// Fallback to basic renderer if notty fails
		renderer, _ = glamour.NewTermRenderer(
			glamour.WithStandardStyle("ascii"), // Another no-color option
			glamour.WithWordWrap(width),
		)
	}
	m.markdownRenderer = renderer
}

// Update markdown renderer width when terminal resizes
func (m *ChatModel) updateMarkdownWidth() {
	if m.markdownRenderer == nil {
		return
	}
	
	// Account for viewport padding and borders
	effectiveWidth := m.width - 8
	if effectiveWidth < 40 {
		effectiveWidth = 40 // Minimum readable width
	}
	
	// Recreate renderer with new width
	m.initializeMarkdownRendererWithWidth(effectiveWidth)
}

func (m *ChatModel) initializeMarkdownRendererWithWidth(width int) {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("notty"), // No colors, plain text formatting only
		glamour.WithWordWrap(width),
	)
	if err != nil {
		// Fallback
		renderer, _ = glamour.NewTermRenderer(
			glamour.WithStandardStyle("ascii"), // Another no-color option
			glamour.WithWordWrap(width),
		)
	}
	m.markdownRenderer = renderer
}

// Render markdown content with Glamour
func (m *ChatModel) renderMarkdown(content string) string {
	if m.markdownRenderer == nil {
		return content // Fallback to plain text
	}
	
	rendered, err := m.markdownRenderer.Render(content)
	if err != nil {
		m.debugLog("ERROR", "Failed to render markdown: %v", err)
		return content // Fallback to plain text
	}
	
	return strings.TrimSpace(rendered)
}

// Build context about available MCP tools for the LLM
func (m *ChatModel) buildToolsContext() string {
	if len(m.mcpClients) == 0 {
		return ""
	}
	
	var context strings.Builder
	context.WriteString("AVAILABLE TOOLS: You have access to the following MCP tools that can help answer questions:\n\n")
	
	toolCount := 0
	for serverName, client := range m.mcpClients {
		if len(client.Tools) > 0 {
			context.WriteString(fmt.Sprintf("**%s Server:**\n", serverName))
			for _, tool := range client.Tools {
				context.WriteString(fmt.Sprintf("- `%s`: %s\n", tool.Name, tool.Description))
				toolCount++
			}
			context.WriteString("\n")
		}
	}
	
	if toolCount == 0 {
		return ""
	}
	
	// Add native tools information
	context.WriteString("**Native Tools (always available):**\n")
	for name, tool := range m.nativeTools {
		context.WriteString(fmt.Sprintf("- `%s`: %s\n", name, tool.Description))
	}
	context.WriteString("\n")
	
	context.WriteString("USAGE INSTRUCTIONS:\n")
	context.WriteString("- When users ask questions that could be answered using these tools, suggest using the tool\n")
	context.WriteString("- Use format: \"I can help with that! Let me use the `tool-name` tool: [TOOL_CALL:tool-name:arguments]\"\n")
	context.WriteString("- For questions about DigitalOcean apps/droplets/resources, use the appropriate digitalocean tools\n")
	context.WriteString("- Always explain what the tool does before calling it\n\n")
	
	context.WriteString("IMPORTANT - ONE TOOL CALL ONLY:\n")
	context.WriteString("- For simple questions, use EXACTLY ONE tool call\n")
	context.WriteString("- Time/date: [TOOL_CALL:bash:{\"command\":\"date\"}] - NEVER make multiple date calls\n")
	context.WriteString("- Current directory: [TOOL_CALL:bash:{\"command\":\"pwd\"}]\n") 
	context.WriteString("- System info: [TOOL_CALL:bash:{\"command\":\"uname -a\"}]\n")
	context.WriteString("- CRITICAL: Do NOT make 2+ tool calls for simple questions\n")
	context.WriteString("- ONE command = ONE tool call\n\n")
	
	context.WriteString("TOOL CHAINING (only for complex multi-step operations):\n")
	context.WriteString("- ONLY use multiple tool calls when user asks for complex operations requiring multiple steps\n")
	context.WriteString("- For 'list files in current directory' or similar:\n")
	context.WriteString("  1. First: [TOOL_CALL:bash:{\"command\":\"pwd\"}]\n")
	context.WriteString("  2. Then: [TOOL_CALL:list_directory:{\"path\":\"/the/result/path\"}]\n")
	context.WriteString("- Do NOT chain tools for simple single-step questions\n")
	context.WriteString("- Always use absolute paths for filesystem MCP tools\n\n")
	
	return context.String()
}

// Check if LLM response contains tool calls
func (m *ChatModel) containsToolCalls(content string) bool {
	// Check for both formats: [TOOL_CALL:tool-name] and "TOOL_CALL: tool-name"
	re1 := regexp.MustCompile(`\[TOOL_CALL:([^:]+)(?::([^\]]*))?\]`)
	re2 := regexp.MustCompile(`TOOL_CALL:\s*([a-zA-Z0-9_-]+)`)
	
	// Debug log the content being checked
	m.debugLog("TOOL_DETECT", "Checking content for tool calls (length: %d)", len(content))
	
	match1 := re1.MatchString(content)
	match2 := re2.MatchString(content)
	
	if match1 || match2 {
		m.debugLog("TOOL_DETECT", "Found tool call - Format1: %v, Format2: %v", match1, match2)
	}
	
	return match1 || match2
}

// Clean up malformed LLM responses that contain weird tags
func (m *ChatModel) cleanMalformedResponse(content string) string {
	// Remove common malformed tags
	content = regexp.MustCompile(`<\|[^|]+\|>`).ReplaceAllString(content, "")
	content = regexp.MustCompile(`<python_tag>`).ReplaceAllString(content, "")
	content = regexp.MustCompile(`</python_tag>`).ReplaceAllString(content, "")
	
	// Clean up excessive whitespace and repeated patterns
	content = regexp.MustCompile(`\n{3,}`).ReplaceAllString(content, "\n\n")
	content = regexp.MustCompile(`\s{3,}`).ReplaceAllString(content, " ")
	
	// Remove repeated tool call attempts
	content = regexp.MustCompile(`(🔧 Executing tool: .*?\n){2,}`).ReplaceAllString(content, "🔧 Executing tool: `apps-list`...\n")
	
	return strings.TrimSpace(content)
}

// Process LLM response that contains tool calls
func (m ChatModel) processToolCallResponse(content string) (tea.Model, tea.Cmd) {
	// Clean up malformed response content first
	content = m.cleanMalformedResponse(content)
	
	// First, add the LLM's response to conversation (without tool calls)
	cleanContent := m.removeToolCallTags(content)
	assistantMsg := Message{
		Role:      "assistant",
		Content:   cleanContent,
		Timestamp: time.Now(),
	}
	m.conversation = append(m.conversation, assistantMsg)
	
	// Extract and execute tool calls
	toolCalls := m.extractToolCalls(content)
	if len(toolCalls) == 0 {
		m.updateViewport()
		return m, nil
	}
	
	// For any tool calls (single or multiple), ask LLM to plan proper steps
	m.debugLog("PIPELINE_PLAN", "Detected %d tool calls - asking LLM to plan execution steps", len(toolCalls))
	
	// Extract the user's original request from conversation
	var userRequest string
	if len(m.conversation) > 0 {
		lastMsg := m.conversation[len(m.conversation)-2] // Get user message before assistant response
		if lastMsg.Role == "user" {
			userRequest = lastMsg.Content
		}
	}
	
	if userRequest == "" {
		userRequest = "Execute the suggested tools"
	}
	
	// Ask LLM to plan the execution steps
	return m.handlePlanCommand(userRequest)
}

// Remove tool call tags from content for display
func (m *ChatModel) removeToolCallTags(content string) string {
	// Remove both formats
	re1 := regexp.MustCompile(`\[TOOL_CALL:[^\]]+\]`)
	content = re1.ReplaceAllString(content, "")
	
	re2 := regexp.MustCompile(`TOOL_CALL:\s*[a-zA-Z0-9_-]+`)
	content = re2.ReplaceAllString(content, "")
	
	return strings.TrimSpace(content)
}

// Extract tool calls from LLM response
func (m *ChatModel) extractToolCalls(content string) []ToolCall {
	var toolCalls []ToolCall
	
	m.debugLog("TOOL_EXTRACT", "Extracting tool calls from content: %s", content[:min(200, len(content))])
	
	// Format 1: [TOOL_CALL:tool-name:arguments]
	re1 := regexp.MustCompile(`\[TOOL_CALL:([^:]+)(?::([^\]]*))?\]`)
	matches1 := re1.FindAllStringSubmatch(content, -1)
	
	m.debugLog("TOOL_EXTRACT", "Format 1 matches found: %d", len(matches1))
	
	for _, match := range matches1 {
		toolCall := ToolCall{
			Name: strings.TrimSpace(match[1]),
		}
		
		m.debugLog("TOOL_EXTRACT", "Found tool call (Format 1): %s", toolCall.Name)
		
		// Parse arguments if provided
		if len(match) > 2 && match[2] != "" {
			argsStr := strings.TrimSpace(match[2])
			if argsStr != "" {
				var args map[string]interface{}
				if json.Unmarshal([]byte(argsStr), &args) == nil {
					toolCall.Arguments = args
					m.debugLog("TOOL_EXTRACT", "Parsed arguments: %v", args)
				} else {
					m.debugLog("TOOL_EXTRACT", "Failed to parse arguments: %s", argsStr)
				}
			}
		}
		
		// Ensure Arguments is never nil - initialize as empty map if not provided
		if toolCall.Arguments == nil {
			toolCall.Arguments = make(map[string]interface{})
		}
		
		toolCalls = append(toolCalls, toolCall)
	}
	
	// Format 2: TOOL_CALL: tool-name
	re2 := regexp.MustCompile(`TOOL_CALL:\s*([a-zA-Z0-9_-]+)`)
	matches2 := re2.FindAllStringSubmatch(content, -1)
	
	m.debugLog("TOOL_EXTRACT", "Format 2 matches found: %d", len(matches2))
	
	for _, match := range matches2 {
		toolCall := ToolCall{
			Name:      strings.TrimSpace(match[1]),
			Arguments: make(map[string]interface{}), // Initialize as empty map
		}
		m.debugLog("TOOL_EXTRACT", "Found tool call (Format 2): %s", toolCall.Name)
		toolCalls = append(toolCalls, toolCall)
	}
	
	m.debugLog("TOOL_EXTRACT", "Total tool calls extracted: %d", len(toolCalls))
	return toolCalls
}

// Execute tool call automatically suggested by LLM
func (m ChatModel) executeAutoToolCall(toolCall ToolCall) (tea.Model, tea.Cmd) {
	// First check if it's a native tool
	if _, exists := m.nativeTools[toolCall.Name]; exists {
		m.debugLog("NATIVE_TOOL", "Executing native tool: %s", toolCall.Name)
		// Add a message showing the tool execution
		executionMsg := Message{
			Role:      "system",
			Content:   fmt.Sprintf("🔧 Executing native tool: `%s`...", toolCall.Name),
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, executionMsg)
		m.updateViewport()
		
		return m.executeNativeTool(toolCall.Name, toolCall.Arguments)
	}
	
	// Then check MCP tools
	client := m.getMCPClientForTool(toolCall.Name)
	if client == nil {
		// Tool not found anywhere, add error message
		errorMsg := Message{
			Role:      "system",
			Content:   fmt.Sprintf("❌ Tool '%s' not found in native tools or any active MCP server", toolCall.Name),
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, errorMsg)
		m.updateViewport()
		return m, nil
	}
	
	// Add a message showing the tool execution
	executionMsg := Message{
		Role:      "system",
		Content:   fmt.Sprintf("🔧 Executing tool: `%s`...", toolCall.Name),
		Timestamp: time.Now(),
	}
	m.conversation = append(m.conversation, executionMsg)
	m.updateViewport()
	
	// Execute the tool call asynchronously
	return m, func() tea.Msg {
		result, err := client.callTool(toolCall.Name, toolCall.Arguments)
		if err != nil {
			return autoToolCallResult{
				toolName: toolCall.Name,
				error:    err,
			}
		}
		return autoToolCallResult{
			toolName: toolCall.Name,
			result:   result,
		}
	}
}

type ToolCall struct {
	Name      string
	Arguments map[string]interface{}
}

type autoToolCallResult struct {
	toolName string
	result   interface{}
	error    error
}

// Native tool structures
type NativeTool struct {
	Name        string
	Description string
	Handler     func(args map[string]interface{}) (interface{}, error)
}

type nativeToolResult struct {
	toolName string
	result   interface{}
	error    error
}

// Handle result from automatically executed tool call
func (m ChatModel) handleAutoToolCallResult(result autoToolCallResult) (tea.Model, tea.Cmd) {
	if result.error != nil {
		m.debugLog("TOOL_ERROR", "Tool '%s' failed: %v", result.toolName, result.error)
		// Add error message
		errorMsg := Message{
			Role:      "system",
			Content:   fmt.Sprintf("❌ Tool '%s' failed: %v", result.toolName, result.error),
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, errorMsg)
		m.updateViewport()
		return m, nil
	}
	
	// Debug: Print detailed info about the raw tool result
	switch v := result.result.(type) {
	case string:
		m.debugLog("MCP_RESULT", "Tool '%s' returned STRING (length: %d): %s", result.toolName, len(v), v[:min(500, len(v))])
	case map[string]interface{}:
		resultJSON, _ := json.MarshalIndent(v, "", "  ")
		m.debugLog("MCP_RESULT", "Tool '%s' returned MAP (length: %d): %s", result.toolName, len(resultJSON), string(resultJSON)[:min(500, len(resultJSON))])
	case []interface{}:
		resultJSON, _ := json.MarshalIndent(v, "", "  ")
		m.debugLog("MCP_RESULT", "Tool '%s' returned ARRAY (length: %d): %s", result.toolName, len(resultJSON), string(resultJSON)[:min(500, len(resultJSON))])
	default:
		resultJSON, _ := json.MarshalIndent(result.result, "", "  ")
		m.debugLog("MCP_RESULT", "Tool '%s' returned %T (length: %d): %s", result.toolName, result.result, len(resultJSON), string(resultJSON)[:min(500, len(resultJSON))])
	}
	
	// Check if the result is programmatic/non-human friendly
	isProgrammatic := m.isProgrammaticResponse(result.result)
	m.debugLog("TOOL_RESULT", "Tool result programmatic check: %v for tool: %s", isProgrammatic, result.toolName)
	
	if isProgrammatic {
		// Use the same sophisticated formatting as MCP calls
		m.debugLog("TOOL_RESULT", "Using sophisticated formatting for programmatic data")
		return m.formatMCPResponseWithLLM(result.toolName, result.result)
	}
	
	// Format the result using our smart formatter for human-friendly data
	markdownContent := m.formatMCPResponse(result.toolName, result.result)
	renderedContent := m.renderMarkdown(markdownContent)
	
	// Add the formatted result
	resultMsg := Message{
		Role:      "assistant",
		Content:   renderedContent,
		Timestamp: time.Now(),
	}
	m.conversation = append(m.conversation, resultMsg)
	
	m.updateViewport()
	
	// Check if there are pending tool calls to execute
	if len(m.pendingToolCalls) > 0 {
		nextToolCall := m.pendingToolCalls[0]
		m.pendingToolCalls = m.pendingToolCalls[1:]
		
		// Apply intelligent argument substitution for common patterns
		nextToolCall = m.applyToolChainSubstitution(result.toolName, result.result, nextToolCall)
		
		m.debugLog("TOOL_CHAIN", "Executing next tool in chain: %s", nextToolCall.Name)
		return m.executeAutoToolCall(nextToolCall)
	}
	
	return m, nil
}

// Feed tool result back to LLM for natural language response
func (m ChatModel) feedbackToolResultToLLM(toolName string, result interface{}) (tea.Model, tea.Cmd) {
	m.debugLog("LLM_FEEDBACK", "Starting LLM feedback loop for tool: %s", toolName)
	
	// First, extract the actual data from MCP response format
	actualData := m.extractMCPContent(result)
	
	// Convert result to JSON for LLM
	resultJSON, err := json.MarshalIndent(actualData, "", "  ")
	if err != nil {
		// Fallback to simple display
		markdownContent := m.formatMCPResponse(toolName, result)
		renderedContent := m.renderMarkdown(markdownContent)
		
		resultMsg := Message{
			Role:      "assistant",
			Content:   renderedContent,
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, resultMsg)
		m.updateViewport()
		return m, nil
	}
	
	// Add system message with tool result for LLM to process
	systemMsg := Message{
		Role:      "system",
		Content:   fmt.Sprintf("The MCP tool returned the following result. Please format this in a beautiful, human-readable way and explain what it means:\n\n```json\n%s\n```", string(resultJSON)),
		Timestamp: time.Now(),
	}
	m.conversation = append(m.conversation, systemMsg)
	
	// Debug: Show what we're sending to the LLM
	m.debugLog("LLM_FEEDBACK", "Sending system message to LLM (length: %d chars)", len(systemMsg.Content))
	
	// Set flag to prevent tool call loops and trigger LLM to process the tool result
	m.processingToolResult = true
	m.loading = true
	m.updateViewport()
	return m, m.sendMessage()
}

// Check if response data is programmatic/non-human friendly
func (m *ChatModel) isProgrammaticResponse(data interface{}) bool {
	switch v := data.(type) {
	case map[string]interface{}:
		return m.isComplexStructuredData(v)
	case []interface{}:
		return m.isComplexArrayData(v)
	case string:
		return m.isRawDataString(v)
	default:
		return false
	}
}

// Check if map contains complex structured data that needs LLM interpretation
func (m *ChatModel) isComplexStructuredData(data map[string]interface{}) bool {
	// Check for MCP response wrapper format
	if content, exists := data["content"]; exists {
		if contentArray, ok := content.([]interface{}); ok && len(contentArray) > 0 {
			if firstContent, ok := contentArray[0].(map[string]interface{}); ok {
				if text, exists := firstContent["text"]; exists {
					if textStr, ok := text.(string); ok {
						// Check if the text field contains JSON or other structured data
						return m.isRawDataString(textStr)
					}
				}
			}
		}
	}
	
	// Check if it's a complex nested object with technical field names
	complexFieldCount := 0
	totalFields := len(data)
	
	for key, value := range data {
		// Technical/programmatic field indicators
		if m.isTechnicalFieldName(key) {
			complexFieldCount++
		}
		
		// Deep nested objects indicate programmatic data
		if nested, ok := value.(map[string]interface{}); ok {
			if len(nested) > 3 {
				complexFieldCount++
			}
		}
		
		// Large arrays suggest raw data dumps
		if arr, ok := value.([]interface{}); ok {
			if len(arr) > 5 {
				complexFieldCount++
			}
		}
	}
	
	// If more than 50% of fields are technical/complex, it's programmatic
	return totalFields > 2 && float64(complexFieldCount)/float64(totalFields) > 0.5
}

// Check if array contains complex data
func (m *ChatModel) isComplexArrayData(data []interface{}) bool {
	if len(data) == 0 {
		return false
	}
	
	// Check first few items
	checkCount := len(data)
	if checkCount > 3 {
		checkCount = 3
	}
	
	complexItems := 0
	for i := 0; i < checkCount; i++ {
		if obj, ok := data[i].(map[string]interface{}); ok {
			if len(obj) > 5 || m.isComplexStructuredData(obj) {
				complexItems++
			}
		}
	}
	
	// If most items are complex objects, it's programmatic
	return float64(complexItems)/float64(checkCount) > 0.6
}

// Check if string contains raw structured data
func (m *ChatModel) isRawDataString(data string) bool {
	data = strings.TrimSpace(data)
	
	// Check for JSON
	if (strings.HasPrefix(data, "{") && strings.HasSuffix(data, "}")) ||
		(strings.HasPrefix(data, "[") && strings.HasSuffix(data, "]")) {
		var parsed interface{}
		if json.Unmarshal([]byte(data), &parsed) == nil {
			return true
		}
	}
	
	// Check for XML
	if strings.HasPrefix(data, "<") && strings.HasSuffix(data, ">") {
		return true
	}
	
	// Check for other structured formats (YAML, etc.)
	if strings.Contains(data, "---") || strings.Contains(data, "...") {
		return true
	}
	
	// Check for technical patterns (UUIDs, long hashes, etc.)
	uuidPattern := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	if uuidPattern.MatchString(data) {
		return true
	}
	
	// Long base64 or hash-like strings
	if len(data) > 50 && regexp.MustCompile(`^[A-Za-z0-9+/=_-]+$`).MatchString(data) {
		return true
	}
	
	return false
}

// Check if field name is technical/programmatic
func (m *ChatModel) isTechnicalFieldName(fieldName string) bool {
	technicalFields := []string{
		"id", "uuid", "guid", "hash", "token", "key", "secret",
		"timestamp", "created_at", "updated_at", "modified_at",
		"metadata", "config", "settings", "params", "properties",
		"attributes", "headers", "payload", "data", "raw",
		"json", "xml", "yaml", "base64", "encoded",
	}
	
	fieldLower := strings.ToLower(fieldName)
	for _, tech := range technicalFields {
		if strings.Contains(fieldLower, tech) {
			return true
		}
	}
	
	// Check for patterns like "field_name", "fieldName", etc.
	if strings.Contains(fieldName, "_") || 
		(fieldName != strings.ToLower(fieldName) && fieldName != strings.ToUpper(fieldName)) {
		return true
	}
	
	return false
}

func (m *ChatModel) stopMCPServer(serverName string) error {
	m.debugLog("MCP", "Stopping MCP server: %s", serverName)

	client, exists := m.mcpClients[serverName]
	if !exists {
		m.debugLog("ERROR", "MCP server '%s' is not running", serverName)
		return fmt.Errorf("MCP server '%s' is not running", serverName)
	}

	if client.Process != nil && client.Process.Process != nil {
		m.debugLog("MCP", "Killing MCP server process: %s", serverName)
		client.Process.Process.Kill()
		client.Process.Wait()
	}

	delete(m.mcpClients, serverName)
	m.debugLog("SUCCESS", "MCP server stopped: %s", serverName)

	if server, exists := m.config.MCPServers[serverName]; exists {
		server.Active = false
		m.config.MCPServers[serverName] = server
	}

	return nil
}

func (client *MCPClient) sendRequest(method string, params interface{}) (*JSONRPCResponse, error) {
	client.Mutex.Lock()
	defer client.Mutex.Unlock()

	request := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      client.RequestID,
		Method:  method,
		Params:  params,
	}
	client.RequestID++

	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Send request
	if client.DebugLog != nil {
		client.DebugLog("NET", "Sending MCP request: %s", string(data))
	}
	if _, err := client.Stdin.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("failed to write request: %w", err)
	}

	// Read response with very short timeout to prevent hanging
	responseChan := make(chan JSONRPCResponse, 1)
	errorChan := make(chan error, 1)

	go func() {
		if client.Stdout.Scan() {
			responseData := client.Stdout.Bytes()
			// Truncate debug output if response is too long
			debugOutput := string(responseData)
			if len(debugOutput) > 500 {
				debugOutput = debugOutput[:500] + "... [truncated]"
			}
			if client.DebugLog != nil {
				client.DebugLog("NET", "Received MCP response (%d bytes): %s", len(responseData), debugOutput)
			}

			var response JSONRPCResponse
			if err := json.Unmarshal(responseData, &response); err != nil {
				if client.DebugLog != nil {
					client.DebugLog("ERROR", "Failed to unmarshal response: %v", err)
					client.DebugLog("ERROR", "Response data length: %d bytes", len(responseData))
				}
				errorChan <- fmt.Errorf("failed to unmarshal response: %w", err)
			} else {
				responseChan <- response
			}
		} else {
			if client.DebugLog != nil {
				client.DebugLog("ERROR", "Failed to scan stdout from MCP server")
				if err := client.Stdout.Err(); err != nil {
					client.DebugLog("ERROR", "Scanner error: %v", err)
				}
			}
			errorChan <- fmt.Errorf("failed to read response")
		}
	}()

	select {
	case response := <-responseChan:
		if response.Error != nil {
			return nil, fmt.Errorf("MCP error: %s", response.Error.Message)
		}
		return &response, nil
	case err := <-errorChan:
		return nil, err
	case <-time.After(2 * time.Second):
		return nil, fmt.Errorf("timeout waiting for response")
	}
}

func (client *MCPClient) initialize() error {
	params := map[string]interface{}{
		"protocolVersion": "2025-06-18",
		"capabilities": map[string]interface{}{
			"sampling": map[string]interface{}{},
		},
		"clientInfo": map[string]interface{}{
			"name":    "voyager",
			"version": "1.0.0",
		},
	}

	_, err := client.sendRequest("initialize", params)
	if err != nil {
		return err
	}

	// Send initialized notification (no ID for notifications)
	notification := JSONRPCNotification{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}

	data, _ := json.Marshal(notification)
	client.Stdin.Write(append(data, '\n'))

	return nil
}

func (client *MCPClient) discoverCapabilities() error {
	// Discover tools
	if response, err := client.sendRequest("tools/list", nil); err == nil {
		if result, ok := response.Result.(map[string]interface{}); ok {
			if tools, ok := result["tools"].([]interface{}); ok {
				for _, toolData := range tools {
					if toolMap, ok := toolData.(map[string]interface{}); ok {
						// Safely get the tool name
						if nameVal, ok := toolMap["name"].(string); ok {
							tool := MCPTool{
								Name: nameVal,
							}
							if desc, ok := toolMap["description"].(string); ok {
								tool.Description = desc
							}
							if schema, ok := toolMap["inputSchema"].(map[string]interface{}); ok {
								tool.Schema = schema
							}
							client.Tools = append(client.Tools, tool)
						}
					}
				}
			}
		}
	} else {
		// Log the error for debugging
		if client.DebugLog != nil {
			client.DebugLog("ERROR", "Failed to list tools: %v", err)
		}
	}

	// Discover resources
	if response, err := client.sendRequest("resources/list", nil); err == nil {
		if result, ok := response.Result.(map[string]interface{}); ok {
			if resources, ok := result["resources"].([]interface{}); ok {
				for _, resourceData := range resources {
					if resourceMap, ok := resourceData.(map[string]interface{}); ok {
						// Safely get the resource URI
						if uriVal, ok := resourceMap["uri"].(string); ok {
							resource := MCPResource{
								URI: uriVal,
							}
							if name, ok := resourceMap["name"].(string); ok {
								resource.Name = name
							}
							if desc, ok := resourceMap["description"].(string); ok {
								resource.Description = desc
							}
							client.Resources = append(client.Resources, resource)
						}
					}
				}
			}
		}
	} else {
		// Log the error for debugging
		if client.DebugLog != nil {
			client.DebugLog("WARN", "Failed to list resources: %v", err)
		}
	}

	return nil
}

func (client *MCPClient) callTool(name string, arguments map[string]interface{}) (interface{}, error) {
	params := map[string]interface{}{
		"name":      name,
		"arguments": arguments,
	}

	response, err := client.sendRequest("tools/call", params)
	if err != nil {
		return nil, err
	}

	return response.Result, nil
}

func (m *ChatModel) listMCPTools() []MCPTool {
	var allTools []MCPTool
	for _, client := range m.mcpClients {
		allTools = append(allTools, client.Tools...)
	}
	return allTools
}

func (m *ChatModel) getMCPClientForTool(toolName string) *MCPClient {
	for _, client := range m.mcpClients {
		for _, tool := range client.Tools {
			if tool.Name == toolName {
				return client
			}
		}
	}
	return nil
}

// expandMCPEnvVar expands environment variables in MCP server env values
// If the value starts with ${ and ends with }, it looks up the environment variable
// Otherwise, it returns the value as-is
func expandMCPEnvVar(envVar string) string {
	// Split on = to separate key from value
	parts := strings.SplitN(envVar, "=", 2)
	if len(parts) != 2 {
		// Note: No debug logging here as this function doesn't have access to debug context
		return envVar // Return as-is if not in KEY=VALUE format
	}

	key := parts[0]
	value := parts[1]

	// Check if value starts with ${ and ends with }
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		// Extract the environment variable name
		envName := strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
		// Look up the environment variable
		envValue := os.Getenv(envName)
		return key + "=" + envValue
	}

	// Return as-is if not in ${...} format
	return envVar
}

// Debug logging system with colored text labels
func (m *ChatModel) debugLog(level string, message string, args ...interface{}) {
	if !m.debugEnabled {
		return
	}

	// Color codes for labels only
	colors := map[string]string{
		"INFO":    "\033[36m", // Cyan
		"ERROR":   "\033[31m", // Red
		"SUCCESS": "\033[32m", // Green
		"WARN":    "\033[33m", // Yellow
		"MCP":     "\033[35m", // Magenta
		"NET":     "\033[34m", // Blue
		"API":     "\033[93m", // Bright Yellow
		"TUI":     "\033[90m", // Dim Gray (matching placeholder opacity)
		"CONFIG":  "\033[96m", // Bright Cyan
		"AUTH":    "\033[95m", // Bright Magenta
		"LOAD":    "\033[92m", // Bright Green
		"SAVE":    "\033[94m", // Bright Blue
		"ORCHESTRATOR": "\033[38;5;208m", // Orange
	}
	reset := "\033[0m"

	// Format timestamp
	timestamp := time.Now().Format("15:04:05")

	// Get color for level
	color, exists := colors[level]
	if !exists {
		color = colors["INFO"]
	}

	// Format the message with only the label colored
	formattedMsg := fmt.Sprintf(message, args...)
	dimGray := "\033[90m" // Dim gray for timestamps and TUI messages

	var logLine string
	if level == "TUI" {
		// For TUI messages, make everything dimmed (label, timestamp, content)
		logLine = fmt.Sprintf("%s[%s] %s %s%s",
			dimGray, level, timestamp, formattedMsg, reset)
	} else {
		// For other messages, colored label with dimmed timestamp
		logLine = fmt.Sprintf("[%s%s%s] %s%s%s %s",
			color, level, reset,
			dimGray, timestamp, reset,
			formattedMsg)
	}

	// Add to debug logs (keep only last 1000 lines for scrollback history)
	m.debugLogs = append(m.debugLogs, logLine)
	if len(m.debugLogs) > 1000 {
		m.debugLogs = m.debugLogs[len(m.debugLogs)-1000:]
	}

	// Update debug window content
	m.debugWindow.SetContent(strings.Join(m.debugLogs, "\n"))
	m.debugWindow.GotoBottom()
}

func (m *ChatModel) updateTextareaHeight() {
	lines := strings.Count(m.textarea.Value(), "\n") + 1
	if lines < 1 {
		lines = 1
	}
	if lines > 10 {
		lines = 10
	}
	m.textarea.SetHeight(lines)
}

func (m *ChatModel) updateViewport() {
	var content strings.Builder

	// Always add ASCII art at the beginning of content
	asciiArt := `
    ██╗   ██╗ ██████╗ ██╗   ██╗ █████╗  ██████╗ ███████╗██████╗
    ██║   ██║██╔═══██╗╚██╗ ██╔╝██╔══██╗██╔════╝ ██╔════╝██╔══██╗
    ██║   ██║██║   ██║ ╚████╔╝ ███████║██║  ███╗█████╗  ██████╔╝
    ╚██╗ ██╔╝██║   ██║  ╚██╔╝  ██╔══██║██║   ██║██╔══╝  ██╔══██╗
     ╚████╔╝ ╚██████╔╝   ██║   ██║  ██║╚██████╔╝███████╗██║  ██║
      ╚═══╝   ╚═════╝    ╚═╝   ╚═╝  ╚═╝ ╚═════╝ ╚══════╝╚═╝  ╚═╝

                       Multi-Provider LLM CLI
`
	content.WriteString(lipgloss.NewStyle().
		Foreground(lipgloss.Color("#8B4513")).
		Bold(true).
		Render(asciiArt))
	content.WriteString("\n\n")

	// Add conversation messages
	for _, msg := range m.conversation {
		switch msg.Role {
		case "user":
			// Don't show user messages

		case "assistant":
			// Process content through markdown renderer for proper wrapping
			processedContent := m.renderMarkdown(strings.TrimLeft(msg.Content, "\n"))
			content.WriteString(msgContentStyle.Render(processedContent))
			content.WriteString("\n\n")

		case "system":
			// Skip internal TOOL_RESULT messages - they're only for LLM feedback loops
			if !strings.Contains(msg.Content, "TOOL_RESULT") {
				content.WriteString(systemMsgStyle.Render(msg.Content))
				content.WriteString("\n\n")
			}
		}
	}

	if m.loading {
		spinner := spinnerFrames[m.spinnerIndex]
		content.WriteString(msgContentStyle.Render(fmt.Sprintf("%s thinking...", spinner)))
		content.WriteString("\n")
	}
	
	if m.toolCallActive {
		spinner := spinnerFrames[m.spinnerIndex]
		content.WriteString(msgContentStyle.Render(fmt.Sprintf("%s %s", spinner, m.toolCallStep)))
		content.WriteString("\n")
	}

	if m.err != nil {
		content.WriteString(errorStyle.Render(m.err.Error()))
		content.WriteString("\n\n")
	}

	m.viewport.SetContent(content.String())

	// If there are no conversation messages, show the ASCII art centered
	// Otherwise, scroll to bottom to show latest messages
	if len(m.conversation) == 0 && !m.loading && m.err == nil {
		// Center the ASCII art vertically in the viewport
		lines := strings.Count(asciiArt, "\n")
		viewportLines := m.viewport.Height
		if lines < viewportLines {
			offset := (viewportLines - lines) / 2
			if offset > 0 {
				m.viewport.LineDown(offset)
			}
		}
	} else {
		// Scroll to bottom to show latest content
		m.viewport.GotoBottom()
	}
}

func (m ChatModel) View() string {
	if !m.ready {
		return "\n  Initializing Voyager..."
	}

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "unknown"
	} else {
		cwd = filepath.Base(cwd)
	}

	// Small info line with provider, model, and working directory
	info := smallInfoStyle.Render(fmt.Sprintf("%s/%s • %s", m.currentProvider, m.currentModel, cwd))

	// Help
	help := helpStyle.Render("Controls: Enter=Send • Ctrl+C=Quit • /help=Commands")

	// Chat area without border - this now contains the ASCII art when appropriate
	chatArea := lipgloss.NewStyle().
		Padding(0, 1). // Reduced padding to allow more content width
		Height(m.viewport.Height).
		Render(m.viewport.View())

	// Debug area (if enabled)
	var debugArea string
	if m.debugEnabled {
		debugStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Foreground(lipgloss.Color("#666666")). // 40% opacity of normal text
			Padding(0, 1).
			Height(6) // Increased height for better scrolling
		debugArea = debugStyle.Render(m.debugWindow.View())
	}

	// Input area
	inputArea := inputStyle.Render(m.textarea.View())

	// Build layout with or without debug window
	if m.debugEnabled {
		return lipgloss.JoinVertical(
			lipgloss.Left,
			info,
			chatArea,
			debugArea,
			inputArea,
			help,
		)
	} else {
		return lipgloss.JoinVertical(
			lipgloss.Left,
			info,
			chatArea,
			inputArea,
			help,
		)
	}
}

// Configuration functions
func autoDetectProviders() map[string]Provider {
	providers := make(map[string]Provider)

	// Auto-detect DigitalOcean Serverless Inference
	if apiKey := os.Getenv("DO_SERVERLESS_INFERENCE"); apiKey != "" {
		providers["digitalocean"] = Provider{
			Type:    ProviderDigitalOcean,
			BaseURL: "https://inference.do-ai.run/v1/chat/completions",
			APIKey:  apiKey,
			Models:  []string{"llama3.3-70b-instruct", "llama3.1-8b-instruct", "meta-llama/Llama-3.2-3B-Instruct"},
		}
	}

	return providers
}

// loadEnvFile loads environment variables from a .env file
func loadEnvFile(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		// .env file is optional, so don't error if it doesn't exist
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split on first = to separate key from value
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Remove quotes if present
		if len(value) >= 2 {
			if (strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"")) ||
				(strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
				value = value[1 : len(value)-1]
			}
		}

		// Set the environment variable if it's not already set
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}

	return scanner.Err()
}

func loadConfig() (*Config, error) {
	// Load .env file first
	if err := loadEnvFile(".env"); err != nil {
		fmt.Printf("Warning: Failed to load .env file: %v\n", err)
	}
	// Try to load existing config
	configFile := "voyager-config.json"
	config := &Config{
		Providers:  make(map[string]Provider),
		MCPServers: make(map[string]MCPServer),
	}

	if data, err := os.ReadFile(configFile); err == nil {
		json.Unmarshal(data, config)

		// Expand environment variables in API keys
		for name, provider := range config.Providers {
			if strings.HasPrefix(provider.APIKey, "${") && strings.HasSuffix(provider.APIKey, "}") {
				envVar := provider.APIKey[2 : len(provider.APIKey)-1] // Remove ${ and }
				if envValue := os.Getenv(envVar); envValue != "" {
					provider.APIKey = envValue
					config.Providers[name] = provider
				}
			}
		}
	}

	// Auto-detect providers from environment variables
	autoProviders := autoDetectProviders()

	// Merge auto-detected providers with existing config
	for name, provider := range autoProviders {
		config.Providers[name] = provider
	}

	// Set defaults if not configured
	if config.DefaultProvider == "" && len(config.Providers) > 0 {
		// Use digitalocean as default
		if _, exists := config.Providers["digitalocean"]; exists {
			config.DefaultProvider = "digitalocean"
		} else {
			for name := range config.Providers {
				config.DefaultProvider = name
				break
			}
		}
	}

	if config.DefaultModel == "" && config.DefaultProvider != "" {
		if provider, exists := config.Providers[config.DefaultProvider]; exists && len(provider.Models) > 0 {
			config.DefaultModel = provider.Models[0]
		}
	}

	// Only require providers for chat functionality, not for MCP management
	if len(config.Providers) == 0 {
		// Allow empty providers for MCP-only operations
		return config, nil
	}

	return config, nil
}

func saveConfig(config *Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile("voyager-config.json", data, 0644)
}

// Deploy command handlers
func (m ChatModel) handleDeployCommand() (tea.Model, tea.Cmd) {
	m.debugLog("DEPLOY", "Starting deployment process")
	
	// Load embedded deployment contexts
	deployContext, err := m.loadDeployContext()
	if err != nil {
		systemMsg := Message{
			Role:      "system",
			Content:   fmt.Sprintf("Failed to load deployment context: %v", err),
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)
		m.updateViewport()
		return m, nil
	}
	
	// Analyze current directory
	projectAnalysis, err := m.analyzeCurrentProject()
	if err != nil {
		systemMsg := Message{
			Role:      "system",
			Content:   fmt.Sprintf("Failed to analyze project: %v", err),
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, systemMsg)
		m.updateViewport()
		return m, nil
	}
	
	// Create system message with deployment context and project analysis
	systemMsg := Message{
		Role: "system",
		Content: fmt.Sprintf(`%s

## Current Project Analysis
%s

The user has activated deployment mode. You now have deployment knowledge loaded and can help with DigitalOcean App Platform questions and tasks.`, deployContext, projectAnalysis),
		Timestamp: time.Now(),
	}
	m.conversation = append(m.conversation, systemMsg)
	
	// Add a friendly assistant message
	assistantMsg := Message{
		Role:      "assistant", 
		Content:   "✅ Deployment mode activated! I've analyzed your project and loaded DigitalOcean App Platform knowledge. I can now help you create app specs, answer deployment questions, or deploy your application. What would you like to do?",
		Timestamp: time.Now(),
	}
	m.conversation = append(m.conversation, assistantMsg)
	
	m.updateViewport()
	return m, nil
}

func (m *ChatModel) loadDeployContext() (string, error) {
	var contextBuilder strings.Builder
	
	// Load tools.md (general tool usage)
	toolsContent, err := embeddedContexts.ReadFile("contexts/tools.md")
	if err != nil {
		return "", fmt.Errorf("failed to load tools.md: %v", err)
	}
	contextBuilder.WriteString(string(toolsContent))
	contextBuilder.WriteString("\n\n")
	
	// Load deploy.md
	deployContent, err := embeddedContexts.ReadFile("contexts/deploy.md")
	if err != nil {
		return "", fmt.Errorf("failed to load deploy.md: %v", err)
	}
	contextBuilder.WriteString(string(deployContent))
	contextBuilder.WriteString("\n\n")
	
	// Load project-types.md
	projectTypesContent, err := embeddedContexts.ReadFile("contexts/project-types.md")
	if err != nil {
		return "", fmt.Errorf("failed to load project-types.md: %v", err)
	}
	contextBuilder.WriteString(string(projectTypesContent))
	contextBuilder.WriteString("\n\n")
	
	// Load app-specs.md
	appSpecsContent, err := embeddedContexts.ReadFile("contexts/app-specs.md")
	if err != nil {
		return "", fmt.Errorf("failed to load app-specs.md: %v", err)
	}
	contextBuilder.WriteString(string(appSpecsContent))
	
	return contextBuilder.String(), nil
}

func (m *ChatModel) analyzeCurrentProject() (string, error) {
	var analysis strings.Builder
	
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %v", err)
	}
	
	analysis.WriteString(fmt.Sprintf("**Directory**: %s\n", filepath.Base(cwd)))
	analysis.WriteString(fmt.Sprintf("**Full Path**: %s\n\n", cwd))
	
	// Analyze project files
	files, err := os.ReadDir(cwd)
	if err != nil {
		return "", fmt.Errorf("failed to read directory: %v", err)
	}
	
	var projectFiles []string
	var configFiles []string
	var hasDockerfile bool
	var detectedType string
	
	for _, file := range files {
		name := file.Name()
		projectFiles = append(projectFiles, name)
		
		// Detect project type
		switch name {
		case "go.mod":
			detectedType = "Go"
			configFiles = append(configFiles, name)
		case "package.json":
			if detectedType == "" {
				detectedType = "Node.js"
			}
			configFiles = append(configFiles, name)
		case "requirements.txt", "Pipfile", "pyproject.toml":
			if detectedType == "" {
				detectedType = "Python"
			}
			configFiles = append(configFiles, name)
		case "Dockerfile":
			hasDockerfile = true
			configFiles = append(configFiles, name)
		case ".env", ".env.example":
			configFiles = append(configFiles, name)
		}
	}
	
	// Determine final project type
	if hasDockerfile {
		detectedType = "Docker"
	}
	if detectedType == "" {
		detectedType = "Unknown"
	}
	
	analysis.WriteString(fmt.Sprintf("**Detected Type**: %s\n", detectedType))
	analysis.WriteString(fmt.Sprintf("**Config Files**: %s\n", strings.Join(configFiles, ", ")))
	
	// Read key config files for more details
	if detectedType == "Go" {
		if goModContent, err := os.ReadFile("go.mod"); err == nil {
			lines := strings.Split(string(goModContent), "\n")
			if len(lines) > 0 && strings.HasPrefix(lines[0], "module ") {
				moduleName := strings.TrimSpace(strings.TrimPrefix(lines[0], "module "))
				analysis.WriteString(fmt.Sprintf("**Go Module**: %s\n", moduleName))
			}
		}
	}
	
	if detectedType == "Node.js" {
		if pkgContent, err := os.ReadFile("package.json"); err == nil {
			var pkg map[string]interface{}
			if json.Unmarshal(pkgContent, &pkg) == nil {
				if name, ok := pkg["name"].(string); ok {
					analysis.WriteString(fmt.Sprintf("**Package Name**: %s\n", name))
				}
				if scripts, ok := pkg["scripts"].(map[string]interface{}); ok {
					var scriptNames []string
					for script := range scripts {
						scriptNames = append(scriptNames, script)
					}
					analysis.WriteString(fmt.Sprintf("**NPM Scripts**: %s\n", strings.Join(scriptNames, ", ")))
				}
			}
		}
	}
	
	// Check for environment variables
	if envContent, err := os.ReadFile(".env.example"); err == nil {
		envVars := m.extractEnvVars(string(envContent))
		if len(envVars) > 0 {
			analysis.WriteString(fmt.Sprintf("**Environment Variables**: %s\n", strings.Join(envVars, ", ")))
		}
	} else if envContent, err := os.ReadFile(".env"); err == nil {
		envVars := m.extractEnvVars(string(envContent))
		if len(envVars) > 0 {
			analysis.WriteString(fmt.Sprintf("**Environment Variables**: %s\n", strings.Join(envVars, ", ")))
		}
	}
	
	return analysis.String(), nil
}

func (m *ChatModel) extractEnvVars(content string) []string {
	var vars []string
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "="); idx > 0 {
			varName := strings.TrimSpace(line[:idx])
			vars = append(vars, varName)
		}
	}
	return vars
}

// Native tool implementations
func (m *ChatModel) initializeNativeTools() {
	// Bash tool
	m.nativeTools["bash"] = NativeTool{
		Name:        "bash",
		Description: "Execute shell commands",
		Handler:     m.handleBashTool,
	}
	
	// Read tool
	m.nativeTools["read"] = NativeTool{
		Name:        "read",
		Description: "Read file contents",
		Handler:     m.handleReadTool,
	}
	
	// Write tool
	m.nativeTools["write"] = NativeTool{
		Name:        "write",
		Description: "Write file contents",
		Handler:     m.handleWriteTool,
	}
	
	// Glob tool
	m.nativeTools["glob"] = NativeTool{
		Name:        "glob",
		Description: "Find files matching patterns",
		Handler:     m.handleGlobTool,
	}
	
	// Grep tool
	m.nativeTools["grep"] = NativeTool{
		Name:        "grep",
		Description: "Search file contents",
		Handler:     m.handleGrepTool,
	}
}

func (m ChatModel) handleNativeToolResult(result nativeToolResult) (tea.Model, tea.Cmd) {
	if result.error != nil {
		m.debugLog("NATIVE_TOOL_ERROR", "Tool '%s' failed: %v", result.toolName, result.error)
		errorMsg := Message{
			Role:      "system",
			Content:   fmt.Sprintf("❌ Tool '%s' failed: %v", result.toolName, result.error),
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, errorMsg)
		m.updateViewport()
		return m, nil
	}
	
	// Format and display the result
	resultStr := fmt.Sprintf("%v", result.result)
	resultMsg := Message{
		Role:      "assistant",
		Content:   resultStr,
		Timestamp: time.Now(),
	}
	m.conversation = append(m.conversation, resultMsg)
	m.updateViewport()
	
	// Check if there are pending tool calls to execute
	if len(m.pendingToolCalls) > 0 {
		nextToolCall := m.pendingToolCalls[0]
		m.pendingToolCalls = m.pendingToolCalls[1:]
		
		// Apply intelligent argument substitution for common patterns
		nextToolCall = m.applyToolChainSubstitution(result.toolName, result.result, nextToolCall)
		
		m.debugLog("TOOL_CHAIN", "Executing next tool in chain: %s", nextToolCall.Name)
		return m.executeAutoToolCall(nextToolCall)
	}
	
	return m, nil
}

func (m ChatModel) executeNativeTool(toolName string, args map[string]interface{}) (tea.Model, tea.Cmd) {
	if tool, exists := m.nativeTools[toolName]; exists {
		// Execute the tool asynchronously
		return m, func() tea.Msg {
			result, err := tool.Handler(args)
			return nativeToolResult{
				toolName: toolName,
				result:   result,
				error:    err,
			}
		}
	}
	
	return m, nil
}

func (m *ChatModel) handleBashTool(args map[string]interface{}) (interface{}, error) {
	command, ok := args["command"].(string)
	if !ok {
		return nil, fmt.Errorf("bash tool requires 'command' argument")
	}
	
	cmd := exec.Command("bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("command failed: %v\nOutput: %s", err, string(output))
	}
	
	return string(output), nil
}

func (m *ChatModel) handleReadTool(args map[string]interface{}) (interface{}, error) {
	filePath, ok := args["file_path"].(string)
	if !ok {
		return nil, fmt.Errorf("read tool requires 'file_path' argument")
	}
	
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %v", err)
	}
	
	return string(content), nil
}

func (m *ChatModel) handleWriteTool(args map[string]interface{}) (interface{}, error) {
	filePath, ok := args["file_path"].(string)
	if !ok {
		return nil, fmt.Errorf("write tool requires 'file_path' argument")
	}
	
	content, ok := args["content"].(string)
	if !ok {
		return nil, fmt.Errorf("write tool requires 'content' argument")
	}
	
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to write file: %v", err)
	}
	
	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), filePath), nil
}

func (m *ChatModel) handleGlobTool(args map[string]interface{}) (interface{}, error) {
	pattern, ok := args["pattern"].(string)
	if !ok {
		return nil, fmt.Errorf("glob tool requires 'pattern' argument")
	}
	
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob pattern failed: %v", err)
	}
	
	return matches, nil
}

func (m *ChatModel) handleGrepTool(args map[string]interface{}) (interface{}, error) {
	pattern, ok := args["pattern"].(string)
	if !ok {
		return nil, fmt.Errorf("grep tool requires 'pattern' argument")
	}
	
	path, ok := args["path"].(string)
	if !ok {
		path = "." // Default to current directory
	}
	
	// Use ripgrep-like functionality
	cmd := exec.Command("grep", "-r", "-n", pattern, path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// grep returns non-zero when no matches found, which is normal
		if len(output) == 0 {
			return "No matches found", nil
		}
	}
	
	return string(output), nil
}

// Create dynamic pipeline from multiple tool calls with automatic context passing
func (m *ChatModel) createDynamicPipelineFromToolCalls(toolCalls []ToolCall) (tea.Model, tea.Cmd) {
	// Create pipeline structure
	pipeline := OrchestratorPipeline{
		Name:          fmt.Sprintf("auto-pipeline-%d-tools", len(toolCalls)),
		StartStep:     "step1",
		Steps:         make(map[string]OrchestratorStep),
		Context:       make(map[string]interface{}),
		MaxSteps:      len(toolCalls),
		GlobalTimeout: 300 * time.Second, // 5 minutes
	}
	
	// Convert each tool call to a pipeline step
	for i, toolCall := range toolCalls {
		stepID := fmt.Sprintf("step%d", i+1)
		nextStepID := ""
		if i < len(toolCalls)-1 {
			nextStepID = fmt.Sprintf("step%d", i+2)
		}
		
		step := OrchestratorStep{
			ID:     stepID,
			Type:   "tool",
			Action: toolCall.Name,
			Input:  toolCall.Arguments,
		}
		
		// Set next step
		if nextStepID != "" {
			step.NextStep = map[string]string{"success": nextStepID}
		} else {
			step.NextStep = map[string]string{} // Final step
		}
		
		pipeline.Steps[stepID] = step
	}
	
	m.debugLog("PIPELINE_AUTO", "Created pipeline with %d steps: %v", len(pipeline.Steps), pipeline.Name)
	
	// Execute the pipeline
	return m.startOrchestrator(&pipeline)
}

// Apply context substitution for tool chaining - used by pipeline orchestrator
func (m *ChatModel) applyToolChainSubstitution(prevToolName string, prevResult interface{}, nextToolCall ToolCall) ToolCall {
	// For simple tool chaining (non-pipeline), just store result for potential LLM-generated pipeline
	if nextToolCall.Arguments == nil {
		nextToolCall.Arguments = make(map[string]interface{})
	}
	
	// Store previous result in context for potential pipeline creation
	if m.orchestratorState == nil {
		// Not in pipeline mode - trigger pipeline planning with context
		m.debugLog("TOOL_CHAIN", "Tool chain detected - initiating pipeline planning")
		return nextToolCall
	}
	
	// Apply pipeline context substitution using orchestrator
	return ToolCall{
		Name: nextToolCall.Name,
		Arguments: m.applyContextSubstitution(nextToolCall.Arguments),
	}
}

// Dynamic pipeline planning
func (m ChatModel) handlePlanCommand(taskDescription string) (tea.Model, tea.Cmd) {
	m.debugLog("PLANNER", "Creating dynamic pipeline for task: %s", taskDescription)
	
	// Create a system message with available tools context for planning
	availableTools := m.buildAvailableToolsContext()
	
	planningPrompt := fmt.Sprintf(`You are a pipeline planner. Create a JSON pipeline to accomplish this task: "%s"

AVAILABLE TOOLS:
%s

Create a pipeline with these step types:
- "llm": For generating content, explanations, or analysis
- "tool": For executing native tools (bash, read, write, glob, grep) or MCP tools
- "condition": For conditional logic based on previous results

Pipeline JSON format:
{
  "name": "descriptive-name",
  "start_step": "step1",
  "steps": {
    "step1": {
      "id": "step1", 
      "type": "tool",
      "action": "bash",
      "input": {"command": "pwd"},
      "next_step": {"success": "step2"}
    },
    "step2": {
      "id": "step2",
      "type": "tool", 
      "action": "list_directory",
      "input": {"path": "{{.prev_result}}"},
      "next_step": {}
    }
  }
}

IMPORTANT:
- For "tool" type: "action" = tool name, "input" = JSON object with tool arguments
- For "llm" type: "action" = prompt text, "input" = context variables
- For bash tool: use {"command": "shell_command_here"}
- For file tools: use {"path": "/file/path"} or {"path": "{{.prev_result}}"}
- Use {{.prev_result}} to pass output from previous step
- Use {{.step_name}} to reference output from specific named steps
- For "next_step": use {"success": "next_step_id"} or {} for final step
- NEVER use arrays [] for next_step, always use objects {}

Respond with ONLY the JSON pipeline, no explanation.`, taskDescription, availableTools)
	
	// Create planning request
	planningMsg := Message{
		Role:    "user",
		Content: planningPrompt,
	}
	
	// Make API request for planning
	m.loading = true
	m.updateViewport()
	
	return m, func() tea.Msg {
		response, err := m.makeAPIRequestWithCustomMessages([]Message{planningMsg})
		if err != nil {
			return responseMsg{content: "", err: fmt.Errorf("planning failed: %v", err)}
		}
		return responseMsg{content: response, err: nil}
	}
}

func (m ChatModel) makeAPIRequestWithCustomMessages(messages []Message) (string, error) {
	provider := m.config.Providers[m.currentProvider]
	
	// Convert messages to API format
	var apiMessages []Message
	for _, msg := range messages {
		apiMessages = append(apiMessages, Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	
	// Use DigitalOcean provider (extend for other providers as needed)
	type DigitalOceanRequest struct {
		Model       string    `json:"model"`
		Messages    []Message `json:"messages"`
		Temperature float64   `json:"temperature,omitempty"`
		MaxTokens   int       `json:"max_tokens,omitempty"`
	}
	
	request := DigitalOceanRequest{
		Model:       m.currentModel,
		Messages:    apiMessages,
		Temperature: 0.7,
		MaxTokens:   4000,
	}
	
	jsonData, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %v", err)
	}
	
	req, err := http.NewRequest("POST", provider.BaseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	
	// Set headers based on provider type  
	req.Header.Set("Content-Type", "application/json")
	if provider.Type == "digitalocean" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", provider.APIKey))
	}
	
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %v", err)
	}
	
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error: %s", string(body))
	}
	
	type DigitalOceanResponse struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	
	var response DigitalOceanResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %v", err)
	}
	
	if response.Error != nil {
		return "", fmt.Errorf("API error: %s", response.Error.Message)
	}
	
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("no response received")
	}
	
	return response.Choices[0].Message.Content, nil
}

func (m *ChatModel) buildAvailableToolsContext() string {
	var tools []string
	
	// Add native tools
	for name, tool := range m.nativeTools {
		tools = append(tools, fmt.Sprintf("- %s (native): %s", name, tool.Description))
	}
	
	// Add MCP tools
	for _, client := range m.mcpClients {
		for _, tool := range client.Tools {
			desc := tool.Description
			if desc == "" {
				desc = "No description available"
			}
			tools = append(tools, fmt.Sprintf("- %s (mcp): %s", tool.Name, desc))
		}
	}
	
	return strings.Join(tools, "\n")
}

// Pipeline response processing
func (m *ChatModel) isPipelineResponse(content string) bool {
	// Check if the response contains a JSON pipeline structure
	content = strings.TrimSpace(content)
	
	m.debugLog("PIPELINE_DETECT", "Checking if response is pipeline - length: %d, starts with {: %v", len(content), strings.HasPrefix(content, "{"))
	
	// Handle markdown code blocks
	if strings.Contains(content, "```json") {
		startIdx := strings.Index(content, "```json") + 7
		endIdx := strings.LastIndex(content, "```")
		if endIdx > startIdx {
			content = content[startIdx:endIdx]
			content = strings.TrimSpace(content)
			m.debugLog("PIPELINE_DETECT", "Extracted JSON from markdown: %s", content[:min(100, len(content))])
		}
	} else if strings.Contains(content, "```") {
		// Handle plain code blocks
		parts := strings.Split(content, "```")
		if len(parts) >= 3 {
			content = strings.TrimSpace(parts[1])
			m.debugLog("PIPELINE_DETECT", "Extracted from code block: %s", content[:min(100, len(content))])
		}
	}
	
	m.debugLog("PIPELINE_DETECT", "After extraction - starts with {: %v, has steps: %v, has start_step: %v", 
		strings.HasPrefix(content, "{"), 
		strings.Contains(content, "\"steps\""), 
		strings.Contains(content, "\"start_step\""))
	
	if strings.HasPrefix(content, "{") && strings.Contains(content, "\"steps\"") && strings.Contains(content, "\"start_step\"") {
		// Try to parse as pipeline JSON
		var pipeline OrchestratorPipeline
		err := json.Unmarshal([]byte(content), &pipeline)
		parseSuccess := err == nil
		if !parseSuccess {
			m.debugLog("PIPELINE_DETECT", "JSON parse error: %v", err)
		}
		m.debugLog("PIPELINE_DETECT", "Pipeline JSON parse success: %v", parseSuccess)
		return parseSuccess
	}
	m.debugLog("PIPELINE_DETECT", "Pipeline detection failed - not a valid pipeline format")
	return false
}

func (m ChatModel) processPipelineResponse(content string) (tea.Model, tea.Cmd) {
	// Extract JSON from the response (handle cases where LLM adds extra text)
	content = strings.TrimSpace(content)
	
	// Handle markdown code blocks first
	if strings.Contains(content, "```json") {
		startIdx := strings.Index(content, "```json") + 7
		endIdx := strings.LastIndex(content, "```")
		if endIdx > startIdx {
			content = content[startIdx:endIdx]
			content = strings.TrimSpace(content)
		}
	} else if strings.Contains(content, "```") {
		// Handle plain code blocks
		parts := strings.Split(content, "```")
		if len(parts) >= 3 {
			content = strings.TrimSpace(parts[1])
		}
	}
	
	// Find JSON boundaries
	startIdx := strings.Index(content, "{")
	if startIdx == -1 {
		return m, nil
	}
	
	// Find the matching closing brace
	braceCount := 0
	endIdx := -1
	for i := startIdx; i < len(content); i++ {
		if content[i] == '{' {
			braceCount++
		} else if content[i] == '}' {
			braceCount--
			if braceCount == 0 {
				endIdx = i
				break
			}
		}
	}
	
	if endIdx == -1 {
		return m, nil
	}
	
	pipelineJSON := content[startIdx : endIdx+1]
	
	// Parse the pipeline
	var pipeline OrchestratorPipeline
	if err := json.Unmarshal([]byte(pipelineJSON), &pipeline); err != nil {
		m.debugLog("PIPELINE_ERROR", "Failed to parse pipeline JSON: %v", err)
		errorMsg := Message{
			Role:      "system",
			Content:   fmt.Sprintf("❌ Failed to parse pipeline: %v", err),
			Timestamp: time.Now(),
		}
		m.conversation = append(m.conversation, errorMsg)
		m.updateViewport()
		return m, nil
	}
	
	// Log pipeline creation to debug instead of showing to user
	m.debugLog("PIPELINE_CREATE", "Created dynamic pipeline '%s' with %d steps", pipeline.Name, len(pipeline.Steps))
	
	// Execute the pipeline
	m.debugLog("PIPELINE", "Executing dynamic pipeline: %s with %d steps", pipeline.Name, len(pipeline.Steps))
	return m.startOrchestrator(&pipeline)
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "voyager",
		Short: "Voyager - Multi-provider AI CLI with beautiful TUI",
		Long:  "Voyager is an interactive TUI for chatting with multiple AI providers: OpenAI, Anthropic, Ollama, and local models.",
		Run: func(cmd *cobra.Command, args []string) {
			// Default to chat command when no subcommand provided
			config, err := loadConfig()
			if err != nil {
				fmt.Printf("Error loading config: %v\n", err)
				fmt.Println("Run 'voyager init' to set up configuration")
				return
			}

			debugEnabled, _ := cmd.Flags().GetBool("debug")
			model := initialModel(config, debugEnabled)
			p := tea.NewProgram(model, tea.WithAltScreen())

			if _, err := p.Run(); err != nil {
				fmt.Printf("Error running program: %v\n", err)
				os.Exit(1)
			}
		},
	}

	// Chat command with TUI
	chatCmd := &cobra.Command{
		Use:   "chat",
		Short: "Start interactive chat with TUI",
		Run: func(cmd *cobra.Command, args []string) {
			config, err := loadConfig()
			if err != nil {
				fmt.Printf("Error loading config: %v\n", err)
				fmt.Println("Run 'voyager init' to set up configuration")
				return
			}

			debugEnabled, _ := cmd.Flags().GetBool("debug")
			model := initialModel(config, debugEnabled)
			p := tea.NewProgram(model, tea.WithAltScreen())

			if _, err := p.Run(); err != nil {
				fmt.Printf("Error running program: %v\n", err)
				os.Exit(1)
			}
		},
	}

	// Initialize command
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize Voyager configuration",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Initializing Voyager...")

			config := &Config{
				Providers: map[string]Provider{
					"digitalocean": {
						Type:    ProviderDigitalOcean,
						BaseURL: "https://inference.do-ai.run/v1/chat/completions",
						APIKey:  "${DO_SERVERLESS_INFERENCE}",
						Models:  []string{"llama3.3-70b-instruct", "llama3.1-8b-instruct", "meta-llama/Llama-3.2-3B-Instruct"},
					},
				},
				DefaultProvider: "digitalocean",
				DefaultModel:    "llama3.3-70b-instruct",
			}

			if err := saveConfig(config); err != nil {
				fmt.Printf("Error saving config: %v\n", err)
				return
			}

			fmt.Println("Voyager configuration initialized!")
			fmt.Println("Set your DigitalOcean API key:")
			fmt.Println("   export DO_SERVERLESS_INFERENCE=your-api-key")
			fmt.Println()
			fmt.Println("Run 'voyager chat' to start your AI journey!")
		},
	}

	// Add provider command
	addCmd := &cobra.Command{
		Use:   "add [provider-name]",
		Short: "Add a new AI provider",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			_, err := loadConfig()
			if err != nil {
				fmt.Printf("Error loading config: %v\n", err)
				return
			}

			providerName := args[0]
			fmt.Printf("Adding provider: %s\n", providerName)
			fmt.Println("📋 Example configuration:")
			fmt.Println(`
DigitalOcean Serverless Inference:
{
  "type": "digitalocean",
  "base_url": "https://inference.do-ai.run/v1/chat/completions",
  "api_key": "your-model-access-key",
  "models": ["llama3.3-70b-instruct", "llama3.1-8b-instruct", "meta-llama/Llama-3.2-3B-Instruct"]
}`)

			fmt.Printf("\n💡 Edit voyager-config.json to add the '%s' provider\n", providerName)
		},
	}

	// Status command
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show Voyager status",
		Run: func(cmd *cobra.Command, args []string) {
			config, err := loadConfig()
			if err != nil {
				fmt.Printf("❌ Config not found: %v\n", err)
				return
			}

			fmt.Println("Voyager Status")
			fmt.Printf("Config file: voyager-config.json\n")
			fmt.Printf("Default provider: %s\n", config.DefaultProvider)
			fmt.Printf("🤖 Default model: %s\n", config.DefaultModel)
			fmt.Println("\nConfigured providers:")

			for name, provider := range config.Providers {
				status := "OK"
				if provider.APIKey == "" {
					status = "(no API key)"
				}
				fmt.Printf("  %s (%s) %s - %d models\n",
					name, provider.Type, status, len(provider.Models))
			}
		},
	}

	// MCP command
	mcpCmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage MCP servers",
		Long:  "Add, remove, list, and manage Model Context Protocol servers",
	}

	mcpAddCmd := &cobra.Command{
		Use:   "add <name> [flags] -- <command>...",
		Short: "Add a new MCP server",
		Long: `Add a new MCP server to voyager configuration.

Examples:
  voyager mcp add filesystem -- npx -y @modelcontextprotocol/server-filesystem /tmp
  voyager mcp add digitalocean -e DIGITALOCEAN_API_TOKEN=dop_xxx -- npx -y @digitalocean/mcp --services apps,droplets
  voyager mcp add sqlite -- npx -y @modelcontextprotocol/server-sqlite --db-path ./data.db`,
		Args: cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			// Get all args including those after --
			allArgs := os.Args[3:] // Skip "voyager mcp add"

			// Find the position of "--"
			dashIndex := -1
			for i, arg := range allArgs {
				if arg == "--" {
					dashIndex = i
					break
				}
			}

			if dashIndex == -1 {
				fmt.Println("Error: Command must be specified after '--'")
				fmt.Println("Example: voyager mcp add myserver -- npx server-command")
				return
			}

			serverName := allArgs[0]
			command := allArgs[dashIndex+1:]

			envVars, _ := cmd.Flags().GetStringArray("env")

			config, err := loadConfig()
			if err != nil {
				fmt.Printf("Error loading config: %v\n", err)
				return
			}

			if config.MCPServers == nil {
				config.MCPServers = make(map[string]MCPServer)
			}

			config.MCPServers[serverName] = MCPServer{
				Name:    serverName,
				Command: command,
				Env:     envVars,
				Active:  false,
			}

			if err := saveConfig(config); err != nil {
				fmt.Printf("Error saving config: %v\n", err)
				return
			}

			fmt.Printf("✅ Added MCP server '%s'\n", serverName)
			fmt.Printf("Command: %v\n", command)
			if len(envVars) > 0 {
				fmt.Printf("Environment: %v\n", envVars)
			}
			fmt.Printf("Start it with: voyager mcp start %s\n", serverName)
		},
	}
	mcpAddCmd.Flags().StringArrayP("env", "e", []string{}, "Environment variables (KEY=value)")

	mcpRemoveCmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove an MCP server",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			serverName := args[0]
			config, err := loadConfig()
			if err != nil {
				fmt.Printf("Error loading config: %v\n", err)
				return
			}

			if _, exists := config.MCPServers[serverName]; !exists {
				fmt.Printf("❌ MCP server '%s' not found\n", serverName)
				return
			}

			delete(config.MCPServers, serverName)

			if err := saveConfig(config); err != nil {
				fmt.Printf("Error saving config: %v\n", err)
				return
			}

			fmt.Printf("✅ Removed MCP server '%s'\n", serverName)
		},
	}

	mcpListCmd := &cobra.Command{
		Use:   "list",
		Short: "List all MCP servers",
		Run: func(cmd *cobra.Command, args []string) {
			config, err := loadConfig()
			if err != nil {
				fmt.Printf("Error loading config: %v\n", err)
				return
			}

			if len(config.MCPServers) == 0 {
				fmt.Println("No MCP servers configured")
				return
			}

			fmt.Println("Configured MCP servers:")
			for name, server := range config.MCPServers {
				status := "inactive"
				if server.Active {
					status = "active"
				}
				fmt.Printf("  %s (%s) - %s\n", name, server.Name, status)
				fmt.Printf("    Command: %v\n", server.Command)
				if len(server.Env) > 0 {
					fmt.Printf("    Environment: %v\n", server.Env)
				}
			}
		},
	}

	mcpStartCmd := &cobra.Command{
		Use:   "start <name>",
		Short: "Start an MCP server",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			serverName := args[0]
			fmt.Printf("Starting MCP server '%s'...\n", serverName)
			fmt.Println("Note: Use 'voyager chat' and '/mcp start' for interactive management")
		},
	}

	mcpStopCmd := &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop an MCP server",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			serverName := args[0]
			fmt.Printf("Note: MCP server management is available in 'voyager chat' with '/mcp stop %s'\n", serverName)
		},
	}

	mcpCmd.AddCommand(mcpAddCmd, mcpRemoveCmd, mcpListCmd, mcpStartCmd, mcpStopCmd)

	// Add debug flags
	rootCmd.PersistentFlags().Bool("debug", false, "Enable debug mode with debug window")
	chatCmd.Flags().Bool("debug", false, "Enable debug mode with debug window")

	rootCmd.AddCommand(chatCmd, initCmd, addCmd, statusCmd, mcpCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

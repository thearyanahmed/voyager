package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// Provider types
type ProviderType string

const (
	ProviderAnthropic     ProviderType = "anthropic"
	ProviderOpenAI        ProviderType = "openai"
	ProviderOllama        ProviderType = "ollama"
	ProviderLocal         ProviderType = "local"
	ProviderGitHub        ProviderType = "github"
	ProviderDigitalOcean  ProviderType = "digitalocean"
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
	Providers       map[string]Provider   `json:"providers"`
	DefaultModel    string                `json:"default_model"`
	DefaultProvider string                `json:"default_provider"`
	MCPServers      map[string]MCPServer  `json:"mcp_servers,omitempty"`
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
	JSONRPC string                 `json:"jsonrpc"`
	ID      int                    `json:"id,omitempty"`
	Result  interface{}            `json:"result,omitempty"`
	Error   *JSONRPCError         `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Styles for the TUI
var (
	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8B4513")).
			Background(lipgloss.Color("#F5F5DC")).
			Padding(0, 1).
			Bold(true)

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262")).
			Italic(true)
	userMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0")).
			Bold(true).
			MarginLeft(0).
			MarginRight(0)

	assistantMsgStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#A0A0A0")).
				Bold(true).
				MarginLeft(0).
				MarginRight(0)

	systemMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0")).
			Bold(true).
			MarginLeft(0).
			MarginRight(0)

	msgContentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0")).
			MarginLeft(0).
			MarginBottom(0)

	inputStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#FAFAF7")).
			Padding(0).
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
}

type responseMsg struct {
	content string
	err     error
}

type tickMsg time.Time

var spinnerFrames = []string{"[+]", "[/]", "[-]", "[\\]"}

func initialModel(config *Config) ChatModel {
	ta := textarea.New()
	ta.Placeholder = "Type your message here..."
	ta.Focus()
	ta.Prompt = ""
	ta.CharLimit = 4000
	ta.SetWidth(80)
	ta.SetHeight(1)
	ta.MaxHeight = 10
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetEnabled(true)

	vp := viewport.New(80, 20)
	vp.SetContent("")

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

	return ChatModel{
		config:          config,
		conversation:    []Message{},
		currentProvider: provider,
		currentModel:    model,
		textarea:        ta,
		viewport:        vp,
		client:          &http.Client{Timeout: 120 * time.Second},
		mcpClients:      make(map[string]*MCPClient),
	}
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
		if m.loading {
			m.spinnerIndex = (m.spinnerIndex + 1) % len(spinnerFrames)
			m.updateViewport()
			return m, tickCmd()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		headerHeight := 5
		footerHeight := 8
		verticalMarginHeight := headerHeight + footerHeight

		if !m.ready {
			m.viewport = viewport.New(msg.Width-4, msg.Height-verticalMarginHeight)
			m.viewport.YPosition = headerHeight
			m.ready = true
		} else {
			m.viewport.Width = msg.Width - 4
			m.viewport.Height = msg.Height - verticalMarginHeight
		}

		m.textarea.SetWidth(msg.Width)

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
				break
			}

			// Handle commands
			if strings.HasPrefix(userInput, "/") {
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
			m.textarea.Reset()
			m.loading = true
			m.spinnerIndex = 0
			m.updateViewport()

			return m, tea.Batch(m.sendMessage(), tickCmd())
		}

	case responseMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
		} else if strings.TrimSpace(msg.content) == "" {
			m.err = fmt.Errorf("received empty response from API")
		} else {
			assistantMsg := Message{
				Role:      "assistant",
				Content:   msg.content,
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, assistantMsg)
			m.err = nil
		}
		m.updateViewport()
	}

	return m, tea.Batch(tiCmd, vpCmd)
}

func (m *ChatModel) handleCommand(cmd string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return *m, nil
	}

	switch parts[0] {
	case "clear":
		m.conversation = []Message{}
		m.err = nil
		m.updateViewport()

	case "model":
		if len(parts) > 1 {
			m.currentModel = parts[1]
			systemMsg := Message{
				Role:      "system",
				Content:   fmt.Sprintf("Switched to model: %s", m.currentModel),
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
			m.updateViewport()
		} else {
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
				m.currentProvider = parts[1]
				if len(m.config.Providers[parts[1]].Models) > 0 {
					m.currentModel = m.config.Providers[parts[1]].Models[0]
				}
				systemMsg := Message{
					Role:      "system",
					Content:   fmt.Sprintf("Switched to provider: %s (model: %s)", m.currentProvider, m.currentModel),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, systemMsg)
				m.updateViewport()
			} else {
				systemMsg := Message{
					Role:      "system",
					Content:   fmt.Sprintf("Provider '%s' not found", parts[1]),
					Timestamp: time.Now(),
				}
				m.conversation = append(m.conversation, systemMsg)
				m.updateViewport()
			}
		} else {
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
/mcp start <server> - Start MCP server
/mcp stop <server> - Stop MCP server
/mcp list - List MCP servers
/mcp tools - List available MCP tools
/mcp call <tool> [args] - Call MCP tool
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

	case "mcp":
		if len(parts) < 2 {
			systemMsg := Message{
				Role:      "system",
				Content:   "MCP commands: /mcp start <server>, /mcp stop <server>, /mcp list, /mcp tools, /mcp call <tool> [args...]",
				Timestamp: time.Now(),
			}
			m.conversation = append(m.conversation, systemMsg)
			m.updateViewport()
		} else {
			return m.handleMCPCommand(parts[1:])
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

	switch provider.Type {
	case ProviderOpenAI:
		return m.sendOpenAIRequest(provider)
	case ProviderAnthropic:
		return m.sendAnthropicRequest(provider)
	case ProviderOllama:
		return m.sendOllamaRequest(provider)
	case ProviderLocal:
		return m.sendLocalRequest(provider)
	case ProviderGitHub:
		return m.sendGitHubRequest(provider)
	case ProviderDigitalOcean:
		return m.sendDigitalOceanRequest(provider)
	default:
		return "", fmt.Errorf("unknown provider type: %s", provider.Type)
	}
}

func (m ChatModel) sendOpenAIRequest(provider Provider) (string, error) {
	type OpenAIRequest struct {
		Model     string    `json:"model"`
		Messages  []Message `json:"messages"`
		MaxTokens int       `json:"max_tokens,omitempty"`
	}

	type OpenAIResponse struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	// Convert messages to API format (without timestamp)
	apiMessages := make([]Message, len(m.conversation))
	for i, msg := range m.conversation {
		if msg.Role != "system" { // Skip system messages for API
			apiMessages[i] = Message{
				Role:    msg.Role,
				Content: msg.Content,
			}
		}
	}

	request := OpenAIRequest{
		Model:     m.currentModel,
		Messages:  apiMessages,
		MaxTokens: 4000,
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", provider.BaseURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var apiResp OpenAIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return apiResp.Choices[0].Message.Content, nil
}

func (m ChatModel) sendAnthropicRequest(provider Provider) (string, error) {
	type AnthropicRequest struct {
		Model     string    `json:"model"`
		Messages  []Message `json:"messages"`
		MaxTokens int       `json:"max_tokens"`
	}

	type AnthropicResponse struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	// Convert messages to API format (without timestamp and system messages)
	var apiMessages []Message
	for _, msg := range m.conversation {
		if msg.Role != "system" {
			apiMessages = append(apiMessages, Message{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}

	request := AnthropicRequest{
		Model:     m.currentModel,
		Messages:  apiMessages,
		MaxTokens: 4000,
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", provider.BaseURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", provider.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var apiResp AnthropicResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("no content in response")
	}

	return apiResp.Content[0].Text, nil
}

func (m ChatModel) sendOllamaRequest(provider Provider) (string, error) {
	type OllamaRequest struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
		Stream   bool      `json:"stream"`
	}

	type OllamaResponse struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}

	// Convert messages to API format
	var apiMessages []Message
	for _, msg := range m.conversation {
		if msg.Role != "system" {
			apiMessages = append(apiMessages, Message{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}

	request := OllamaRequest{
		Model:    m.currentModel,
		Messages: apiMessages,
		Stream:   false,
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	url := provider.BaseURL + "/api/chat"

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var apiResp OllamaResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	return apiResp.Message.Content, nil
}

func (m ChatModel) sendLocalRequest(provider Provider) (string, error) {
	// Try OpenAI-compatible format first
	response, err := m.sendOpenAIRequest(provider)
	if err == nil {
		return response, nil
	}

	// Fallback to generic format
	var apiMessages []Message
	for _, msg := range m.conversation {
		if msg.Role != "system" {
			apiMessages = append(apiMessages, Message{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}

	request := map[string]interface{}{
		"model":      m.currentModel,
		"messages":   apiMessages,
		"max_tokens": 4000,
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", provider.BaseURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return string(body), nil // Return raw response as fallback
	}

	// Try common response field names
	if content, ok := result["content"].(string); ok {
		return content, nil
	}
	if response, ok := result["response"].(string); ok {
		return response, nil
	}
	if message, ok := result["message"].(string); ok {
		return message, nil
	}

	return string(body), nil
}

func (m ChatModel) sendGitHubRequest(provider Provider) (string, error) {
	type GitHubRequest struct {
		Model     string    `json:"model"`
		Messages  []Message `json:"messages"`
		MaxTokens int       `json:"max_tokens,omitempty"`
	}

	type GitHubResponse struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	// Convert messages to API format (without timestamp)
	var apiMessages []Message
	for _, msg := range m.conversation {
		if msg.Role != "system" {
			apiMessages = append(apiMessages, Message{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}

	request := GitHubRequest{
		Model:     m.currentModel,
		Messages:  apiMessages,
		MaxTokens: 4000,
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", provider.BaseURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	req.Header.Set("User-Agent", "Voyager/1.0")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var apiResp GitHubResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		// Try to parse as generic response
		var genericResp map[string]interface{}
		if err2 := json.Unmarshal(body, &genericResp); err2 == nil {
			if content, ok := genericResp["content"].(string); ok {
				return content, nil
			}
			if message, ok := genericResp["message"].(string); ok {
				return message, nil
			}
		}
		return "", fmt.Errorf("failed to parse response: %w\nResponse: %s", err, string(body))
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return apiResp.Choices[0].Message.Content, nil
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
		MaxTokens:   4000,
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", provider.BaseURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var apiResp DigitalOceanResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return apiResp.Choices[0].Message.Content, nil
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
					resultJSON, _ := json.MarshalIndent(result, "", "  ")
					systemMsg := Message{
						Role:      "system",
						Content:   fmt.Sprintf("Tool '%s' result:\n%s", toolName, string(resultJSON)),
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
	server, exists := m.config.MCPServers[serverName]
	if !exists {
		return fmt.Errorf("MCP server '%s' not found in configuration", serverName)
	}

	if len(server.Command) == 0 {
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
		fmt.Printf("DEBUG: Starting MCP server with command: %v\n", expandedCommand)
		fmt.Printf("DEBUG: Environment: %v\n", expandedEnv)
		
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
			fmt.Printf("DEBUG: Failed to start MCP server: %v\n", err)
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
		}

		m.mcpClients[serverName] = client

		// Give server time to start
		time.Sleep(1 * time.Second)
		
		// Try to initialize
		if err := client.initialize(); err != nil {
			fmt.Printf("DEBUG: Failed to initialize MCP server: %v\n", err)
			fmt.Printf("DEBUG: Server stderr: %s\n", stderr.String())
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

func (m *ChatModel) stopMCPServer(serverName string) error {
	client, exists := m.mcpClients[serverName]
	if !exists {
		return fmt.Errorf("MCP server '%s' is not running", serverName)
	}

	if client.Process != nil && client.Process.Process != nil {
		client.Process.Process.Kill()
		client.Process.Wait()
	}

	delete(m.mcpClients, serverName)
	
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
	fmt.Printf("DEBUG: Sending MCP request: %s\n", string(data))
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
			fmt.Printf("DEBUG: Received MCP response (%d bytes): %s\n", len(responseData), debugOutput)
			
			var response JSONRPCResponse
			if err := json.Unmarshal(responseData, &response); err != nil {
				fmt.Printf("DEBUG: Failed to unmarshal response: %v\n", err)
				fmt.Printf("DEBUG: Response data length: %d bytes\n", len(responseData))
				errorChan <- fmt.Errorf("failed to unmarshal response: %w", err)
			} else {
				responseChan <- response
			}
		} else {
			fmt.Printf("DEBUG: Failed to scan stdout from MCP server\n")
			if err := client.Stdout.Err(); err != nil {
				fmt.Printf("DEBUG: Scanner error: %v\n", err)
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
		fmt.Printf("DEBUG: Failed to list tools: %v\n", err)
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
		fmt.Printf("DEBUG: Failed to list resources: %v\n", err)
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
		fmt.Printf("DEBUG: Invalid env var format (no =): %s\n", envVar)
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
		debugValue := envValue
		if len(debugValue) > 20 {
			debugValue = debugValue[:20] + "..."
		}
		fmt.Printf("DEBUG: Expanding ${%s} -> '%s' (length: %d)\n", envName, debugValue, len(envValue))
		return key + "=" + envValue
	}
	
	// Return as-is if not in ${...} format
	fmt.Printf("DEBUG: Not expanding env var (no ${} pattern): %s\n", envVar)
	return envVar
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

	for _, msg := range m.conversation {
		switch msg.Role {
		case "user":
			// Don't show user messages

		case "assistant":
			content.WriteString(msgContentStyle.Render(strings.TrimLeft(msg.Content, "\n")))
			content.WriteString("\n\n")

		case "system":
			content.WriteString(systemMsgStyle.Render(msg.Content))
			content.WriteString("\n\n")
		}
	}

	if m.loading {
		spinner := spinnerFrames[m.spinnerIndex]
		content.WriteString(msgContentStyle.Render(fmt.Sprintf("%s thinking...", spinner)))
		content.WriteString("\n")
	}

	if m.err != nil {
		content.WriteString(errorStyle.Render(m.err.Error()))
		content.WriteString("\n\n")
	}

	m.viewport.SetContent(content.String())
	m.viewport.GotoBottom()
}

func (m ChatModel) View() string {
	if !m.ready {
		return "\n  Initializing Voyager..."
	}

	// Header with voyager branding
	title := titleStyle.Render(fmt.Sprintf(" VOYAGER - %s/%s ", m.currentProvider, m.currentModel))

	// Status line
	status := statusStyle.Render(fmt.Sprintf("Provider: %s | Model: %s | Messages: %d",
		m.currentProvider, m.currentModel, len(m.conversation)))

	// Help
	help := helpStyle.Render("Controls: Enter=Send • Ctrl+C=Quit • /help=Commands")

	// Chat area without border
	chatArea := lipgloss.NewStyle().
		Padding(0, 2).
		Height(m.viewport.Height).
		Render(m.viewport.View())

	// Input area
	inputArea := inputStyle.Render(m.textarea.View())

	return lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		status,
		"",
		chatArea,
		"",
		inputArea,
		help,
	)
}

// Configuration functions
func autoDetectProviders() map[string]Provider {
	providers := make(map[string]Provider)

	// Auto-detect Anthropic
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		providers["anthropic"] = Provider{
			Type:    ProviderAnthropic,
			BaseURL: "https://api.anthropic.com/v1/messages",
			APIKey:  apiKey,
			Models:  []string{"claude-sonnet-4-20250514", "claude-opus-4-20250514", "claude-haiku-3-5-20250114"},
		}
	}

	// Auto-detect GitHub Copilot
	if apiKey := os.Getenv("GITHUB_TOKEN"); apiKey != "" {
		providers["github"] = Provider{
			Type:    ProviderGitHub,
			BaseURL: "https://api.githubcopilot.com/chat/completions",
			APIKey:  apiKey,
			Models:  []string{"gpt-4o", "gpt-4", "gpt-3.5-turbo"},
		}
	}

	// Auto-detect OpenAI
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		providers["openai"] = Provider{
			Type:    ProviderOpenAI,
			BaseURL: "https://api.openai.com/v1/chat/completions",
			APIKey:  apiKey,
			Models:  []string{"gpt-5", "gpt-oss-120b", "gpt-4", "gpt-4-turbo", "gpt-3.5-turbo"},
		}
	}

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

func loadConfig() (*Config, error) {
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
		// Prefer digitalocean, then github, then anthropic, then openai
		if _, exists := config.Providers["digitalocean"]; exists {
			config.DefaultProvider = "digitalocean"
		} else if _, exists := config.Providers["github"]; exists {
			config.DefaultProvider = "github"
		} else if _, exists := config.Providers["anthropic"]; exists {
			config.DefaultProvider = "anthropic"
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

			model := initialModel(config)
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

			model := initialModel(config)
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
					"ollama": {
						Type:    ProviderOllama,
						BaseURL: "http://localhost:11434",
						Models:  []string{"llama3.2", "codellama", "mistral", "phi3", "qwen2.5-coder"},
					},
				},
				DefaultProvider: "ollama",
				DefaultModel:    "llama3.2",
			}

			if err := saveConfig(config); err != nil {
				fmt.Printf("Error saving config: %v\n", err)
				return
			}

			fmt.Println("Voyager configuration initialized!")
			fmt.Println("Edit voyager-config.json to add more providers:")
			fmt.Println("   - OpenAI (GPT-4, GPT-3.5)")
			fmt.Println("   - Anthropic (Claude)")
			fmt.Println("   - Custom local endpoints")
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
			fmt.Println("📋 Example configurations:")
			fmt.Println(`
OpenAI:
{
  "type": "openai",
  "base_url": "https://api.openai.com/v1/chat/completions",
  "api_key": "your-api-key",
  "models": ["gpt-oss-120b","gpt-4", "gpt-3.5-turbo", "gpt-4o"]
}

Anthropic:
{
  "type": "anthropic",
  "base_url": "https://api.anthropic.com/v1/messages",
  "api_key": "your-api-key",
  "models": ["claude-sonnet-4-20250514", "claude-opus-4-20250514"]
}

DigitalOcean Gradient Platform:
{
  "type": "digitalocean",
  "base_url": "https://inference.do-ai.run/v1/chat/completions",
  "api_key": "your-model-access-key",
  "models": ["llama3.3-70b-instruct", "llama3.1-8b-instruct", "meta-llama/Llama-3.2-3B-Instruct"]
}

GitHub Copilot:
{
  "type": "github",
  "base_url": "https://api.githubcopilot.com/chat/completions",
  "api_key": "your-github-token",
  "models": ["gpt-4o", "gpt-4", "gpt-3.5-turbo"]
}

Local/Custom:
{
  "type": "local",
  "base_url": "http://localhost:8000/v1/chat/completions",
  "api_key": "optional",
  "models": ["your-model"]
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
				if provider.Type != ProviderOllama && provider.APIKey == "" {
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

	rootCmd.AddCommand(chatCmd, initCmd, addCmd, statusCmd, mcpCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

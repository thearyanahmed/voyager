# Voyager

Multi-provider AI CLI with a beautiful TUI (Terminal User Interface) built in Go. Provides a unified interface for chatting with multiple AI providers including OpenAI, Anthropic, Ollama, and custom local models.

## Quick Start

### Install and Run
```bash
# Build the binary
go build -o voyager

# Initialize configuration 
./voyager init

# Start chatting
./voyager chat
```

## Running with Ollama Moondream

Moondream is a vision-language model that can analyze images and answer questions about them.

### Prerequisites
1. Install [Ollama](https://ollama.ai)
2. Pull the Moondream model:
   ```bash
   ollama pull moondream
   ```

### Setup
1. Initialize Voyager configuration:
   ```bash
   ./voyager init
   ```

2. Edit `voyager-config.json` to include Moondream:
   ```json
   {
     "providers": {
       "ollama": {
         "type": "ollama",
         "base_url": "http://localhost:11434",
         "models": ["moondream", "llama3.2", "codellama"]
       }
     },
     "default_provider": "ollama",
     "default_model": "moondream"
   }
   ```

3. Start Voyager:
   ```bash
   ./voyager chat
   ```

4. Switch to Moondream model (if not already default):
   ```
   /model moondream
   ```

### Usage with Images
Once running with Moondream, you can:
- Ask questions about images in your conversation
- Analyze visual content
- Get descriptions of images

### Available Commands
- `/clear` - Clear conversation
- `/model <name>` - Switch model (e.g., `/model moondream`)
- `/provider <name>` - Switch provider
- `/providers` - List available providers
- `/models` - List models for current provider
- `/help` - Show help
- `/quit` - Exit

### Controls
- **Enter** - Send message
- **Ctrl+C** - Quit
- **Esc** - Quit

## Other Providers

### GitHub Copilot
```bash
export GITHUB_TOKEN="your-github-token"
./voyager chat
```

Note: You need a GitHub account with Copilot access. Get your token from GitHub Settings > Personal access tokens.

### Anthropic (Claude)
```bash
export ANTHROPIC_API_KEY="your-api-key"
./voyager chat
```

### OpenAI (GPT)
```bash
export OPENAI_API_KEY="your-api-key"  
./voyager chat
```

### Custom Local Models
Edit `voyager-config.json` to add custom endpoints:
```json
{
  "providers": {
    "custom": {
      "type": "local",
      "base_url": "http://localhost:8000/v1/chat/completions",
      "api_key": "optional",
      "models": ["your-model"]
    }
  }
}
```

## Configuration

Configuration is stored in `voyager-config.json`. Environment variables are auto-detected:
- `GITHUB_TOKEN` - For GitHub Copilot models
- `ANTHROPIC_API_KEY` - For Claude models
- `OPENAI_API_KEY` - For GPT models

## Building

```bash
# Standard build
go build

# Optimized build (smaller size)
go build -ldflags="-s -w" -o voyager

# Clean dependencies
go mod tidy
```
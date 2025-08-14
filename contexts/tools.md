# Tool Usage Guide

## Available Tools

### Native Tools (Built-in)
Always available and reliable:
- `bash` - Execute shell commands (use `pwd` to get current directory)
- `read` - Read file contents (requires absolute path)
- `write` - Write file contents
- `glob` - Find files by patterns
- `grep` - Search file contents

### MCP Tools

#### DigitalOcean MCP Tools
Use these for App Platform operations:
- Deploy applications  
- Create apps from specs
- List existing apps
- Manage deployments

#### Filesystem MCP Tools  
Use these for local file operations (require absolute paths):
- `read_file` - Read local files
- `write_file` - Write/create files
- `list_directory` - Browse directories
- `search_files` - Search for files with patterns

#### Fetch MCP Tools
Use these for web content:
- Navigate to web pages
- Take screenshots
- Extract content from websites

## Tool Chaining Strategy

### When users ask about "current directory" or "this directory":
1. **First**: Use native `bash` tool with `{"command": "pwd"}` to get current directory
2. **Then**: Use that result in filesystem MCP tools like `{"path": "/the/current/directory"}`

### Example workflows:

**User asks: "list files in this directory"**
- Step 1: Call `bash` with `{"command": "pwd"}` → get "/Users/user/project"  
- Step 2: Call `list_directory` with `{"path": "/Users/user/project"}`

**User asks: "read the go.mod file"**
- Step 1: Call `bash` with `{"command": "pwd"}` → get "/Users/user/project"
- Step 2: Call `read_file` with `{"path": "/Users/user/project/go.mod"}`

**User asks: "find all .go files"**
- Step 1: Call `bash` with `{"command": "pwd"}` → get "/Users/user/project"
- Step 2: Call `search_files` with `{"path": "/Users/user/project", "pattern": "*.go"}`

## Key Principles
- **Always get current directory first** when user refers to "this directory", "current directory", "here", etc.
- **Use absolute paths** for all filesystem operations
- **Chain tools together** to build context and complete complex tasks
- **Native tools are preferred** for simple operations like getting current directory
# DigitalOcean App Platform Specification Guide

## Core App Spec Structure

### Required Fields
- `name`: App name (2-32 chars, lowercase, hyphens allowed)
- `region`: Deployment region (`nyc1`, `fra1`, `sfo3`, `sgp1`, `tor1`, `lon1`, `ams3`, `blr1`)

### Service Types
- **services**: HTTP-accessible web applications
- **static_sites**: Static websites (HTML/CSS/JS)
- **workers**: Background processing tasks
- **jobs**: One-time or scheduled tasks
- **functions**: Serverless functions
- **databases**: Managed databases

## Service Configuration

### Source Options
```yaml
git:
  repo_clone_url: https://github.com/user/repo.git
  branch: main
```

### Build & Runtime
- `environment_slug`: Runtime environment
  - Go: `go`
  - Node.js: `node-js` 
  - Python: `python`
  - PHP: `php`
  - Ruby: `ruby`
- `build_command`: Build command to execute
- `run_command`: Command to start the service
- `source_dir`: Source directory (default: `/`)

### Instance Sizing (Current Plans)

#### Shared CPU (Recommended for most apps)
- `apps-s-1vcpu-0.5gb`: 1 vCPU, 512MB RAM, $5/mo
- `apps-s-1vcpu-1gb`: 1 vCPU, 1GB RAM, $12/mo  
- `apps-s-1vcpu-2gb`: 1 vCPU, 2GB RAM, $25/mo
- `apps-s-2vcpu-4gb`: 2 vCPU, 4GB RAM, $50/mo

#### Dedicated CPU (For high-performance apps)
- `apps-d-1vcpu-1gb`: 1 vCPU, 1GB RAM, $34/mo
- `apps-d-1vcpu-2gb`: 1 vCPU, 2GB RAM, $39/mo
- `apps-d-2vcpu-4gb`: 2 vCPU, 4GB RAM, $78/mo

### Scaling & Health
- `instance_count`: Number of instances (1-250)
- `instance_size_slug`: Instance size from above
- `health_check`: Health check configuration
- `http_port`: Port for HTTP traffic

## Example Configurations

### Go Web Service
```yaml
name: go-api
region: nyc1
services:
- name: api
  git:
    repo_clone_url: https://github.com/user/go-api.git
    branch: main
  build_command: go build -o main .
  run_command: ./main
  environment_slug: go
  instance_count: 1
  instance_size_slug: apps-s-1vcpu-0.5gb
  http_port: 8080
  health_check:
    http_path: /health
  envs:
  - key: PORT
    value: "8080"
```

### Node.js Application
```yaml
name: node-app
region: nyc1
services:
- name: web
  git:
    repo_clone_url: https://github.com/user/node-app.git
    branch: main
  build_command: npm install
  run_command: npm start
  environment_slug: node-js
  instance_count: 1
  instance_size_slug: apps-s-1vcpu-0.5gb
  http_port: 3000
```

### Static Site
```yaml
name: static-site
region: nyc1
static_sites:
- name: frontend
  git:
    repo_clone_url: https://github.com/user/frontend.git
    branch: main
  build_command: npm run build
  output_dir: /dist
```

## Best Practices

### Naming
- Use directory name or git repo name as base
- Convert to lowercase, replace special chars with hyphens
- Keep under 32 characters

### Resource Sizing
- Start with `apps-s-1vcpu-0.5gb` for cost efficiency
- Scale up based on actual usage
- Use dedicated CPU for production workloads

### Environment Variables
- Always set `PORT` for web services
- Use `"type": "SECRET"` for sensitive values
- Detect from .env files when available

### Health Checks
- Add `/health`, `/ping`, or `/status` endpoints
- Only configure if endpoint exists
- Improves deployment reliability
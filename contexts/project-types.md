# Project Type Detection Rules

## Go Projects
**Detection**: `go.mod` file present
- **Buildpack**: `GOLANG`
- **Build Command**: `go build -o main .`
- **Run Command**: `./main`
- **Port**: Check for `PORT` env var usage in code, default to `8080`
- **Environment**: Look for `.env` files and Go env var patterns

## Node.js Projects
**Detection**: `package.json` file present
- **Buildpack**: `NODEJS`
- **Build Command**: `npm install` (or `npm ci` for production)
- **Run Command**: Check `package.json` scripts for `start`, fallback to `node index.js`
- **Port**: Check package.json scripts and code for port, default to `3000`
- **Environment**: Look for `.env` files and process.env usage

## Python Projects
**Detection**: `requirements.txt`, `Pipfile`, or `pyproject.toml` present
- **Buildpack**: `PYTHON`
- **Build Command**: `pip install -r requirements.txt`
- **Run Command**: Look for `main.py`, `app.py`, or `wsgi.py`
- **Port**: Check for Flask/Django patterns, default to `5000`
- **Environment**: Look for `.env` files and os.environ usage

## Static Sites
**Detection**: Only HTML/CSS/JS files, or `index.html` present
- **Type**: `STATIC_SITE`
- **Build Command**: None required
- **Output Directory**: Current directory or `dist/`
- **Environment**: Generally none needed

## Docker Projects
**Detection**: `Dockerfile` present
- **Type**: `DOCKER`
- **Build Command**: Use Dockerfile
- **Run Command**: Use Dockerfile CMD
- **Port**: Parse EXPOSE directive from Dockerfile
- **Environment**: Parse ENV directives from Dockerfile

## Detection Priority
1. Check for Dockerfile (highest priority)
2. Check for language-specific files (go.mod, package.json, etc.)
3. Check for static site patterns (lowest priority)

## Common Environment Variables to Detect
- `PORT` - Application port
- `DATABASE_URL` - Database connection
- `API_KEY`, `SECRET_KEY` - API credentials
- `ENV`, `ENVIRONMENT` - Environment type (development/production)
- `DEBUG` - Debug mode flag
# Adding a Dockerfile

SimplePanel builds an app from the `Dockerfile` in your repository. Without
one, it serves the repository as a static website, which only works for
HTML/CSS/JS files and site generators such as Vite, Astro or Hugo.

Apps that run a server (Node.js, Next.js, Python, PHP, Go…) need a
Dockerfile. Copy the example for your language into a file named
`Dockerfile` at the root of the repository, adjust the start command, commit
it and deploy again.

Two things to check in any Dockerfile:

- **`EXPOSE` the port your app listens on.** SimplePanel reads it and sends
  your domain's traffic there, so you don't have to set the port by hand.
- **Listen on `0.0.0.0`, not `localhost`.** Otherwise the app can't be
  reached from outside its container.

Settings your app needs (database URLs, API keys) go in the app's
**Environment** tab, not in the Dockerfile.

## Node.js (Express, Fastify, Nest…)

```dockerfile
FROM node:22-alpine
WORKDIR /app
COPY package*.json ./
RUN npm ci --omit=dev
COPY . .
ENV NODE_ENV=production PORT=3000
EXPOSE 3000
CMD ["npm", "start"]
```

## Next.js

Add `output: 'standalone'` to `next.config.js`, then:

```dockerfile
FROM node:22-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production PORT=3000 HOSTNAME=0.0.0.0
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
COPY --from=build /app/public ./public
EXPOSE 3000
CMD ["node", "server.js"]
```

## Python (Flask, Django, FastAPI)

```dockerfile
FROM python:3.13-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt gunicorn
COPY . .
EXPOSE 8000
# Flask: "app:app". Django: "myproject.wsgi". FastAPI: add uvicorn and use
# ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
CMD ["gunicorn", "--bind", "0.0.0.0:8000", "app:app"]
```

## PHP

```dockerfile
FROM php:8.3-apache
COPY . /var/www/html/
EXPOSE 80
```

## Go

```dockerfile
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /app .

FROM alpine:3.21
COPY --from=build /app /app
EXPOSE 8080
CMD ["/app"]
```

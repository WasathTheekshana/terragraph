# Local development tasks. Settings come from .env (copy .env.example), with the defaults below.
# Every recipe is a single command with no shell-specific syntax, so it runs the same from
# PowerShell, cmd, bash, and zsh.

-include .env

TERRAGRAPH_API_URL           ?= http://localhost:8080
TERRAGRAPH_INGEST_TOKEN      ?= dev-token
TERRAGRAPH_TOKEN             ?= $(TERRAGRAPH_INGEST_TOKEN)
TERRAGRAPH_TEST_DATABASE_URL ?= postgres://terragraph:terragraph@localhost:5432/terragraph?sslmode=disable
export TERRAGRAPH_API_URL TERRAGRAPH_INGEST_TOKEN TERRAGRAPH_TOKEN

MODULE_REPO ?= https://github.com/terraform-aws-modules/terraform-aws-vpc.git
BRANCH      ?= main

TAILWIND_VERSION := 4.3.3
ifeq ($(OS),Windows_NT)
EXE := .exe
TAILWIND_ASSET := tailwindcss-windows-x64.exe
else
TAILWIND_OS := $(if $(filter Darwin,$(shell uname -s)),macos,linux)
TAILWIND_ARCH := $(if $(filter arm64 aarch64,$(shell uname -m)),arm64,x64)
TAILWIND_ASSET := tailwindcss-$(TAILWIND_OS)-$(TAILWIND_ARCH)
endif
# Tailwind's standalone CLI, so generating CSS needs no Node.js.
TAILWIND := .bin/tailwindcss-$(TAILWIND_VERSION)$(EXE)
WEB := server/internal/web

COMPOSE := docker compose -f server/docker-compose.yml
# -C runs the scanner from scanner/, so relative scan paths are made absolute first.
SCANNER := go run -C scanner ./cmd/terragraph

.DEFAULT_GOAL := help

.PHONY: help up down reset logs build scan scan-sample scan-module projects modules generate vet test test-db

# $(info) prints from make itself, so the text isn't subject to any shell's quoting.
help:
	$(info Local stack:)
	$(info make up            start Postgres and the server in the background)
	$(info make down          stop the stack, keeping its data)
	$(info make reset         stop the stack and delete its data)
	$(info make logs          follow the server logs)
	$(info Scanner:)
	$(info make build         build scanner/terragraph$(EXE))
	$(info make scan-sample   scan the bundled sample project)
	$(info make scan-module   list a module repo's versions (MODULE_REPO=url to change it))
	$(info make scan PROJECT=path/to/terraform [BRANCH=main]   scan your own project)
	$(info Queries:)
	$(info make projects      list projects)
	$(info make modules       list modules)
	$(info Web UI:)
	$(info make generate      regenerate the UI's templ code and CSS after editing it)
	$(info Checks:)
	$(info make vet           go vet both modules)
	$(info make test          unit tests; database tests are skipped)
	$(info make test-db       all tests; run make up first)
	@exit 0

up:
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down

reset:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f server

build:
	go build -C scanner -o terragraph$(EXE) ./cmd/terragraph

scan-sample:
	$(SCANNER) scan --mode project --path testdata/sample-project --repo-url https://github.com/example/app.git --branch $(BRANCH)

scan-module:
	$(SCANNER) scan --mode module-repo --repo-url $(MODULE_REPO)

scan:
	$(if $(PROJECT),,$(error PROJECT is required, e.g. make scan PROJECT=path/to/terraform))
	$(SCANNER) scan --mode project --path $(abspath $(PROJECT)) --branch $(BRANCH)

projects:
	curl -s $(TERRAGRAPH_API_URL)/api/v1/projects

modules:
	curl -s $(TERRAGRAPH_API_URL)/api/v1/modules

generate: $(TAILWIND)
	go tool -C server templ generate -path internal/web
	$(TAILWIND) -i $(WEB)/styles/app.css -o $(WEB)/static/app.css --minify

$(TAILWIND):
	curl -sSfL --create-dirs -o $(TAILWIND) https://github.com/tailwindlabs/tailwindcss/releases/download/v$(TAILWIND_VERSION)/$(TAILWIND_ASSET)
ifneq ($(OS),Windows_NT)
	chmod +x $(TAILWIND)
endif

vet:
	go vet -C scanner ./...
	go vet -C server ./...

test:
	go test -C scanner ./...
	go test -C server ./...

test-db: export TERRAGRAPH_TEST_DATABASE_URL := $(TERRAGRAPH_TEST_DATABASE_URL)
test-db:
	go test -C scanner ./...
	go test -C server -count=1 ./...

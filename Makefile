# ==============================================================================
# Definición de Variables
# ==============================================================================
BINARY_NAME=gobalancer
WIZARD_NAME=gobalancer-wizard
MAIN_PATH=cmd/balancer/main.go
WIZARD_PATH=cmd/wizard/main.go

BIN_DIR=bin
SYSTEM_BIN=/usr/local/bin
CONFIG_DIR=/etc/gobalancer
SERVICE_DIR=/etc/systemd/system

# Flags de compilación
# -s: Omitir la tabla de símbolos (reduce tamaño)
# -w: Omitir información de depuración DWARF (reduce tamaño)
LDFLAGS=-ldflags "-s -w"

# Go Command
GO=go

# ==============================================================================
# Targets Principales
# ==============================================================================

.PHONY: all help build build-prod install uninstall run run-wizard clean test deps fmt vet build-linux build-arm64

## help: Muestra esta ayuda
help:
	@echo "Uso: make [TARGET]"
	@echo ""
	@echo "Targets disponibles:"
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' |  sed -e 's/^/ /'

## all: Descarga dependencias, formatea y compila todo (Default)
all: deps fmt build

## deps: Descarga y limpia las dependencias del go.mod
deps:
	@echo "📦 Gestionando dependencias..."
	$(GO) mod tidy
	$(GO) mod verify

## build: Compila Core y Wizard para la arquitectura actual (Desarrollo)
build:
	@echo "🔨 Compilando $(BINARY_NAME) y $(WIZARD_NAME)..."
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	$(GO) build -o $(BIN_DIR)/$(WIZARD_NAME) $(WIZARD_PATH)
	@echo "✅ Build finalizado en $(BIN_DIR)/"

## build-prod: Compila optimizado para producción (Static, sin debug info)
build-prod:
	@echo "🏭 Compilando para Producción (Static Binary)..."
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	CGO_ENABLED=0 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(WIZARD_NAME) $(WIZARD_PATH)
	@echo "✅ Binarios optimizados listos."

## install: Instala binarios, configuración y servicio Systemd (Requiere sudo)
install: build-prod
	@echo "📦 Instalando en el sistema..."
	
	@# 1. Copiar binarios
	@echo " -> Copiando binarios a $(SYSTEM_BIN)..."
	@install -m 755 $(BIN_DIR)/$(BINARY_NAME) $(SYSTEM_BIN)/$(BINARY_NAME)
	@install -m 755 $(BIN_DIR)/$(WIZARD_NAME) $(SYSTEM_BIN)/$(WIZARD_NAME)
	
	@# 2. Configuración
	@echo " -> Creando directorio $(CONFIG_DIR)..."
	@mkdir -p $(CONFIG_DIR)
	@if [ -f config.toml ]; then \
		echo " -> Copiando config.toml..."; \
		install -m 644 config.toml $(CONFIG_DIR)/config.toml; \
	else \
		echo "⚠️  No se encontró config.toml local. Ejecuta el wizard tras instalar."; \
	fi

	@# 3. Systemd
	@echo " -> Instalando servicio systemd..."
	@install -m 644 gobalancer.service $(SERVICE_DIR)/gobalancer.service
	
	@# 4. Reload
	@systemctl daemon-reload
	@systemctl enable gobalancer
	
	@echo "✅ Instalación completada."
	@echo "👉 Configurar: $(WIZARD_NAME) (Sin sudo)"
	@echo "👉 Iniciar:    sudo systemctl start gobalancer"

## uninstall: Elimina binarios y servicio del sistema
uninstall:
	@echo "🗑️ Desinstalando..."
	@systemctl stop gobalancer || true
	@systemctl disable gobalancer || true
	@rm -f $(SYSTEM_BIN)/$(BINARY_NAME)
	@rm -f $(SYSTEM_BIN)/$(WIZARD_NAME)
	@rm -f $(SERVICE_DIR)/gobalancer.service
	@systemctl daemon-reload
	@echo "✅ Desinstalado (Configuración mantenida en $(CONFIG_DIR))."

## run: Ejecuta el Balanceador (Requiere sudo para Netlink/Iptables)
run: build
	@echo "🚀 Ejecutando Core (Privilegiado)..."
	sudo ./$(BIN_DIR)/$(BINARY_NAME)

## run-wizard: Ejecuta el Asistente (Usuario normal, solo lectura de red)
run-wizard: build
	@echo "🧙 Ejecutando Wizard..."
	./$(BIN_DIR)/$(WIZARD_NAME)

## clean: Elimina binarios y archivos temporales
clean:
	@echo "🧹 Limpiando..."
	@rm -rf $(BIN_DIR)
	@go clean

# ==============================================================================
# Calidad de Código y Tests
# ==============================================================================

## fmt: Formatea el código fuente (go fmt)
fmt:
	@echo "📝 Formateando código..."
	$(GO) fmt ./...

## vet: Analiza el código en busca de errores sospechosos
vet:
	@echo "🔍 Ejecutando go vet..."
	$(GO) vet ./...

## test: Ejecuta los tests unitarios
test:
	@echo "🧪 Ejecutando tests..."
	$(GO) test -v ./...

# ==============================================================================
# Compilación Cruzada (Cross-Compilation)
# ==============================================================================

## build-linux: Compila para Linux AMD64 (Servidores)
build-linux:
	@echo "🐧 Compilando para Linux AMD64..."
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)

## build-arm64: Compila para ARM64 (Raspberry Pi 4 / Routers modernos)
build-arm64:
	@echo "🍓 Compilando para ARM64..."
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-linux-arm64 $(MAIN_PATH)

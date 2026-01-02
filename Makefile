# ==============================================================================
# Definición de Variables
# ==============================================================================
BINARY_NAME=gobalancer
WIZARD_NAME=gobalancer-wizard
CTL_NAME=gobalancer-ctl

MAIN_PATH=cmd/balancer/main.go
WIZARD_PATH=cmd/wizard/main.go
CTL_PATH=cmd/ctl/main.go

BIN_DIR=bin
SYSTEM_BIN=/usr/local/bin
CONFIG_DIR=/etc/gobalancer
SERVICE_DIR=/etc/systemd/system

# Flags de compilación para reducir tamaño
# -s: Omitir la tabla de símbolos
# -w: Omitir información de depuración DWARF
LDFLAGS=-ldflags "-s -w"

# Go Command
GO=go

# ==============================================================================
# Targets Principales
# ==============================================================================

.PHONY: all help build build-prod install uninstall run run-wizard clean test deps fmt vet vulncheck build-linux build-arm64 build-armv7 build-armv6 shrink

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

## build: Compila Core, Wizard y CTL para la arquitectura actual (Desarrollo)
build:
	@echo "🔨 Compilando suite completa (Dev)..."
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	$(GO) build -o $(BIN_DIR)/$(WIZARD_NAME) $(WIZARD_PATH)
	$(GO) build -o $(BIN_DIR)/$(CTL_NAME) $(CTL_PATH)
	@echo "✅ Build finalizado en $(BIN_DIR)/"

## build-prod: Compila optimizado para producción (AMD64 Linux)
build-prod:
	@echo "🏭 Compilando para Producción (Linux AMD64 Static)..."
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(WIZARD_NAME) $(WIZARD_PATH)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(CTL_NAME) $(CTL_PATH)
	@echo "✅ Binarios listos."

## install: Instala binarios, configuración y servicio Systemd (Requiere sudo)
install: build-prod
	@echo "📦 Instalando en el sistema..."
	@echo " -> Copiando binarios a $(SYSTEM_BIN)..."
	@install -m 755 $(BIN_DIR)/$(BINARY_NAME) $(SYSTEM_BIN)/$(BINARY_NAME)
	@install -m 755 $(BIN_DIR)/$(WIZARD_NAME) $(SYSTEM_BIN)/$(WIZARD_NAME)
	@install -m 755 $(BIN_DIR)/$(CTL_NAME) $(SYSTEM_BIN)/$(CTL_NAME)
	
	@echo " -> Creando directorio $(CONFIG_DIR)..."
	@mkdir -p $(CONFIG_DIR)
	@if [ -f config.toml ]; then \
		echo " -> Copiando config.toml..."; \
		install -m 644 config.toml $(CONFIG_DIR)/config.toml; \
	else \
		echo "⚠️  No se encontró config.toml local."; \
	fi

	@echo " -> Instalando servicio systemd..."
	@install -m 644 gobalancer.service $(SERVICE_DIR)/gobalancer.service
	@systemctl daemon-reload
	@systemctl enable gobalancer
	
	@echo "✅ Instalación completada."

## uninstall: Elimina binarios y servicio del sistema
uninstall:
	@echo "🗑️ Desinstalando..."
	@systemctl stop gobalancer || true
	@systemctl disable gobalancer || true
	@rm -f $(SYSTEM_BIN)/$(BINARY_NAME)
	@rm -f $(SYSTEM_BIN)/$(WIZARD_NAME)
	@rm -f $(SYSTEM_BIN)/$(CTL_NAME)
	@rm -f $(SERVICE_DIR)/gobalancer.service
	@systemctl daemon-reload
	@echo "✅ Desinstalado."

## run: Ejecuta el Balanceador (Requiere sudo)
run: build
	@echo "🚀 Ejecutando Core (Privilegiado)..."
	sudo ./$(BIN_DIR)/$(BINARY_NAME)

## run-wizard: Ejecuta el Asistente
run-wizard: build
	@echo "🧙 Ejecutando Wizard..."
	./$(BIN_DIR)/$(WIZARD_NAME)

## clean: Elimina binarios y archivos temporales
clean:
	@echo "🧹 Limpiando..."
	@rm -rf $(BIN_DIR)
	@go clean

# ==============================================================================
# Cross-Compilation para OpenWrt / Routers ARM
# ==============================================================================

## build-arm64: Routers Modernos (Raspberry Pi 4, Nanopi R4S, Linksys nuevos)
build-arm64:
	@echo "🦂 Compilando para ARM64 (AArch64)..."
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-arm64 $(MAIN_PATH)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(CTL_NAME)-arm64 $(CTL_PATH)
	@echo "✅ Binarios ARM64 generados."

## build-armv7: Routers Mid-Range (Raspberry Pi 2/3, Asus RT, etc)
build-armv7:
	@echo "🦂 Compilando para ARMv7 (Hardfloat)..."
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-armv7 $(MAIN_PATH)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(CTL_NAME)-armv7 $(CTL_PATH)
	@echo "✅ Binarios ARMv7 generados."

## build-armv6: Routers Antiguos o RPi Zero 1
build-armv6:
	@echo "🦂 Compilando para ARMv6..."
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-armv6 $(MAIN_PATH)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(CTL_NAME)-armv6 $(CTL_PATH)
	@echo "✅ Binarios ARMv6 generados."

## build-armv5: Routers muy antiguos / IoT (Softfloat)
build-armv5:
	@echo "🦂 Compilando para ARMv5 (Softfloat)..."
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=5 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-armv5 $(MAIN_PATH)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=5 $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(CTL_NAME)-armv5 $(CTL_PATH)
	@echo "✅ Binarios ARMv5 generados."

# ==============================================================================
# Cross-Compilation para MIPS (Xiaomi R3, MediaTek, Atheros)
# ==============================================================================

## build-mipsle: Para Xiaomi R3 / Mini (MediaTek MT76xx) - MIPS Little Endian Softfloat
build-mipsle:
	@echo "🦂 Compilando para MIPSLE (Xiaomi R3 / MediaTek)..."
	@mkdir -p $(BIN_DIR)
	# GOMIPS=softfloat es OBLIGATORIO porque el MT7620A no tiene FPU
	CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY_NAME)-mipsle $(MAIN_PATH)
	CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat $(GO) build $(LDFLAGS) -o $(BIN_DIR)/$(CTL_NAME)-mipsle $(CTL_PATH)
	@echo "✅ Binarios MIPSLE generados. Listos para copiar al router."

# ==============================================================================
# Optimización de Tamaño (Requiere UPX instalado: sudo apt install upx)
# ==============================================================================

## shrink: Comprime los binarios generados con UPX (Reduce ~60% tamaño)
shrink:
	@echo "📦 Comprimiendo binarios con UPX..."
	@if command -v upx >/dev/null 2>&1; then \
		upx --best --lzma $(BIN_DIR)/*; \
		echo "✅ Compresión finalizada."; \
	else \
		echo "❌ Error: UPX no está instalado. Instálalo con 'sudo apt install upx'"; \
	fi
